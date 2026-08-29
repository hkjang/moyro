// Package bootstrap performs the one-time creation or adoption of the
// environment-designated system administrator. Completion is persisted so a
// stale bootstrap password can never reset an account on restart.
package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrExistingInstallation = errors.New("bootstrap: users exist but BOOTSTRAP_ADMIN does not identify one of them")
	ErrInactiveAdmin        = errors.New("bootstrap: designated administrator is deactivated")
	ErrInvalidEmail         = errors.New("bootstrap: administrator must be a plain email address")
	ErrWeakPassword         = errors.New("bootstrap: administrator password must contain at least 12 bytes")
	ErrPasswordTooLong      = errors.New("bootstrap: administrator password must contain at most 72 bytes")
)

// Stable, application-specific PostgreSQL advisory lock key.
const bootstrapAdvisoryLock int64 = 0x4d4f59524f // "MOYRO"

type Result struct {
	AdminUserID     string
	Email           string
	Username        string
	Created         bool
	Promoted        bool
	AlreadyComplete bool
}

type Service struct {
	db  *store.DB
	now func() time.Time
}

func New(db *store.DB) (*Service, error) {
	if db == nil || db.Pool == nil {
		return nil, errors.New("bootstrap: nil database")
	}
	return &Service{db: db, now: time.Now}, nil
}

// EnsureAdmin is concurrency-safe across multiple starting containers. If a
// completion marker exists, it returns without reading or hashing password. If
// users predate the marker, only an exact email match may be adopted.
func (s *Service) EnsureAdmin(ctx context.Context, email, password string) (Result, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if err := validateEmail(email); err != nil {
		return Result{}, err
	}
	if len(password) < 12 {
		return Result{}, ErrWeakPassword
	}
	if len(password) > 72 {
		return Result{}, ErrPasswordTooLong
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, bootstrapAdvisoryLock); err != nil {
		return Result{}, fmt.Errorf("bootstrap: acquire lock: %w", err)
	}

	var completedID string
	err = tx.QueryRow(ctx, `SELECT admin_user_id FROM bootstrap_state WHERE id=1`).Scan(&completedID)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Result{}, err
		}
		return Result{AdminUserID: completedID, AlreadyComplete: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, fmt.Errorf("bootstrap: read completion marker: %w", err)
	}

	result := Result{Email: email}
	var roles string
	var deleteAt int64
	err = tx.QueryRow(ctx, `
		SELECT id, username, roles, delete_at
		FROM users WHERE LOWER(email)=LOWER($1)
	`, email).Scan(&result.AdminUserID, &result.Username, &roles, &deleteAt)
	switch {
	case err == nil:
		if deleteAt != 0 {
			return Result{}, ErrInactiveAdmin
		}
		updatedRoles, changed := ensureAdminRoles(roles)
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE users SET roles=$2, update_at=$3 WHERE id=$1`, result.AdminUserID, updatedRoles, s.now().UnixMilli()); err != nil {
				return Result{}, fmt.Errorf("bootstrap: promote existing administrator: %w", err)
			}
			result.Promoted = true
		}
	case errors.Is(err, pgx.ErrNoRows):
		var count int64
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
			return Result{}, fmt.Errorf("bootstrap: count users: %w", err)
		}
		if count != 0 {
			return Result{}, ErrExistingInstallation
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return Result{}, fmt.Errorf("bootstrap: hash password: %w", err)
		}
		result.AdminUserID = uuid.NewString()
		result.Username = UsernameFromEmail(email)
		now := s.now().UnixMilli()
		if _, err := tx.Exec(ctx, `
			INSERT INTO users
			    (id, username, email, password_hash, roles, create_at, update_at)
			VALUES ($1,$2,$3,$4,'system_user system_admin',$5,$5)
		`, result.AdminUserID, result.Username, email, string(hash), now); err != nil {
			return Result{}, fmt.Errorf("bootstrap: create administrator: %w", err)
		}
		result.Created = true
	default:
		return Result{}, fmt.Errorf("bootstrap: lookup administrator: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO bootstrap_state (id, admin_user_id, completed_at)
		VALUES (1,$1,$2)
	`, result.AdminUserID, s.now().UnixMilli()); err != nil {
		return Result{}, fmt.Errorf("bootstrap: save completion marker: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateEmail(email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || !strings.EqualFold(addr.Address, email) {
		return ErrInvalidEmail
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" {
		return ErrInvalidEmail
	}
	return nil
}

var unsafeUsername = regexp.MustCompile(`[^a-z0-9._-]+`)
var repeatedSeparators = regexp.MustCompile(`[-_.]{2,}`)

// UsernameFromEmail deterministically creates a Mattermost-safe bootstrap
// username. The email remains the canonical BOOTSTRAP_ADMIN identity.
func UsernameFromEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	local, _, _ := strings.Cut(email, "@")
	name := unsafeUsername.ReplaceAllString(local, "-")
	name = repeatedSeparators.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-_.")
	if len(name) > 30 {
		name = strings.Trim(name[:30], "-_.")
	}
	if len(name) < 3 {
		sum := sha256.Sum256([]byte(email))
		name = "admin-" + hex.EncodeToString(sum[:4])
	}
	return name
}

func ensureAdminRoles(roles string) (string, bool) {
	set := map[string]struct{}{}
	for _, role := range strings.Fields(roles) {
		set[role] = struct{}{}
	}
	before := len(set)
	set["system_user"] = struct{}{}
	set["system_admin"] = struct{}{}
	items := make([]string, 0, len(set))
	for role := range set {
		items = append(items, role)
	}
	sort.Strings(items)
	canonical := strings.Join(items, " ")
	return canonical, len(set) != before || strings.Join(strings.Fields(roles), " ") != canonical
}
