// Admin-only drawer for managing bots, personal access tokens, and
// incoming/outgoing webhooks. Rendered as a modal-style overlay so it
// doesn't compete with the main chat layout for grid tracks.
//
// We intentionally keep the UI dense and unstyled beyond the shared
// primitives: operators use this rarely, discoverability matters less
// than fitting everything on one screen.
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import type { RootState } from "@/store";
import {
  api,
  integrationsApi,
  type AuditEntry,
  type Bot,
  type Channel,
  type CreatedPAT,
  type Emoji,
  type IncomingWebhook,
  type Invite,
  type OutgoingWebhook,
  type PAT,
  type User,
} from "@/api/client";
import { invalidateEmojiCache } from "@/components/EmojiPicker";
import { useEscClose, useConfirm } from "@/components/shared";

type Tab =
  | "bots"
  | "incoming"
  | "outgoing"
  | "emoji"
  | "invites"
  | "users"
  | "audit";

const TAB_LABELS: Record<Tab, string> = {
  bots: "봇",
  incoming: "인커밍 웹훅",
  outgoing: "아웃고잉 웹훅",
  emoji: "이모지",
  invites: "초대 링크",
  users: "사용자",
  audit: "감사 로그",
};

const ALL_TABS: Tab[] = [
  "bots",
  "incoming",
  "outgoing",
  "emoji",
  "invites",
  "users",
  "audit",
];

// Human-readable labels for the TTL dropdown when issuing invites. Values
// are seconds; server converts to unix-millis `expires_at`.
const INVITE_TTL_CHOICES: { label: string; seconds: number }[] = [
  { label: "1일", seconds: 24 * 60 * 60 },
  { label: "7일", seconds: 7 * 24 * 60 * 60 },
  { label: "30일", seconds: 30 * 24 * 60 * 60 },
];

// Common audit action prefix filters. Empty string = no filter.
const AUDIT_PREFIXES: { label: string; value: string }[] = [
  { label: "전체", value: "" },
  { label: "사용자", value: "user." },
  { label: "초대", value: "invite." },
  { label: "세션", value: "session." },
  { label: "채널", value: "channel." },
  { label: "웹훅", value: "webhook." },
  { label: "봇", value: "bot." },
];

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
  const [tab, setTab] = useState<Tab>("bots");
  const [error, setError] = useState<string | null>(null);

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

  // Phase 16 — audit browse. Filters are driven client-side into the
  // server's `?action_prefix=` + `?actor=` params; changing either kicks
  // off a refresh via the effect on `refresh`.
  const [auditRows, setAuditRows] = useState<AuditEntry[]>([]);
  const [auditPrefix, setAuditPrefix] = useState<string>("");
  const [auditActor, setAuditActor] = useState<string>("");

  const nonDMChannels = useMemo(() => channels.filter((c) => c.type !== "D"), [channels]);

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      if (tab === "bots") {
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
  }, [token, tab, currentTeamId, auditPrefix, auditActor]);

  useEffect(() => { refresh(); }, [refresh]);

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

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card integrations-panel" onClick={(e) => e.stopPropagation()}>
        <header className="integrations-header">
          <h3 style={{ margin: 0 }}>통합 관리</h3>
          <button type="button" className="action-btn" onClick={onClose} title="닫기">✕</button>
        </header>
        <div className="integrations-tabs">
          {ALL_TABS.map((t) => (
            <button
              key={t}
              className="login-tab"
              aria-selected={tab === t}
              onClick={() => setTab(t)}
            >{TAB_LABELS[t]}</button>
          ))}
        </div>

        {error && <div className="login-error" style={{ margin: "12px 0" }}>{error}</div>}

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
                    <div style={{ color: "var(--muted)", fontSize: 12 }}>{b.description || "—"}</div>
                    {botTokens[b.user_id] && (
                      <div style={{ marginTop: 6 }}>
                        {botTokens[b.user_id].length === 0
                          ? <span style={{ color: "var(--muted)", fontSize: 12 }}>발급된 토큰 없음</span>
                          : (
                            <ul className="pat-list">
                              {botTokens[b.user_id].map((t) => (
                                <li key={t.id}>
                                  <span>{t.description || "(설명없음)"}</span>
                                  <span style={{ color: "var(--muted)", fontSize: 11, marginLeft: 8 }}>
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
                    <div style={{ color: "var(--muted)", fontSize: 12 }}>채널 {hk.channel_id} · 잠금 {hk.channel_locked ? "ON" : "OFF"}</div>
                    <code className="reveal-code" style={{ marginTop: 4, padding: "2px 6px", fontSize: 11 }}>{`${window.location.origin}/hooks/${hk.id}`}</code>
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
                    <div style={{ color: "var(--muted)", fontSize: 12 }}>
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
                  <img src={api.emojiImageURL(token ?? "", e.id)} alt={e.name} />
                  <div className="emoji-tile-name" title={`:${e.name}:`}>:{e.name}:</div>
                  <button
                    type="button"
                    className="btn-ghost"
                    style={{ width: "auto", padding: "0 8px", height: 26, color: "var(--danger)", fontSize: 11 }}
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
                          <div style={{ fontWeight: 600, fontSize: 12, wordBreak: "break-all" }}>{inv.url}</div>
                          <div style={{ color: "var(--muted)", fontSize: 12, marginTop: 2 }}>
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
              {users.length === 0 && (
                <li className="chat-empty" style={{ padding: 12 }}>등록된 사용자가 없습니다.</li>
              )}
              {users.map((u) => {
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
                          <span style={{ marginLeft: 8, color: "var(--danger)", fontSize: 11 }}>
                            비활성
                          </span>
                        )}
                      </div>
                      <div style={{ color: "var(--muted)", fontSize: 12 }}>
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
                      <div style={{ color: "var(--muted)", fontSize: 11, marginTop: 2 }}>
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
                            fontSize: 11,
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
      </div>
      {confirmer.render()}
    </div>
  );
}
