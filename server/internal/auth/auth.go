package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moddle/moddle/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserExists         = errors.New("user already exists")
)

type Service struct {
	db        *store.DB
	jwtSecret []byte
	ttl       time.Duration
}

func New(db *store.DB, jwtSecret []byte, ttl time.Duration) *Service {
	return &Service{db: db, jwtSecret: jwtSecret, ttl: ttl}
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
	// DeleteAt is zero for active users and a unix-millis timestamp for
	// deactivated ones. Most lookups filter `delete_at = 0` on the DB
	// side so the field stays zero; the admin `ListUsersIncludingDeleted`
	// path is the one caller that needs a non-zero value here.
	DeleteAt int64 `json:"delete_at,omitempty"`
}

type Claims struct {
	UserID string `json:"sub"`
	jwt.RegisteredClaims
}

func (s *Service) Register(ctx context.Context, username, email, password string) (*User, error) {
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

	var u User
	var hash string
	var isBot bool
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, username, email, roles, COALESCE(picture,''), password_hash, COALESCE(is_bot, FALSE)
		FROM users WHERE (LOWER(username)=LOWER($1) OR LOWER(email)=LOWER($1)) AND delete_at=0
	`, loginID).Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture, &hash, &isBot)
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
	tok, err := s.issueToken(u.ID)
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token, device_id, expires_at, create_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, uuid.NewString(), u.ID, tok, deviceID, now.Add(s.ttl).UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, "", err
	}
	return &u, tok, nil
}

// Revoke deletes the session identified by the given JWT token. Returns nil
// even if the session was not found so logout is idempotent.
func (s *Service) Revoke(ctx context.Context, token string) error {
	_, err := s.db.Pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

// IssueSession mints a fresh JWT for userID and records a matching session
// row, exactly like the end of Login. Exposed so sibling packages (OAuth
// callback) can complete a sign-in without re-implementing token minting.
func (s *Service) IssueSession(ctx context.Context, userID string) (string, error) {
	tok, err := s.issueToken(userID)
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token, expires_at, create_at)
		VALUES ($1,$2,$3,$4,$5)
	`, uuid.NewString(), userID, tok, now.Add(s.ttl).UnixMilli(), now.UnixMilli())
	if err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Service) issueToken(userID string) (string, error) {
	claims := &Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "moddle",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
}

func (s *Service) Parse(tokenStr string) (*Claims, error) {
	tok, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return c, nil
}

func (s *Service) UserByID(ctx context.Context, id string) (*User, error) {
	var u User
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, username, email, roles, COALESCE(picture,'') FROM users WHERE id=$1 AND delete_at=0
	`, id).Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UserByUsername looks up a user by username. Returns ErrInvalidCredentials
// shape (pgx.ErrNoRows) when missing so handlers can 404 cleanly.
func (s *Service) UserByUsername(ctx context.Context, name string) (*User, error) {
	var u User
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, username, email, roles, COALESCE(picture,'') FROM users WHERE username=$1 AND delete_at=0
	`, name).Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture)
	if err != nil {
		return nil, err
	}
	return &u, nil
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
		SELECT id, username, email, roles, COALESCE(picture,''), delete_at FROM users
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
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture, &u.DeleteAt); err != nil {
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
		SELECT id, username, email, roles, COALESCE(picture,'') FROM users
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
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Roles, &u.Picture); err != nil {
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

// Session is the admin-visible shape of a sessions row. Token is the raw
// JWT — only exposed to the session's owner via /users/me/sessions so the
// "this is my current device" match can be made client-side.
type Session struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Token     string `json:"token"`
	DeviceID  string `json:"device_id"`
	ExpiresAt int64  `json:"expires_at"`
	CreateAt  int64  `json:"create_at"`
}

// ListSessions returns every active session row for a user, newest first.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, user_id, token, COALESCE(device_id,''), expires_at, create_at
		FROM sessions WHERE user_id=$1 ORDER BY create_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var ss Session
		if err := rows.Scan(&ss.ID, &ss.UserID, &ss.Token, &ss.DeviceID, &ss.ExpiresAt, &ss.CreateAt); err != nil {
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

// RevokeOthers deletes every session for the user EXCEPT the one matching
// currentToken. Used by the "sign out everywhere else" flow on the
// profile menu. Returns the number of sessions removed.
func (s *Service) RevokeOthers(ctx context.Context, userID, currentToken string) (int64, error) {
	cmd, err := s.db.Pool.Exec(ctx, `
		DELETE FROM sessions WHERE user_id=$1 AND token<>$2
	`, userID, currentToken)
	if err != nil {
		return 0, err
	}
	return cmd.RowsAffected(), nil
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

// UpdatePassword verifies the current password then swaps in the new one.
func (s *Service) UpdatePassword(ctx context.Context, userID, current, next string) error {
	var hash string
	if err := s.db.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&hash); err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)); err != nil {
		return ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Pool.Exec(ctx, `UPDATE users SET password_hash=$1, update_at=$2 WHERE id=$3`, string(newHash), time.Now().UnixMilli(), userID)
	return err
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
