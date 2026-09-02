// Moyro-native management API under /api/moyro/v1.
//
// Split out of the former single `client.ts`. `client.ts` re-exports every
// symbol here, so callers keep importing from `@/api/client`.

// ---- Moyro-native management API ---------------------------------------
// These endpoints intentionally live outside the Mattermost compatibility
// surface. They back product-specific settings while `/api/v4` remains a
// stable compatibility boundary for existing clients and integrations.
import {
  MOYRO_API_BASE as MOYRO_BASE,
  moyroRequest,
} from "./transport";
import type { SystemInfo } from "./chat";

export type SecretConfigured = { configured: boolean };

export type OIDCProviderSettings = {
  id?: string;
  kind: "keycloak";
  name: string;
  enabled: boolean;
  issuer_url: string;
  client_id: string;
  client_secret?: string;
  client_secret_state?: SecretConfigured;
  scopes: string[];
  username_claim: string;
  email_claim: string;
  allow_signup: boolean;
  require_verified_email: boolean;
  allow_insecure_backchannel: boolean;
  ca_certificate_pem?: string;
  redirect_url?: string;
  discovery_status?: "unknown" | "ready" | "error";
  last_tested_at?: number;
};

export type AIProviderSettings = {
  id?: string;
  name: string;
  enabled: boolean;
  api_type: "openai-compatible" | "openai";
  base_url: string;
  model: string;
  api_key?: string;
  api_key_state?: SecretConfigured;
  streaming_default: boolean;
  context_window_tokens: number;
  max_output_tokens: number;
  timeout_seconds: number;
  status?: "unknown" | "ready" | "error";
  last_tested_at?: number;
};

export type KeyPolicySettings = {
  enabled: boolean;
  allowed_scopes: string[];
  default_scopes: string[];
  default_ttl_days: number;
  max_ttl_days: number;
  rotation_days: number;
  rotation_grace_hours: number;
  allow_personal_keys: boolean;
  allow_scope_self_service: boolean;
};

export type SiteSettings = {
  site_name: string;
  public_base_url: string;
  allowed_outgoing_hosts: string[];
  trusted_proxy_cidrs: string[];
  local_signup_enabled: boolean;
	draft_storage_mode: "local" | "session" | "disabled";
	draft_retention_days: number;
	draft_clear_on_logout: boolean;
};

export type RBACPermission = {
  name: string;
  description: string;
  resource_type: string;
  built_in: boolean;
};

export type EffectivePermissions = {
  permissions: string[];
};

export type RBACRole = {
  id: string;
  name: string;
  display_name: string;
  description: string;
  scope_type: string;
  built_in: boolean;
  permissions: string[];
  revision: number;
  create_at: number;
  update_at: number;
};

export type MCPSettings = {
  enabled: boolean;
  transport: "streamable-http";
  endpoint_path: string;
  allowed_tools: string[];
  allowed_resources: string[];
  required_scopes: string[];
};

export type ApprovalPolicy = {
  id?: string;
  name: string;
  enabled: boolean;
  protected_actions: string[];
  reviewer_roles: string[];
  require_rejection_reason: boolean;
  allow_self_approval: boolean;
  expires_after_hours: number;
};

export type ApprovalRequestServerPreview = {
  title: string;
  risk_level: "low" | "medium" | "high" | "unknown";
  actor: { type: string; display_name: string };
  target: { type: string; display_name: string };
  changes: Array<{ label: string; after: string }>;
  policy: { name: string; reason: string };
  secrets_redacted: boolean;
};

export type ApprovalRequest = {
  id: string;
  policy_id: string;
  action_type: string;
  requester_id: string;
  team_id: string;
  resource_type: string;
  resource_id: string;
  preview?: ApprovalRequestServerPreview;
  // Compatibility fallback for pre-preview Moyro servers. Current native
  // browser APIs omit this field so execution credentials never reach the UI.
  payload?: unknown;
  status: string;
  idempotency_key?: string;
  create_at: number;
  update_at: number;
  decided_at: number;
  executed_at: number;
  expires_at: number;
};

