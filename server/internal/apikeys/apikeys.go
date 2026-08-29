// Package apikeys issues scoped, rotating credentials for users, service
// accounts, and MCP clients. Plaintext secrets are returned exactly once and
// only a keyed digest reaches PostgreSQL.
package apikeys

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/rbac"
)

const (
	SecretPrefix  = "moyro_"
	digestPurpose = "moyro/api-key/v1"

	KindUser    = "user"
	KindService = "service"
	KindMCP     = "mcp"

	StatusActive   = "active"
	StatusRetiring = "retiring"
	StatusRevoked  = "revoked"
)

var (
	ErrInvalidCredential = errors.New("apikeys: invalid credential")
	ErrNotFound          = errors.New("apikeys: key not found")
	ErrInvalidRequest    = errors.New("apikeys: invalid request")
	ErrGrantDenied       = errors.New("apikeys: requested grant exceeds owner permissions")
	ErrRotationConflict  = errors.New("apikeys: key is not active or was already rotated")
	ErrRevisionConflict  = errors.New("apikeys: revision conflict")
)

// ValidName accepts readable internationalized labels while rejecting empty,
// overlong, or control-character-bearing values. Key names are display-only;
// they are never used as filesystem paths or credential identifiers.
func ValidName(value string) bool {
	value = strings.TrimSpace(value)
	count := utf8.RuneCountInString(value)
	if count < 1 || count > 80 {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

type Digester interface {
	Digest(purpose string, secret []byte) ([]byte, error)
}

type Constraints struct {
	TeamIDs    []string `json:"team_ids,omitempty"`
	ChannelIDs []string `json:"channel_ids,omitempty"`
}

type GrantValidator interface {
	ValidateKeyGrant(ctx context.Context, ownerID string, permissions []string, constraints Constraints) error
}

type Key struct {
	ID              string      `json:"id"`
	OwnerUserID     string      `json:"user_id"`
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	Prefix          string      `json:"prefix"`
	Kind            string      `json:"kind"`
	Status          string      `json:"status"`
	Permissions     []string    `json:"permissions"`
	Constraints     Constraints `json:"constraints"`
	ExpiresAt       int64       `json:"expires_at"`
	ValidUntil      int64       `json:"valid_until"`
	RotationGroupID string      `json:"rotation_group_id"`
	Version         int         `json:"version"`
	RotatedFromID   string      `json:"rotated_from_id,omitempty"`
	CreatedBy       string      `json:"created_by"`
	CreateAt        int64       `json:"create_at"`
	UpdateAt        int64       `json:"update_at"`
	LastUsedAt      int64       `json:"last_used_at"`
	RevokedAt       int64       `json:"revoked_at"`
	Revision        int64       `json:"revision"`
}

type Created struct {
	Key    Key    `json:"key"`
	Secret string `json:"secret"`
}

type CreateRequest struct {
	OwnerUserID string
	CreatedBy   string
	Name        string
	Description string
	Kind        string
	Permissions []string
	Constraints Constraints
	ExpiresAt   int64
}

type Repository interface {
	Create(ctx context.Context, key Key, digest []byte) (Key, error)
	GetForOwner(ctx context.Context, keyID, ownerID string) (Key, error)
	ListForOwner(ctx context.Context, ownerID string) ([]Key, error)
	ResolveByDigest(ctx context.Context, digest []byte) (Key, error)
	Rotate(ctx context.Context, oldKeyID, ownerID string, replacement Key, replacementDigest []byte, graceUntil, now int64) (old Key, created Key, err error)
	ReplacePermissions(ctx context.Context, keyID, ownerID string, permissions []string, constraints Constraints, updateAt int64, expectedRevision *int64) (Key, error)
	Revoke(ctx context.Context, keyID, ownerID string, revokedAt int64) error
	MarkUsed(ctx context.Context, keyID string, usedAt int64) error
}

type Options struct {
	DefaultTTL           time.Duration
	MaxTTL               time.Duration
	DefaultRotationGrace time.Duration
	MaxRotationGrace     time.Duration
	Now                  func() time.Time
	Random               io.Reader
}

func DefaultOptions() Options {
	return Options{
		DefaultTTL:           90 * 24 * time.Hour,
		MaxTTL:               365 * 24 * time.Hour,
		DefaultRotationGrace: 5 * time.Minute,
		MaxRotationGrace:     24 * time.Hour,
		Now:                  time.Now,
		Random:               rand.Reader,
	}
}

type Service struct {
	repo      Repository
	digester  Digester
	validator GrantValidator
	opts      Options
}

func New(repo Repository, digester Digester, validator GrantValidator, opts Options) (*Service, error) {
	if repo == nil || digester == nil || validator == nil {
		return nil, errors.New("apikeys: repository, digester, and grant validator are required")
	}
	defaults := DefaultOptions()
	if opts.DefaultTTL == 0 {
		opts.DefaultTTL = defaults.DefaultTTL
	}
	if opts.MaxTTL == 0 {
		opts.MaxTTL = defaults.MaxTTL
	}
	if opts.DefaultRotationGrace == 0 {
		opts.DefaultRotationGrace = defaults.DefaultRotationGrace
	}
	if opts.MaxRotationGrace == 0 {
		opts.MaxRotationGrace = defaults.MaxRotationGrace
	}
	if opts.Now == nil {
		opts.Now = defaults.Now
	}
	if opts.Random == nil {
		opts.Random = defaults.Random
	}
	if opts.DefaultTTL < time.Minute || opts.MaxTTL < opts.DefaultTTL || opts.MaxRotationGrace < 0 || opts.DefaultRotationGrace < 0 || opts.DefaultRotationGrace > opts.MaxRotationGrace {
		return nil, errors.New("apikeys: invalid options")
	}
	return &Service{repo: repo, digester: digester, validator: validator, opts: opts}, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (Created, error) {
	request.OwnerUserID = strings.TrimSpace(request.OwnerUserID)
	request.CreatedBy = strings.TrimSpace(request.CreatedBy)
	request.Name = strings.TrimSpace(request.Name)
	request.Kind = strings.ToLower(strings.TrimSpace(request.Kind))
	if request.Kind == "" {
		request.Kind = KindUser
	}
	request.Permissions = canonicalStrings(request.Permissions)
	request.Constraints = canonicalConstraints(request.Constraints)
	if err := validateCreateRequest(request); err != nil {
		return Created{}, err
	}
	if err := s.validator.ValidateKeyGrant(ctx, request.OwnerUserID, request.Permissions, request.Constraints); err != nil {
		if errors.Is(err, ErrGrantDenied) {
			return Created{}, err
		}
		return Created{}, fmt.Errorf("apikeys: validate grant: %w", err)
	}

	now := s.opts.Now()
	if request.ExpiresAt == 0 {
		request.ExpiresAt = now.Add(s.opts.DefaultTTL).UnixMilli()
	}
	if request.ExpiresAt <= now.UnixMilli() || request.ExpiresAt > now.Add(s.opts.MaxTTL).UnixMilli() {
		return Created{}, fmt.Errorf("%w: expires_at outside allowed range", ErrInvalidRequest)
	}
	secret, digest, err := s.generateSecret()
	if err != nil {
		return Created{}, err
	}
	id := uuid.NewString()
	key := Key{
		ID: id, OwnerUserID: request.OwnerUserID, Name: request.Name,
		Description: request.Description, Prefix: displayPrefix(secret), Kind: request.Kind,
		Status: StatusActive, Permissions: request.Permissions, Constraints: request.Constraints,
		ExpiresAt: request.ExpiresAt, RotationGroupID: id, Version: 1,
		CreatedBy: request.CreatedBy, CreateAt: now.UnixMilli(), UpdateAt: now.UnixMilli(), Revision: 1,
	}
	created, err := s.repo.Create(ctx, key, digest)
	if err != nil {
		return Created{}, err
	}
	return Created{Key: created, Secret: secret}, nil
}

func (s *Service) List(ctx context.Context, ownerID string) ([]Key, error) {
	if strings.TrimSpace(ownerID) == "" {
		return nil, ErrInvalidRequest
	}
	return s.repo.ListForOwner(ctx, ownerID)
}

// Resolve returns a restricted RBAC principal. All invalid states deliberately
// collapse to ErrInvalidCredential to avoid a token-state oracle.
func (s *Service) Resolve(ctx context.Context, plaintext string) (rbac.Principal, Key, error) {
	if !validSecretFormat(plaintext) {
		return rbac.Principal{}, Key{}, ErrInvalidCredential
	}
	digest, err := s.digester.Digest(digestPurpose, []byte(plaintext))
	if err != nil {
		return rbac.Principal{}, Key{}, ErrInvalidCredential
	}
	key, err := s.repo.ResolveByDigest(ctx, digest)
	if err != nil || !keyUsableAt(key, s.opts.Now().UnixMilli()) {
		return rbac.Principal{}, Key{}, ErrInvalidCredential
	}
	// Last-used visibility is best-effort and must never turn a valid request
	// into an authentication failure.
	_ = s.repo.MarkUsed(ctx, key.ID, s.opts.Now().UnixMilli())
	return principalFor(key), key, nil
}

// ResolveCurrent validates a persisted credential reference without needing
// the plaintext secret again. It is intended for deferred operations (for
// example, an approved MCP action) that must prove the exact key used to
// submit the request is still usable and apply its current permissions and
// resource constraints. Invalid owner/key pairs, revoked keys, and expired
// keys deliberately collapse to ErrInvalidCredential.
func (s *Service) ResolveCurrent(ctx context.Context, ownerID, keyID string) (rbac.Principal, Key, error) {
	ownerID = strings.TrimSpace(ownerID)
	keyID = strings.TrimSpace(keyID)
	if ownerID == "" || keyID == "" {
		return rbac.Principal{}, Key{}, ErrInvalidCredential
	}
	key, err := s.repo.GetForOwner(ctx, keyID, ownerID)
	if err != nil || !keyUsableAt(key, s.opts.Now().UnixMilli()) {
		return rbac.Principal{}, Key{}, ErrInvalidCredential
	}
	return principalFor(key), key, nil
}

func (s *Service) Rotate(ctx context.Context, ownerID, keyID, createdBy string, grace time.Duration) (Created, Key, error) {
	ownerID = strings.TrimSpace(ownerID)
	keyID = strings.TrimSpace(keyID)
	createdBy = strings.TrimSpace(createdBy)
	if ownerID == "" || keyID == "" || createdBy == "" {
		return Created{}, Key{}, ErrInvalidRequest
	}
	if grace == 0 {
		grace = s.opts.DefaultRotationGrace
	}
	if grace < 0 || grace > s.opts.MaxRotationGrace {
		return Created{}, Key{}, fmt.Errorf("%w: rotation grace outside allowed range", ErrInvalidRequest)
	}
	old, err := s.repo.GetForOwner(ctx, keyID, ownerID)
	if err != nil {
		return Created{}, Key{}, err
	}
	if old.Status != StatusActive || old.ExpiresAt <= s.opts.Now().UnixMilli() {
		return Created{}, Key{}, ErrRotationConflict
	}
	secret, digest, err := s.generateSecret()
	if err != nil {
		return Created{}, Key{}, err
	}
	now := s.opts.Now()
	replacement := old
	replacement.ID = uuid.NewString()
	replacement.Prefix = displayPrefix(secret)
	replacement.Status = StatusActive
	replacement.ValidUntil = 0
	replacement.RotationGroupID = firstNonEmpty(old.RotationGroupID, old.ID)
	replacement.Version = old.Version + 1
	replacement.RotatedFromID = old.ID
	replacement.CreatedBy = createdBy
	replacement.CreateAt = now.UnixMilli()
	replacement.UpdateAt = now.UnixMilli()
	replacement.LastUsedAt = 0
	replacement.RevokedAt = 0
	replacement.Revision = 1
	retired, created, err := s.repo.Rotate(ctx, old.ID, ownerID, replacement, digest, now.Add(grace).UnixMilli(), now.UnixMilli())
	if err != nil {
		return Created{}, Key{}, err
	}
	return Created{Key: created, Secret: secret}, retired, nil
}

func (s *Service) ReplacePermissions(ctx context.Context, ownerID, keyID string, permissions []string, constraints Constraints, expectedRevision *int64) (Key, error) {
	ownerID = strings.TrimSpace(ownerID)
	keyID = strings.TrimSpace(keyID)
	permissions = canonicalStrings(permissions)
	constraints = canonicalConstraints(constraints)
	if ownerID == "" || keyID == "" || len(permissions) > 128 || !validConstraintIDs(constraints.TeamIDs) || !validConstraintIDs(constraints.ChannelIDs) {
		return Key{}, ErrInvalidRequest
	}
	if err := s.validator.ValidateKeyGrant(ctx, ownerID, permissions, constraints); err != nil {
		return Key{}, err
	}
	return s.repo.ReplacePermissions(ctx, keyID, ownerID, permissions, constraints, s.opts.Now().UnixMilli(), expectedRevision)
}

func (s *Service) Revoke(ctx context.Context, ownerID, keyID string) error {
	if ownerID == "" || keyID == "" {
		return ErrInvalidRequest
	}
	return s.repo.Revoke(ctx, keyID, ownerID, s.opts.Now().UnixMilli())
}

func (s *Service) generateSecret() (string, []byte, error) {
	random := make([]byte, 32)
	if _, err := io.ReadFull(s.opts.Random, random); err != nil {
		return "", nil, fmt.Errorf("apikeys: generate secret: %w", err)
	}
	plaintext := SecretPrefix + base64.RawURLEncoding.EncodeToString(random)
	digest, err := s.digester.Digest(digestPurpose, []byte(plaintext))
	if err != nil {
		return "", nil, fmt.Errorf("apikeys: digest secret: %w", err)
	}
	return plaintext, digest, nil
}

func validateCreateRequest(request CreateRequest) error {
	if request.OwnerUserID == "" || request.CreatedBy == "" || !ValidName(request.Name) || len(request.Description) > 500 {
		return ErrInvalidRequest
	}
	switch request.Kind {
	case KindUser, KindService, KindMCP:
	default:
		return ErrInvalidRequest
	}
	if len(request.Permissions) == 0 || len(request.Permissions) > 128 {
		return ErrInvalidRequest
	}
	if !validConstraintIDs(request.Constraints.TeamIDs) || !validConstraintIDs(request.Constraints.ChannelIDs) {
		return ErrInvalidRequest
	}
	return nil
}

func validConstraintIDs(ids []string) bool {
	if len(ids) > 256 {
		return false
	}
	for _, id := range ids {
		if len(id) == 0 || len(id) > 128 {
			return false
		}
	}
	return true
}

func validSecretFormat(secret string) bool {
	if !strings.HasPrefix(secret, SecretPrefix) || len(secret) != len(SecretPrefix)+43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(secret, SecretPrefix))
	return err == nil && len(decoded) == 32
}

func keyUsableAt(key Key, now int64) bool {
	if key.ExpiresAt != 0 && key.ExpiresAt <= now {
		return false
	}
	switch key.Status {
	case StatusActive:
		return key.RevokedAt == 0
	case StatusRetiring:
		return key.RevokedAt == 0 && key.ValidUntil > now
	default:
		return false
	}
}

func principalFor(key Key) rbac.Principal {
	principal := rbac.Principal{
		UserID: key.OwnerUserID, CredentialID: key.ID, Restricted: true,
		GrantedPermissions: make(map[string]struct{}, len(key.Permissions)),
		AllowedTeamIDs:     make(map[string]struct{}, len(key.Constraints.TeamIDs)),
		AllowedChannelIDs:  make(map[string]struct{}, len(key.Constraints.ChannelIDs)),
	}
	for _, permission := range key.Permissions {
		principal.GrantedPermissions[permission] = struct{}{}
	}
	for _, id := range key.Constraints.TeamIDs {
		principal.AllowedTeamIDs[id] = struct{}{}
	}
	for _, id := range key.Constraints.ChannelIDs {
		principal.AllowedChannelIDs[id] = struct{}{}
	}
	return principal
}

func canonicalConstraints(c Constraints) Constraints {
	c.TeamIDs = canonicalStrings(c.TeamIDs)
	c.ChannelIDs = canonicalStrings(c.ChannelIDs)
	return c
}

func canonicalStrings(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func displayPrefix(secret string) string {
	const visible = 14
	if len(secret) <= visible {
		return secret
	}
	return secret[:visible]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// RBACGrantValidator checks every requested grant against the owner's current
// global or resource-scoped authority.
type RBACGrantValidator struct{ Authorizer *rbac.Service }

func (v RBACGrantValidator) ValidateKeyGrant(ctx context.Context, ownerID string, permissions []string, constraints Constraints) error {
	if v.Authorizer == nil {
		return ErrGrantDenied
	}
	principal := rbac.UserPrincipal(ownerID)
	scopes := []rbac.Scope{{}}
	if len(constraints.TeamIDs) != 0 || len(constraints.ChannelIDs) != 0 {
		scopes = scopes[:0]
		for _, teamID := range constraints.TeamIDs {
			scopes = append(scopes, rbac.Scope{TeamID: teamID})
		}
		for _, channelID := range constraints.ChannelIDs {
			scopes = append(scopes, rbac.Scope{ChannelID: channelID})
		}
	}
	for _, permission := range permissions {
		for _, scope := range scopes {
			ok, err := v.Authorizer.Allowed(ctx, principal, permission, scope)
			if err != nil {
				return err
			}
			if !ok {
				return ErrGrantDenied
			}
		}
	}
	return nil
}
