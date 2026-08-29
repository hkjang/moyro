// Package bots manages bot accounts and their personal access tokens.
//
// A bot is a row in the `users` table with is_bot=true and an empty
// password_hash. The auth.Login path explicitly rejects bot accounts so
// the only way to act as a bot is to present a personal access token
// (sha256 hashed in personal_access_tokens, shown plain ONCE on creation).
//
// Bots auto-join the default team + #general so a freshly created bot
// can post immediately without an extra membership round-trip.
package bots

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

var (
	ErrBotNotFound  = errors.New("bot not found")
	ErrTokenInvalid = errors.New("token invalid")
)

type Service struct {
	db *store.DB
}

func New(db *store.DB) *Service { return &Service{db: db} }

type Bot struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	OwnerID     string `json:"owner_id"`
	CreateAt    int64  `json:"create_at"`
	UpdateAt    int64  `json:"update_at"`
	DeleteAt    int64  `json:"delete_at"`
}

// Create inserts a new bot user. The username must be a unique slug —
// duplicates surface as a generic error since pgx returns the underlying
// constraint violation; callers translate to 409.
func (s *Service) Create(ctx context.Context, ownerID, username, displayName, description string) (*Bot, error) {
	id := uuid.NewString()
	now := time.Now().UnixMilli()
	// Bots are users; we also need a row so chi @-mentions and DM creation
	// keep working. Roles stays minimal so a leaked PAT can't escalate.
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at, is_bot, bot_owner_id, bot_description)
		VALUES ($1, $2, $3, '', 'system_user', $4, $4, TRUE, $5, $6)
	`, id, username, syntheticBotEmail(username), now, ownerID, description)
	if err != nil {
		return nil, err
	}
	if displayName == "" {
		displayName = username
	}
	return &Bot{
		UserID: id, Username: username, DisplayName: displayName,
		Description: description, OwnerID: ownerID,
		CreateAt: now, UpdateAt: now,
	}, nil
}

// Get returns a bot by user id. nil + ErrBotNotFound if missing or not a bot.
func (s *Service) Get(ctx context.Context, userID string) (*Bot, error) {
	var b Bot
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, username, COALESCE(bot_owner_id, ''), COALESCE(bot_description, ''),
		       create_at, update_at, delete_at
		FROM users WHERE id=$1 AND COALESCE(is_bot, FALSE) = TRUE
	`, userID).Scan(&b.UserID, &b.Username, &b.OwnerID, &b.Description,
		&b.CreateAt, &b.UpdateAt, &b.DeleteAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBotNotFound
	}
	if err != nil {
		return nil, err
	}
	b.DisplayName = b.Username
	return &b, nil
}

// List returns every active bot. Admin-only consumer.
func (s *Service) List(ctx context.Context) ([]Bot, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, username, COALESCE(bot_owner_id,''), COALESCE(bot_description,''),
		       create_at, update_at, delete_at
		FROM users
		WHERE COALESCE(is_bot, FALSE) = TRUE AND delete_at = 0
		ORDER BY username
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bot{}
	for rows.Next() {
		var b Bot
		if err := rows.Scan(&b.UserID, &b.Username, &b.OwnerID, &b.Description,
			&b.CreateAt, &b.UpdateAt, &b.DeleteAt); err != nil {
			return nil, err
		}
		b.DisplayName = b.Username
		out = append(out, b)
	}
	return out, rows.Err()
}

// Disable soft-deletes the bot user. Existing posts remain attributed.
func (s *Service) Disable(ctx context.Context, userID string) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE users SET delete_at = $2, update_at = $2
		WHERE id = $1 AND COALESCE(is_bot, FALSE) = TRUE AND delete_at = 0
	`, userID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBotNotFound
	}
	// Also revoke every outstanding PAT — once disabled, the bot must not
	// be usable. Otherwise a forgotten token would remain valid.
	_, _ = s.db.Pool.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at = 0`, userID, now)
	return nil
}