export type PersonalAPIKey = {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  status: "active" | "grace" | "revoked" | "expired";
  created_at: number;
  last_used_at?: number;
  expires_at?: number;
};

export type PersonalAPIKeySecret = PersonalAPIKey & { secret: string };

export type AdminAPIKey = {
  id: string;
  user_id: string;
  username: string;
  email: string;
  name: string;
  prefix: string;
  kind: "user" | "service" | "mcp";
  status: "active" | "grace" | "revoked" | "expired";
  scopes: string[];
  created_at: number;
  last_used_at: number;
  expires_at: number;
  revoked_at: number;
};

export type PersonalAIPreferences = {
  enabled: boolean;
  provider_id?: string;
  model?: string;
  streaming: boolean;
  max_output_tokens: number;
  temperature: number;
};

export type AICompletionRequest = {
  model?: string;
  messages: { role: "system" | "user" | "assistant"; content: string }[];
  max_output_tokens?: number;
  temperature?: number;
  stream?: true;
};

export const publicMoyroApi = {
  systemInfo: () => moyroRequest<SystemInfo>(null, "/system/info"),
};

export const moyroAdminApi = {
  getSettings: <T>(token: string, section: "site" | "key-policy" | "mcp") =>
    moyroRequest<T>(token, `/admin/settings/${encodeURIComponent(section)}`),
  patchSettings: <T>(token: string, section: "site" | "key-policy" | "mcp", value: T) =>
    moyroRequest<T>(token, `/admin/settings/${encodeURIComponent(section)}`, {
      method: "PATCH",
      body: value,
    }),

  listPermissions: (token: string) =>
    moyroRequest<RBACPermission[]>(token, "/admin/permissions"),
  listRoles: (token: string) =>
    moyroRequest<RBACRole[]>(token, "/admin/roles"),
  patchRole: (token: string, id: string, value: { permissions: string[]; revision: number }) =>
    moyroRequest<RBACRole>(token, `/admin/roles/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),

  listAPIKeys: (token: string, page = 0, perPage = 100) =>
    moyroRequest<AdminAPIKey[]>(
      token,
      `/admin/api-keys?page=${encodeURIComponent(String(page))}&per_page=${encodeURIComponent(String(perPage))}`,
    ),
  revokeAPIKey: (token: string, id: string) =>
    moyroRequest<void>(token, `/admin/api-keys/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  listOIDCProviders: (token: string) =>
    moyroRequest<OIDCProviderSettings[]>(token, "/admin/oidc/providers"),
  createOIDCProvider: (token: string, value: OIDCProviderSettings) =>
    moyroRequest<OIDCProviderSettings>(token, "/admin/oidc/providers", {
      method: "POST",
      body: value,
    }),
  patchOIDCProvider: (token: string, id: string, value: Partial<OIDCProviderSettings>) =>
    moyroRequest<OIDCProviderSettings>(token, `/admin/oidc/providers/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
  testOIDCProvider: (token: string, value: OIDCProviderSettings) =>
    moyroRequest<{ ok: boolean; issuer?: string; message?: string }>(
      token,
      "/admin/oidc/providers/test",
      { method: "POST", body: value },
    ),

  listAIProviders: (token: string) =>
    moyroRequest<AIProviderSettings[]>(token, "/admin/ai/providers"),
  createAIProvider: (token: string, value: AIProviderSettings) =>
    moyroRequest<AIProviderSettings>(token, "/admin/ai/providers", {
      method: "POST",
      body: value,
    }),
  patchAIProvider: (token: string, id: string, value: Partial<AIProviderSettings>) =>
    moyroRequest<AIProviderSettings>(token, `/admin/ai/providers/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
  testAIProvider: (token: string, value: AIProviderSettings) =>
    moyroRequest<{ ok: boolean; model?: string; message?: string }>(
      token,
      "/admin/ai/providers/test",
      { method: "POST", body: value },
    ),

  listApprovalPolicies: (token: string) =>
    moyroRequest<ApprovalPolicy[]>(token, "/admin/approval-policies"),
  createApprovalPolicy: (token: string, value: ApprovalPolicy) =>
    moyroRequest<ApprovalPolicy>(token, "/admin/approval-policies", {
      method: "POST",
      body: value,
    }),
  patchApprovalPolicy: (token: string, id: string, value: Partial<ApprovalPolicy>) =>
    moyroRequest<ApprovalPolicy>(token, `/admin/approval-policies/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
};

export const moyroMeApi = {
  getPermissions: (token: string) =>
    moyroRequest<EffectivePermissions>(token, "/me/permissions"),
  listApprovalRequests: (token: string, status = "") =>
    moyroRequest<ApprovalRequest[]>(
      token,
      `/me/approval-requests${status ? `?status=${encodeURIComponent(status)}` : ""}`,
    ),
  getAIPreferences: (token: string) =>
    moyroRequest<PersonalAIPreferences>(token, "/me/ai-preferences"),
  patchAIPreferences: (token: string, value: PersonalAIPreferences) =>
    moyroRequest<PersonalAIPreferences>(token, "/me/ai-preferences", {
      method: "PATCH",
      body: value,
    }),
  listAPIKeys: (token: string) =>
    moyroRequest<PersonalAPIKey[]>(token, "/me/api-keys"),
  createAPIKey: (
    token: string,
    value: { name: string; scopes: string[]; ttl_days?: number },
  ) =>
    moyroRequest<PersonalAPIKeySecret>(token, "/me/api-keys", {
      method: "POST",
      body: value,
    }),
  patchAPIKey: (token: string, id: string, value: { name?: string; scopes?: string[] }) =>
    moyroRequest<PersonalAPIKey>(token, `/me/api-keys/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
  deleteAPIKey: (token: string, id: string) =>
    moyroRequest<void>(token, `/me/api-keys/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),
  rotateAPIKey: (token: string, id: string) =>
    moyroRequest<PersonalAPIKeySecret>(token, `/me/api-keys/${encodeURIComponent(id)}/rotate`, {
      method: "POST",
    }),
  streamAICompletion: async (
    token: string,
    value: AICompletionRequest,
    onDelta: (delta: string) => void,
    signal?: AbortSignal,
  ): Promise<void> => {
    const res = await fetch(`${MOYRO_BASE}/me/ai/completions`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
        Accept: "text/event-stream",
      },
      body: JSON.stringify({ ...value, stream: true }),
      signal,
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({ message: res.statusText }));
      throw new Error(err.message ?? `HTTP ${res.status}`);
    }
    if (!res.body) throw new Error("streaming response body is unavailable");

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    const emitData = (data: string) => {
      if (!data || data === "[DONE]") return;
      try {
        const parsed = JSON.parse(data) as {
          delta?: string | { text?: string; content?: string };
          content?: string;
          choices?: { delta?: { content?: string }; text?: string }[];
        };
        const delta = typeof parsed.delta === "string"
          ? parsed.delta
          : parsed.delta?.text
            ?? parsed.delta?.content
            ?? parsed.choices?.[0]?.delta?.content
            ?? parsed.choices?.[0]?.text
            ?? parsed.content
            ?? "";
        if (delta) onDelta(delta);
      } catch {
        onDelta(data);
      }
    };
    while (true) {
      const { value: chunk, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(chunk, { stream: true });
      const lines = buffer.split(/\r?\n/);
      buffer = lines.pop() ?? "";
      for (const line of lines) {
        if (!line.startsWith("data:")) continue;
        emitData(line.slice(5).trim());
      }
    }
    const tail = buffer.trim();
    if (tail.startsWith("data:")) emitData(tail.slice(5).trim());
  },
};

export const moyroReviewApi = {
  listApprovalRequests: (token: string, status = "") =>
    moyroRequest<ApprovalRequest[]>(
      token,
      `/reviews/approval-requests${status ? `?status=${encodeURIComponent(status)}` : ""}`,
    ),
  decideApprovalRequest: (
    token: string,
    id: string,
    value: { decision: "approve" | "reject"; reason: string },
  ) => moyroRequest<ApprovalRequest>(
    token,
    `/reviews/approval-requests/${encodeURIComponent(id)}/decision`,
    { method: "POST", body: value },
  ),
};
