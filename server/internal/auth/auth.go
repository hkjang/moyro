package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

// Thin local wrappers so the notify_props code reads cleanly without
// importing encoding/json at every call site.
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidSession     = errors.New("invalid or revoked session")
	ErrInvalidPassword    = errors.New("password must contain between 12 and 72 bytes and no NUL characters")
	ErrUserExists         = errors.New("user already exists")
	ErrNotFound           = errors.New("user not found")
)

const (
	minimumPasswordBytes    = 12
	maximumPasswordBytes    = 72 // bcrypt's defined input limit
	sessionIssuer           = "moyro"
	sessionJTIDigestPurpose = "session-jti/v1"
	sessionJTIDigestSize    = 32
)

// SessionDigester is implemented by secrets.Manager. Keeping this interface
// narrow prevents the auth package from receiving Moyro's root encryption key.
type SessionDigester interface {
	Digest(purpose string, secret []byte) ([]byte, error)
}

type Service struct {
	db              *store.DB
	jwtSecret       []byte
	ttl             time.Duration
	sessionDigester SessionDigester
}

func New(db *store.DB, jwtSecret []byte, ttl time.Duration, sessionDigester SessionDigester) *Service {
	return &Service{db: db, jwtSecret: jwtSecret, ttl: ttl, sessionDigester: sessionDigester}
}

// ValidatePassword applies the local-account password boundary at the service
// layer. HTTP validation is useful for friendly errors, but every caller that
// can create or rotate a password must share the same fail-closed rule.
func ValidatePassword(password string) error {
	if len(password) < minimumPasswordBytes || len(password) > maximumPasswordBytes || strings.ContainsRune(password, '\x00') {
		return ErrInvalidPassword
	}
	return nil
}

// DB returns the underlying store handle. Callers outside this package
// should use it only for operational concerns (health checks, metrics
// queries), not for running business-logic SQL — that belongs in the
// per-service layers.
func (s *Service) DB() *store.DB { return s.db }

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Roles    string `json:"roles"`
	// Picture is either an external URL (set via OAuth provider user-info
	// import) or a server-relative path (set via self-upload). Empty ⇒
	// UI falls back to initial-tile avatars. Kept as TEXT not URL so both
	// forms pass through without validation fuss.
	Picture string `json:"picture"`
	// Phase 23 — first-class profile fields surfaced via PUT /users/{id}
	// and PUT /users/{id}/patch. All four default to "" (never null) so
	// JSON marshalling stays predictable. They're omitempty here so older
	// API consumers that just want id/username/email still see a tight
	// envelope, but the field set on the wire is stable for those that do
	// care.
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	Position  string `json:"position,omitempty"`
	// DeleteAt is zero for active users and a unix-millis timestamp for
	// deactivated ones. Most lookups filter `delete_at = 0` on the DB
	// side so the field stays zero; the admin `ListUsersIncludingDeleted`
	// path is the one caller that needs a non-zero value here.
	DeleteAt int64 `json:"delete_at,omitempty"`
}

// userColumns is the canonical SELECT clause for hydrating User. Kept as a
// constant so adding a column to the struct only edits one place. COALESCE
// guards against any pre-Phase-23 NULL rows since the migration uses
// NOT NULL DEFAULT ” but ALTER on a populated table can hand out NULLs
// during the transitional moment.
const userColumns = `id, username, email, roles, COALESCE(picture,''),
       COALESCE(first_name,''), COALESCE(last_name,''),
       COALESCE(nickname,''),  COALESCE(position,'')`

func scanUser(row interface {
	Scan(...any) error
}) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture,
		&u.FirstName, &u.LastName, &u.Nickname, &u.Position); err != nil {
		return nil, err
	}
	return &u, nil
}

// scanUserRow scans the userColumns column set out of a *pgx.Rows cursor.
// Used by the *List* / *ByIDs / *ByUsernames bulk readers — separate from
// scanUser to keep the latter's pgx.Row vs. pgx.Rows interfaces straight.
func scanUserRow(rows pgx.Rows) (User, error) {
	var u User
	err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture,
		&u.FirstName, &u.LastName, &u.Nickname, &u.Position)
	return u, err
}

