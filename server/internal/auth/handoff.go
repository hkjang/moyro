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
	loginHandoffRetryWindow  = 60 * time.Second
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
// the same database transaction. A transient insert/commit failure leaves the
// handoff pending. After commit, the same browser binding can recover the exact
// same session for a short window, covering a lost HTTP response without
// minting duplicate sessions or making the handoff portable to another browser.
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
	var sessionID *string
	var exchangedAt *int64
	now := time.Now()
	err = tx.QueryRow(ctx, `
		SELECT user_id, session_id, exchanged_at
		FROM login_handoffs
		WHERE code_hash=$1 AND binding_hash=$2 AND expires_at>$3
		FOR UPDATE
	`, digest[:], bindingDigest[:], now.UnixMilli()).Scan(&userID, &sessionID, &exchangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrInvalidLoginHandoff
	}
	if err != nil {
		return nil, "", err
	}
	if (sessionID == nil) != (exchangedAt == nil) {
		return nil, "", errors.New("auth: login handoff completion is inconsistent")
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

	var token string
	if sessionID != nil {
		if now.UnixMilli()-*exchangedAt > loginHandoffRetryWindow.Milliseconds() {
			return nil, "", ErrInvalidLoginHandoff
		}
		err = tx.QueryRow(ctx, `
			SELECT token FROM sessions
			WHERE id=$1 AND user_id=$2 AND expires_at>$3
		`, *sessionID, userID, now.UnixMilli()).Scan(&token)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrInvalidLoginHandoff
		}
		if err != nil {
			return nil, "", err
		}
	} else {
		issued, err := s.issueToken(userID)
		if err != nil {
			return nil, "", err
		}
		newSessionID := uuid.NewString()
		if _, err := tx.Exec(ctx, `
			INSERT INTO sessions (id, user_id, token, jti_hash, expires_at, create_at)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, newSessionID, userID, issued.Token, issued.JTIHash, issued.ExpiresAt, now.UnixMilli()); err != nil {
			return nil, "", err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE login_handoffs SET session_id=$2, exchanged_at=$3
			WHERE code_hash=$1
		`, digest[:], newSessionID, now.UnixMilli()); err != nil {
			return nil, "", err
		}
		token = issued.Token
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return u, token, nil
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
