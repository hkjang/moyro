// Package rbac resolves the existing Mattermost-shaped role assignments to
// database-managed permissions. It also carries credential restrictions so an
// API key can never exercise more authority than its owner currently has.
package rbac

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"time"
)

const (
	PermissionManageSystem           = "manage_system"
	PermissionManageRoles            = "manage_roles"
	PermissionManageSettings         = "manage_settings"
	PermissionManageOIDC             = "manage_oidc"
	PermissionManageAI               = "manage_ai"
	PermissionUseAI                  = "use_ai"
	PermissionManageAPIKeys          = "manage_api_keys"
	PermissionManageOwnAPIKeys       = "manage_own_api_keys"
	PermissionManageKeyPermissions   = "manage_key_permissions"
	PermissionManageApprovalPolicies = "manage_approval_policies"
	PermissionRequestApproval        = "request_approval"
	PermissionReviewApproval         = "review_approval"
	PermissionMCPRead                = "mcp_read"
	PermissionMCPWrite               = "mcp_write"
)

var (
	ErrNotFound          = errors.New("rbac: role not found")
	ErrRevisionConflict  = errors.New("rbac: revision conflict")
	ErrUnknownPermission = errors.New("rbac: unknown permission")
	ErrProtectedRole     = errors.New("rbac: protected role must retain manage_system")
	ErrInvalidPrincipal  = errors.New("rbac: invalid principal")
)

var permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

type Scope struct {
	TeamID    string
	ChannelID string
}

// Principal represents either a browser session or a scoped API key.
// Restricted=false means the credential inherits all current user authority.
// Restricted=true intersects it with GrantedPermissions and optional resource
// allow-lists.
type Principal struct {
	UserID             string
	CredentialID       string
	Restricted         bool
	GrantedPermissions map[string]struct{}
	AllowedTeamIDs     map[string]struct{}
	AllowedChannelIDs  map[string]struct{}
}

func UserPrincipal(userID string) Principal { return Principal{UserID: userID} }

type Permission struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ResourceType string `json:"resource_type"`
	BuiltIn      bool   `json:"built_in"`
}

type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	ScopeType   string   `json:"scope_type"`
	BuiltIn     bool     `json:"built_in"`
	Permissions []string `json:"permissions"`
	Revision    int64    `json:"revision"`
	CreateAt    int64    `json:"create_at"`
	UpdateAt    int64    `json:"update_at"`
}

type Repository interface {
	EffectivePermissions(ctx context.Context, userID string, scope Scope) (map[string]struct{}, error)
	ListPermissions(ctx context.Context) ([]Permission, error)
	GetRole(ctx context.Context, roleID string) (Role, error)
	ListRoles(ctx context.Context) ([]Role, error)
	ReplaceRolePermissions(ctx context.Context, roleID string, permissions []string, actorID string, expectedRevision *int64, updateAt int64) (Role, error)
}

type Service struct {
	repo Repository
	now  func() time.Time
}

func New(repo Repository) (*Service, error) {
	if repo == nil {
		return nil, errors.New("rbac: nil repository")
	}
	return &Service{repo: repo, now: time.Now}, nil
}

func (s *Service) Allowed(ctx context.Context, principal Principal, permission string, scope Scope) (bool, error) {
	if principal.UserID == "" || !permissionPattern.MatchString(permission) {
		return false, ErrInvalidPrincipal
	}
	if principal.Restricted {
		if _, ok := principal.GrantedPermissions[permission]; !ok {
			return false, nil
		}
		if !credentialScopeAllowed(principal, scope) {
			return false, nil
		}
	}
	effective, err := s.repo.EffectivePermissions(ctx, principal.UserID, scope)
	if err != nil {
		return false, err
	}
	_, ok := effective[permission]
	if !ok {
		// manage_system is the non-removable recovery authority of the built-in
		// system_admin role. Treating it as an owner-side wildcard prevents an
		// administrator from permanently locking every administrator out after
		// removing a more specific permission from that role. Restricted API
		// keys still pass through the exact requested-grant check above, so this
		// fallback cannot manufacture a permission that was not placed on the
		// credential explicitly.
		_, ok = effective[PermissionManageSystem]
	}
	return ok, nil
}

func (s *Service) EffectivePermissions(ctx context.Context, principal Principal, scope Scope) ([]string, error) {
	if principal.UserID == "" {
		return nil, ErrInvalidPrincipal
	}
	if principal.Restricted && !credentialScopeAllowed(principal, scope) {
		return []string{}, nil
	}
	effective, err := s.repo.EffectivePermissions(ctx, principal.UserID, scope)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(effective))
	for permission := range effective {
		if principal.Restricted {
			if _, ok := principal.GrantedPermissions[permission]; !ok {
				continue
			}
		}
		out = append(out, permission)
	}
	sort.Strings(out)
	return out, nil
}

func credentialScopeAllowed(principal Principal, scope Scope) bool {
	if scope.TeamID != "" && len(principal.AllowedTeamIDs) != 0 {
		if _, ok := principal.AllowedTeamIDs[scope.TeamID]; !ok {
			return false
		}
	}
	if scope.ChannelID != "" && len(principal.AllowedChannelIDs) != 0 {
		if _, ok := principal.AllowedChannelIDs[scope.ChannelID]; !ok {
			return false
		}
	}
	return true
}

func (s *Service) ListPermissions(ctx context.Context) ([]Permission, error) {
	return s.repo.ListPermissions(ctx)
}

func (s *Service) GetRole(ctx context.Context, roleID string) (Role, error) {
	return s.repo.GetRole(ctx, roleID)
}

func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	return s.repo.ListRoles(ctx)
}

func (s *Service) PatchRolePermissions(ctx context.Context, roleID string, permissions []string, actorID string, expectedRevision *int64) (Role, error) {
	role, err := s.repo.GetRole(ctx, roleID)
	if err != nil {
		return Role{}, err
	}
	canonical, err := s.validatePermissions(ctx, permissions)
	if err != nil {
		return Role{}, err
	}
	if role.Name == "system_admin" && !contains(canonical, PermissionManageSystem) {
		return Role{}, ErrProtectedRole
	}
	return s.repo.ReplaceRolePermissions(ctx, roleID, canonical, actorID, expectedRevision, s.now().UnixMilli())
}

func (s *Service) validatePermissions(ctx context.Context, requested []string) ([]string, error) {
	knownList, err := s.repo.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(knownList))
	for _, permission := range knownList {
		known[permission.Name] = struct{}{}
	}
	set := map[string]struct{}{}
	for _, permission := range requested {
		if !permissionPattern.MatchString(permission) {
			return nil, ErrUnknownPermission
		}
		if _, ok := known[permission]; !ok {
			return nil, ErrUnknownPermission
		}
		set[permission] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for permission := range set {
		out = append(out, permission)
	}
	sort.Strings(out)
	return out, nil
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
