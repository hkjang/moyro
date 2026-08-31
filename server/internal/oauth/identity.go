package oauth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrUnverifiedLink is returned when a provider offers an email that matches
// an existing moyro user but the provider hasn't confirmed the email. We
// refuse silent linking in that case — an attacker could claim any
// unverified email on a permissive provider.
var (
	ErrUnverifiedLink   = errors.New("oauth: refusing to link unverified email to existing account")
	ErrIdentityConflict = errors.New("oauth: provider subject is already linked to another user")
)

// usernameSafe strips characters illegal in our username column. We match
// Mattermost's rule: lowercase alnum plus . - _. Empty result means the
// email local-part gave us nothing usable and we fall back to a uuid prefix.
var usernameSafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// IdentityStore is the persistence backing for the three-branch OAuth
// resolution: existing identity row → existing email match → create fresh.
// The auth.Service reference is retained only so we can mint JWTs without
// leaking the private session-insert SQL back out.
type IdentityStore struct {
	db    *store.DB
	auth  *auth.Service
	begin func(context.Context) (identityTx, error)
}

func NewIdentityStore(db *store.DB, authSvc *auth.Service) *IdentityStore {
	return &IdentityStore{
		db: db, auth: authSvc,
		begin: func(ctx context.Context) (identityTx, error) {
			return db.Pool.Begin(ctx)
		},
	}
}

type identityExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type identityTx interface {
	identityExecutor
	Commit(context.Context) error
	Rollback(context.Context) error
}

// NormalizeEmail returns the canonical representation used by every OAuth
// identity lookup and insert.  Keeping this in the persistence package is
// important: callers which apply an "existing account only" policy must make
// exactly the same comparison as ResolveOrCreate or a whitespace/case variant
// could pass the policy check and then fall through to account creation.
func NormalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ResolveOrCreate takes provider-normalised UserInfo and returns the
// Moyro-native user plus a freshly minted session token. Browser SSO
// callbacks use ResolveOrCreateUser instead so a bearer token is created only
// after the browser exchanges its short-lived handoff code.
func (s *IdentityStore) ResolveOrCreate(ctx context.Context, provider string, info *UserInfo) (*auth.User, string, bool /*created*/, error) {
	u, created, err := s.ResolveOrCreateUser(ctx, provider, info)
	if err != nil {
		return nil, "", false, err
	}
	token, err := s.auth.IssueSession(ctx, u.ID)
	if err != nil {
		return nil, "", false, err
	}
	return u, token, created, nil
}

// ResolveOrCreateUser resolves or creates only the local identity. Keeping
// session issuance separate lets browser callbacks return an opaque one-time
// code instead of leaking the long-lived bearer credential through the URL.
//
// Branch 1: (provider, subject) row exists → load user.
// Branch 2: no identity row, but a user with matching email exists AND
//
//	the provider reports the email as verified → link (insert
//	identity row). Unverified → refuse.
//
// Branch 3: brand-new → create a user with a unique username derived from
//
//	the email local-part, empty password_hash (OAuth-only), insert
//	identity row. System administrators are created only by the
//	explicit bootstrap process.
//
// The caller is responsible for session creation and post-login side effects
// (default team/channel membership, audit logging).
func (s *IdentityStore) ResolveOrCreateUser(ctx context.Context, provider string, info *UserInfo) (*auth.User, bool /*created*/, error) {
	if info == nil || info.Subject == "" {
		return nil, false, errors.New("oauth: empty subject")
	}
	// Work on a copy so normalization cannot unexpectedly mutate a structure
	// retained by a provider implementation or by an audit caller.
	normalizedInfo := *info
	normalizedInfo.Email = NormalizeEmail(info.Email)
	info = &normalizedInfo

	// --- Branch 1: existing identity row ---
	var userID string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT user_id FROM user_identities WHERE provider=$1 AND subject=$2
	`, provider, info.Subject).Scan(&userID)
	if err == nil {
		u, err := s.auth.UserByID(ctx, userID)
		if err != nil {
			return nil, false, err
		}
		return u, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	// --- Branch 2: link by matching email ---
	if info.Email != "" {
		var existingID string
		err := s.db.Pool.QueryRow(ctx, `
			SELECT id FROM users WHERE LOWER(BTRIM(email))=$1 AND delete_at=0
		`, info.Email).Scan(&existingID)
		if err == nil {
			if !info.EmailVerified {
				// Refuse silent linking — force the operator to add a
				// "Connected accounts" flow later. Better to fail loud
				// than silently hand account control to a provider
				// that doesn't verify email ownership.
				return nil, false, ErrUnverifiedLink
			}
			if err := linkExistingIdentity(ctx, s.begin, existingID, provider, info, time.Now().UnixMilli()); err != nil {
				return nil, false, err
			}
			u, err := s.auth.UserByID(ctx, existingID)
			if err != nil {
				return nil, false, err
			}
			return u, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, err
		}
	}

	// --- Branch 3: brand-new user ---
	username, err := s.pickUsername(ctx, info)
	if err != nil {
		return nil, false, err
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
	if err := createIdentityAccount(ctx, s.begin, newID, username, email, provider, info, now); err != nil {
		return nil, false, err
	}

	u, err := s.auth.UserByID(ctx, newID)
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}

// linkExistingIdentity serializes the identity insert, ownership check, and
// optional avatar adoption in one transaction. ON CONFLICT alone is not
// sufficient: a concurrent callback may have linked the same provider subject
// to a different local account after our initial lookup. In that case we must
// not mint a session for the email-matched account.
func linkExistingIdentity(
	ctx context.Context,
	begin func(context.Context) (identityTx, error),
	userID, provider string,
	info *UserInfo,
	now int64,
) error {
	tx, err := begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_identities (user_id, provider, subject, email, create_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (provider, subject) DO NOTHING
	`, userID, provider, info.Subject, info.Email, now); err != nil {
		return err
	}
	var ownerID string
	if err := tx.QueryRow(ctx, `
		SELECT user_id FROM user_identities WHERE provider=$1 AND subject=$2
	`, provider, info.Subject).Scan(&ownerID); err != nil {
		return err
	}
	if ownerID != userID {
		return ErrIdentityConflict
	}

	// Opportunistically adopt the provider's avatar when the user has not set
	// one locally. The transaction keeps this side effect aligned with the link.
	if info.Picture != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE users SET picture=$2, update_at=$3
			WHERE id=$1 AND COALESCE(picture,'')=''
		`, userID, info.Picture, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// createIdentityAccount keeps the user and its provider identity indivisible.
// A provider-subject collision or any other second-insert failure rolls back the
// new user instead of leaving an OAuth-only orphan account behind.
func createIdentityAccount(
	ctx context.Context,
	begin func(context.Context) (identityTx, error),
	userID, username, email, provider string,
	info *UserInfo,
	now int64,
) error {
	tx, err := begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, picture, create_at, update_at)
		VALUES ($1,$2,$3,'','system_user',$4,$5,$5)
	`, userID, username, email, info.Picture, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_identities (user_id, provider, subject, email, create_at)
		VALUES ($1,$2,$3,$4,$5)
	`, userID, provider, info.Subject, info.Email, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// pickUsername derives a unique, schema-legal username from the provider's
// email / name / subject, retrying with a numeric suffix on collision. We
// cap attempts at 30 — practically unreachable even on a chatty provider.
func (s *IdentityStore) pickUsername(ctx context.Context, info *UserInfo) (string, error) {
	base := strings.ToLower(strings.TrimSpace(info.Username))
	if base == "" && info.Email != "" {
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
