// Integrations and administration: bots, webhooks, plugins, admin compat.
//
// Split out of the former single `client.ts`. `client.ts` re-exports every
// symbol here, so callers keep importing from `@/api/client`.

// ---- Phase 12 types ----
import {
  compatRequest as request,
} from "./transport";
import type { AuditEntry, Invite } from "./chat";

export type Bot = {
  user_id: string;
  username: string;
  display_name: string;
  description: string;
  owner_id: string;
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type PAT = {
  id: string;
  user_id: string;
  description: string;
  create_at: number;
  last_used_at: number;
  revoked_at: number;
};

// CreatedPAT is returned exactly once on creation — stash the plaintext
// immediately; there is no way to recover it afterwards.
export type CreatedPAT = PAT & { token: string };

export type IncomingWebhook = {
  id: string;
  creator_id: string;
  channel_id: string;
  team_id: string;
  display_name: string;
  username: string;
  icon_url: string;
  channel_locked: boolean;
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type OutgoingWebhook = {
  id: string;
  token: string;
  creator_id: string;
  team_id: string;
  channel_id: string;
  trigger_words: string[];
  trigger_when: number;
  callback_urls: string[];
  display_name: string;
  content_type: string;
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type AdminConfigSnapshot = Record<string, Record<string, unknown>>;

export type AdminClusterNode = {
  id: string;
  status: string;
  hostname?: string;
  version?: string;
  server_version?: string;
  last_ping_at?: number;
  busy?: boolean;
  [key: string]: unknown;
};

export type AdminPlugin = {
  id?: string;
  plugin_id?: string;
  name?: string;
  version?: string;
  state?: string;
  description?: string;
	 enabled?: boolean;
	 runtime?: string;
	 error?: string;
	 manifest?: Record<string, unknown>;
  [key: string]: unknown;
};

export type AdminPluginStatus = {
  plugin_id: string;
  state: string;
};

export type PluginWebappBundle = {
  id: string;
  version: string;
  url: string;
};

export type PluginInstallResult = {
  id: string;
  version: string;
  state: string;
  enabled: boolean;
  runtime: string;
  sha256: string;
  replaced: boolean;
};

export type PluginConfiguration = {
  configuration: Record<string, unknown>;
  schema: Record<string, unknown>;
};

export type PluginManagementCapabilities = {
  management_enabled: boolean;
  uploads_enabled: boolean;
};

export const pluginApi = {
  listWebapps: (token: string) =>
    request<PluginWebappBundle[]>(token, "/plugins/webapp"),
};

export type AdminRole = {
  id: string;
  name: string;
  display_name: string;
  description: string;
  permissions: string[];
  scheme_managed?: boolean;
  built_in?: boolean;
  create_at?: number;
  update_at?: number;
};

export type AdminJob = {
  id: string;
  type: string;
  status: string;
  create_at: number;
  start_at?: number;
  last_activity_at?: number;
  progress?: number;
  data?: Record<string, unknown>;
};

export type AdminCompatRecord = Record<string, unknown>;

export type AdminAccessControlSearchResult = {
  policies?: AdminCompatRecord[];
  total_count?: number;
  [key: string]: unknown;
};

export const integrationsApi = {
  // bots
  listBots: (token: string) => request<Bot[]>(token, "/bots"),
  createBot: (token: string, username: string, display_name: string, description: string) =>
    request<Bot>(token, "/bots", {
      method: "POST",
      body: { username, display_name, description },
    }),
  disableBot: (token: string, botId: string) =>
    request<{ status: string }>(token, `/bots/${botId}`, { method: "DELETE" }),

  // PATs
  listTokens: (token: string, userId: string) =>
    request<PAT[]>(token, `/users/${userId}/tokens`),
  createToken: (token: string, userId: string, description: string) =>
    request<CreatedPAT>(token, `/users/${userId}/tokens`, {
      method: "POST",
      body: { description },
    }),
  revokeToken: (token: string, tokenId: string) =>
    request<{ status: string }>(token, `/tokens/${tokenId}/revoke`, { method: "POST" }),

  // incoming webhooks
  listIncoming: (token: string) => request<IncomingWebhook[]>(token, "/hooks/incoming"),
  createIncoming: (
    token: string,
    channel_id: string,
    display_name: string,
    username = "",
    icon_url = "",
    channel_locked = true,
  ) =>
    request<IncomingWebhook>(token, "/hooks/incoming", {
      method: "POST",
      body: { channel_id, display_name, username, icon_url, channel_locked },
    }),
  deleteIncoming: (token: string, hookId: string) =>
    request<{ status: string }>(token, `/hooks/incoming/${hookId}`, { method: "DELETE" }),

  // outgoing webhooks
  listOutgoing: (token: string) => request<OutgoingWebhook[]>(token, "/hooks/outgoing"),
  createOutgoing: (
    token: string,
    team_id: string,
    channel_id: string,
    trigger_words: string[],
    callback_urls: string[],
    display_name = "",
    trigger_when = 0,
    content_type = "application/json",
  ) =>
    request<OutgoingWebhook>(token, "/hooks/outgoing", {
      method: "POST",
      body: {
        team_id,
        channel_id,
        trigger_words,
        callback_urls,
        display_name,
        trigger_when,
        content_type,
      },
    }),
  deleteOutgoing: (token: string, hookId: string) =>
    request<{ status: string }>(token, `/hooks/outgoing/${hookId}`, { method: "DELETE" }),

  // ---- Phase 16: team invites (admin) ----
  //
  // `maxUses = 0` means unlimited within the TTL window. `ttlSeconds` is
  // converted server-side to an expires_at; no client-side clock is trusted.
  createInvite: (
    token: string,
    teamId: string,
    maxUses: number,
    ttlSeconds: number,
  ) =>
    request<Invite>(token, `/teams/${encodeURIComponent(teamId)}/invites`, {
      method: "POST",
      body: { max_uses: maxUses, ttl_seconds: ttlSeconds },
    }),
  listInvites: (token: string, teamId: string) =>
    request<Invite[]>(token, `/teams/${encodeURIComponent(teamId)}/invites`),
  revokeInvite: (token: string, teamId: string, inviteId: string) =>
    request<{ status: string }>(
      token,
      `/teams/${encodeURIComponent(teamId)}/invites/${encodeURIComponent(inviteId)}`,
      { method: "DELETE" },
    ),

  // ---- Phase 16: user deactivate / reactivate ----
  //
  // Deactivate also drops sessions + kicks live WS sockets server-side, so
  // the target's other tabs get a close frame within the read timeout.
  // Reactivate is admin-only and just clears users.delete_at.
  deactivateUser: (token: string, userId: string) =>
    request<{ status: string }>(
      token,
      `/users/${encodeURIComponent(userId)}`,
      { method: "DELETE" },
    ),
  reactivateUser: (token: string, userId: string) =>
    request<{ status: string }>(
      token,
      `/users/${encodeURIComponent(userId)}/reactivate`,
      { method: "POST" },
    ),

  // ---- Phase 16: audit log browse ----
  //
  // `actionPrefix` matches against the leading part of audit.action (e.g.
  // `user.` catches `user.create`/`user.deactivate`/`user.reactivate`).
  // `actor` is a username; the server resolves it to an actor_id and returns
  // an empty list on unknown usernames so typos don't 500.
  listAuditLogs: (
    token: string,
    opts: { limit?: number; actionPrefix?: string; actor?: string } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.limit) qs.set("limit", String(opts.limit));
    if (opts.actionPrefix) qs.set("action_prefix", opts.actionPrefix);
    if (opts.actor) qs.set("actor", opts.actor);
    const tail = qs.toString();
    return request<AuditEntry[]>(token, `/audit/logs${tail ? `?${tail}` : ""}`);
  },
};

export const adminApi = {
  getConfig: (token: string) => request<AdminConfigSnapshot>(token, "/config"),
  reloadConfig: (token: string) =>
    request<{ status: string }>(token, "/config/reload", { method: "POST" }),
  listLogs: (token: string, limit = 20) =>
    request<string[]>(token, `/logs?logs_per_page=${encodeURIComponent(String(limit))}`),
  postLog: (token: string, level: string, message: string) =>
    request<{ status: string }>(token, "/logs", {
      method: "POST",
      body: { level, message },
    }),
  clusterStatus: (token: string) => request<AdminClusterNode[]>(token, "/cluster/status"),
  getServerBusy: (token: string) => request<{ busy: boolean }>(token, "/server_busy"),
  setServerBusy: (token: string) =>
    request<{ status: string }>(token, "/server_busy", { method: "POST" }),
  clearServerBusy: (token: string) =>
    request<{ status: string }>(token, "/server_busy", { method: "DELETE" }),

  listPlugins: (token: string) => request<AdminPlugin[]>(token, "/plugins"),
  getPluginManagementCapabilities: (token: string) =>
    request<PluginManagementCapabilities>(token, "/plugins/capabilities"),
  listPluginStatuses: (token: string) =>
    request<AdminPluginStatus[]>(token, "/plugins/statuses"),
  enablePlugin: (token: string, pluginId: string) =>
    request<{ status: string }>(token, `/plugins/${encodeURIComponent(pluginId)}/enable`, {
      method: "POST",
    }),
  disablePlugin: (token: string, pluginId: string) =>
    request<{ status: string }>(token, `/plugins/${encodeURIComponent(pluginId)}/disable`, {
      method: "POST",
    }),
  uploadPlugin: (token: string, file: File, replace = false) => {
    const body = new FormData();
    body.set("plugin", file, file.name);
    return request<PluginInstallResult>(
      token,
      `/plugins${replace ? "?force=true" : ""}`,
      { method: "POST", body },
    );
  },
  deletePlugin: (token: string, pluginId: string) =>
    request<{ status: string }>(token, `/plugins/${encodeURIComponent(pluginId)}`, {
      method: "DELETE",
    }),
  getPluginConfiguration: (token: string, pluginId: string) =>
    request<PluginConfiguration>(
      token,
      `/plugins/${encodeURIComponent(pluginId)}/configuration`,
    ),
  updatePluginConfiguration: (
    token: string,
    pluginId: string,
    configuration: Record<string, unknown>,
  ) => request<{ status: string }>(
    token,
    `/plugins/${encodeURIComponent(pluginId)}/configuration`,
    { method: "PUT", body: { configuration } },
  ),

  listRoles: (token: string) => request<AdminRole[]>(token, "/roles"),
  patchRole: (token: string, roleId: string, permissions: string[]) =>
    request<AdminRole>(token, `/roles/${encodeURIComponent(roleId)}/patch`, {
      method: "PUT",
      body: { permissions },
    }),

  listJobs: (token: string) => request<AdminJob[]>(token, "/jobs"),
  createJob: (token: string, type: string) =>
    request<AdminJob>(token, "/jobs", { method: "POST", body: { type } }),
  cancelJob: (token: string, jobId: string) =>
    request<AdminJob>(token, `/jobs/${encodeURIComponent(jobId)}/cancel`, { method: "POST" }),

  getLicenseRenewal: (token: string) => request<AdminCompatRecord>(token, "/license/renewal"),
  getLicenseLoadMetric: (token: string) => request<AdminCompatRecord>(token, "/license/load_metric"),
  listLDAPGroups: (token: string) => request<AdminCompatRecord[]>(token, "/ldap/groups"),
  testLDAPConnection: (token: string) =>
    request<AdminCompatRecord>(token, "/ldap/test_connection", { method: "POST" }),
  getSAMLCertificateStatus: (token: string) =>
    request<AdminCompatRecord>(token, "/saml/certificate/status"),

  listRemoteClusters: (token: string) => request<AdminCompatRecord[]>(token, "/remotecluster"),
  listSchemes: (token: string) => request<AdminCompatRecord[]>(token, "/schemes"),
  listGroups: (token: string) => request<AdminCompatRecord[]>(token, "/groups"),
  listDataRetentionPolicies: (token: string) =>
    request<AdminCompatRecord[]>(token, "/data_retention/policies"),
  getDataRetentionPolicy: (token: string) =>
    request<AdminCompatRecord>(token, "/data_retention/policy"),
  listComplianceReports: (token: string) =>
    request<AdminCompatRecord[]>(token, "/compliance/reports"),
  getContentFlaggingConfig: (token: string) =>
    request<AdminCompatRecord>(token, "/content_flagging/config"),
  listContentFlaggingFields: (token: string) =>
    request<AdminCompatRecord[]>(token, "/content_flagging/fields"),
  searchAccessControlPolicies: (token: string) =>
    request<AdminAccessControlSearchResult>(token, "/access_control_policies/search", {
      method: "POST",
      body: { term: "", page: 0, per_page: 50 },
    }),
  listAccessControlCELFields: (token: string) =>
    request<AdminCompatRecord[]>(token, "/access_control_policies/cel/autocomplete/fields"),
};

export function openWebSocket(): WebSocket {
  const scheme = window.location.protocol === "https:" ? "wss" : "ws";
  const url = `${scheme}://${window.location.host}/api/v4/websocket`;
  return new WebSocket(url);
}
