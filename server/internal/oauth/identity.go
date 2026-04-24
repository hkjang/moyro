package oauth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moddle/moddle/server/internal/auth"
	"github.com/moddle/moddle/server/internal/store"
)

// ErrUnverifiedLink is returned when a provider offers an email that matches
// an existing moddle user but the provider hasn't confirmed the email. We
// refuse silent linking in that case — an attacker could claim any
// unverified email on a permissive provider.
var ErrUnverifiedLink = errors.New("oauth: refusing to link unverified email to existing account")

// usernameSafe strips characters illegal in our username column. We match
// Mattermost's rule: lowercase alnum plus . - _. Empty result means the
// email local-part gave us nothing usable and we fall back to a uuid prefix.
var usernameSafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// IdentityStore is the persistence backing for the three-branch OAuth
// resolution: existing identity row → existing email match → create fresh.
// The auth.Service reference is retained only so we can mint JWTs without
// leaking the private session-insert SQL back out.
type IdentityStore struct {
	db   *store.DB
	auth *auth.Service
}

func NewIdentityStore(db *store.DB, authSvc *auth.Service) *IdentityStore {
	return &IdentityStore{db: db, auth: authSvc}
}

// ResolveOrCreate takes a provider-normalised UserInfo and returns the
// moddle-native user plus a freshly-minted session token, creating the
// user and/or identity row as needed.
//
// Branch 1: (provider, subject) row exists → load user, return session.
// Branch 2: no identity row, but a user with matching email exists AND
//           the provider reports the email as verified → link (insert
//           identity row) and return session. Unverified → refuse.
// Branch 3: brand-new → create a user with a unique username derived from
//           the email local-part, empty password_hash (OAuth-only), insert
//           identity row, bootstrap-promote to system_admin when the
//           instance has no admins yet.
//
// The caller is responsible for any post-login side effects (default
// team/channel membership, audit logging) — keeping them out of here lets
// the function be tested without an HTTP context.
func (s *IdentityStore) ResolveOrCreate(ctx context.Context, provider string, info *UserInfo) (*auth.User, string, bool /*created*/, error) {
	if info == nil || info.Subject == "" {
		return nil, "", false, errors.New("oauth: empty subject")
	}

	// --- Branch 1: existing identity row ---
	var userID string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT user_id FROM user_identities WHERE provider=$1 AND subject=$2
	`, provider, info.Subject).Scan(&userID)
	if err == nil {
		u, err := s.auth.UserByID(ctx, userID)
		if err != nil {
			return nil, "", false, err
		}
		tok, err := s.auth.IssueSession(ctx, u.ID)
		if err != nil {
			return nil, "", false, err
		}
		return u, tok, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", false, err
	}

	// --- Branch 2: link by matching email ---
	if info.Email != "" {
		var existingID string
		err := s.db.Pool.QueryRow(ctx, `
			SELECT id FROM users WHERE email=$1 AND delete_at=0
		`, info.Email).Scan(&existingID)
		if err == nil {
			if !info.EmailVerified {
				// Refuse silent linking — force the operator to add a
				// "Connected accounts" flow later. Better to fail loud
				// than silently hand account control to a provider
				// that doesn't verify email ownership.
				return nil, "", false, ErrUnverifiedLink
			}
			if _, err := s.db.Pool.Exec(ctx, `
				INSERT INTO user_identities (user_id, provider, subject, email, create_at)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (provider, subject) DO NOTHING
			`, existingID, provider, info.Subject, info.Email, time.Now().UnixMilli()); err != nil {
				return nil, "", false, err
			}
			// Opportunistically adopt the provider's avatar when the user
			// hasn't set one locally. We only write on empty — respecting
			// an explicit self-upload that came before — and a no-op
			// update when picture is blank on both sides.
			if info.Picture != "" {
				if _, err := s.db.Pool.Exec(ctx, `
					UPDATE users SET picture=$2, update_at=$3
					WHERE id=$1 AND COALESCE(picture,'')=''
				`, existingID, info.Picture, time.Now().UnixMilli()); err != nil {
					return nil, "", false, err
				}
			}
			u, err := s.auth.UserByID(ctx, existingID)
			if err != nil {
				return nil, "", false, err
			}
			tok, err := s.auth.IssueSession(ctx, u.ID)
			if err != nil {
				return nil, "", false, err
			}
			return u, tok, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", false, err
		}
	}

	// --- Branch 3: brand-new user ---
	username, err := s.pickUsername(ctx, info)
	if err != nil {
		return nil, "", false, err
	}
	newID := uuid.NewString()
	now := time.Now().UnixMilli()
	email := info.Email
	if email == "" {
		// Synthesize a stable but obviously-fake address so the NOT NULL
		// UNIQUE constraint is satisfied. Users can correct via /profile.
		email = fmt.Sprintf("%s+%s@oauth.local", username, provider)
	}
	// password_hash = "" means this account can never sign in via the
	// password endpoint — auth.Login's bcrypt compare will always fail.
	// picture falls straight through: external URL from the provider, or
	// empty when the provider didn't supply one.
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, picture, create_at, update_at)
		VALUES ($1,$2,$3,'','system_user',$4,$5,$5)
	`, newID, username, email, info.Picture, now); err != nil {
		return nil, "", false, err
	}
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO user_identities (user_id, provider, subject, email, create_at)
		VALUES ($1,$2,$3,$4,$5)
	`, newID, provider, info.Subject, info.Email, now); err != nil {
		return nil, "", false, err
	}

	// Bootstrap: if this is the very first user, make them an admin so
	// the instance has someone to operate it. Mirrors auth.Register.
	if existed, err := s.auth.HasAnySystemAdmin(ctx); err == nil && !existed {
		_ = s.auth.PromoteSystemAdmin(ctx, newID)
	}

	u, err := s.auth.UserByID(ctx, newID)
	if err != nil {
		return nil, "", false, err
	}
	tok, err := s.auth.IssueSession(ctx, u.ID)
	if err != nil {
		return nil, "", false, err
	}
	return u, tok, true, nil
}

// pickUsername derives a unique, schema-legal username from the provider's
// email / name / subject, retrying with a numeric suffix on collision. We
// cap attempts at 30 — practically unreachable even on a chatty provider.
func (s *IdentityStore) pickUsername(ctx context.Context, info *UserInfo) (string, error) {
	base := ""
	if info.Email != "" {
		if at := strings.IndexByte(info.Email, '@'); at > 0 {
			base = strings.ToLower(info.Email[:at])
		}
	}
	if base == "" && info.Name != "" {
		base = strings.ToLower(info.Name)
	}
	base = usernameSafe.ReplaceAllString(base, "")
	if len(base) < 3 {
		// Guarantee a stable, legal minimum by prefixing with the provider.
		base = "user" + strings.ReplaceAll(info.Subject, "-", "")
	}
	if len(base) > 20 {
		base = base[:20]
	}

	candidate := base
	for i := 0; i < 30; i++ {
		var exists bool
		if err := s.db.Pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM users WHERE username=$1)
		`, candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s%d", base, i+1)
	}
	// Ridiculously unlikely fallback — append a uuid chunk.
	return base + "-" + uuid.NewString()[:8], nil
}
