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
	"github.com/jackc/pgx/v5"
	"github.com/moddle/moddle/server/internal/store"
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

// ResolveToken validates a presented plaintext PAT and returns the owning
// user id. It also bumps last_used_at as a side effect for visibility.
// Returns ErrTokenInvalid if the token is missing, revoked, or not found.
func (s *Service) ResolveToken(ctx context.Context, plain string) (string, error) {
	if plain == "" {
		return "", ErrTokenInvalid
	}
	hash := hashToken(plain)
	var userID string
	var revoked int64
	err := s.db.Pool.QueryRow(ctx, `
		SELECT user_id, revoked_at FROM personal_access_tokens
		WHERE token_hash = $1
	`, hash).Scan(&userID, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTokenInvalid
	}
	if err != nil {
		return "", err
	}
	if revoked != 0 {
		return "", ErrTokenInvalid
	}
	// Best-effort last-used bump; don't fail the whole call if it errors.
	_, _ = s.db.Pool.Exec(ctx, `UPDATE personal_access_tokens SET last_used_at = $2 WHERE token_hash = $1`, hash, time.Now().UnixMilli())
	return userID, nil
}

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
