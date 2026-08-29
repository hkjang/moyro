package apikeys

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/secrets"
)

type allowValidator struct{ err error }

func (v allowValidator) ValidateKeyGrant(context.Context, string, []string, Constraints) error {
	return v.err
}

type memoryRepository struct {
	mu      sync.Mutex
	keys    map[string]Key
	digests map[string]string
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{keys: map[string]Key{}, digests: map[string]string{}}
}

func (r *memoryRepository) Create(_ context.Context, key Key, digest []byte) (Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[key.ID] = cloneKey(key)
	r.digests[string(digest)] = key.ID
	return cloneKey(key), nil
}

func (r *memoryRepository) GetForOwner(_ context.Context, keyID, ownerID string) (Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[keyID]
	if !ok || key.OwnerUserID != ownerID {
		return Key{}, ErrNotFound
	}
	return cloneKey(key), nil
}

func (r *memoryRepository) ListForOwner(_ context.Context, ownerID string) ([]Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Key{}
	for _, key := range r.keys {
		if key.OwnerUserID == ownerID {
			out = append(out, cloneKey(key))
		}
	}
	return out, nil
}

func (r *memoryRepository) ResolveByDigest(_ context.Context, digest []byte) (Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.digests[string(digest)]
	if !ok {
		return Key{}, ErrInvalidCredential
	}
	return cloneKey(r.keys[id]), nil
}

func (r *memoryRepository) Rotate(_ context.Context, oldKeyID, ownerID string, replacement Key, replacementDigest []byte, graceUntil, now int64) (Key, Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.keys[oldKeyID]
	if !ok || old.OwnerUserID != ownerID {
		return Key{}, Key{}, ErrNotFound
	}
	if old.Status != StatusActive {
		return Key{}, Key{}, ErrRotationConflict
	}
	old.Status = StatusRetiring
	old.ValidUntil = graceUntil
	old.UpdateAt = now
	old.Revision++
	r.keys[old.ID] = cloneKey(old)
	r.keys[replacement.ID] = cloneKey(replacement)
	r.digests[string(replacementDigest)] = replacement.ID
	return cloneKey(old), cloneKey(replacement), nil
}

func (r *memoryRepository) ReplacePermissions(_ context.Context, keyID, ownerID string, permissions []string, constraints Constraints, updateAt int64, expected *int64) (Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[keyID]
	if !ok || key.OwnerUserID != ownerID {
		return Key{}, ErrNotFound
	}
	if expected != nil && *expected != key.Revision {
		return Key{}, ErrRevisionConflict
	}
	key.Permissions = append([]string(nil), permissions...)
	key.Constraints = constraints
	key.UpdateAt = updateAt
	key.Revision++
	r.keys[keyID] = cloneKey(key)
	return cloneKey(key), nil
}

func (r *memoryRepository) Revoke(_ context.Context, keyID, ownerID string, at int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[keyID]
	if !ok || key.OwnerUserID != ownerID || key.Status == StatusRevoked {
		return ErrNotFound
	}
	key.Status = StatusRevoked
	key.RevokedAt = at
	r.keys[keyID] = key
	return nil
}

func (r *memoryRepository) MarkUsed(_ context.Context, keyID string, at int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.keys[keyID]
	key.LastUsedAt = at
	r.keys[keyID] = key
	return nil
}

func cloneKey(key Key) Key {
	key.Permissions = append([]string(nil), key.Permissions...)
	key.Constraints.TeamIDs = append([]string(nil), key.Constraints.TeamIDs...)
	key.Constraints.ChannelIDs = append([]string(nil), key.Constraints.ChannelIDs...)
	return key
}

