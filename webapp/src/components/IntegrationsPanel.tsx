// Admin-only drawer for managing bots, personal access tokens, and
// incoming/outgoing webhooks. Rendered as a modal-style overlay so it
// doesn't compete with the main chat layout for grid tracks.
//
// We intentionally keep the UI dense and unstyled beyond the shared
// primitives: operators use this rarely, discoverability matters less
// than fitting everything on one screen.
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { useSearchParams } from "react-router-dom";
import type { RootState } from "@/store";
import {
  adminApi,
  api,
  integrationsApi,
  type AdminClusterNode,
  type AdminConfigSnapshot,
  type AdminCompatRecord,
  type AdminJob,
  type AdminPlugin,
  type AdminPluginStatus,
  type AdminRole,
  type AuditEntry,
  type Bot,
  type Channel,
  type CreatedPAT,
  type Emoji,
  type IncomingWebhook,
  type Invite,
  type OutgoingWebhook,
  type PAT,
  type Team,
  type User,
} from "@/api/client";
import { AuthenticatedImage } from "@/components/AuthenticatedMedia";
import { invalidateEmojiCache } from "@/components/EmojiPicker";
import { useEscClose, useConfirm } from "@/components/shared";
import { PluginAdminPanel } from "@/plugins/PluginAdminPanel";
import {
  ADMIN_NAV,
  AUDIT_PREFIXES,
  INVITE_TTL_CHOICES,
  TAB_LABELS,
  type AdminAuthProbe,
  type AdminDetailPanel,
  type AdminPolicyProbe,
  type Tab,
} from "@/features/admin/integrations-config";

