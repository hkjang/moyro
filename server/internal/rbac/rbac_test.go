package rbac

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeRepository struct {
	effective   map[string]struct{}
	permissions []Permission
	role        Role
	replaced    []string
}

func (f *fakeRepository) EffectivePermissions(context.Context, string, Scope) (map[string]struct{}, error) {
	return f.effective, nil
}
func (f *fakeRepository) ListPermissions(context.Context) ([]Permission, error) {
	return f.permissions, nil
}
func (f *fakeRepository) GetRole(context.Context, string) (Role, error) { return f.role, nil }
func (f *fakeRepository) ListRoles(context.Context) ([]Role, error)     { return []Role{f.role}, nil }
func (f *fakeRepository) ReplaceRolePermissions(_ context.Context, _ string, permissions []string, _ string, _ *int64, _ int64) (Role, error) {
	f.replaced = append([]string(nil), permissions...)
	role := f.role
	role.Permissions = permissions
	return role, nil
}

func TestAllowedIntersectsCredentialAndOwnerPermissions(t *testing.T) {
	repo := &fakeRepository{effective: map[string]struct{}{PermissionMCPRead: {}, PermissionMCPWrite: {}}}
	svc, _ := New(repo)
	principal := Principal{
		UserID: "user", CredentialID: "key", Restricted: true,
		GrantedPermissions: map[string]struct{}{PermissionMCPRead: {}},
	}
	if ok, err := svc.Allowed(context.Background(), principal, PermissionMCPRead, Scope{}); err != nil || !ok {
		t.Fatalf("read allowed = %v, %v", ok, err)
	}
	if ok, err := svc.Allowed(context.Background(), principal, PermissionMCPWrite, Scope{}); err != nil || ok {
		t.Fatalf("write allowed = %v, %v", ok, err)
	}
	// A grant on the key cannot manufacture authority absent from the owner.
	principal.GrantedPermissions[PermissionManageSystem] = struct{}{}
	if ok, _ := svc.Allowed(context.Background(), principal, PermissionManageSystem, Scope{}); ok {
		t.Fatal("key escalated beyond owner")
	}
}

func TestAllowedHonoursResourceConstraints(t *testing.T) {
	repo := &fakeRepository{effective: map[string]struct{}{PermissionMCPRead: {}}}
	svc, _ := New(repo)
	principal := Principal{
		UserID: "user", Restricted: true,
		GrantedPermissions: map[string]struct{}{PermissionMCPRead: {}},
		AllowedTeamIDs:     map[string]struct{}{"team-a": {}},
	}
	if ok, _ := svc.Allowed(context.Background(), principal, PermissionMCPRead, Scope{TeamID: "team-b"}); ok {
		t.Fatal("out-of-scope team was allowed")
	}
	if ok, _ := svc.Allowed(context.Background(), principal, PermissionMCPRead, Scope{TeamID: "team-a"}); !ok {
		t.Fatal("allowed team was rejected")
	}
	permissions, err := svc.EffectivePermissions(context.Background(), principal, Scope{TeamID: "team-b"})
	if err != nil || len(permissions) != 0 {
		t.Fatalf("out-of-scope effective permissions = %#v, %v", permissions, err)
	}
}

func TestResourceConstraintsFailClosedForMissingOrForeignResourceIDs(t *testing.T) {
	principal := Principal{
		UserID:            "user",
		Restricted:        true,
		AllowedTeamIDs:    map[string]struct{}{"team-a": {}},
		AllowedChannelIDs: map[string]struct{}{"channel-a": {}},
	}
	constraints := ResourceConstraintsFor(principal)
	if !constraints.Allows("team-a", "channel-a") {
		t.Fatal("allowed resource intersection was rejected")
	}
	for _, scope := range []Scope{
		{},
		{TeamID: "team-a"},
		{ChannelID: "channel-a"},
		{TeamID: "team-b", ChannelID: "channel-a"},
		{TeamID: "team-a", ChannelID: "channel-b"},
	} {
		if constraints.Allows(scope.TeamID, scope.ChannelID) {
			t.Fatalf("out-of-scope resource was allowed: %#v", scope)
		}
	}
	if got := ResourceConstraintsFor(UserPrincipal("user")); !got.Allows("", "") || !got.Allows("any-team", "any-channel") {
		t.Fatalf("unrestricted principal was constrained: %#v", got)
	}
}

func TestManageSystemIsOwnerRecoveryAuthorityButNotAKeyGrantWildcard(t *testing.T) {
	repo := &fakeRepository{effective: map[string]struct{}{PermissionManageSystem: {}}}
	svc, _ := New(repo)
	if ok, err := svc.Allowed(context.Background(), UserPrincipal("admin"), PermissionManageRoles, Scope{}); err != nil || !ok {
		t.Fatalf("manage_system recovery authority = %v, %v", ok, err)
	}

	key := Principal{
		UserID: "admin", CredentialID: "key", Restricted: true,
		GrantedPermissions: map[string]struct{}{PermissionMCPRead: {}},
	}
	if ok, err := svc.Allowed(context.Background(), key, PermissionManageRoles, Scope{}); err != nil || ok {
		t.Fatalf("ungranted key permission = %v, %v", ok, err)
	}
	key.GrantedPermissions[PermissionManageRoles] = struct{}{}
	if ok, err := svc.Allowed(context.Background(), key, PermissionManageRoles, Scope{}); err != nil || !ok {
		t.Fatalf("explicit key grant backed by manage_system = %v, %v", ok, err)
	}
}

func TestPatchRoleValidatesAndCanonicalizesPermissions(t *testing.T) {
	repo := &fakeRepository{
		permissions: []Permission{{Name: PermissionMCPRead}, {Name: PermissionMCPWrite}},
		role:        Role{ID: "custom", Name: "custom"},
	}
	svc, _ := New(repo)
	_, err := svc.PatchRolePermissions(context.Background(), "custom", []string{PermissionMCPWrite, PermissionMCPRead, PermissionMCPRead}, "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{PermissionMCPRead, PermissionMCPWrite}
	if !reflect.DeepEqual(repo.replaced, want) {
		t.Fatalf("replaced = %#v, want %#v", repo.replaced, want)
	}
	if _, err := svc.PatchRolePermissions(context.Background(), "custom", []string{"does_not_exist"}, "admin", nil); !errors.Is(err, ErrUnknownPermission) {
		t.Fatalf("unknown permission error = %v", err)
	}
}

func TestSystemAdminMustRetainManageSystem(t *testing.T) {
	repo := &fakeRepository{
		permissions: []Permission{{Name: PermissionManageSystem}, {Name: PermissionMCPRead}},
		role:        Role{ID: "system_admin", Name: "system_admin"},
	}
	svc, _ := New(repo)
	if _, err := svc.PatchRolePermissions(context.Background(), "system_admin", []string{PermissionMCPRead}, "admin", nil); !errors.Is(err, ErrProtectedRole) {
		t.Fatalf("protected role error = %v", err)
	}
}