// Update changes the bot's display name and description. Mirrors
// `PUT /bots/{bot_user_id}`. Username changes go through the normal
// user-rename path (PUT /users/{id}/patch) — kept separate so admin tooling
// can audit a username flip from a description tweak.
func (s *Service) Update(ctx context.Context, userID, displayName, description string) (*Bot, error) {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE users
		SET bot_description = $2,
		    update_at = $3
		WHERE id = $1 AND COALESCE(is_bot, FALSE) = TRUE AND delete_at = 0
	`, userID, description, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrBotNotFound
	}
	b, err := s.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	if displayName != "" {
		b.DisplayName = displayName
	}
	return b, nil
}

// Enable un-soft-deletes a previously disabled bot. Mirrors Disable but
// it does NOT touch the personal_access_tokens column — Disable revokes
// every PAT, so a re-enabled bot needs the admin to mint a fresh token
// before it can act. That asymmetry is intentional: revoke-on-disable is
// a security guarantee; auto-restore-on-enable would silently re-arm a
// previously leaked credential.
func (s *Service) Enable(ctx context.Context, userID string) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE users SET delete_at = 0, update_at = $2
		WHERE id = $1 AND COALESCE(is_bot, FALSE) = TRUE AND delete_at != 0
	`, userID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBotNotFound
	}
	return nil
}

// AssignOwner reassigns ownership of a bot to a different user. The new
// owner must already exist; the handler validates that. Returns the
// updated bot row so the admin tool can render the new owner inline.
func (s *Service) AssignOwner(ctx context.Context, botUserID, newOwnerID string) (*Bot, error) {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE users
		   SET bot_owner_id = NULLIF($2, ''),
		       update_at    = $3
		 WHERE id = $1
		   AND COALESCE(is_bot, FALSE) = TRUE
		   AND delete_at = 0
	`, botUserID, newOwnerID, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrBotNotFound
	}
	return s.Get(ctx, botUserID)
}

// IsBot reports whether the given user id corresponds to an active bot.
func (s *Service) IsBot(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := s.db.Pool.QueryRow(ctx, `SELECT COALESCE(is_bot, FALSE) FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

// ---- Personal access tokens ----

type Token struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Description string `json:"description"`
	CreateAt    int64  `json:"create_at"`
	LastUsedAt  int64  `json:"last_used_at"`
	RevokedAt   int64  `json:"revoked_at"`
}

// CreatedToken bundles the saved token row with the plaintext token —
// returned ONCE at creation; we never persist the plaintext.
type CreatedToken struct {
	Token Token  `json:"token"`
	Plain string `json:"plain"`
}

// CreateToken issues a new PAT for the given user. The plaintext is returned
// only this one time; the DB only stores its sha256 hash.
func (s *Service) CreateToken(ctx context.Context, userID, description string) (*CreatedToken, error) {
	plain, err := generatePlaintextToken()
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	hash := hashToken(plain)
	now := time.Now().UnixMilli()
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO personal_access_tokens (id, user_id, token_hash, description, create_at)
		VALUES ($1,$2,$3,$4,$5)
	`, id, userID, hash, description, now)
	if err != nil {
		return nil, err
	}
	return &CreatedToken{
		Token: Token{ID: id, UserID: userID, Description: description, CreateAt: now},
		Plain: plain,
	}, nil
}

// ListTokens returns the user's tokens (no plaintext). Caller is responsible
// for permission checks (self or admin).
func (s *Service) ListTokens(ctx context.Context, userID string) ([]Token, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, user_id, description, create_at, last_used_at, revoked_at
		FROM personal_access_tokens
		WHERE user_id = $1
		ORDER BY create_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.UserID, &t.Description, &t.CreateAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken marks the token as revoked. No-op if already revoked or unknown.
func (s *Service) RevokeToken(ctx context.Context, tokenID string) error {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE personal_access_tokens SET revoked_at = $2
		WHERE id = $1 AND revoked_at = 0
	`, tokenID, time.Now().UnixMilli())
	return err
}