type Claims struct {
	UserID string `json:"sub"`
	jwt.RegisteredClaims
	// SessionID is hydrated from PostgreSQL after authentication. It is never
	// signed into or serialized with the bearer JWT.
	SessionID string `json:"-"`
}

type issuedSessionToken struct {
	Token     string
	JTIHash   []byte
	ExpiresAt int64
}

func (s *Service) Register(ctx context.Context, username, email, password string) (*User, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	now := time.Now().UnixMilli()
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ($1,$2,$3,$4,'system_user',$5,$5)
	`, id, username, email, string(hash), now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return &User{ID: id, Username: username, Email: email, Roles: "system_user"}, nil
}

func (s *Service) Login(ctx context.Context, loginID, password string) (*User, string, error) {
	return s.LoginWithDevice(ctx, loginID, password, "")
}

func (s *Service) LoginWithDevice(ctx context.Context, loginID, password, deviceID string) (*User, string, error) {
	loginID = strings.TrimSpace(loginID)
	deviceID = strings.TrimSpace(deviceID)
	if loginID == "" || password == "" {
		return nil, "", ErrInvalidCredentials
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	var u User
	var hash string
	var isBot bool
	err = tx.QueryRow(ctx, `
		SELECT `+userColumns+`, password_hash, COALESCE(is_bot, FALSE)
		FROM users WHERE (LOWER(username)=LOWER($1) OR LOWER(email)=LOWER($1)) AND delete_at=0
		FOR SHARE
	`, loginID).Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture,
		&u.FirstName, &u.LastName, &u.Nickname, &u.Position, &hash, &isBot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrInvalidCredentials
	}
	if err != nil {
		return nil, "", err
	}
	// Bots authenticate via personal access tokens (sha256 lookup) only —
	// blanket-block password login here so a leaked PAT can't be turned
	// into a session that survives token revocation.
	if isBot {
		return nil, "", ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, "", ErrInvalidCredentials
	}
	issued, err := s.issueToken(u.ID)
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	_, err = tx.Exec(ctx, `
			INSERT INTO sessions (id, user_id, token, jti_hash, device_id, expires_at, create_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
		`, uuid.NewString(), u.ID, issued.Token, issued.JTIHash, deviceID, issued.ExpiresAt, now.UnixMilli())
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return &u, issued.Token, nil
}

// Revoke deletes the session identified by the given JWT token. Returns nil
// even if the session was not found so logout is idempotent.
func (s *Service) Revoke(ctx context.Context, token string) error {
	claims, err := s.Parse(token)
	if err != nil {
		// Logout remains idempotent for credentials that do not identify a JWT
		// session (for example a PAT authenticated through the same route).
		return nil
	}
	digest, err := s.digestJTI(claims.ID)
	if err != nil {
		return err
	}
	_, err = s.db.Pool.Exec(ctx, `
		DELETE FROM sessions
		WHERE jti_hash=$1 OR (jti_hash IS NULL AND token=$2)
	`, digest, token)
	return err
}

// IssueSession mints a fresh JWT for userID and records a matching session
// row, exactly like the end of Login. Exposed so sibling packages (OAuth
// callback) can complete a sign-in without re-implementing token minting.
func (s *Service) IssueSession(ctx context.Context, userID string) (string, error) {
	issued, err := s.issueToken(userID)
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, err = s.db.Pool.Exec(ctx, `
			INSERT INTO sessions (id, user_id, token, jti_hash, expires_at, create_at)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, uuid.NewString(), userID, issued.Token, issued.JTIHash, issued.ExpiresAt, now.UnixMilli())
	if err != nil {
		return "", err
	}
	return issued.Token, nil
}

func (s *Service) issueToken(userID string) (issuedSessionToken, error) {
	if strings.TrimSpace(userID) == "" {
		return issuedSessionToken{}, errors.New("session subject is required")
	}
	now := time.Now()
	expiresAt := now.Add(s.ttl)
	claims := &Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    sessionIssuer,
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
	if err != nil {
		return issuedSessionToken{}, err
	}
	digest, err := s.digestJTI(claims.ID)
	if err != nil {
		return issuedSessionToken{}, err
	}
	return issuedSessionToken{Token: token, JTIHash: digest, ExpiresAt: expiresAt.UnixMilli()}, nil
}

