package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hkjang/moyro/server/internal/aiprovider"
	"github.com/hkjang/moyro/server/internal/apikeys"
	"github.com/hkjang/moyro/server/internal/approval"
	"github.com/hkjang/moyro/server/internal/buildinfo"
	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/mcpserver"
	"github.com/hkjang/moyro/server/internal/oidcauth"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/hkjang/moyro/server/internal/store"
)

type nativeServices struct {
	settings  *settings.Service
	secrets   *secrets.Manager
	rbac      *rbac.Service
	apiKeys   *apikeys.Service
	approval  *approval.Service
	ai        *aiprovider.Service
	oidc      *oidcauth.Manager
	oidcFlows *oidcauth.FlowStore
	mcp       *mcpserver.Service
	site      atomic.Pointer[siteSettingsView]

	// settingsUpdateMu serializes the durable commit and the corresponding
	// in-process activation. Without it, two administrators can commit A then
	// B while activating B then A, leaving live policy older than PostgreSQL.
	settingsUpdateMu sync.Mutex
}

func (n *nativeServices) beginSettingsUpdate() func() {
	n.settingsUpdateMu.Lock()
	return n.settingsUpdateMu.Unlock
}

func (n *nativeServices) revealOptionalSecret(ctx context.Context, section, key string) (string, error) {
	value, _, err := n.settings.RevealSecret(ctx, section, key)
	if errors.Is(err, settings.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func newNativeServices(ctx context.Context, cfg *config.Config, db *store.DB, h *handlers, logger *slog.Logger, secretManager *secrets.Manager, rbacService *rbac.Service) (*nativeServices, error) {
	if secretManager == nil {
		return nil, errors.New("nil secret manager")
	}
	if rbacService == nil {
		return nil, errors.New("nil rbac service")
	}
	settingsService, err := settings.NewPostgres(db.Pool, secretManager)
	if err != nil {
		return nil, err
	}
	keyService, err := apikeys.NewPostgres(
		db.Pool,
		secretManager,
		apikeys.RBACGrantValidator{Authorizer: rbacService},
		apikeys.DefaultOptions(),
	)
	if err != nil {
		return nil, err
	}
	approvalService := approval.New(db, func(ctx context.Context, reviewerID string, policy *approval.Policy, request *approval.Request) (bool, error) {
		teamID := policy.ScopeID
		if teamID == "" && request != nil {
			teamID = request.TeamID
		}
		allowed, err := rbacService.Allowed(ctx, rbac.UserPrincipal(reviewerID), policy.ReviewerPermission, rbac.Scope{TeamID: teamID})
		if err != nil || !allowed {
			return allowed, err
		}
		reviewerRoles, err := configuredReviewerRoles(policy.Config)
		if err != nil {
			return false, err
		}
		if len(reviewerRoles) == 0 {
			return true, nil
		}
		var roleAssigned bool
		err = db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM users u
				WHERE u.id=$1 AND u.delete_at=0
				  AND regexp_split_to_array(BTRIM(COALESCE(u.roles,'')), E'\\s+') && $3::text[]
			) OR EXISTS (
				SELECT 1 FROM team_members tm
				WHERE $2<>'' AND tm.user_id=$1 AND tm.team_id=$2
				  AND regexp_split_to_array(BTRIM(COALESCE(tm.roles,'')), E'\\s+') && $3::text[]
			)
		`, reviewerID, teamID, reviewerRoles).Scan(&roleAssigned)
		return roleAssigned, err
	})
	oidcManager := oidcauth.NewManager(nil)
	flowStore, err := oidcauth.NewFlowStore(db, secretManager)
	if err != nil {
		return nil, err
	}
	aiService := aiprovider.New(nil)

	native := &nativeServices{
		settings: settingsService, secrets: secretManager, rbac: rbacService,
		apiKeys: keyService, approval: approvalService, ai: aiService,
		oidc: oidcManager, oidcFlows: flowStore,
	}
	if err := native.reloadSite(ctx, h.outDisp); err != nil {
		return nil, err
	}
	// An empty administrator-managed URL deliberately stays empty here. OIDC
	// then reuses its previously validated durable callback instead of silently
	// rebinding to the development-only localhost default after a restart.
	baseURL := native.currentSiteSettings().PublicBaseURL
	if err := native.reloadOIDC(ctx, baseURL); err != nil && !errors.Is(err, settings.ErrNotFound) {
		logger.Warn("saved Keycloak OIDC configuration disabled", "err", err)
	}
	if err := native.reloadAI(ctx); err != nil && !errors.Is(err, settings.ErrNotFound) {
		logger.Warn("saved AI provider configuration disabled", "err", err)
	}

	mcpService, err := mcpserver.New(mcpserver.Dependencies{
		Teams: h.teams, Channels: h.channels, Posts: h.posts, PostCommands: h.postCommands, Approval: approvalService,
		UserID: UserIDFromContext,
		CredentialID: func(ctx context.Context) string {
			principal, ok := PrincipalFromContext(ctx)
			if !ok || !principal.Restricted {
				return ""
			}
			return principal.CredentialID
		},
		CredentialAllows: func(ctx context.Context, permission string) bool {
			principal, ok := PrincipalFromContext(ctx)
			if !ok || !principal.Restricted {
				return false
			}
			_, ok = principal.GrantedPermissions[permission]
			return ok
		},
		Authorize: func(ctx context.Context, permission, resourceType, resourceID string) (bool, error) {
			principal, ok := PrincipalFromContext(ctx)
			if !ok {
				return false, nil
			}
			scope := rbac.Scope{}
			switch resourceType {
			case "team":
				scope.TeamID = resourceID
				if principal.Restricted && len(principal.AllowedChannelIDs) > 0 {
					visible := false
					for channelID := range principal.AllowedChannelIDs {
						channel, err := h.channels.Get(ctx, channelID)
						if err == nil && channel != nil && channel.TeamID == resourceID {
							visible = true
							break
						}
					}
					if !visible {
						return false, nil
					}
				}
			case "team_search":
				if principal.Restricted && len(principal.AllowedChannelIDs) > 0 {
					return false, nil
				}
				scope.TeamID = resourceID
			case "channel":
				channel, err := h.channels.Get(ctx, resourceID)
				if err != nil || channel == nil {
					return false, err
				}
				scope.TeamID, scope.ChannelID = channel.TeamID, resourceID
			case "post":
				post, err := h.posts.Get(ctx, resourceID)
				if err != nil || post == nil {
					return false, err
				}
				channel, err := h.channels.Get(ctx, post.ChannelID)
				if err != nil || channel == nil {
					return false, err
				}
				scope.TeamID, scope.ChannelID = channel.TeamID, channel.ID
			case "approval_request":
				request, err := approvalService.Get(ctx, resourceID)
				if err != nil || request == nil {
					return false, err
				}
				scope.TeamID = request.TeamID
				switch request.ResourceType {
				case "channel":
					channel, err := h.channels.Get(ctx, request.ResourceID)
					if err != nil || channel == nil || channel.TeamID != request.TeamID {
						return false, err
					}
					scope.TeamID, scope.ChannelID = channel.TeamID, channel.ID
				case "team":
					if request.ResourceID != "" && request.ResourceID != request.TeamID {
						return false, nil
					}
				default:
					return false, nil
				}
			}
			return rbacService.Allowed(ctx, principal, permission, scope)
		},
		AuthorizeApproved: func(ctx context.Context, requesterID, credentialID, permission, resourceType, resourceID string) (bool, error) {
			// Deferred actions are tied to the exact MCP key that submitted
			// them. Reloading by ID makes revocation, expiry, rotation grace,
			// narrowed grants, and narrowed resource constraints effective at
			// execution time. RBAC then intersects those grants with the
			// requester's current role assignments.
			principal, key, err := keyService.ResolveCurrent(ctx, requesterID, credentialID)
			if err != nil || key.Kind != apikeys.KindMCP || resourceType != "channel" {
				return false, nil
			}
			channel, err := h.channels.Get(ctx, resourceID)
			if err != nil || channel == nil {
				return false, err
			}
			scope := rbac.Scope{TeamID: channel.TeamID, ChannelID: channel.ID}

			// Approval may be executed minutes or hours after submission. Re-read
			// both administrator policies here so disabling MCP, disabling keys,
			// narrowing allowed scopes, or adding an MCP-required scope takes
			// effect before any deferred side effect is applied.
			keyPolicy := defaultKeyPolicy()
			if err := native.loadJSON(ctx, "key-policy", nativeSettingsKey, &keyPolicy); err != nil && !errors.Is(err, settings.ErrNotFound) {
				return false, err
			}
			if !keyPolicy.Enabled || keyScopesAllowed(key.Permissions, keyPolicy.AllowedScopes) != nil {
				return false, nil
			}
			mcpPolicy := defaultMCPSettings()
			if err := native.loadJSON(ctx, "mcp", nativeSettingsKey, &mcpPolicy); err != nil && !errors.Is(err, settings.ErrNotFound) {
				return false, err
			}
			if !mcpPolicy.Enabled {
				return false, nil
			}
			for _, required := range mcpPolicy.RequiredScopes {
				if _, granted := principal.GrantedPermissions[required]; !granted {
					return false, nil
				}
				allowed, err := rbacService.Allowed(ctx, principal, required, scope)
				if err != nil || !allowed {
					return allowed, err
				}
			}
			return rbacService.Allowed(ctx, principal, permission, scope)
		},
		Audit: func(_ context.Context, userID, tool, resourceType, resourceID string, callErr error) {
			if h.audit == nil {
				return
			}
			payload := map[string]any{"tool": tool, "resource_type": resourceType, "ok": callErr == nil}
			if callErr != nil {
				payload["error"] = callErr.Error()
			}
			h.audit.LogAsync(userID, "mcp.tool.call", resourceID, payload)
		},
		Version: buildinfo.Current().Version,
	})
	if err != nil {
		return nil, err
	}
	native.mcp = mcpService
	if err := native.reloadMCPPolicy(ctx); err != nil {
		return nil, err
	}
	return native, nil
}

func (n *nativeServices) loadJSON(ctx context.Context, section, key string, target any) error {
	setting, err := n.settings.Get(ctx, section, key)
	if err != nil {
		return err
	}
	if len(setting.Value) == 0 {
		return settings.ErrNotFound
	}
	return json.Unmarshal(setting.Value, target)
}

func (n *nativeServices) apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r)
		if token == "" || n.apiKeys == nil || len(token) < len(apikeys.SecretPrefix) || token[:len(apikeys.SecretPrefix)] != apikeys.SecretPrefix {
			next.ServeHTTP(w, r)
			return
		}
		principal, key, err := n.apiKeys.Resolve(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "api.moyro.key.invalid", "invalid API key")
			return
		}
		policy := defaultKeyPolicy()
		if err := n.loadJSON(r.Context(), "key-policy", nativeSettingsKey, &policy); err != nil && !errors.Is(err, settings.ErrNotFound) {
			writeError(w, http.StatusServiceUnavailable, "api.moyro.key.policy", "API key policy is unavailable")
			return
		}
		if !policy.Enabled || keyScopesAllowed(key.Permissions, policy.AllowedScopes) != nil {
			writeError(w, http.StatusUnauthorized, "api.moyro.key.policy", "API key is disabled by the current administrator policy")
			return
		}
		next.ServeHTTP(w, r.WithContext(setPrincipalOnContext(r.Context(), principal)))
	})
}

func nativeBearerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.URL.Query().Get("access_token")) != "" {
			writeError(w, http.StatusBadRequest, "api.moyro.auth.header_required", "native API credentials must use the Authorization header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handlers) nativeAPIKeyMiddleware(next http.Handler) http.Handler {
	if h.native == nil {
		return next
	}
	return h.native.apiKeyMiddleware(next)
}

func (h *handlers) nativeMCPGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.native == nil || h.native.mcp == nil {
			writeError(w, http.StatusServiceUnavailable, "api.moyro.mcp.unavailable", "MCP service is unavailable")
			return
		}
		value := defaultMCPSettings()
		if err := h.native.loadJSON(r.Context(), "mcp", nativeSettingsKey, &value); err != nil && !errors.Is(err, settings.ErrNotFound) {
			writeError(w, http.StatusServiceUnavailable, "api.moyro.mcp.settings", "MCP settings are unavailable")
			return
		}
		if !value.Enabled {
			writeError(w, http.StatusNotFound, "api.moyro.mcp.disabled", "MCP endpoint is disabled")
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || !principal.Restricted || principal.CredentialID == "" {
			writeError(w, http.StatusUnauthorized, "api.moyro.mcp.key_required", "a scoped MCP API key is required")
			return
		}
		for _, permission := range value.RequiredScopes {
			if _, ok := principal.GrantedPermissions[permission]; !ok {
				writeError(w, http.StatusForbidden, "api.moyro.mcp.scope", "MCP API key is missing required scope: "+permission)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handlers) nativeRequire(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.native == nil || h.native.rbac == nil {
				writeError(w, http.StatusServiceUnavailable, "api.moyro.disabled", "moyro management services are unavailable")
				return
			}
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "api.context.session_expired.app_error", "authentication required")
				return
			}
			allowed, err := h.native.rbac.Allowed(r.Context(), principal, permission, rbac.Scope{})
			if err != nil || !allowed {
				writeError(w, http.StatusForbidden, "api.context.permissions.app_error", "missing permission: "+permission)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