// EnableToken un-revokes a token by zeroing revoked_at. Used by the
// Mattermost compat endpoint so an admin tool's "re-enable" button works.
// Returns ErrTokenInvalid for unknown ids.
func (s *Service) EnableToken(ctx context.Context, tokenID string) error {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE personal_access_tokens SET revoked_at = 0
		WHERE id = $1
	`, tokenID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenInvalid
	}
	return nil
}

// GetToken returns a token row by id. nil + ErrTokenInvalid for unknown ids.
// Plaintext is never available — only the hash is stored.
func (s *Service) GetToken(ctx context.Context, tokenID string) (*Token, error) {
	var t Token
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, user_id, description, create_at, last_used_at, revoked_at
		FROM personal_access_tokens WHERE id = $1
	`, tokenID).Scan(&t.ID, &t.UserID, &t.Description, &t.CreateAt, &t.LastUsedAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// SearchTokens runs an admin-scope search over PAT description + user_id.
// Empty term returns the most-recent 100 rows. Caller must have admin role.
func (s *Service) SearchTokens(ctx context.Context, term string, limit int) ([]Token, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	pat := "%" + term + "%"
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, user_id, description, create_at, last_used_at, revoked_at
		FROM personal_access_tokens
		WHERE ($1 = '' OR description ILIKE $2 OR user_id = $1 OR id = $1)
		ORDER BY create_at DESC
		LIMIT $3
	`, term, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.UserID, &t.Description, &t.CreateAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ResolvedToken is the non-secret identity carried forward after a PAT has
// been authenticated. ID is the credential row identifier used for audit
// provenance; the plaintext token and its digest never leave this package.
type ResolvedToken struct {
	ID     string
	UserID string
}

// ResolveTokenCredential validates a presented plaintext PAT and returns its
// non-secret credential and owner identifiers. It also bumps last_used_at as
// a best-effort visibility signal. Returns ErrTokenInvalid if the token is
// missing, revoked, owned by an inactive user, or not found.
func (s *Service) ResolveTokenCredential(ctx context.Context, plain string) (ResolvedToken, error) {
	if plain == "" {
		return ResolvedToken{}, ErrTokenInvalid
	}
	hash := hashToken(plain)
	var resolved ResolvedToken
	var revoked int64
	err := s.db.Pool.QueryRow(ctx, resolveTokenSQL, hash).Scan(&resolved.ID, &resolved.UserID, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedToken{}, ErrTokenInvalid
	}
	if err != nil {
		return ResolvedToken{}, err
	}
	if revoked != 0 {
		return ResolvedToken{}, ErrTokenInvalid
	}
	// Best-effort last-used bump; don't fail the whole call if it errors.
	_, _ = s.db.Pool.Exec(ctx, `UPDATE personal_access_tokens SET last_used_at = $2 WHERE id = $1`, resolved.ID, time.Now().UnixMilli())
	return resolved, nil
}

// ResolveToken retains the original user-id-only API for callers that do not
// need credential provenance.
func (s *Service) ResolveToken(ctx context.Context, plain string) (string, error) {
	resolved, err := s.ResolveTokenCredential(ctx, plain)
	if err != nil {
		return "", err
	}
	return resolved.UserID, nil
}

// Keep the account-activity predicate in the token lookup itself. Resolving a
// PAT first and checking the user in a later statement would leave a race in
// which a deactivated account could authenticate between the two queries.
const resolveTokenSQL = `
		SELECT pat.id, pat.user_id, pat.revoked_at
		FROM personal_access_tokens AS pat
		JOIN users AS u ON u.id = pat.user_id AND u.delete_at = 0
		WHERE pat.token_hash = $1
`

// ---- Helpers ----

const tokenPrefix = "mdp_"

func generatePlaintextToken() (string, error) {
	// 32 random bytes → ~43 chars base64. Prefix marks it as a PAT so the
	// auth middleware can fast-path-skip JWT parsing when present.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// IsPATFormat reports whether the given bearer token looks like a PAT
// (used by the dual auth middleware to pick the lookup strategy).
func IsPATFormat(s string) bool { return strings.HasPrefix(s, tokenPrefix) }

func syntheticBotEmail(username string) string {
	// Bots can't receive mail; we still need a unique email value to
	// satisfy the UNIQUE constraint on `users.email`.
	return username + "@bot.local"
}
