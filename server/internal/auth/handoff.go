package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInvalidLoginHandoff = errors.New("login handoff is invalid or expired")

const (
	loginHandoffBytes        = 32
	loginHandoffTTL          = 5 * time.Minute
	loginHandoffCleanupLimit = 256
)

type LoginHandoff struct {
	Code           string
	BrowserBinding string
	ExpiresAt      int64
}

// CreateLoginHandoff creates a short-lived browser handoff after an external
// identity has been resolved. The browser receives the random code while
// an independent HttpOnly cookie binds the exchange to the browser which
// completed the provider flow. PostgreSQL stores only their SHA-256 digests,
// so neither URLs nor the database contain a reusable Moyro session token.
func (s *Service) CreateLoginHandoff(ctx context.Context, userID string) (LoginHandoff, error) {
	if s == nil || s.db == nil || s.db.Pool == nil {
		return LoginHandoff{}, errors.New("auth: login handoff store is unavailable")
	}

	// Cleanup is opportunistic and bounded. A cleanup failure must not turn a
	// valid provider callback into a failed login; the indexed rows are retried
	// on a later callback.
	_, _ = s.db.Pool.Exec(ctx, `
		DELETE FROM login_handoffs
		WHERE code_hash IN (
			SELECT code_hash
			FROM login_handoffs
			WHERE expires_at <= $1
			ORDER BY expires_at, code_hash
			LIMIT $2
		)
	`, time.Now().UnixMilli(), loginHandoffCleanupLimit)

	code, codeDigest, err := newLoginHandoffSecret()
	if err != nil {
		return LoginHandoff{}, fmt.Errorf("auth: generate login handoff code: %w", err)
	}
	binding, bindingDigest, err := newLoginHandoffSecret()
	if err != nil {
		return LoginHandoff{}, fmt.Errorf("auth: generate login handoff binding: %w", err)
	}
	now := time.Now()
	expiresAt := now.Add(loginHandoffTTL).UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		INSERT INTO login_handoffs (code_hash, binding_hash, user_id, expires_at, create_at)
		SELECT $1, $2, id, $4, $5
		FROM users
		WHERE id=$3 AND delete_at=0
	`, codeDigest[:], bindingDigest[:], userID, expiresAt, now.UnixMilli())
	if err != nil {
		return LoginHandoff{}, fmt.Errorf("auth: store login handoff: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return LoginHandoff{}, ErrNotFound
	}
	return LoginHandoff{Code: code, BrowserBinding: binding, ExpiresAt: expiresAt}, nil
}

// ExchangeLoginHandoff consumes one browser code and creates its session in
// the same database transaction. A transient session insert/commit failure
// rolls the code deletion back, while a successful exchange cannot be replayed.
func (s *Service) ExchangeLoginHandoff(ctx context.Context, code, browserBinding string) (*User, string, error) {
	if s == nil || s.db == nil || s.db.Pool == nil {
		return nil, "", errors.New("auth: login handoff store is unavailable")
	}
	digest, ok := loginHandoffDigest(code)
	if !ok {
		return nil, "", ErrInvalidLoginHandoff
	}
	bindingDigest, ok := loginHandoffDigest(browserBinding)
	if !ok {
		return nil, "", ErrInvalidLoginHandoff
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	var userID string
	err = tx.QueryRow(ctx, `
		DELETE FROM login_handoffs
		WHERE code_hash=$1 AND binding_hash=$2 AND expires_at>$3
		RETURNING user_id
	`, digest[:], bindingDigest[:], time.Now().UnixMilli()).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrInvalidLoginHandoff
	}
	if err != nil {
		return nil, "", err
	}

	u, err := scanUser(tx.QueryRow(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE id=$1 AND delete_at=0
		FOR SHARE
	`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrInvalidLoginHandoff
	}
	if err != nil {
		return nil, "", err
	}

	issued, err := s.issueToken(userID)
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token, jti_hash, expires_at, create_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, uuid.NewString(), userID, issued.Token, issued.JTIHash, issued.ExpiresAt, now.UnixMilli()); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return u, issued.Token, nil
}

func loginHandoffDigest(code string) ([sha256.Size]byte, bool) {
	var zero [sha256.Size]byte
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) != loginHandoffBytes {
		return zero, false
	}
	return sha256.Sum256(raw), true
}

func newLoginHandoffSecret() (string, [sha256.Size]byte, error) {
	raw := make([]byte, loginHandoffBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", [sha256.Size]byte{}, err
	}
	return base64.RawURLEncoding.EncodeToString(raw), sha256.Sum256(raw), nil
}