func testService(t *testing.T, validator GrantValidator) (*Service, *memoryRepository, *time.Time) {
	t.Helper()
	digester, err := secrets.New(bytes.Repeat([]byte{0x35}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repo := newMemoryRepository()
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	service, err := New(repo, digester, validator, Options{
		DefaultTTL: 24 * time.Hour, MaxTTL: 30 * 24 * time.Hour,
		DefaultRotationGrace: 5 * time.Minute, MaxRotationGrace: time.Hour,
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(deterministicRandom()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, repo, &now
}

func deterministicRandom() []byte {
	var out []byte
	for value := byte(1); value <= 16; value++ {
		out = append(out, bytes.Repeat([]byte{value}, 32)...)
	}
	return out
}

func createTestKey(t *testing.T, service *Service) Created {
	t.Helper()
	created, err := service.Create(context.Background(), CreateRequest{
		OwnerUserID: "user-1", CreatedBy: "user-1", Name: "MCP workstation", Kind: KindMCP,
		Permissions: []string{rbac.PermissionMCPWrite, rbac.PermissionMCPRead, rbac.PermissionMCPRead},
		Constraints: Constraints{TeamIDs: []string{"team-2", "team-1", "team-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestCreateReturnsSecretOnceAndResolveBuildsRestrictedPrincipal(t *testing.T) {
	service, _, _ := testService(t, allowValidator{})
	created := createTestKey(t, service)
	if !validSecretFormat(created.Secret) || created.Key.Prefix == created.Secret {
		t.Fatalf("created secret/prefix invalid: %#v", created)
	}
	if !reflect.DeepEqual(created.Key.Permissions, []string{rbac.PermissionMCPRead, rbac.PermissionMCPWrite}) {
		t.Fatalf("permissions = %#v", created.Key.Permissions)
	}
	principal, resolved, err := service.Resolve(context.Background(), created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.Restricted || principal.UserID != "user-1" || principal.CredentialID != created.Key.ID {
		t.Fatalf("principal = %#v", principal)
	}
	if _, ok := principal.AllowedTeamIDs["team-1"]; !ok || resolved.LastUsedAt != 0 {
		// Resolve returns the lookup snapshot; MarkUsed is deliberately best-effort.
		t.Fatalf("resolved/principal = %#v / %#v", resolved, principal)
	}
	listed, err := service.List(context.Background(), "user-1")
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
}

func TestRotationGraceAndRevocation(t *testing.T) {
	service, _, now := testService(t, allowValidator{})
	old := createTestKey(t, service)
	replacement, retired, err := service.Rotate(context.Background(), "user-1", old.Key.ID, "user-1", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if retired.Status != StatusRetiring || retired.ValidUntil == 0 || replacement.Secret == old.Secret || replacement.Key.Version != 2 {
		t.Fatalf("rotation = replacement %#v, retired %#v", replacement, retired)
	}
	if _, _, err := service.Resolve(context.Background(), old.Secret); err != nil {
		t.Fatalf("old key rejected during grace: %v", err)
	}
	*now = now.Add(6 * time.Minute)
	if _, _, err := service.Resolve(context.Background(), old.Secret); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expired retiring key error = %v", err)
	}
	if _, _, err := service.Resolve(context.Background(), replacement.Secret); err != nil {
		t.Fatalf("replacement key rejected: %v", err)
	}
	if err := service.Revoke(context.Background(), "user-1", replacement.Key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Resolve(context.Background(), replacement.Secret); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("revoked key error = %v", err)
	}
	if _, _, err := service.ResolveCurrent(context.Background(), "user-1", replacement.Key.ID); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("revoked persisted key error = %v", err)
	}
}

func TestResolveCurrentReloadsNarrowedPermissionsAndScope(t *testing.T) {
	service, _, _ := testService(t, allowValidator{})
	created := createTestKey(t, service)
	updated, err := service.ReplacePermissions(context.Background(), "user-1", created.Key.ID,
		[]string{rbac.PermissionMCPRead}, Constraints{ChannelIDs: []string{"channel-2"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, resolved, err := service.ResolveCurrent(context.Background(), "user-1", created.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Revision != updated.Revision {
		t.Fatalf("resolved revision = %d, want %d", resolved.Revision, updated.Revision)
	}
	if _, ok := principal.GrantedPermissions[rbac.PermissionMCPWrite]; ok {
		t.Fatal("removed write permission remained in persisted principal")
	}
	if _, ok := principal.AllowedChannelIDs["channel-2"]; !ok || len(principal.AllowedTeamIDs) != 0 {
		t.Fatalf("persisted constraints = %#v / %#v", principal.AllowedChannelIDs, principal.AllowedTeamIDs)
	}
}

func TestGrantValidationAndRevisionConflict(t *testing.T) {
	service, _, _ := testService(t, allowValidator{err: ErrGrantDenied})
	if _, err := service.Create(context.Background(), CreateRequest{
		OwnerUserID: "user", CreatedBy: "user", Name: "denied", Kind: KindUser,
		Permissions: []string{rbac.PermissionManageSystem},
	}); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("grant error = %v", err)
	}

	service, _, _ = testService(t, allowValidator{})
	created := createTestKey(t, service)
	wrong := int64(99)
	if _, err := service.ReplacePermissions(context.Background(), "user-1", created.Key.ID,
		[]string{rbac.PermissionMCPRead}, Constraints{}, &wrong); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision error = %v", err)
	}
}

func TestCreateRejectsEmptyCanonicalGrantsAndOversizedScopes(t *testing.T) {
	service, _, _ := testService(t, allowValidator{})
	base := CreateRequest{
		OwnerUserID: "user", CreatedBy: "user", Name: "invalid scope", Kind: KindUser,
		Permissions: []string{"   "},
	}
	if _, err := service.Create(context.Background(), base); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty canonical grants error = %v", err)
	}
	base.Permissions = []string{rbac.PermissionMCPRead}
	base.Constraints.TeamIDs = []string{strings.Repeat("x", 129)}
	if _, err := service.Create(context.Background(), base); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized constraint error = %v", err)
	}
	created := createTestKey(t, service)
	if _, _, err := service.Rotate(context.Background(), "user-1", created.Key.ID, "", 0); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing rotation actor error = %v", err)
	}
}

func TestInvalidAndExpiredCredentialsAreIndistinguishable(t *testing.T) {
	service, _, now := testService(t, allowValidator{})
	if _, _, err := service.Resolve(context.Background(), "bad"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("malformed error = %v", err)
	}
	created := createTestKey(t, service)
	*now = now.Add(25 * time.Hour)
	if _, _, err := service.Resolve(context.Background(), created.Secret); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expired error = %v", err)
	}
}

func TestValidNameSupportsReadableInternationalLabels(t *testing.T) {
	for _, name := range []string{"릴리스 검증 키", "개발 PC MCP", "Service key 01"} {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "   ", "line\nbreak", strings.Repeat("가", 81)} {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true, want false", name)
		}
	}
}