func (s *Service) Parse(tokenStr string) (*Claims, error) {
	tok, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return s.jwtSecret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(sessionIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, err
	}
	c, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid || strings.TrimSpace(c.UserID) == "" || strings.TrimSpace(c.ID) == "" {
		return nil, errors.New("invalid token")
	}
	return c, nil
}

func (s *Service) digestJTI(jti string) ([]byte, error) {
	if s.sessionDigester == nil {
		return nil, errors.New("session digester is unavailable")
	}
	if strings.TrimSpace(jti) == "" {
		return nil, errors.New("session jti is required")
	}
	digest, err := s.sessionDigester.Digest(sessionJTIDigestPurpose, []byte(jti))
	if err != nil {
		return nil, fmt.Errorf("digest session jti: %w", err)
	}
	if len(digest) != sessionJTIDigestSize {
		return nil, fmt.Errorf("digest session jti: got %d bytes, want %d", len(digest), sessionJTIDigestSize)
	}
	return digest, nil
}

// Authenticate validates both the signed JWT and its live database session.
// This makes logout, administrator revocation, expiry, and user deactivation
// effective on the very next HTTP or WebSocket request.
func (s *Service) Authenticate(ctx context.Context, tokenStr string) (*Claims, error) {
	claims, err := s.Parse(tokenStr)
	if err != nil {
		return nil, ErrInvalidSession
	}

	digest, err := s.digestJTI(claims.ID)
	if err != nil {
		return nil, err
	}
	var sessionID string
	var hasDigest bool
	err = s.db.Pool.QueryRow(ctx, `
			SELECT s.id, s.jti_hash IS NOT NULL
			FROM sessions AS s
			JOIN users AS u ON u.id = s.user_id
			WHERE (s.jti_hash = $1 OR (s.jti_hash IS NULL AND s.token = $2))
			  AND s.user_id = $3
			  AND s.expires_at > $4
			  AND u.delete_at = 0
			LIMIT 1
		`, digest, tokenStr, claims.UserID, time.Now().UnixMilli()).Scan(&sessionID, &hasDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidSession
	}
	if err != nil {
		return nil, err
	}
	if !hasDigest {
		// Legacy v0.1 rows contain only the raw token. Backfill only after its
		// signature, issuer, subject, JTI, user and expiry have all validated.
		if _, err := s.db.Pool.Exec(ctx, `
			UPDATE sessions SET jti_hash=$1 WHERE id=$2 AND jti_hash IS NULL
		`, digest, sessionID); err != nil {
			return nil, err
		}
	}
	claims.SessionID = sessionID
	return claims, nil
}

func (s *Service) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(s.db.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1 AND delete_at=0`, id))
}

// UserByUsername looks up a user by username. Returns ErrInvalidCredentials
// shape (pgx.ErrNoRows) when missing so handlers can 404 cleanly.
func (s *Service) UserByUsername(ctx context.Context, name string) (*User, error) {
	return scanUser(s.db.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username=$1 AND delete_at=0`, name))
}

// ListUsers returns a paginated slice of active users ordered by username.
func (s *Service) ListUsers(ctx context.Context, page, perPage int) ([]User, error) {
	return s.listUsersPaginated(ctx, page, perPage, false)
}

// ListUsersIncludingDeleted is the admin variant — it returns inactive
// rows too so the panel can offer a "reactivate" button. `delete_at` on
// the returned struct is populated so the UI can visually mark inactive
// rows without a second query.
func (s *Service) ListUsersIncludingDeleted(ctx context.Context, page, perPage int) ([]User, error) {
	return s.listUsersPaginated(ctx, page, perPage, true)
}

