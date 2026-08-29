package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/apikeys"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/jackc/pgx/v5"
)

type personalAPIKeyView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	Status     string   `json:"status"`
	CreatedAt  int64    `json:"created_at"`
	LastUsedAt int64    `json:"last_used_at,omitempty"`
	ExpiresAt  int64    `json:"expires_at,omitempty"`
	Secret     string   `json:"secret,omitempty"`
}

type adminAPIKeyView struct {
	ID         string   `json:"id"`
	UserID     string   `json:"user_id"`
	Username   string   `json:"username"`
	Email      string   `json:"email"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	Scopes     []string `json:"scopes"`
	CreatedAt  int64    `json:"created_at"`
	LastUsedAt int64    `json:"last_used_at"`
	ExpiresAt  int64    `json:"expires_at"`
	RevokedAt  int64    `json:"revoked_at"`
}

func keyView(key apikeys.Key, secret string) personalAPIKeyView {
	status := key.Status
	now := time.Now().UnixMilli()
	if key.ExpiresAt <= now {
		status = "expired"
	} else if status == apikeys.StatusRetiring {
		status = "grace"
	}
	return personalAPIKeyView{
		ID: key.ID, Name: key.Name, Prefix: key.Prefix, Scopes: key.Permissions,
		Status: status, CreatedAt: key.CreateAt, LastUsedAt: key.LastUsedAt,
		ExpiresAt: key.ExpiresAt, Secret: secret,
	}
}

// listAdminAPIKeys exposes inventory metadata only. Plaintext credentials and
// keyed digests never leave the credential package/database boundary.
func (h *handlers) listAdminAPIKeys(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 0 {
		page = 0
	}
	if perPage < 1 || perPage > 200 {
		perPage = 100
	}
	rows, err := h.auth.DB().Pool.Query(r.Context(), `
		SELECT k.id, k.owner_user_id, u.username, u.email, k.name, k.key_prefix,
		       k.kind, k.status,
		       COALESCE(array_agg(kp.permission_name ORDER BY kp.permission_name)
		           FILTER (WHERE kp.permission_name IS NOT NULL), '{}'),
		       k.create_at, k.last_used_at, k.expires_at, k.revoked_at
		FROM api_keys k
		JOIN users u ON u.id=k.owner_user_id
		LEFT JOIN api_key_permissions kp ON kp.api_key_id=k.id
		GROUP BY k.id, u.id
		ORDER BY k.create_at DESC
		LIMIT $1 OFFSET $2
	`, perPage, page*perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.keys.admin_list", err.Error())
		return
	}
	defer rows.Close()
	result := make([]adminAPIKeyView, 0)
	now := time.Now().UnixMilli()
	for rows.Next() {
		var key adminAPIKeyView
		if err := rows.Scan(
			&key.ID, &key.UserID, &key.Username, &key.Email, &key.Name, &key.Prefix,
			&key.Kind, &key.Status, &key.Scopes, &key.CreatedAt, &key.LastUsedAt,
			&key.ExpiresAt, &key.RevokedAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.keys.admin_list", err.Error())
			return
		}
		if key.Status != apikeys.StatusRevoked && key.ExpiresAt > 0 && key.ExpiresAt <= now {
			key.Status = "expired"
		} else if key.Status == apikeys.StatusRetiring {
			key.Status = "grace"
		}
		result = append(result, key)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.keys.admin_list", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handlers) revokeAdminAPIKey(w http.ResponseWriter, r *http.Request) {
	keyID := strings.TrimSpace(chi.URLParam(r, "keyID"))
	if keyID == "" {
		writeError(w, http.StatusBadRequest, "api.moyro.keys.id", "API key ID is required")
		return
	}
	now := time.Now().UnixMilli()
	var ownerID, name string
	err := h.auth.DB().Pool.QueryRow(r.Context(), `
		UPDATE api_keys
		SET status='revoked', revoked_at=$2, valid_until=0, update_at=$2, revision=revision+1
		WHERE id=$1 AND status<>'revoked'
		RETURNING owner_user_id, name
	`, keyID, now).Scan(&ownerID, &name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "api.moyro.keys.not_found", "active API key not found")
		} else {
			writeError(w, http.StatusInternalServerError, "api.moyro.keys.admin_revoke", err.Error())
		}
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "api_key.admin_revoke", keyID, map[string]any{"owner_user_id": ownerID, "name": name})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) loadKeyPolicy(ctx context.Context) (keyPolicyView, error) {
	value := defaultKeyPolicy()
	err := h.native.loadJSON(ctx, "key-policy", nativeSettingsKey, &value)
	if errors.Is(err, settings.ErrNotFound) {
		return value, nil
	}
	return value, err
}

func (h *handlers) listPersonalAPIKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := h.native.apiKeys.List(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.keys.list", err.Error())
		return
	}
	views := make([]personalAPIKeyView, 0, len(rows))
	for _, key := range rows {
		views = append(views, keyView(key, ""))
	}
	writeJSON(w, http.StatusOK, views)
}

type createPersonalAPIKeyRequest struct {
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
	TTLDays int      `json:"ttl_days"`
}

func (h *handlers) createPersonalAPIKey(w http.ResponseWriter, r *http.Request) {
	var input createPersonalAPIKeyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.keys.body", err.Error())
		return
	}
	policy, err := h.loadKeyPolicy(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.keys.policy", err.Error())
		return
	}
	if !policy.Enabled || !policy.AllowPersonalKeys {
		writeError(w, http.StatusForbidden, "api.moyro.keys.disabled", "personal API keys are disabled by policy")
		return
	}
	input.Scopes = canonicalNativeStrings(input.Scopes)
	if len(input.Scopes) == 0 {
		input.Scopes = slices.Clone(policy.DefaultScopes)
	}
	if err := keyScopesAllowed(input.Scopes, policy.AllowedScopes); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.keys.scopes", err.Error())
		return
	}
	if input.TTLDays == 0 {
		input.TTLDays = policy.DefaultTTLDays
	}
	if input.TTLDays < 1 || input.TTLDays > policy.MaxTTLDays {
		writeError(w, http.StatusBadRequest, "api.moyro.keys.ttl", "ttl_days is outside the administrator policy")
		return
	}
	kind := apikeys.KindUser
	if slices.Contains(input.Scopes, rbac.PermissionMCPRead) || slices.Contains(input.Scopes, rbac.PermissionMCPWrite) {
		kind = apikeys.KindMCP
	}
	actor := userID(r)
	created, err := h.native.apiKeys.Create(r.Context(), apikeys.CreateRequest{
		OwnerUserID: actor, CreatedBy: actor, Name: strings.TrimSpace(input.Name), Kind: kind,
		Permissions: input.Scopes, ExpiresAt: time.Now().Add(time.Duration(input.TTLDays) * 24 * time.Hour).UnixMilli(),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.keys.create", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(actor, "api_key.create", created.Key.ID, map[string]any{"permissions": created.Key.Permissions, "kind": created.Key.Kind})
	}
	writeJSON(w, http.StatusCreated, keyView(created.Key, created.Secret))
}

type patchPersonalAPIKeyRequest struct {
	Name   *string  `json:"name,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

func (h *handlers) patchPersonalAPIKey(w http.ResponseWriter, r *http.Request) {
	var input patchPersonalAPIKeyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.keys.body", err.Error())
		return
	}
	actor, keyID := userID(r), chi.URLParam(r, "keyID")
	policy, err := h.loadKeyPolicy(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.keys.policy", err.Error())
		return
	}
	if input.Scopes != nil {
		if !policy.AllowScopeSelfService {
			writeError(w, http.StatusForbidden, "api.moyro.keys.scope_change", "scope changes are disabled by policy")
			return
		}
		input.Scopes = canonicalNativeStrings(input.Scopes)
		if err := keyScopesAllowed(input.Scopes, policy.AllowedScopes); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.keys.scopes", err.Error())
			return
		}
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if !apikeys.ValidName(name) {
			writeError(w, http.StatusBadRequest, "api.moyro.keys.name", "invalid key name")
			return
		}
		result, err := h.auth.DB().Pool.Exec(r.Context(), `UPDATE api_keys SET name=$3, update_at=$4, revision=revision+1 WHERE id=$1 AND owner_user_id=$2 AND status<>'revoked'`, keyID, actor, name, time.Now().UnixMilli())
		if err != nil || result.RowsAffected() != 1 {
			writeError(w, http.StatusNotFound, "api.moyro.keys.not_found", "API key not found")
			return
		}
	}
	if input.Scopes != nil {
		if _, err := h.native.apiKeys.ReplacePermissions(r.Context(), actor, keyID, input.Scopes, apikeys.Constraints{}, nil); err != nil {
			writeError(w, http.StatusBadRequest, "api.moyro.keys.patch", err.Error())
			return
		}
	}
	rows, err := h.native.apiKeys.List(r.Context(), actor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.keys.list", err.Error())
		return
	}
	for _, key := range rows {
		if key.ID == keyID {
			writeJSON(w, http.StatusOK, keyView(key, ""))
			return
		}
	}
	writeError(w, http.StatusNotFound, "api.moyro.keys.not_found", "API key not found")
}