export function IntegrationsPanel({
  channels,
  currentTeamId,
  onClose,
}: {
  channels: Channel[];
  currentTeamId: string | null;
  onClose: () => void;
}) {
  useEscClose(true, onClose);
  const confirmer = useConfirm();
  const token = useSelector((s: RootState) => s.auth.token);
  const currentUser = useSelector((s: RootState) => s.auth.user);
  const [searchParams, setSearchParams] = useSearchParams();
  const queryTab = searchParams.get("tab");
  const tab: Tab = queryTab && Object.prototype.hasOwnProperty.call(TAB_LABELS, queryTab)
    ? queryTab as Tab
    : "org";
  const selectTab = useCallback((nextTab: Tab) => {
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      next.set("tab", nextTab);
      return next;
    });
  }, [setSearchParams]);
  const [error, setError] = useState<string | null>(null);
  const [adminSearch, setAdminSearch] = useState("");
  const [collapsedSections, setCollapsedSections] = useState<Record<string, boolean>>({});
  const [adminDetail, setAdminDetail] = useState<AdminDetailPanel | null>(null);

  // Bots
  const [bots, setBots] = useState<Bot[]>([]);
  const [newBotName, setNewBotName] = useState("");
  const [newBotDisplay, setNewBotDisplay] = useState("");
  const [newBotDesc, setNewBotDesc] = useState("");
  // Tokens keyed by bot user_id. Only the freshly created one holds the
  // plaintext `.token`; list refreshes produce the redacted shape.
  const [botTokens, setBotTokens] = useState<Record<string, PAT[]>>({});
  const [freshPAT, setFreshPAT] = useState<CreatedPAT | null>(null);

  // Incoming
  const [incoming, setIncoming] = useState<IncomingWebhook[]>([]);
  const [newIn, setNewIn] = useState({
    channel_id: "",
    display_name: "",
    username: "",
    icon_url: "",
    channel_locked: true,
  });
  const [freshIncomingURL, setFreshIncomingURL] = useState<string | null>(null);

  // Outgoing
  const [outgoing, setOutgoing] = useState<OutgoingWebhook[]>([]);
  const [newOut, setNewOut] = useState({
    channel_id: "",
    trigger_words: "",
    callback_urls: "",
    display_name: "",
    trigger_when: 0,
  });

  // Emoji
  const [emojis, setEmojis] = useState<Emoji[]>([]);
  const [newEmojiName, setNewEmojiName] = useState("");
  const [newEmojiFile, setNewEmojiFile] = useState<File | null>(null);

  // Phase 16 — invites. `maxUsesText` is a string so the input can carry
  // "무제한" via 0; the spinner's default is 1 for principle-of-least-trust.
  const [invites, setInvites] = useState<Invite[]>([]);
  const [inviteMaxUses, setInviteMaxUses] = useState<number>(1);
  const [inviteTTLSeconds, setInviteTTLSeconds] = useState<number>(
    INVITE_TTL_CHOICES[1].seconds, // default 7일
  );

  // Phase 16 — users directory (admin). We fetch the first page; the
  // existing `listUsers` endpoint already paginates and returns `User[]`.
  const [users, setUsers] = useState<User[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);

  // Phase 16 — audit browse. Filters are driven client-side into the
  // server's `?action_prefix=` + `?actor=` params; changing either kicks
  // off a refresh via the effect on `refresh`.
  const [auditRows, setAuditRows] = useState<AuditEntry[]>([]);
  const [auditPrefix, setAuditPrefix] = useState<string>("");
  const [auditActor, setAuditActor] = useState<string>("");

  // Admin compatibility console. These calls intentionally use Mattermost
  // route shapes, so the operator UI doubles as a contract smoke surface.
  const [adminConfig, setAdminConfig] = useState<AdminConfigSnapshot | null>(null);
  const [clusterNodes, setClusterNodes] = useState<AdminClusterNode[]>([]);
  const [serverBusy, setServerBusyState] = useState<boolean>(false);
  const [logRows, setLogRows] = useState<string[]>([]);
  const [pluginRows, setPluginRows] = useState<AdminPlugin[]>([]);
  const [pluginStatuses, setPluginStatuses] = useState<AdminPluginStatus[]>([]);
  const [roles, setRoles] = useState<AdminRole[]>([]);
  const [jobs, setJobs] = useState<AdminJob[]>([]);
  const [newJobType, setNewJobType] = useState<string>("compatibility");
  const [authRows, setAuthRows] = useState<AdminAuthProbe[]>([]);
  const [policyRows, setPolicyRows] = useState<AdminPolicyProbe[]>([]);
  const [globalRetentionPolicy, setGlobalRetentionPolicy] = useState<AdminCompatRecord | null>(null);

  const nonDMChannels = useMemo(() => channels.filter((c) => c.type !== "D"), [channels]);
  const pluginStateByID = useMemo(() => {
    const out: Record<string, string> = {};
    for (const row of pluginStatuses) out[row.plugin_id] = row.state;
    return out;
  }, [pluginStatuses]);
  const activeNavItem = useMemo(() => {
    for (const section of ADMIN_NAV) {
      const item = section.items.find((candidate) => candidate.tab === tab);
      if (item) return { section: section.section, item };
    }
    return null;
  }, [tab]);
  const isSystemAdmin = useMemo(
    () => (currentUser?.roles ?? "").split(/\s+/).includes("system_admin"),
    [currentUser],
  );
  const organizationName = String(adminConfig?.TeamSettings?.SiteName ?? "moyro");
  const pluginRuntimeManagementEnabled = adminConfig?.PluginSettings?.EnableUploads === true;
  const workspaceScope = currentTeamId ? currentTeamId.slice(0, 8) : "all workspaces";
  const query = adminSearch.trim().toLowerCase();
  const filteredUsers = useMemo(
    () => users.filter((u) =>
      !query ||
      u.username.toLowerCase().includes(query) ||
      u.email.toLowerCase().includes(query) ||
      (u.roles ?? "").toLowerCase().includes(query),
    ),
    [query, users],
  );
  const filteredChannels = useMemo(
    () => channels.filter((c) =>
      !query ||
      c.display_name.toLowerCase().includes(query) ||
      c.name.toLowerCase().includes(query) ||
      c.type.toLowerCase().includes(query),
    ),
    [channels, query],
  );
  const filteredTeams = useMemo(
    () => teams.filter((team) =>
      !query ||
      team.display_name.toLowerCase().includes(query) ||
      team.name.toLowerCase().includes(query) ||
      team.type.toLowerCase().includes(query),
    ),
    [query, teams],
  );
  const filteredPlugins = useMemo(
    () => pluginRows.filter((plugin, idx) => {
      const pluginID = String(plugin.id ?? plugin.plugin_id ?? `plugin-${idx}`);
      return !query ||
        pluginID.toLowerCase().includes(query) ||
        String(plugin.name ?? "").toLowerCase().includes(query) ||
        String(plugin.description ?? "").toLowerCase().includes(query);
    }),
    [pluginRows, query],
  );
  const canAccessTab = useCallback((candidate: Tab) => {
    if (isSystemAdmin) return true;
    return candidate === "users" || candidate === "channels" || candidate === "audit";
  }, [isSystemAdmin]);
  const openDetail = useCallback((detail: AdminDetailPanel) => setAdminDetail(detail), []);

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      if (tab === "org") {
        const [config, listedUsers, listedTeams, rolesList] = await Promise.all([
          adminApi.getConfig(token),
          api.listUsers(token, 0, 200, true),
          api.listTeams(token),
          adminApi.listRoles(token),
        ]);
        setAdminConfig(config);
        setUsers(listedUsers);
        setTeams(listedTeams);
        setRoles(rolesList);
      } else if (tab === "workspaces") {
        setTeams(await api.listTeams(token));
      } else if (tab === "channels") {
        // Channels are provided by the chat shell; this tab keeps the admin
        // surface read-only until server-side admin pagination is wired.
      } else if (tab === "apps") {
        const [plugins, statuses, botRows] = await Promise.all([
          adminApi.listPlugins(token),
          adminApi.listPluginStatuses(token),
          integrationsApi.listBots(token),
        ]);
        setPluginRows(plugins);
        setPluginStatuses(statuses);
        setBots(botRows);
      } else if (tab === "bots") {
        setBots(await integrationsApi.listBots(token));
      } else if (tab === "incoming") {
        setIncoming(await integrationsApi.listIncoming(token));
      } else if (tab === "outgoing") {
        setOutgoing(await integrationsApi.listOutgoing(token));
      } else if (tab === "emoji") {
        setEmojis(await api.listEmojis(token));
      } else if (tab === "invites") {
        if (currentTeamId) {
          setInvites(await integrationsApi.listInvites(token, currentTeamId));
        } else {
          setInvites([]);
        }
      } else if (tab === "users") {
        // Admin-only include_deleted so we can render reactivate buttons
        // for deactivated rows. Non-admins would be better off not seeing
        // this tab at all; the backend would silently drop the flag.
        setUsers(await api.listUsers(token, 0, 200, true));
      } else if (tab === "auth") {
        const [config, licenseRenewal, licenseMetric, ldapGroups, ldapProbe, samlStatus] =
          await Promise.all([
            adminApi.getConfig(token),
            adminApi.getLicenseRenewal(token),
            adminApi.getLicenseLoadMetric(token),
            adminApi.listLDAPGroups(token),
            adminApi.testLDAPConnection(token),
            adminApi.getSAMLCertificateStatus(token),
          ]);
        const serviceSettings = config.ServiceSettings ?? {};
        const teamSettings = config.TeamSettings ?? {};
        const samlCertCount = [
          samlStatus.idp_certificate_file,
          samlStatus.public_certificate_file,
          samlStatus.private_key_file,
        ].filter(Boolean).length;
        const mfaEnabled = serviceSettings.EnableMultifactorAuthentication === true;
        setAuthRows([
          {
            key: "ldap",
            label: "LDAP",
            status: ldapProbe.enabled === true ? "enabled" : "disabled",
            detail: "directory groups",
            count: ldapGroups.length,
            tone: ldapProbe.enabled === true ? "ok" : undefined,
          },
          {
            key: "saml",
            label: "SAML / SSO",
            status:
              samlStatus.can_login_with_saml === true ||
              samlStatus.can_login_with_saml_test === true
                ? "ready"
                : "disabled",
            detail: "certificate material",
            count: samlCertCount,
            tone: samlCertCount > 0 ? "ok" : undefined,
          },
          {
            key: "mfa",
            label: "MFA",
            status: mfaEnabled ? "enabled" : "disabled",
            detail: "multifactor policy",
            tone: mfaEnabled ? "ok" : undefined,
          },
          {
            key: "license",
            label: "Enterprise License",
            status: licenseRenewal.is_licensed === true ? "licensed" : "community",
            detail: "renewal and trial state",
            tone: licenseRenewal.is_licensed === true ? "ok" : undefined,
          },
          {
            key: "sessions",
            label: "Session Policy",
            status: `${String(serviceSettings.SessionLengthWebInHours ?? "auto")}h`,
            detail: `open server ${teamSettings.EnableOpenServer === false ? "off" : "on"}`,
            count: Number(licenseMetric.active_users ?? 0),
          },
        ]);
      } else if (tab === "system") {
        const [config, cluster, busy, logs] = await Promise.all([
          adminApi.getConfig(token),
          adminApi.clusterStatus(token),
          adminApi.getServerBusy(token),
          adminApi.listLogs(token, 12),
        ]);
        setAdminConfig(config);
        setClusterNodes(cluster);
        setServerBusyState(Boolean(busy.busy));
        setLogRows(logs);
      } else if (tab === "plugins") {
        const [plugins, statuses, config] = await Promise.all([
          adminApi.listPlugins(token),
          adminApi.listPluginStatuses(token),
          adminApi.getConfig(token),
        ]);
        setPluginRows(plugins);
        setPluginStatuses(statuses);
        setAdminConfig(config);
      } else if (tab === "roles") {
        setRoles(await adminApi.listRoles(token));
      } else if (tab === "jobs") {
        setJobs(await adminApi.listJobs(token));
      } else if (tab === "policies") {
        const [
          globalRetention,
          retentionPolicies,
          complianceReports,
          contentFlaggingConfig,
          contentFlaggingFields,
          remoteClusters,
          groups,
          schemes,
          accessControl,
          accessControlFields,
        ] = await Promise.all([
          adminApi.getDataRetentionPolicy(token),
          adminApi.listDataRetentionPolicies(token),
          adminApi.listComplianceReports(token),
          adminApi.getContentFlaggingConfig(token),
          adminApi.listContentFlaggingFields(token),
          adminApi.listRemoteClusters(token),
          adminApi.listGroups(token),
          adminApi.listSchemes(token),
          adminApi.searchAccessControlPolicies(token),
          adminApi.listAccessControlCELFields(token),
        ]);
        const accessControlPolicies = Array.isArray(accessControl.policies)
          ? accessControl.policies
          : [];
        const accessControlCount = Number(accessControl.total_count ?? accessControlPolicies.length);
        setGlobalRetentionPolicy(globalRetention);
        setPolicyRows([
          {
            key: "access-control",
            label: "Access Control",
            status: accessControlCount > 0 ? "policies" : "ready",
            detail: `CEL fields ${accessControlFields.length}`,
            count: accessControlCount,
            tone: "ok",
          },
          {
            key: "data-retention",
            label: "Data Retention",
            status: retentionPolicies.length > 0 ? "custom" : "global",
            detail: "message/file retention policy",
            count: retentionPolicies.length,
            tone: retentionPolicies.length > 0 ? "ok" : undefined,
          },
          {
            key: "content-flagging",
            label: "Content Flagging",
            status: contentFlaggingConfig.enabled === true ? "enabled" : "disabled",
            detail: "review queue fields",
            count: contentFlaggingFields.length,
            tone: contentFlaggingConfig.enabled === true ? "ok" : undefined,
          },
          {
            key: "compliance",
            label: "Compliance Reports",
            status: complianceReports.length > 0 ? "reports" : "ready",
            detail: "exportable audit report surface",
            count: complianceReports.length,
            tone: "ok",
          },
          {
            key: "remote-clusters",
            label: "Remote Clusters",
            status: remoteClusters.length > 0 ? "linked" : "none",
            detail: "shared channel federation",
            count: remoteClusters.length,
          },
          {
            key: "groups",
            label: "Groups",
            status: groups.length > 0 ? "synced" : "none",
            detail: "LDAP/custom group policy shape",
            count: groups.length,
          },
          {
            key: "schemes",
            label: "Permission Schemes",
            status: schemes.length > 0 ? "custom" : "default",
            detail: "team/channel role scheme shape",
            count: schemes.length,
          },
        ]);
      } else if (tab === "audit") {
        setAuditRows(
          await integrationsApi.listAuditLogs(token, {
            limit: 100,
            actionPrefix: auditPrefix || undefined,
            actor: auditActor.trim() || undefined,
          }),
        );
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "로드 실패");
    }
  }, [token, tab, currentTeamId, auditPrefix, auditActor, channels]);

  useEffect(() => { refresh(); }, [refresh]);
  useEffect(() => { setAdminDetail(null); }, [tab]);

  // ---- Bot actions ----
  async function onCreateBot() {
    if (!token || !newBotName.trim()) return;
    try {
      const b = await integrationsApi.createBot(token, newBotName.trim(), newBotDisplay.trim(), newBotDesc.trim());
      setBots((prev) => [...prev, b]);
      setNewBotName(""); setNewBotDisplay(""); setNewBotDesc("");
      // Auto-mint a PAT so the operator has something usable right away.
      // Without this they have to separately click "Create token".
      const pat = await integrationsApi.createToken(token, b.user_id, "initial");
      setFreshPAT(pat);
    } catch (e) {
      setError(e instanceof Error ? e.message : "봇 생성 실패");
    }
  }

  async function onDisableBot(botId: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "봇 비활성화",
      message: "봇을 비활성화할까요? 모든 토큰이 무효화됩니다.",
      confirmLabel: "비활성화",
      destructive: true,
    });
    if (!ok) return;
    try {
      await integrationsApi.disableBot(token, botId);
      setBots((prev) => prev.filter((b) => b.user_id !== botId));
    } catch (e) {
      setError(e instanceof Error ? e.message : "봇 비활성화 실패");
    }
  }

  async function onLoadTokens(botId: string) {
    if (!token) return;
    try {
      const list = await integrationsApi.listTokens(token, botId);
      setBotTokens((prev) => ({ ...prev, [botId]: list }));
    } catch (e) {
      setError(e instanceof Error ? e.message : "토큰 조회 실패");
    }
  }

  async function onCreatePAT(botId: string) {
    if (!token) return;
    const description = prompt("토큰 설명(옵션)") ?? "";
    try {
      const pat = await integrationsApi.createToken(token, botId, description);
      setFreshPAT(pat);
      onLoadTokens(botId);
    } catch (e) {
      setError(e instanceof Error ? e.message : "토큰 생성 실패");
    }
  }

  async function onRevokePAT(tokenId: string, botId: string) {
    if (!token) return;
    try {
      await integrationsApi.revokeToken(token, tokenId);
      onLoadTokens(botId);
    } catch (e) {
      setError(e instanceof Error ? e.message : "토큰 취소 실패");
    }
  }

  // ---- Incoming actions ----
  async function onCreateIncoming() {
    if (!token || !newIn.channel_id) return;
    try {
      const hk = await integrationsApi.createIncoming(
        token, newIn.channel_id, newIn.display_name, newIn.username, newIn.icon_url, newIn.channel_locked,
      );
      setIncoming((prev) => [hk, ...prev]);
      // Build the user-facing URL relative to the current origin — the
      // server mounts /hooks/{id} outside /api/v4 so we construct directly.
      const url = `${window.location.origin}/hooks/${hk.id}`;
      setFreshIncomingURL(url);
      setNewIn({ channel_id: "", display_name: "", username: "", icon_url: "", channel_locked: true });
    } catch (e) {
      setError(e instanceof Error ? e.message : "웹훅 생성 실패");
    }
  }

  async function onDeleteIncoming(id: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "인커밍 웹훅 삭제",
      message: "이 웹훅을 삭제할까요? URL이 즉시 무효화됩니다.",
      confirmLabel: "삭제",
      destructive: true,
    });
    if (!ok) return;
    try {
      await integrationsApi.deleteIncoming(token, id);
      setIncoming((prev) => prev.filter((h) => h.id !== id));
    } catch (e) {
      setError(e instanceof Error ? e.message : "웹훅 삭제 실패");
    }
  }

  // ---- Outgoing actions ----
  async function onCreateOutgoing() {
    if (!token || !currentTeamId) return;
    const words = newOut.trigger_words.split(",").map((s) => s.trim()).filter(Boolean);
    const urls = newOut.callback_urls.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
    if (urls.length === 0) { setError("콜백 URL이 필요합니다"); return; }
    try {
      const hk = await integrationsApi.createOutgoing(
        token, currentTeamId, newOut.channel_id, words, urls, newOut.display_name, newOut.trigger_when,
      );
      setOutgoing((prev) => [hk, ...prev]);
      setNewOut({ channel_id: "", trigger_words: "", callback_urls: "", display_name: "", trigger_when: 0 });
    } catch (e) {
      setError(e instanceof Error ? e.message : "아웃고잉 웹훅 생성 실패");
    }
  }

  async function onDeleteOutgoing(id: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "아웃고잉 웹훅 삭제",
      message: "이 아웃고잉 웹훅을 삭제할까요?",
      confirmLabel: "삭제",
      destructive: true,
    });
    if (!ok) return;
    try {
      await integrationsApi.deleteOutgoing(token, id);
      setOutgoing((prev) => prev.filter((h) => h.id !== id));
    } catch (e) {
      setError(e instanceof Error ? e.message : "삭제 실패");
    }
  }

  // ---- Emoji actions ----
  async function onCreateEmoji() {
    if (!token || !newEmojiFile) return;
    const name = newEmojiName.trim().toLowerCase();
    if (!/^[a-z0-9_-]{1,40}$/.test(name)) {
      setError("이모지 이름은 영소문자/숫자/_/- 로 1~40자");
      return;
    }
    try {
      const e = await api.createEmoji(token, name, newEmojiFile);
      setEmojis((prev) => [e, ...prev]);
      setNewEmojiName("");
      setNewEmojiFile(null);
      // The picker caches its list aggressively; bust it so the new
      // emoji shows up on next open without a page reload.
      invalidateEmojiCache();
    } catch (e) {
      setError(e instanceof Error ? e.message : "이모지 업로드 실패");
    }
  }

  async function onDeleteEmoji(id: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "이모지 삭제",
      message: "이 이모지를 삭제할까요? 기존 메시지의 반응 표시가 깨질 수 있습니다.",
      confirmLabel: "삭제",
      destructive: true,
    });
    if (!ok) return;
    try {
      await api.deleteEmoji(token, id);
      setEmojis((prev) => prev.filter((e) => e.id !== id));
      invalidateEmojiCache();
    } catch (e) {
      setError(e instanceof Error ? e.message : "이모지 삭제 실패");
    }
  }

  // ---- Invite actions ----
  async function onCreateInvite() {
    if (!token || !currentTeamId) {
      setError("팀을 먼저 선택하세요");
      return;
    }
    try {
      const inv = await integrationsApi.createInvite(
        token,
        currentTeamId,
        inviteMaxUses,
        inviteTTLSeconds,
      );
      setInvites((prev) => [inv, ...prev]);
    } catch (e) {
      setError(e instanceof Error ? e.message : "초대 링크 생성 실패");
    }
  }

  async function onCopyInvite(url: string) {
    // Relative URLs need to be absolutised for the clipboard payload so
    // the recipient can open the link outside the current tab's context.
    const abs = url.startsWith("http") ? url : `${window.location.origin}${url}`;
    try {
      await navigator.clipboard.writeText(abs);
    } catch {
      // Fallback: temporary textarea. navigator.clipboard requires HTTPS
      // or localhost, and this panel is often used on LAN/dev setups.
      const ta = document.createElement("textarea");
      ta.value = abs;
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand("copy"); } catch { /* ignore */ }
      document.body.removeChild(ta);
    }
  }

  async function onRevokeInvite(id: string) {
    if (!token || !currentTeamId) return;
    const ok = await confirmer.confirm({
      title: "초대 링크 무효화",
      message: "이 초대 링크를 즉시 무효화할까요? 아직 가입하지 않은 수신자는 더 이상 사용할 수 없습니다.",
      confirmLabel: "무효화",
      destructive: true,
    });
    if (!ok) return;
    try {
      await integrationsApi.revokeInvite(token, currentTeamId, id);
      setInvites((prev) => prev.filter((i) => i.id !== id));
    } catch (e) {
      setError(e instanceof Error ? e.message : "초대 링크 무효화 실패");
    }
  }

  // ---- User actions ----
  async function onDeactivateUser(userId: string, username: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "사용자 비활성화",
      message: `${username} 님을 비활성화할까요? 모든 세션이 종료되고 로그인할 수 없게 됩니다.`,
      confirmLabel: "비활성화",
      destructive: true,
    });
    if (!ok) return;
    try {
      await integrationsApi.deactivateUser(token, userId);
      // Mirror the server state locally without a refetch round-trip.
      setUsers((prev) =>
        prev.map((u) =>
          u.id === userId ? { ...u, update_at: Date.now() } : u,
        ),
      );
      // Simplest correct approach: refresh once so delete_at reflects.
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "사용자 비활성화 실패");
    }
  }

  async function onReactivateUser(userId: string) {
    if (!token) return;
    try {
      await integrationsApi.reactivateUser(token, userId);
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "사용자 활성화 실패");
    }
  }

  // ---- Admin compatibility actions ----
  async function onReloadConfig() {
    if (!token) return;
    try {
      await adminApi.reloadConfig(token);
      await adminApi.postLog(token, "info", "admin console requested config reload");
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "설정 리로드 실패");
    }
  }

  async function onSetBusy(next: boolean) {
    if (!token) return;
    try {
      if (next) await adminApi.setServerBusy(token);
      else await adminApi.clearServerBusy(token);
      setServerBusyState(next);
    } catch (e) {
      setError(e instanceof Error ? e.message : "서버 busy 상태 변경 실패");
    }
  }

  async function onCreateJob() {
    if (!token || !newJobType.trim()) return;
    try {
      const job = await adminApi.createJob(token, newJobType.trim());
      setJobs((prev) => [job, ...prev]);
    } catch (e) {
      setError(e instanceof Error ? e.message : "작업 생성 실패");
    }
  }

  async function onCancelJob(jobId: string) {
    if (!token) return;
    try {
      const job = await adminApi.cancelJob(token, jobId);
      setJobs((prev) => prev.map((row) => row.id === job.id ? job : row));
    } catch (e) {
      setError(e instanceof Error ? e.message : "작업 취소 실패");
    }
  }

  return (
    <main className="admin-page">
      <div className="integrations-panel admin-page-frame">
        <header className="integrations-header">
          <h3 style={{ margin: 0 }}>System Console</h3>
          <button type="button" className="action-btn" onClick={onClose} title="닫기">✕</button>
        </header>
        <div className="admin-console-topbar">
          <div className="admin-scope-chip">
            <span>조직</span>
            <strong>{organizationName}</strong>
          </div>
          <div className="admin-scope-chip">
            <span>Workspace Scope</span>
            <strong>{workspaceScope}</strong>
          </div>
          <input
            className="field-input admin-console-search"
            value={adminSearch}
            onChange={(e) => setAdminSearch(e.target.value)}
            placeholder="Search members, channels, apps, logs"
            aria-label="Search admin data"
          />
          <div className="admin-account-chip">
            <span>{currentUser?.username ?? "admin"}</span>
            <small>{currentUser?.roles ?? "system_user"}</small>
          </div>
        </div>
        <div className="admin-console-shell">
          <nav className="admin-console-tree moyro-scrollbar admin-user-menu-scroll" aria-label="System Console">
            {ADMIN_NAV.map((section) => {
              const collapsed = collapsedSections[section.section] === true;
              return (
                <div key={section.section} className="admin-console-tree-section">
                  <button
                    type="button"
                    className="admin-console-tree-heading"
                    aria-expanded={!collapsed}
                    onClick={() =>
                      setCollapsedSections((prev) => ({
                        ...prev,
                        [section.section]: !prev[section.section],
                      }))
                    }
                  >
                    <span>{section.section}</span>
                    <small>{collapsed ? "+" : "−"}</small>
                  </button>
                  {!collapsed && section.items.map((item) => {
                    const disabled = !canAccessTab(item.tab);
                    return (
                      <button
                        key={item.tab}
                        type="button"
                        className="admin-console-tree-item"
                        aria-selected={tab === item.tab}
                        aria-current={tab === item.tab ? "page" : undefined}
                        aria-disabled={disabled}
                        disabled={disabled}
                        onClick={() => selectTab(item.tab)}
                        title={disabled ? "권한이 필요합니다" : item.label}
                      >
                        <span>{item.label}</span>
                        <small>{TAB_LABELS[item.tab]}</small>
                      </button>
                    );
                  })}
                </div>
              );
            })}
          </nav>
          <section className="admin-console-panel">
            <div className="admin-console-panel-heading">
              <div>
                <span className="admin-console-panel-kicker">
                  {activeNavItem?.section ?? "System Console"}
                </span>
                <strong>{activeNavItem?.item.label ?? TAB_LABELS[tab]}</strong>
              </div>
              <span className="admin-pill">{TAB_LABELS[tab]}</span>
            </div>

            {error && <div className="login-error" style={{ margin: "0 0 12px" }}>{error}</div>}
            {adminDetail && (
              <aside className="admin-detail-panel" aria-label="Admin detail">
                <div className="admin-detail-panel-head">
                  <div>
                    <span>Detail</span>
                    <strong>{adminDetail.title}</strong>
                  </div>
                  <button type="button" className="action-btn" onClick={() => setAdminDetail(null)} title="Close detail">✕</button>
                </div>
                <p>{adminDetail.subtitle}</p>
                <dl>
                  {adminDetail.rows.map((row) => (
                    <div key={row.label}>
                      <dt>{row.label}</dt>
                      <dd>{row.value}</dd>
                    </div>
                  ))}
                </dl>
              </aside>
            )}

            {/* One-time reveal for newly minted PATs */}
            {freshPAT && (
          <div className="reveal-card">
            <div style={{ fontWeight: 600 }}>토큰이 생성되었습니다. 지금 복사해 두세요. 이후에는 다시 볼 수 없습니다.</div>
            <code className="reveal-code">{freshPAT.token}</code>
            <button type="button" className="btn-ghost" onClick={() => setFreshPAT(null)}>확인</button>
          </div>
            )}
            {freshIncomingURL && (
          <div className="reveal-card">
            <div style={{ fontWeight: 600 }}>인커밍 웹훅 URL이 생성되었습니다. 이 URL을 공유하면 누구나 메시지를 보낼 수 있습니다.</div>
            <code className="reveal-code">{freshIncomingURL}</code>
            <button type="button" className="btn-ghost" onClick={() => setFreshIncomingURL(null)}>확인</button>
          </div>
            )}

        {tab === "org" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >조직 새로고침</button>
              <span className={isSystemAdmin ? "admin-pill ok" : "admin-pill"}>{isSystemAdmin ? "Org Owner" : "Admin"}</span>
            </div>
            <div className="admin-summary-grid">
              <div className="admin-kv">
                <span>조직</span>
                <strong>{organizationName}</strong>
              </div>
              <div className="admin-kv">
                <span>Members</span>
                <strong>{users.length}</strong>
              </div>
              <div className="admin-kv">
                <span>Workspaces</span>
                <strong>{teams.length}</strong>
              </div>
              <div className="admin-kv">
                <span>System Roles</span>
                <strong>{roles.length}</strong>
              </div>
            </div>
            <table className="admin-data-table">
              <thead>
                <tr>
                  <th>Area</th>
                  <th>Status</th>
                  <th>Controls</th>
                  <th>Audit</th>
                </tr>
              </thead>
              <tbody>
                {[
                  ["Members", `${users.length} loaded`, "Deactivate, reactivate, role review", "user.*"],
                  ["Workspaces", `${teams.length} visible`, "Scope, invites, workspace settings", "team.*"],
                  ["Apps", `${pluginRows.length + bots.length} installed/requested`, "Approve, enable, disable", "plugin.* / bot.*"],
                  ["Security", "policy probes", "SSO, MFA, sessions, license", "system.*"],
                ].map(([area, status, controls, audit]) => (
                  <tr
                    key={area}
                    tabIndex={0}
                    onClick={() => openDetail({
                      title: area,
                      subtitle: "Administrative operating area",
                      rows: [
                        { label: "Status", value: status },
                        { label: "Controls", value: controls },
                        { label: "Audit Filter", value: audit },
                      ],
                    })}
                  >
                    <td>{area}</td>
                    <td><span className="admin-pill ok">{status}</span></td>
                    <td>{controls}</td>
                    <td><code>{audit}</code></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {tab === "workspaces" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >워크스페이스 새로고침</button>
              <span className="admin-pill">server paginated target</span>
            </div>
            <table className="admin-data-table">
              <thead>
                <tr>
                  <th>Workspace</th>
                  <th>Visibility</th>
                  <th>Created</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {filteredTeams.length === 0 && (
                  <tr><td colSpan={4} className="admin-empty-cell">워크스페이스가 없거나 검색 결과가 없습니다.</td></tr>
                )}
                {filteredTeams.map((team) => (
                  <tr
                    key={team.id}
                    tabIndex={0}
                    onClick={() => openDetail({
                      title: team.display_name,
                      subtitle: team.name,
                      rows: [
                        { label: "ID", value: team.id },
                        { label: "Visibility", value: team.type === "O" ? "open" : "invite only" },
                        { label: "Created", value: new Date(team.create_at).toLocaleString() },
                      ],
                    })}
                  >
                    <td>{team.display_name}</td>
                    <td>{team.type === "O" ? "Open" : "Private"}</td>
                    <td>{new Date(team.create_at).toLocaleDateString()}</td>
                    <td><span className="admin-pill ok">active</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {tab === "channels" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >채널 새로고침</button>
              <span className="admin-pill">{filteredChannels.length} rows</span>
            </div>
            <table className="admin-data-table">
              <thead>
                <tr>
                  <th>Channel</th>
                  <th>Type</th>
                  <th>Created</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {filteredChannels.length === 0 && (
                  <tr><td colSpan={4} className="admin-empty-cell">채널이 없거나 검색 결과가 없습니다.</td></tr>
                )}
                {filteredChannels.map((channel) => {
                  const archived = (channel.delete_at ?? 0) > 0;
                  return (
                    <tr
                      key={channel.id}
                      tabIndex={0}
                      onClick={() => openDetail({
                        title: channel.display_name || channel.name,
                        subtitle: `#${channel.name}`,
                        rows: [
                          { label: "ID", value: channel.id },
                          { label: "Team", value: channel.team_id },
                          { label: "Type", value: channel.type },
                          { label: "Status", value: archived ? "archived" : "active" },
                        ],
                      })}
                    >
                      <td>{channel.display_name || channel.name}</td>
                      <td>{channel.type === "O" ? "Public" : channel.type === "P" ? "Private" : "Direct"}</td>
                      <td>{new Date(channel.create_at).toLocaleDateString()}</td>
                      <td><span className={archived ? "admin-pill danger" : "admin-pill ok"}>{archived ? "archived" : "active"}</span></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        {tab === "apps" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >앱 새로고침</button>
              <span className="admin-pill ok">approval workflow ready</span>
            </div>
            <table className="admin-data-table">
              <thead>
                <tr>
                  <th>App</th>
                  <th>Kind</th>
                  <th>Approval</th>
                  <th>Scope</th>
                </tr>
              </thead>
              <tbody>
                {filteredPlugins.length === 0 && bots.length === 0 && (
                  <tr><td colSpan={4} className="admin-empty-cell">앱 또는 플러그인이 없습니다.</td></tr>
                )}
                {filteredPlugins.map((plugin, idx) => {
                  const pluginId = String(plugin.id ?? plugin.plugin_id ?? `plugin-${idx}`);
                  const state = pluginStateByID[pluginId] ?? String(plugin.state ?? "unknown");
                  return (
                    <tr
                      key={pluginId}
                      tabIndex={0}
                      onClick={() => openDetail({
                        title: String(plugin.name ?? pluginId),
                        subtitle: pluginId,
                        rows: [
                          { label: "Kind", value: "Plugin" },
                          { label: "State", value: state },
                          { label: "Version", value: String(plugin.version ?? "dev") },
                        ],
                      })}
                    >
                      <td>{String(plugin.name ?? pluginId)}</td>
                      <td>Plugin</td>
                      <td><span className={state === "running" || state === "enabled" ? "admin-pill ok" : "admin-pill"}>{state}</span></td>
                      <td>system</td>
                    </tr>
                  );
                })}
                {bots
                  .filter((bot) => !query || bot.username.toLowerCase().includes(query) || (bot.description ?? "").toLowerCase().includes(query))
                  .map((bot) => (
                    <tr
                      key={bot.user_id}
                      tabIndex={0}
                      onClick={() => openDetail({
                        title: `@${bot.username}`,
                        subtitle: bot.user_id,
                        rows: [
                          { label: "Kind", value: "Bot" },
                          { label: "Approval", value: "approved" },
                          { label: "Description", value: bot.description || "-" },
                        ],
                      })}
                    >
                      <td>@{bot.username}</td>
                      <td>Bot</td>
                      <td><span className="admin-pill ok">approved</span></td>
                      <td>tokens</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}

        {tab === "bots" && (
          <div className="integrations-body">
            <div className="integrations-create">
              <input className="field-input" placeholder="username (영소문자/숫자)"
                value={newBotName} onChange={(e) => setNewBotName(e.target.value)} />
              <input className="field-input" placeholder="표시 이름"
                value={newBotDisplay} onChange={(e) => setNewBotDisplay(e.target.value)} />
              <input className="field-input" placeholder="설명 (옵션)"
                value={newBotDesc} onChange={(e) => setNewBotDesc(e.target.value)} />
              <button className="btn-primary" onClick={onCreateBot}
                style={{ width: "auto", padding: "0 14px", height: 38 }}>봇 만들기</button>
            </div>
            <ul className="integrations-list">
              {bots.length === 0 && <li className="chat-empty" style={{ padding: 12 }}>등록된 봇이 없습니다.</li>}
              {bots.map((b) => (
                <li key={b.user_id} className="integrations-row">
                  <div style={{ flex: 1 }}>
                    <div style={{ fontWeight: 600 }}>@{b.username}</div>
                    <div style={{ color: "var(--muted)", fontSize: 13 }}>{b.description || "—"}</div>
                    {botTokens[b.user_id] && (
                      <div style={{ marginTop: 6 }}>
                        {botTokens[b.user_id].length === 0
                          ? <span style={{ color: "var(--muted)", fontSize: 13 }}>발급된 토큰 없음</span>
                          : (
                            <ul className="pat-list">
                              {botTokens[b.user_id].map((t) => (
                                <li key={t.id}>
                                  <span>{t.description || "(설명없음)"}</span>
                                  <span style={{ color: "var(--muted)", fontSize: 13, marginLeft: 8 }}>
                                    {t.revoked_at ? "취소됨" : "활성"}
                                  </span>
                                  {!t.revoked_at && (
                                    <button type="button" className="action-btn"
                                      onClick={() => onRevokePAT(t.id, b.user_id)}>🗑</button>
                                  )}
                                </li>
                              ))}
                            </ul>
                          )}
                      </div>
                    )}
                  </div>
                  <div style={{ display: "flex", gap: 6, flexWrap: "wrap", justifyContent: "flex-end" }}>
                    <button className="btn-ghost" style={{ width: "auto", padding: "0 10px", height: 30 }}
                      onClick={() => onLoadTokens(b.user_id)}>토큰 조회</button>
                    <button className="btn-ghost" style={{ width: "auto", padding: "0 10px", height: 30 }}
                      onClick={() => onCreatePAT(b.user_id)}>새 토큰</button>
                    <button className="btn-ghost" style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                      onClick={() => onDisableBot(b.user_id)}>비활성화</button>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "incoming" && (
          <div className="integrations-body">
            <div className="integrations-create">
              <select className="field-input" value={newIn.channel_id}
                onChange={(e) => setNewIn((prev) => ({ ...prev, channel_id: e.target.value }))}>
                <option value="">채널 선택…</option>
                {nonDMChannels.map((c) => (
                  <option key={c.id} value={c.id}>#{c.display_name}</option>
                ))}
              </select>
              <input className="field-input" placeholder="표시 이름 (봇 이름)"
                value={newIn.display_name} onChange={(e) => setNewIn((p) => ({ ...p, display_name: e.target.value }))} />
              <input className="field-input" placeholder="오버라이드 username (옵션)"
                value={newIn.username} onChange={(e) => setNewIn((p) => ({ ...p, username: e.target.value }))} />
              <input className="field-input" placeholder="아이콘 URL (옵션)"
                value={newIn.icon_url} onChange={(e) => setNewIn((p) => ({ ...p, icon_url: e.target.value }))} />
              <label style={{ display: "flex", alignItems: "center", gap: 6 }}>
                <input type="checkbox" checked={newIn.channel_locked}
                  onChange={(e) => setNewIn((p) => ({ ...p, channel_locked: e.target.checked }))} />
                <span style={{ fontSize: 13 }}>채널 고정</span>
              </label>
              <button className="btn-primary" onClick={onCreateIncoming}
                style={{ width: "auto", padding: "0 14px", height: 38 }}>생성</button>
            </div>
            <ul className="integrations-list">
              {incoming.length === 0 && <li className="chat-empty" style={{ padding: 12 }}>인커밍 웹훅 없음.</li>}
              {incoming.map((hk) => (
                <li key={hk.id} className="integrations-row">
                  <div style={{ flex: 1 }}>
                    <div style={{ fontWeight: 600 }}>{hk.display_name || "(이름없음)"}</div>
                    <div style={{ color: "var(--muted)", fontSize: 13 }}>채널 {hk.channel_id} · 잠금 {hk.channel_locked ? "ON" : "OFF"}</div>
                    <code className="reveal-code" style={{ marginTop: 4, padding: "2px 6px", fontSize: 13 }}>{`${window.location.origin}/hooks/${hk.id}`}</code>
                  </div>
                  <button className="btn-ghost" style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                    onClick={() => onDeleteIncoming(hk.id)}>삭제</button>
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "outgoing" && (
          <div className="integrations-body">
            <div className="integrations-create">
              <select className="field-input" value={newOut.channel_id}
                onChange={(e) => setNewOut((p) => ({ ...p, channel_id: e.target.value }))}>
                <option value="">채널 (비우면 팀 전체)</option>
                {nonDMChannels.map((c) => (
                  <option key={c.id} value={c.id}>#{c.display_name}</option>
                ))}
              </select>
              <input className="field-input" placeholder="트리거 단어 (쉼표로 구분)"
                value={newOut.trigger_words} onChange={(e) => setNewOut((p) => ({ ...p, trigger_words: e.target.value }))} />
              <input className="field-input" placeholder="콜백 URL (공백/쉼표로 구분)"
                value={newOut.callback_urls} onChange={(e) => setNewOut((p) => ({ ...p, callback_urls: e.target.value }))} />
              <select className="field-input" value={newOut.trigger_when}
                onChange={(e) => setNewOut((p) => ({ ...p, trigger_when: Number(e.target.value) }))}>
                <option value={0}>첫 단어 일치</option>
                <option value={1}>어디든 포함</option>
              </select>
              <input className="field-input" placeholder="표시 이름 (옵션)"
                value={newOut.display_name} onChange={(e) => setNewOut((p) => ({ ...p, display_name: e.target.value }))} />
              <button className="btn-primary" onClick={onCreateOutgoing}
                style={{ width: "auto", padding: "0 14px", height: 38 }}>생성</button>
            </div>
            <ul className="integrations-list">
              {outgoing.length === 0 && <li className="chat-empty" style={{ padding: 12 }}>아웃고잉 웹훅 없음.</li>}
              {outgoing.map((hk) => (
                <li key={hk.id} className="integrations-row">
                  <div style={{ flex: 1 }}>
                    <div style={{ fontWeight: 600 }}>{hk.display_name || "(이름없음)"}</div>
                    <div style={{ color: "var(--muted)", fontSize: 13 }}>
                      트리거: {hk.trigger_words.join(", ") || "(없음)"} · 콜백 {hk.callback_urls.length}개
                    </div>
                  </div>
                  <button className="btn-ghost" style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                    onClick={() => onDeleteOutgoing(hk.id)}>삭제</button>
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "emoji" && (
          <div className="integrations-body">
            <div className="integrations-create">
              <input className="field-input" placeholder="이름 (영소문자/숫자/_/-)"
                value={newEmojiName}
                onChange={(e) => setNewEmojiName(e.target.value.toLowerCase())}
                style={{ flex: "1 1 180px" }} />
              <input type="file" accept="image/png,image/jpeg,image/gif,image/webp"
                onChange={(e) => setNewEmojiFile(e.target.files?.[0] ?? null)} />
              <button className="btn-primary" onClick={onCreateEmoji}
                disabled={!newEmojiName || !newEmojiFile}
                style={{ width: "auto", padding: "0 14px", height: 38 }}>업로드</button>
            </div>
            <ul className="integrations-list emoji-grid">
              {emojis.length === 0 && <li className="chat-empty" style={{ padding: 12 }}>등록된 이모지가 없습니다.</li>}
              {emojis.map((e) => (
                <li key={e.id} className="emoji-tile">
                  <AuthenticatedImage token={token ?? ""} path={api.emojiImagePath(e.id)} alt={e.name} />
                  <div className="emoji-tile-name" title={`:${e.name}:`}>:{e.name}:</div>
                  <button
                    type="button"
                    className="btn-ghost"
                    style={{ width: "auto", padding: "0 8px", height: 26, color: "var(--danger)", fontSize: 13 }}
                    onClick={() => onDeleteEmoji(e.id)}
                  >삭제</button>
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "invites" && (
          <div className="integrations-body">
            {!currentTeamId ? (
              <div className="chat-empty" style={{ padding: 12 }}>
                팀을 먼저 선택하면 초대 링크를 발급할 수 있습니다.
              </div>
            ) : (
              <>
                <div className="integrations-create">
                  <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13 }}>
                    최대 사용 횟수
                    <select
                      className="field-input"
                      style={{ width: 120 }}
                      value={inviteMaxUses}
                      onChange={(e) => setInviteMaxUses(Number(e.target.value))}
                    >
                      <option value={1}>1회</option>
                      <option value={5}>5회</option>
                      <option value={25}>25회</option>
                      <option value={0}>무제한</option>
                    </select>
                  </label>
                  <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13 }}>
                    만료
                    <select
                      className="field-input"
                      style={{ width: 120 }}
                      value={inviteTTLSeconds}
                      onChange={(e) => setInviteTTLSeconds(Number(e.target.value))}
                    >
                      {INVITE_TTL_CHOICES.map((c) => (
                        <option key={c.seconds} value={c.seconds}>{c.label}</option>
                      ))}
                    </select>
                  </label>
                  <button
                    className="btn-primary"
                    onClick={onCreateInvite}
                    style={{ width: "auto", padding: "0 14px", height: 38 }}
                  >초대 링크 생성</button>
                </div>
                <ul className="integrations-list">
                  {invites.length === 0 && (
                    <li className="chat-empty" style={{ padding: 12 }}>활성 초대 링크가 없습니다.</li>
                  )}
                  {invites.map((inv) => {
                    const remaining = inv.max_uses === 0
                      ? "무제한"
                      : `${inv.max_uses - inv.use_count} / ${inv.max_uses}`;
                    const expires = new Date(inv.expires_at).toLocaleString();
                    return (
                      <li key={inv.id} className="integrations-row">
                        <div style={{ flex: 1, minWidth: 0 }}>
                          <div style={{ fontWeight: 600, fontSize: 13, wordBreak: "break-all" }}>{inv.url}</div>
                          <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                            남은 사용 {remaining} · 만료 {expires}
                          </div>
                        </div>
                        <div style={{ display: "flex", gap: 6, flexWrap: "wrap", justifyContent: "flex-end" }}>
                          <button
                            type="button"
                            className="btn-ghost"
                            style={{ width: "auto", padding: "0 10px", height: 30 }}
                            onClick={() => onCopyInvite(inv.url)}
                          >복사</button>
                          <button
                            type="button"
                            className="btn-ghost"
                            style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                            onClick={() => onRevokeInvite(inv.id)}
                          >무효화</button>
                        </div>
                      </li>
                    );
                  })}
                </ul>
              </>
            )}
          </div>
        )}

        {tab === "users" && (
          <div className="integrations-body">
            <ul className="integrations-list">
              {filteredUsers.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>등록된 사용자가 없습니다.</li>
              )}
              {filteredUsers.map((u) => {
                const inactive = (u.delete_at ?? 0) > 0;
                return (
                  <li
                    key={u.id}
                    className="integrations-row"
                    style={inactive ? { opacity: 0.55 } : undefined}
                  >
                    <div style={{ flex: 1 }}>
                      <div style={{ fontWeight: 600 }}>
                        @{u.username}
                        {inactive && (
                          <span style={{ marginLeft: 8, color: "var(--danger)", fontSize: 13 }}>
                            비활성
                          </span>
                        )}
                      </div>
                      <div style={{ color: "var(--muted)", fontSize: 13 }}>
                        {u.email} · {u.roles || "system_user"}
                      </div>
                    </div>
                    {inactive ? (
                      <button
                        type="button"
                        className="btn-ghost"
                        style={{ width: "auto", padding: "0 10px", height: 30 }}
                        onClick={() => onReactivateUser(u.id)}
                      >활성화</button>
                    ) : (
                      <button
                        type="button"
                        className="btn-ghost"
                        style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                        onClick={() => onDeactivateUser(u.id, u.username)}
                      >비활성화</button>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        )}

        {tab === "auth" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >인증 새로고침</button>
              <span className="admin-pill ok">Mattermost API</span>
            </div>
            <div className="admin-summary-grid">
              {authRows.map((row) => (
                <div key={row.key} className="admin-kv">
                  <span>{row.label}</span>
                  <strong>
                    {row.count !== undefined ? `${row.count} · ` : ""}
                    {row.status}
                  </strong>
                </div>
              ))}
            </div>
            <ul className="integrations-list">
              {authRows.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>인증 상태를 불러오지 않았습니다.</li>
              )}
              {authRows.map((row) => (
                <li key={row.key} className="integrations-row">
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                      {row.label}
                      <span className={`admin-pill ${row.tone ?? ""}`.trim()}>{row.status}</span>
                    </div>
                    <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                      {row.detail}
                      {row.count !== undefined && ` · ${row.count} rows`}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "system" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >새로고침</button>
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={onReloadConfig}
                disabled
                title="레거시 전체 설정 리로드는 v0.2.10에서 지원하지 않습니다. moyro 관리 설정과 플러그인 설정은 저장 즉시 적용됩니다."
              >레거시 리로드 미지원</button>
              <button
                type="button"
                className="btn-ghost"
                style={{
                  width: "auto",
                  padding: "0 12px",
                  height: 34,
                  color: serverBusy ? "var(--danger)" : "var(--fg)",
                }}
                onClick={() => onSetBusy(!serverBusy)}
              >{serverBusy ? "Busy 해제" : "Busy 설정"}</button>
              <span className={serverBusy ? "admin-pill danger" : "admin-pill ok"}>
                {serverBusy ? "busy" : "ready"}
              </span>
            </div>
            <div className="admin-summary-grid">
              <div className="admin-kv">
                <span>SiteURL</span>
                <strong>{String(adminConfig?.ServiceSettings?.SiteURL ?? "—")}</strong>
              </div>
              <div className="admin-kv">
                <span>Listen</span>
                <strong>{String(adminConfig?.ServiceSettings?.ListenAddress ?? "—")}</strong>
              </div>
              <div className="admin-kv">
                <span>Plugin Dir</span>
                <strong>{String(adminConfig?.PluginSettings?.Directory ?? "—")}</strong>
              </div>
              <div className="admin-kv">
                <span>File Driver</span>
                <strong>{String(adminConfig?.FileSettings?.DriverName ?? "—")}</strong>
              </div>
            </div>
            <ul className="integrations-list">
              {clusterNodes.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>클러스터 노드 정보가 없습니다.</li>
              )}
              {clusterNodes.map((node) => (
                <li key={node.id} className="integrations-row">
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                      {node.hostname || node.id}
                      <span className={node.status === "OK" ? "admin-pill ok" : "admin-pill danger"}>
                        {node.status}
                      </span>
                    </div>
                    <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                      {node.version || node.server_version || "moyro"} · 마지막 ping {node.last_ping_at ? new Date(node.last_ping_at).toLocaleTimeString() : "—"}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
            <ul className="integrations-list admin-log-list">
              {logRows.map((row, idx) => (
                <li key={`${idx}-${row}`} className="integrations-row">
                  <code>{row}</code>
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "plugins" && token && (
          <PluginAdminPanel
            token={token}
            plugins={filteredPlugins}
            statuses={pluginStatuses}
            runtimeManagementEnabled={pluginRuntimeManagementEnabled}
            onRefresh={refresh}
            onError={setError}
          />
        )}

        {tab === "roles" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >역할 새로고침</button>
            </div>
            <ul className="integrations-list">
              {roles.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>역할 정보가 없습니다.</li>
              )}
              {roles.map((role) => (
                <li key={role.id} className="integrations-row" style={{ alignItems: "flex-start" }}>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                      {role.display_name || role.name}
                      {role.built_in && <span className="admin-pill">built-in</span>}
                    </div>
                    <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                      {role.name} · 권한 {role.permissions.length}개
                    </div>
                    <div className="admin-permission-list">
                      {role.permissions.slice(0, 10).map((permission) => (
                        <span key={permission}>{permission}</span>
                      ))}
                      {role.permissions.length > 10 && <span>+{role.permissions.length - 10}</span>}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "jobs" && (
          <div className="integrations-body">
            <div className="integrations-create">
              <input
                className="field-input"
                value={newJobType}
                onChange={(e) => setNewJobType(e.target.value)}
                placeholder="작업 타입"
                style={{ flex: "1 1 180px" }}
              />
              <button
                type="button"
                className="btn-primary"
                style={{ width: "auto", padding: "0 14px", height: 38 }}
                onClick={onCreateJob}
              >작업 생성</button>
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 38 }}
                onClick={refresh}
              >새로고침</button>
            </div>
            <ul className="integrations-list">
              {jobs.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>실행 중인 작업이 없습니다.</li>
              )}
              {jobs.map((job) => (
                <li key={job.id} className="integrations-row">
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                      {job.type}
                      <span className={job.status === "canceled" ? "admin-pill danger" : "admin-pill"}>
                        {job.status}
                      </span>
                    </div>
                    <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                      {job.id.slice(0, 8)} · {new Date(job.create_at).toLocaleString()}
                    </div>
                  </div>
                  {job.status !== "canceled" && job.status !== "success" && (
                    <button
                      type="button"
                      className="btn-ghost"
                      style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                      onClick={() => onCancelJob(job.id)}
                    >취소</button>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )}

        {tab === "policies" && (
          <div className="integrations-body">
            <div className="integrations-create admin-toolbar">
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 34 }}
                onClick={refresh}
              >정책 새로고침</button>
              <span className="admin-pill ok">Mattermost API</span>
            </div>
            <div className="admin-summary-grid">
              {policyRows.map((row) => (
                <div key={row.key} className="admin-kv">
                  <span>{row.label}</span>
                  <strong>
                    {row.count !== undefined ? `${row.count} · ` : ""}
                    {row.status}
                  </strong>
                </div>
              ))}
            </div>
            <ul className="integrations-list">
              {policyRows.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>정책 상태를 불러오지 않았습니다.</li>
              )}
              {policyRows.map((row) => (
                <li key={row.key} className="integrations-row">
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                      {row.label}
                      <span className={`admin-pill ${row.tone ?? ""}`.trim()}>{row.status}</span>
                    </div>
                    <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                      {row.detail}
                      {row.count !== undefined && ` · ${row.count} rows`}
                    </div>
                  </div>
                </li>
              ))}
              <li className="integrations-row">
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                    Global Retention
                    <span className="admin-pill">
                      {globalRetentionPolicy?.message_deletion_enabled === true ||
                      globalRetentionPolicy?.file_deletion_enabled === true
                        ? "enabled"
                        : "inactive"}
                    </span>
                  </div>
                  <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                    message {String(globalRetentionPolicy?.message_retention_cutoff ?? 0)}
                    {" · "}
                    file {String(globalRetentionPolicy?.file_retention_cutoff ?? 0)}
                  </div>
                </div>
              </li>
            </ul>
          </div>
        )}

        {tab === "audit" && (
          <div className="integrations-body">
            <div className="integrations-create">
              <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13 }}>
                분류
                <select
                  className="field-input"
                  style={{ width: 140 }}
                  value={auditPrefix}
                  onChange={(e) => setAuditPrefix(e.target.value)}
                >
                  {AUDIT_PREFIXES.map((p) => (
                    <option key={p.value || "all"} value={p.value}>{p.label}</option>
                  ))}
                </select>
              </label>
              <input
                className="field-input"
                placeholder="행위자 username (옵션)"
                value={auditActor}
                onChange={(e) => setAuditActor(e.target.value)}
                style={{ flex: "1 1 180px" }}
              />
              <button
                type="button"
                className="btn-ghost"
                style={{ width: "auto", padding: "0 12px", height: 38 }}
                onClick={refresh}
              >새로고침</button>
            </div>
            <ul className="integrations-list">
              {auditRows.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>조건에 맞는 감사 로그가 없습니다.</li>
              )}
              {auditRows.map((row) => {
                // Payload can be anything the action logger wrote — we stringify
                // so the admin can eyeball it without unfolding a JSON tree.
                // Empty payload shows as "—".
                let payload = "";
                try {
                  payload =
                    row.payload == null || (typeof row.payload === "object" && Object.keys(row.payload as object).length === 0)
                      ? "—"
                      : JSON.stringify(row.payload);
                } catch {
                  payload = String(row.payload);
                }
                return (
                  <li key={row.id} className="integrations-row" style={{ alignItems: "flex-start" }}>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ fontWeight: 600, fontSize: 13 }}>
                        {row.action}
                      </div>
                      <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                        {new Date(row.create_at).toLocaleString()}
                        {row.actor_id && ` · 행위자 ${row.actor_id.slice(0, 8)}`}
                        {row.target && ` · 대상 ${row.target}`}
                      </div>
                      {payload !== "—" && (
                        <pre
                          style={{
                            margin: "4px 0 0",
                            padding: "4px 6px",
                            background: "rgba(255,255,255,0.04)",
                            borderRadius: 4,
                            fontSize: 13,
                            whiteSpace: "pre-wrap",
                            wordBreak: "break-all",
                          }}
                        >{payload}</pre>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          </div>
        )}
          </section>
        </div>
      </div>
      {confirmer.render()}
    </main>
  );
}