func (s *Service) listUsersPaginated(ctx context.Context, page, perPage int, includeDeleted bool) ([]User, error) {
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	if page < 0 {
		page = 0
	}
	where := "WHERE delete_at = 0"
	if includeDeleted {
		where = ""
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+userColumns+`, delete_at FROM users
		`+where+`
		ORDER BY username ASC
		LIMIT $1 OFFSET $2
	`, perPage, page*perPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture,
			&u.FirstName, &u.LastName, &u.Nickname, &u.Position, &u.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserByEmail mirrors UserByUsername but keys on email — lets official
// Mattermost clients implement "is this email registered" probes via
// `GET /api/v4/users/email/{email}` without falling back to the search
// endpoint. Returns pgx.ErrNoRows for missing rows so the handler 404s.
func (s *Service) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email=$1 AND delete_at=0`, email))
}

// UsersByIDs hydrates a batch of user records by id. Order in the input
// slice is NOT preserved (callers re-key by id from the result). Missing
// ids are silently dropped — Mattermost's contract treats unknowns as
// non-fatal.
func (s *Service) UsersByIDs(ctx context.Context, ids []string) ([]User, error) {
	out := []User{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+userColumns+` FROM users
		WHERE id = ANY($1) AND delete_at = 0
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsersByUsernames is the bulk username variant. Same semantics as
// UsersByIDs — missing usernames are dropped, order is not preserved.
func (s *Service) UsersByUsernames(ctx context.Context, names []string) ([]User, error) {
	out := []User{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+userColumns+` FROM users
		WHERE username = ANY($1) AND delete_at = 0
	`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AutocompleteUsers prefixes-first matches on username for the
// /users/autocomplete endpoint. Mattermost's response shape is split into
// "users" (any) + "out_of_channel" (when the caller scopes to a channel).
// We expose the bare list here; the handler shapes the envelope.
func (s *Service) AutocompleteUsers(ctx context.Context, term string, limit int) ([]User, error) {
	if limit <= 0 || limit > 50 {
		limit = 25
	}
	prefix := term + "%"
	contains := "%" + term + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+userColumns+` FROM users
		WHERE delete_at = 0 AND (username ILIKE $1 OR username ILIKE $2)
		ORDER BY (username ILIKE $1) DESC, username ASC
		LIMIT $3
	`, prefix, contains, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SearchUsers does prefix / contains matching on username + email.
func (s *Service) SearchUsers(ctx context.Context, term string, limit int) ([]User, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	like := "%" + term + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+userColumns+` FROM users
		WHERE delete_at = 0 AND (username ILIKE $1 OR email ILIKE $1)
		ORDER BY username ASC
		LIMIT $2
	`, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Deactivate soft-deletes the target user and drops all of their active
// sessions in a single transaction. Returns whether anything actually
// changed (i.e. the user existed and was live), so the caller can decide
// whether to broadcast a WS kick.
func (s *Service) Deactivate(ctx context.Context, targetID string) (bool, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	cmd, err := tx.Exec(ctx, `
		UPDATE users SET delete_at=$2, update_at=$2 WHERE id=$1 AND delete_at=0
	`, targetID, now)
	if err != nil {
		return false, err
	}
	if cmd.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, targetID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Reactivate clears delete_at so the user can log in again. Idempotent —
// reactivating an active user is a no-op at the data layer.
func (s *Service) Reactivate(ctx context.Context, targetID string) (bool, error) {
	now := time.Now().UnixMilli()
	cmd, err := s.db.Pool.Exec(ctx, `
		UPDATE users SET delete_at=0, update_at=$2 WHERE id=$1 AND delete_at<>0
	`, targetID, now)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() > 0, nil
}

// Session is the safe administrative shape of a sessions row. Bearer tokens
// and keyed lookup digests never leave the authentication/storage boundary.
type Session struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	DeviceID  string `json:"device_id"`
	ExpiresAt int64  `json:"expires_at"`
	CreateAt  int64  `json:"create_at"`
}

// ListSessions returns every active session row for a user, newest first.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	now := time.Now().UnixMilli()
	rows, err := s.db.Pool.Query(ctx, `
			SELECT id, user_id, COALESCE(device_id,''), expires_at, create_at
			FROM sessions
			WHERE user_id=$1 AND expires_at>$2
			ORDER BY create_at DESC
		`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var ss Session
		if err := rows.Scan(&ss.ID, &ss.UserID, &ss.DeviceID, &ss.ExpiresAt, &ss.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// RevokeSession deletes one session by primary key, but only if it
// belongs to the given user — prevents a malformed id from nuking another
// user's session row. Returns whether a row was removed so handlers can
// differentiate 404 (wrong owner / bad id) from 200.
func (s *Service) RevokeSession(ctx context.Context, sessionID, userID string) (bool, error) {
	cmd, err := s.db.Pool.Exec(ctx, `
		DELETE FROM sessions WHERE id=$1 AND user_id=$2
	`, sessionID, userID)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() > 0, nil
}

// RevokeOthers deletes every session for the user EXCEPT currentSessionID.
// An empty currentSessionID (for example PAT authentication) preserves the
// historical behaviour of revoking every JWT session. Used by the profile
// menu. Expired rows are cleaned at the same time but do not inflate the
// returned count of live sessions invalidated.
func (s *Service) RevokeOthers(ctx context.Context, userID, currentSessionID string) (int64, error) {
	var revoked int64
	err := s.db.Pool.QueryRow(ctx, `
		WITH removed AS (
			DELETE FROM sessions
			WHERE user_id=$1 AND ($2='' OR id<>$2)
			RETURNING expires_at
		)
		SELECT COUNT(*) FILTER (WHERE expires_at>$3)::BIGINT FROM removed
	`, userID, currentSessionID, time.Now().UnixMilli()).Scan(&revoked)
	if err != nil {
		return 0, err
	}
	return revoked, nil
}

// RevokeAllForUser deletes every session for the given user — the official
// `POST /users/{id}/sessions/revoke/all` semantics. Returns the row count
// so callers can decide whether to broadcast a kick. Used by both the
// admin "force logout" tool and the self-serve "log me out everywhere"
// button (which is functionally equivalent to RevokeOthers + revoke self).
func (s *Service) RevokeAllForUser(ctx context.Context, userID string) (int64, error) {
	cmd, err := s.db.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	if err != nil {
		return 0, err
	}
	return cmd.RowsAffected(), nil
}

// RevokeAllSessionsGlobal nukes every session row in the table. Mirrors
// `POST /users/sessions/revoke/all` — the admin "log everyone out"
// hammer typically used for emergency token rotation. Returns the row
// count so the handler can broadcast a global kick.
func (s *Service) RevokeAllSessionsGlobal(ctx context.Context) (int64, error) {
	cmd, err := s.db.Pool.Exec(ctx, `DELETE FROM sessions`)
	if err != nil {
		return 0, err
	}
	return cmd.RowsAffected(), nil
}

// ListAllUserIDsWithSessions returns every distinct user_id that currently
// has a session row. Used by the global revoke handler to fan out kicks.
// Empty slice when no sessions exist.
func (s *Service) ListAllUserIDsWithSessions(ctx context.Context) ([]string, error) {
	rows, err := s.db.Pool.Query(ctx, `SELECT DISTINCT user_id FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// GetNotifyProps reads the user's top-level Mattermost-shaped notification
// preferences. Empty/missing rows return an empty map (never nil) so the
// JSON response stays `{}` not `null`. Mattermost stores notify_props as a
// flat map of string→string ("desktop":"all", "email":"true", etc.); we
// keep that shape for byte-for-byte API compatibility.
func (s *Service) GetNotifyProps(ctx context.Context, userID string) (map[string]string, error) {
	var raw []byte
	err := s.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(notify_props::text,'{}') FROM users WHERE id=$1 AND delete_at=0
	`, userID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(raw) == 0 || string(raw) == "{}" {
		return out, nil
	}
	if err := jsonUnmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetNotifyProps replaces the user's notify_props blob wholesale. Mattermost's
// PUT contract is "here is the new full map" — partial updates are caller's
// responsibility (the webapp merges before sending). Empty input writes `{}`
// rather than NULL so future reads stay strict-typed.
func (s *Service) SetNotifyProps(ctx context.Context, userID string, props map[string]string) error {
	if props == nil {
		props = map[string]string{}
	}
	blob, err := jsonMarshal(props)
	if err != nil {
		return err
	}
	_, err = s.db.Pool.Exec(ctx, `
		UPDATE users SET notify_props=$2::jsonb, update_at=$3 WHERE id=$1 AND delete_at=0
	`, userID, string(blob), time.Now().UnixMilli())
	return err
}

// UpdatePicture overwrites the user's `picture` field — accepts either a
// full URL or a server-relative path. Returns the refreshed user.
func (s *Service) UpdatePicture(ctx context.Context, userID, picture string) (*User, error) {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE users SET picture=$2, update_at=$3 WHERE id=$1 AND delete_at=0
	`, userID, picture, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

// UpdateProfile lets a user change their username or email. Empty strings
// mean "keep current value". Password changes go through UpdatePassword.
func (s *Service) UpdateProfile(ctx context.Context, userID, username, email string) (*User, error) {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE users SET
			username = COALESCE(NULLIF($2,''), username),
			email    = COALESCE(NULLIF($3,''), email),
			update_at = $4
		WHERE id = $1 AND delete_at = 0
	`, userID, username, email, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

// ProfilePatch carries Phase 23 partial-update fields for PUT /users/{id}/patch.
// Pointer-typed so the handler can distinguish "field omitted" (skip) from
// "field set to empty string" (clear).
type ProfilePatch struct {
	Username  *string
	Email     *string
	FirstName *string
	LastName  *string
	Nickname  *string
	Position  *string
}

// PatchProfile applies the partial update and returns the refreshed user.
// Empty strings explicitly clear the field (handy for "remove your nickname"
// flows); nil pointers leave the existing value intact.
func (s *Service) PatchProfile(ctx context.Context, userID string, p ProfilePatch) (*User, error) {
	// Building one big COALESCE-with-sentinel update keeps the UPDATE atomic
	// and avoids a per-field row spin. We pass each field as a {value, set}
	// pair: the boolean controls whether to overwrite, the value supplies
	// the new content. Postgres CASE handles the branching cheaply and
	// trivially supports the "explicit empty string" case.
	now := time.Now().UnixMilli()
	usernameVal, usernameSet := derefStringPtr(p.Username)
	emailVal, emailSet := derefStringPtr(p.Email)
	firstVal, firstSet := derefStringPtr(p.FirstName)
	lastVal, lastSet := derefStringPtr(p.LastName)
	nickVal, nickSet := derefStringPtr(p.Nickname)
	posVal, posSet := derefStringPtr(p.Position)
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE users SET
			username   = CASE WHEN $2  THEN $3  ELSE username END,
			email      = CASE WHEN $4  THEN $5  ELSE email END,
			first_name = CASE WHEN $6  THEN $7  ELSE first_name END,
			last_name  = CASE WHEN $8  THEN $9  ELSE last_name END,
			nickname   = CASE WHEN $10 THEN $11 ELSE nickname END,
			position   = CASE WHEN $12 THEN $13 ELSE position END,
			update_at  = $14
		WHERE id = $1 AND delete_at = 0
	`, userID,
		usernameSet, usernameVal,
		emailSet, emailVal,
		firstSet, firstVal,
		lastSet, lastVal,
		nickSet, nickVal,
		posSet, posVal,
		now)
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

func derefStringPtr(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
}

// SetActive toggles a user's active/deactivated state. Wrapper over
// Deactivate / Reactivate that picks the right call from the boolean payload
// PUT /users/{id}/active sends. Returns whether anything changed.
func (s *Service) SetActive(ctx context.Context, targetID string, active bool) (bool, error) {
	if active {
		return s.Reactivate(ctx, targetID)
	}
	return s.Deactivate(ctx, targetID)
}

// UserStats is the GET /users/stats response shape. Mattermost includes a
// few more counters but `total_users_count` is the one official clients
// poll for the admin dashboard top-of-page badge — the rest can be added
// piecewise as we wire each enterprise surface.
type UserStats struct {
	TotalUsersCount int64 `json:"total_users_count"`
}

// Stats returns the global user-count snapshot. Counts active users only
// (delete_at = 0) so admin dashboards don't double-count reactivated rows.
func (s *Service) Stats(ctx context.Context) (*UserStats, error) {
	var n int64
	if err := s.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE delete_at = 0`).Scan(&n); err != nil {
		return nil, err
	}
	return &UserStats{TotalUsersCount: n}, nil
}

type passwordMutationExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// replacePasswordAndRevokeSessions keeps password rotation and session
// invalidation inseparable. Callers execute it inside the same transaction so
// neither a new password with old sessions nor deleted sessions with an old
// password can become visible after a partial failure.
func replacePasswordAndRevokeSessions(ctx context.Context, executor passwordMutationExecutor, userID, hash string, now int64) (bool, error) {
	tag, err := executor.Exec(ctx, `
		UPDATE users SET password_hash=$1, update_at=$2
		WHERE id=$3 AND delete_at=0
	`, hash, now, userID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := executor.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return false, err
	}
	return true, nil
}

// UpdatePassword verifies the current password, swaps in the new one, and
// revokes every session for the account in one transaction.
func (s *Service) UpdatePassword(ctx context.Context, userID, current, next string) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var hash string
	if err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 AND delete_at=0 FOR UPDATE`, userID).Scan(&hash); err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)); err != nil {
		return ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	changed, err := replacePasswordAndRevokeSessions(ctx, tx, userID, string(newHash), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if !changed {
		return ErrInvalidCredentials
	}
	return tx.Commit(ctx)
}

// AdminSetPassword force-rotates a user's password without checking the
// current value. Reserved for system_admin tooling — `PUT /users/{id}/password`
// from a privileged actor. Returns ErrInvalidCredentials only when the user
// is missing/deleted (so callers can surface a 404), nil on success.
func (s *Service) AdminSetPassword(ctx context.Context, userID, next string) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	changed, err := replacePasswordAndRevokeSessions(ctx, tx, userID, string(newHash), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if !changed {
		return ErrInvalidCredentials
	}
	return tx.Commit(ctx)
}

// SetRoles overwrites a user's role string. Mirrors Mattermost's
// `PUT /users/{user_id}/roles` body `{roles: "system_user system_admin"}`.
// Whitespace is normalised; duplicate tokens collapsed; empty string rejected
// so we never strip a user out of the system_user baseline by accident.
func (s *Service) SetRoles(ctx context.Context, userID, roles string) error {
	tokens := splitRoles(roles)
	if len(tokens) == 0 {
		return errors.New("empty roles")
	}
	seen := map[string]bool{}
	canon := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if seen[t] {
			continue
		}
		seen[t] = true
		canon = append(canon, t)
	}
	tag, err := s.db.Pool.Exec(ctx, `UPDATE users SET roles=$1, update_at=$2 WHERE id=$3 AND delete_at=0`,
		stringsJoinSpace(canon), time.Now().UnixMilli(), userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidCredentials
	}
	return nil
}

func stringsJoinSpace(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

// SetSessionDeviceID stamps the device_id (e.g. APNS/FCM token) on the current
// authenticated session without passing the raw bearer token into storage SQL.
func (s *Service) SetSessionDeviceID(ctx context.Context, sessionID, userID, deviceID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	tag, err := s.db.Pool.Exec(ctx, `UPDATE sessions SET device_id=$3 WHERE id=$1 AND user_id=$2`, sessionID, userID, deviceID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// HasRole reports whether the user carries a given role token. The roles
// column is a Mattermost-style space-delimited string (e.g. "system_user
// system_admin"), so we split and compare.
func (s *Service) HasRole(ctx context.Context, userID, role string) (bool, error) {
	var roles string
	err := s.db.Pool.QueryRow(ctx, `SELECT roles FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&roles)
	if err != nil {
		return false, err
	}
	for _, r := range splitRoles(roles) {
		if r == role {
			return true, nil
		}
	}
	return false, nil
}

func splitRoles(s string) []string {
	out := []string{}
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, r := range s {
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		cur += string(r)
	}
	flush()
	return out
}

// HasAnySystemAdmin reports whether any active user already holds the
// system_admin role. Used by the bootstrap path to decide if the first
// registration should be auto-promoted.
func (s *Service) HasAnySystemAdmin(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM users
			WHERE delete_at = 0
			  AND (roles = 'system_admin' OR roles LIKE '% system_admin' OR roles LIKE 'system_admin %' OR roles LIKE '% system_admin %')
		)
	`).Scan(&exists)
	return exists, err
}

// PromoteToUser swaps system_guest for system_user on the role string.
// Mirrors Mattermost's `POST /users/{id}/promote`. We don't actually treat
// system_guest specially anywhere in the codebase, so this is a contract-
// shape implementation: it round-trips correctly so an official admin tool
// that flips users back-and-forth gets the same string it sent. Idempotent
// — promoting an already-promoted user is a no-op.
func (s *Service) PromoteToUser(ctx context.Context, userID string) error {
	var roles string
	if err := s.db.Pool.QueryRow(ctx, `SELECT roles FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&roles); err != nil {
		return err
	}
	parts := splitRoles(roles)
	out := make([]string, 0, len(parts)+1)
	hasUser := false
	for _, p := range parts {
		if p == "system_guest" {
			continue
		}
		if p == "system_user" {
			hasUser = true
		}
		out = append(out, p)
	}
	if !hasUser {
		out = append([]string{"system_user"}, out...)
	}
	_, err := s.db.Pool.Exec(ctx, `UPDATE users SET roles=$1, update_at=$2 WHERE id=$3`,
		stringsJoinSpace(out), time.Now().UnixMilli(), userID)
	return err
}

// DemoteToGuest swaps system_user for system_guest on the role string.
// Mirrors Mattermost's `POST /users/{id}/demote`. Idempotent.
func (s *Service) DemoteToGuest(ctx context.Context, userID string) error {
	var roles string
	if err := s.db.Pool.QueryRow(ctx, `SELECT roles FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&roles); err != nil {
		return err
	}
	parts := splitRoles(roles)
	out := make([]string, 0, len(parts)+1)
	hasGuest := false
	for _, p := range parts {
		if p == "system_user" {
			continue
		}
		if p == "system_guest" {
			hasGuest = true
		}
		out = append(out, p)
	}
	if !hasGuest {
		out = append([]string{"system_guest"}, out...)
	}
	_, err := s.db.Pool.Exec(ctx, `UPDATE users SET roles=$1, update_at=$2 WHERE id=$3`,
		stringsJoinSpace(out), time.Now().UnixMilli(), userID)
	return err
}

// PromoteSystemAdmin adds system_admin to a user's role set. Idempotent —
// if the role is already present the stored string is left unchanged.
func (s *Service) PromoteSystemAdmin(ctx context.Context, userID string) error {
	var roles string
	if err := s.db.Pool.QueryRow(ctx, `SELECT roles FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&roles); err != nil {
		return err
	}
	for _, r := range splitRoles(roles) {
		if r == "system_admin" {
			return nil
		}
	}
	if roles == "" {
		roles = "system_admin"
	} else {
		roles = roles + " system_admin"
	}
	_, err := s.db.Pool.Exec(ctx, `UPDATE users SET roles=$1, update_at=$2 WHERE id=$3`, roles, time.Now().UnixMilli(), userID)
	return err
}

// ConvertToBot flips an existing human user into a bot account: blanks
// password_hash so the regular Login path rejects them, sets is_bot=true,
// and revokes every active session so any in-flight JWTs from before the
// conversion stop working. Returns ErrNotFound for unknown / already-bot
// rows. Caller is expected to follow up with bots.Service.CreateToken so
// the new bot has a way to authenticate.
func (s *Service) ConvertToBot(ctx context.Context, userID, ownerID, description string) error {
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE users
		   SET is_bot          = TRUE,
		       password_hash   = '',
		       bot_owner_id    = NULLIF($2,''),
		       bot_description = $3,
		       update_at       = $4
		 WHERE id = $1
		   AND delete_at = 0
		   AND COALESCE(is_bot, FALSE) = FALSE
	`, userID, ownerID, description, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ConvertBotToUser is the inverse of ConvertToBot. Takes a plaintext
// password (we bcrypt it inline; bcrypt is already imported here for
// Login/Register), wipes is_bot/bot_owner_id/bot_description, and revokes
// every outstanding PAT in the same transaction so a leftover token can't
// be used to act as the now-human account.
func (s *Service) ConvertBotToUser(ctx context.Context, userID, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE users
		   SET is_bot          = FALSE,
		       password_hash   = $2,
		       bot_owner_id    = NULL,
		       bot_description = '',
		       update_at       = $3
		 WHERE id = $1
		   AND COALESCE(is_bot, FALSE) = TRUE
		   AND delete_at = 0
	`, userID, string(hash), now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at = 0`, userID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UserIDsByUsernames resolves a set of usernames to their user IDs. Unknown
// names are silently dropped. Used for @-mention detection.
func (s *Service) UserIDsByUsernames(ctx context.Context, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, username FROM users WHERE username = ANY($1::text[]) AND delete_at=0
	`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}