func (h *handlers) rotatePersonalAPIKey(w http.ResponseWriter, r *http.Request) {
	policy, err := h.loadKeyPolicy(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.keys.policy", err.Error())
		return
	}
	actor, keyID := userID(r), chi.URLParam(r, "keyID")
	grace := time.Duration(policy.RotationGraceHours) * time.Hour
	if policy.RotationGraceHours == 0 {
		grace = time.Millisecond
	}
	created, _, err := h.native.apiKeys.Rotate(r.Context(), actor, keyID, actor, grace)
	if err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.keys.rotate", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(actor, "api_key.rotate", keyID, map[string]any{"replacement_id": created.Key.ID, "grace_hours": policy.RotationGraceHours})
	}
	writeJSON(w, http.StatusOK, keyView(created.Key, created.Secret))
}

func (h *handlers) revokePersonalAPIKey(w http.ResponseWriter, r *http.Request) {
	actor, keyID := userID(r), chi.URLParam(r, "keyID")
	if err := h.native.apiKeys.Revoke(r.Context(), actor, keyID); err != nil {
		writeError(w, http.StatusNotFound, "api.moyro.keys.revoke", err.Error())
		return
	}
	if h.audit != nil {
		h.audit.LogAsync(actor, "api_key.revoke", keyID, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

func keyScopesAllowed(scopes, allowed []string) error {
	if len(scopes) == 0 {
		return errors.New("at least one scope is required")
	}
	for _, scope := range scopes {
		if !slices.Contains(allowed, scope) {
			return errors.New("scope is not allowed by policy: " + scope)
		}
	}
	return nil
}
