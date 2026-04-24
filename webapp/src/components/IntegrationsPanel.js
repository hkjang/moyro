import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
// Admin-only drawer for managing bots, personal access tokens, and
// incoming/outgoing webhooks. Rendered as a modal-style overlay so it
// doesn't compete with the main chat layout for grid tracks.
//
// We intentionally keep the UI dense and unstyled beyond the shared
// primitives: operators use this rarely, discoverability matters less
// than fitting everything on one screen.
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { api, integrationsApi, } from "@/api/client";
import { invalidateEmojiCache } from "@/components/EmojiPicker";
import { useEscClose, useConfirm } from "@/components/shared";
const TAB_LABELS = {
    bots: "봇",
    incoming: "인커밍 웹훅",
    outgoing: "아웃고잉 웹훅",
    emoji: "이모지",
    invites: "초대 링크",
    users: "사용자",
    audit: "감사 로그",
};
const ALL_TABS = [
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
const INVITE_TTL_CHOICES = [
    { label: "1일", seconds: 24 * 60 * 60 },
    { label: "7일", seconds: 7 * 24 * 60 * 60 },
    { label: "30일", seconds: 30 * 24 * 60 * 60 },
];
// Common audit action prefix filters. Empty string = no filter.
const AUDIT_PREFIXES = [
    { label: "전체", value: "" },
    { label: "사용자", value: "user." },
    { label: "초대", value: "invite." },
    { label: "세션", value: "session." },
    { label: "채널", value: "channel." },
    { label: "웹훅", value: "webhook." },
    { label: "봇", value: "bot." },
];
export function IntegrationsPanel({ channels, currentTeamId, onClose, }) {
    useEscClose(true, onClose);
    const confirmer = useConfirm();
    const token = useSelector((s) => s.auth.token);
    const [tab, setTab] = useState("bots");
    const [error, setError] = useState(null);
    // Bots
    const [bots, setBots] = useState([]);
    const [newBotName, setNewBotName] = useState("");
    const [newBotDisplay, setNewBotDisplay] = useState("");
    const [newBotDesc, setNewBotDesc] = useState("");
    // Tokens keyed by bot user_id. Only the freshly created one holds the
    // plaintext `.token`; list refreshes produce the redacted shape.
    const [botTokens, setBotTokens] = useState({});
    const [freshPAT, setFreshPAT] = useState(null);
    // Incoming
    const [incoming, setIncoming] = useState([]);
    const [newIn, setNewIn] = useState({
        channel_id: "",
        display_name: "",
        username: "",
        icon_url: "",
        channel_locked: true,
    });
    const [freshIncomingURL, setFreshIncomingURL] = useState(null);
    // Outgoing
    const [outgoing, setOutgoing] = useState([]);
    const [newOut, setNewOut] = useState({
        channel_id: "",
        trigger_words: "",
        callback_urls: "",
        display_name: "",
        trigger_when: 0,
    });
    // Emoji
    const [emojis, setEmojis] = useState([]);
    const [newEmojiName, setNewEmojiName] = useState("");
    const [newEmojiFile, setNewEmojiFile] = useState(null);
    // Phase 16 — invites. `maxUsesText` is a string so the input can carry
    // "무제한" via 0; the spinner's default is 1 for principle-of-least-trust.
    const [invites, setInvites] = useState([]);
    const [inviteMaxUses, setInviteMaxUses] = useState(1);
    const [inviteTTLSeconds, setInviteTTLSeconds] = useState(INVITE_TTL_CHOICES[1].seconds);
    // Phase 16 — users directory (admin). We fetch the first page; the
    // existing `listUsers` endpoint already paginates and returns `User[]`.
    const [users, setUsers] = useState([]);
    // Phase 16 — audit browse. Filters are driven client-side into the
    // server's `?action_prefix=` + `?actor=` params; changing either kicks
    // off a refresh via the effect on `refresh`.
    const [auditRows, setAuditRows] = useState([]);
    const [auditPrefix, setAuditPrefix] = useState("");
    const [auditActor, setAuditActor] = useState("");
    const nonDMChannels = useMemo(() => channels.filter((c) => c.type !== "D"), [channels]);
    const refresh = useCallback(async () => {
        if (!token)
            return;
        try {
            if (tab === "bots") {
                setBots(await integrationsApi.listBots(token));
            }
            else if (tab === "incoming") {
                setIncoming(await integrationsApi.listIncoming(token));
            }
            else if (tab === "outgoing") {
                setOutgoing(await integrationsApi.listOutgoing(token));
            }
            else if (tab === "emoji") {
                setEmojis(await api.listEmojis(token));
            }
            else if (tab === "invites") {
                if (currentTeamId) {
                    setInvites(await integrationsApi.listInvites(token, currentTeamId));
                }
                else {
                    setInvites([]);
                }
            }
            else if (tab === "users") {
                // Admin-only include_deleted so we can render reactivate buttons
                // for deactivated rows. Non-admins would be better off not seeing
                // this tab at all; the backend would silently drop the flag.
                setUsers(await api.listUsers(token, 0, 200, true));
            }
            else if (tab === "audit") {
                setAuditRows(await integrationsApi.listAuditLogs(token, {
                    limit: 100,
                    actionPrefix: auditPrefix || undefined,
                    actor: auditActor.trim() || undefined,
                }));
            }
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "로드 실패");
        }
    }, [token, tab, currentTeamId, auditPrefix, auditActor]);
    useEffect(() => { refresh(); }, [refresh]);
    // ---- Bot actions ----
    async function onCreateBot() {
        if (!token || !newBotName.trim())
            return;
        try {
            const b = await integrationsApi.createBot(token, newBotName.trim(), newBotDisplay.trim(), newBotDesc.trim());
            setBots((prev) => [...prev, b]);
            setNewBotName("");
            setNewBotDisplay("");
            setNewBotDesc("");
            // Auto-mint a PAT so the operator has something usable right away.
            // Without this they have to separately click "Create token".
            const pat = await integrationsApi.createToken(token, b.user_id, "initial");
            setFreshPAT(pat);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "봇 생성 실패");
        }
    }
    async function onDisableBot(botId) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "봇 비활성화",
            message: "봇을 비활성화할까요? 모든 토큰이 무효화됩니다.",
            confirmLabel: "비활성화",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await integrationsApi.disableBot(token, botId);
            setBots((prev) => prev.filter((b) => b.user_id !== botId));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "봇 비활성화 실패");
        }
    }
    async function onLoadTokens(botId) {
        if (!token)
            return;
        try {
            const list = await integrationsApi.listTokens(token, botId);
            setBotTokens((prev) => ({ ...prev, [botId]: list }));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "토큰 조회 실패");
        }
    }
    async function onCreatePAT(botId) {
        if (!token)
            return;
        const description = prompt("토큰 설명(옵션)") ?? "";
        try {
            const pat = await integrationsApi.createToken(token, botId, description);
            setFreshPAT(pat);
            onLoadTokens(botId);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "토큰 생성 실패");
        }
    }
    async function onRevokePAT(tokenId, botId) {
        if (!token)
            return;
        try {
            await integrationsApi.revokeToken(token, tokenId);
            onLoadTokens(botId);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "토큰 취소 실패");
        }
    }
    // ---- Incoming actions ----
    async function onCreateIncoming() {
        if (!token || !newIn.channel_id)
            return;
        try {
            const hk = await integrationsApi.createIncoming(token, newIn.channel_id, newIn.display_name, newIn.username, newIn.icon_url, newIn.channel_locked);
            setIncoming((prev) => [hk, ...prev]);
            // Build the user-facing URL relative to the current origin — the
            // server mounts /hooks/{id} outside /api/v4 so we construct directly.
            const url = `${window.location.origin}/hooks/${hk.id}`;
            setFreshIncomingURL(url);
            setNewIn({ channel_id: "", display_name: "", username: "", icon_url: "", channel_locked: true });
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "웹훅 생성 실패");
        }
    }
    async function onDeleteIncoming(id) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "인커밍 웹훅 삭제",
            message: "이 웹훅을 삭제할까요? URL이 즉시 무효화됩니다.",
            confirmLabel: "삭제",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await integrationsApi.deleteIncoming(token, id);
            setIncoming((prev) => prev.filter((h) => h.id !== id));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "웹훅 삭제 실패");
        }
    }
    // ---- Outgoing actions ----
    async function onCreateOutgoing() {
        if (!token || !currentTeamId)
            return;
        const words = newOut.trigger_words.split(",").map((s) => s.trim()).filter(Boolean);
        const urls = newOut.callback_urls.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
        if (urls.length === 0) {
            setError("콜백 URL이 필요합니다");
            return;
        }
        try {
            const hk = await integrationsApi.createOutgoing(token, currentTeamId, newOut.channel_id, words, urls, newOut.display_name, newOut.trigger_when);
            setOutgoing((prev) => [hk, ...prev]);
            setNewOut({ channel_id: "", trigger_words: "", callback_urls: "", display_name: "", trigger_when: 0 });
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "아웃고잉 웹훅 생성 실패");
        }
    }
    async function onDeleteOutgoing(id) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "아웃고잉 웹훅 삭제",
            message: "이 아웃고잉 웹훅을 삭제할까요?",
            confirmLabel: "삭제",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await integrationsApi.deleteOutgoing(token, id);
            setOutgoing((prev) => prev.filter((h) => h.id !== id));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "삭제 실패");
        }
    }
    // ---- Emoji actions ----
    async function onCreateEmoji() {
        if (!token || !newEmojiFile)
            return;
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
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "이모지 업로드 실패");
        }
    }
    async function onDeleteEmoji(id) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "이모지 삭제",
            message: "이 이모지를 삭제할까요? 기존 메시지의 반응 표시가 깨질 수 있습니다.",
            confirmLabel: "삭제",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await api.deleteEmoji(token, id);
            setEmojis((prev) => prev.filter((e) => e.id !== id));
            invalidateEmojiCache();
        }
        catch (e) {
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
            const inv = await integrationsApi.createInvite(token, currentTeamId, inviteMaxUses, inviteTTLSeconds);
            setInvites((prev) => [inv, ...prev]);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "초대 링크 생성 실패");
        }
    }
    async function onCopyInvite(url) {
        // Relative URLs need to be absolutised for the clipboard payload so
        // the recipient can open the link outside the current tab's context.
        const abs = url.startsWith("http") ? url : `${window.location.origin}${url}`;
        try {
            await navigator.clipboard.writeText(abs);
        }
        catch {
            // Fallback: temporary textarea. navigator.clipboard requires HTTPS
            // or localhost, and this panel is often used on LAN/dev setups.
            const ta = document.createElement("textarea");
            ta.value = abs;
            document.body.appendChild(ta);
            ta.select();
            try {
                document.execCommand("copy");
            }
            catch { /* ignore */ }
            document.body.removeChild(ta);
        }
    }
    async function onRevokeInvite(id) {
        if (!token || !currentTeamId)
            return;
        const ok = await confirmer.confirm({
            title: "초대 링크 무효화",
            message: "이 초대 링크를 즉시 무효화할까요? 아직 가입하지 않은 수신자는 더 이상 사용할 수 없습니다.",
            confirmLabel: "무효화",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await integrationsApi.revokeInvite(token, currentTeamId, id);
            setInvites((prev) => prev.filter((i) => i.id !== id));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "초대 링크 무효화 실패");
        }
    }
    // ---- User actions ----
    async function onDeactivateUser(userId, username) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "사용자 비활성화",
            message: `${username} 님을 비활성화할까요? 모든 세션이 종료되고 로그인할 수 없게 됩니다.`,
            confirmLabel: "비활성화",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await integrationsApi.deactivateUser(token, userId);
            // Mirror the server state locally without a refetch round-trip.
            setUsers((prev) => prev.map((u) => u.id === userId ? { ...u, update_at: Date.now() } : u));
            // Simplest correct approach: refresh once so delete_at reflects.
            refresh();
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "사용자 비활성화 실패");
        }
    }
    async function onReactivateUser(userId) {
        if (!token)
            return;
        try {
            await integrationsApi.reactivateUser(token, userId);
            refresh();
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "사용자 활성화 실패");
        }
    }
    return (_jsxs("div", { className: "modal-backdrop", onClick: onClose, children: [_jsxs("div", { className: "modal-card integrations-panel", onClick: (e) => e.stopPropagation(), children: [_jsxs("header", { className: "integrations-header", children: [_jsx("h3", { style: { margin: 0 }, children: "\uD1B5\uD569 \uAD00\uB9AC" }), _jsx("button", { type: "button", className: "action-btn", onClick: onClose, title: "\uB2EB\uAE30", children: "\u2715" })] }), _jsx("div", { className: "integrations-tabs", children: ALL_TABS.map((t) => (_jsx("button", { className: "login-tab", "aria-selected": tab === t, onClick: () => setTab(t), children: TAB_LABELS[t] }, t))) }), error && _jsx("div", { className: "login-error", style: { margin: "12px 0" }, children: error }), freshPAT && (_jsxs("div", { className: "reveal-card", children: [_jsx("div", { style: { fontWeight: 600 }, children: "\uD1A0\uD070\uC774 \uC0DD\uC131\uB418\uC5C8\uC2B5\uB2C8\uB2E4. \uC9C0\uAE08 \uBCF5\uC0AC\uD574 \uB450\uC138\uC694. \uC774\uD6C4\uC5D0\uB294 \uB2E4\uC2DC \uBCFC \uC218 \uC5C6\uC2B5\uB2C8\uB2E4." }), _jsx("code", { className: "reveal-code", children: freshPAT.token }), _jsx("button", { type: "button", className: "btn-ghost", onClick: () => setFreshPAT(null), children: "\uD655\uC778" })] })), freshIncomingURL && (_jsxs("div", { className: "reveal-card", children: [_jsx("div", { style: { fontWeight: 600 }, children: "\uC778\uCEE4\uBC0D \uC6F9\uD6C5 URL\uC774 \uC0DD\uC131\uB418\uC5C8\uC2B5\uB2C8\uB2E4. \uC774 URL\uC744 \uACF5\uC720\uD558\uBA74 \uB204\uAD6C\uB098 \uBA54\uC2DC\uC9C0\uB97C \uBCF4\uB0BC \uC218 \uC788\uC2B5\uB2C8\uB2E4." }), _jsx("code", { className: "reveal-code", children: freshIncomingURL }), _jsx("button", { type: "button", className: "btn-ghost", onClick: () => setFreshIncomingURL(null), children: "\uD655\uC778" })] })), tab === "bots" && (_jsxs("div", { className: "integrations-body", children: [_jsxs("div", { className: "integrations-create", children: [_jsx("input", { className: "field-input", placeholder: "username (\uC601\uC18C\uBB38\uC790/\uC22B\uC790)", value: newBotName, onChange: (e) => setNewBotName(e.target.value) }), _jsx("input", { className: "field-input", placeholder: "\uD45C\uC2DC \uC774\uB984", value: newBotDisplay, onChange: (e) => setNewBotDisplay(e.target.value) }), _jsx("input", { className: "field-input", placeholder: "\uC124\uBA85 (\uC635\uC158)", value: newBotDesc, onChange: (e) => setNewBotDesc(e.target.value) }), _jsx("button", { className: "btn-primary", onClick: onCreateBot, style: { width: "auto", padding: "0 14px", height: 38 }, children: "\uBD07 \uB9CC\uB4E4\uAE30" })] }), _jsxs("ul", { className: "integrations-list", children: [bots.length === 0 && _jsx("li", { className: "chat-empty", style: { padding: 12 }, children: "\uB4F1\uB85D\uB41C \uBD07\uC774 \uC5C6\uC2B5\uB2C8\uB2E4." }), bots.map((b) => (_jsxs("li", { className: "integrations-row", children: [_jsxs("div", { style: { flex: 1 }, children: [_jsxs("div", { style: { fontWeight: 600 }, children: ["@", b.username] }), _jsx("div", { style: { color: "var(--muted)", fontSize: 12 }, children: b.description || "—" }), botTokens[b.user_id] && (_jsx("div", { style: { marginTop: 6 }, children: botTokens[b.user_id].length === 0
                                                            ? _jsx("span", { style: { color: "var(--muted)", fontSize: 12 }, children: "\uBC1C\uAE09\uB41C \uD1A0\uD070 \uC5C6\uC74C" })
                                                            : (_jsx("ul", { className: "pat-list", children: botTokens[b.user_id].map((t) => (_jsxs("li", { children: [_jsx("span", { children: t.description || "(설명없음)" }), _jsx("span", { style: { color: "var(--muted)", fontSize: 11, marginLeft: 8 }, children: t.revoked_at ? "취소됨" : "활성" }), !t.revoked_at && (_jsx("button", { type: "button", className: "action-btn", onClick: () => onRevokePAT(t.id, b.user_id), children: "\uD83D\uDDD1" }))] }, t.id))) })) }))] }), _jsxs("div", { style: { display: "flex", gap: 6, flexWrap: "wrap", justifyContent: "flex-end" }, children: [_jsx("button", { className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30 }, onClick: () => onLoadTokens(b.user_id), children: "\uD1A0\uD070 \uC870\uD68C" }), _jsx("button", { className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30 }, onClick: () => onCreatePAT(b.user_id), children: "\uC0C8 \uD1A0\uD070" }), _jsx("button", { className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }, onClick: () => onDisableBot(b.user_id), children: "\uBE44\uD65C\uC131\uD654" })] })] }, b.user_id)))] })] })), tab === "incoming" && (_jsxs("div", { className: "integrations-body", children: [_jsxs("div", { className: "integrations-create", children: [_jsxs("select", { className: "field-input", value: newIn.channel_id, onChange: (e) => setNewIn((prev) => ({ ...prev, channel_id: e.target.value })), children: [_jsx("option", { value: "", children: "\uCC44\uB110 \uC120\uD0DD\u2026" }), nonDMChannels.map((c) => (_jsxs("option", { value: c.id, children: ["#", c.display_name] }, c.id)))] }), _jsx("input", { className: "field-input", placeholder: "\uD45C\uC2DC \uC774\uB984 (\uBD07 \uC774\uB984)", value: newIn.display_name, onChange: (e) => setNewIn((p) => ({ ...p, display_name: e.target.value })) }), _jsx("input", { className: "field-input", placeholder: "\uC624\uBC84\uB77C\uC774\uB4DC username (\uC635\uC158)", value: newIn.username, onChange: (e) => setNewIn((p) => ({ ...p, username: e.target.value })) }), _jsx("input", { className: "field-input", placeholder: "\uC544\uC774\uCF58 URL (\uC635\uC158)", value: newIn.icon_url, onChange: (e) => setNewIn((p) => ({ ...p, icon_url: e.target.value })) }), _jsxs("label", { style: { display: "flex", alignItems: "center", gap: 6 }, children: [_jsx("input", { type: "checkbox", checked: newIn.channel_locked, onChange: (e) => setNewIn((p) => ({ ...p, channel_locked: e.target.checked })) }), _jsx("span", { style: { fontSize: 13 }, children: "\uCC44\uB110 \uACE0\uC815" })] }), _jsx("button", { className: "btn-primary", onClick: onCreateIncoming, style: { width: "auto", padding: "0 14px", height: 38 }, children: "\uC0DD\uC131" })] }), _jsxs("ul", { className: "integrations-list", children: [incoming.length === 0 && _jsx("li", { className: "chat-empty", style: { padding: 12 }, children: "\uC778\uCEE4\uBC0D \uC6F9\uD6C5 \uC5C6\uC74C." }), incoming.map((hk) => (_jsxs("li", { className: "integrations-row", children: [_jsxs("div", { style: { flex: 1 }, children: [_jsx("div", { style: { fontWeight: 600 }, children: hk.display_name || "(이름없음)" }), _jsxs("div", { style: { color: "var(--muted)", fontSize: 12 }, children: ["\uCC44\uB110 ", hk.channel_id, " \u00B7 \uC7A0\uAE08 ", hk.channel_locked ? "ON" : "OFF"] }), _jsx("code", { className: "reveal-code", style: { marginTop: 4, padding: "2px 6px", fontSize: 11 }, children: `${window.location.origin}/hooks/${hk.id}` })] }), _jsx("button", { className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }, onClick: () => onDeleteIncoming(hk.id), children: "\uC0AD\uC81C" })] }, hk.id)))] })] })), tab === "outgoing" && (_jsxs("div", { className: "integrations-body", children: [_jsxs("div", { className: "integrations-create", children: [_jsxs("select", { className: "field-input", value: newOut.channel_id, onChange: (e) => setNewOut((p) => ({ ...p, channel_id: e.target.value })), children: [_jsx("option", { value: "", children: "\uCC44\uB110 (\uBE44\uC6B0\uBA74 \uD300 \uC804\uCCB4)" }), nonDMChannels.map((c) => (_jsxs("option", { value: c.id, children: ["#", c.display_name] }, c.id)))] }), _jsx("input", { className: "field-input", placeholder: "\uD2B8\uB9AC\uAC70 \uB2E8\uC5B4 (\uC27C\uD45C\uB85C \uAD6C\uBD84)", value: newOut.trigger_words, onChange: (e) => setNewOut((p) => ({ ...p, trigger_words: e.target.value })) }), _jsx("input", { className: "field-input", placeholder: "\uCF5C\uBC31 URL (\uACF5\uBC31/\uC27C\uD45C\uB85C \uAD6C\uBD84)", value: newOut.callback_urls, onChange: (e) => setNewOut((p) => ({ ...p, callback_urls: e.target.value })) }), _jsxs("select", { className: "field-input", value: newOut.trigger_when, onChange: (e) => setNewOut((p) => ({ ...p, trigger_when: Number(e.target.value) })), children: [_jsx("option", { value: 0, children: "\uCCAB \uB2E8\uC5B4 \uC77C\uCE58" }), _jsx("option", { value: 1, children: "\uC5B4\uB514\uB4E0 \uD3EC\uD568" })] }), _jsx("input", { className: "field-input", placeholder: "\uD45C\uC2DC \uC774\uB984 (\uC635\uC158)", value: newOut.display_name, onChange: (e) => setNewOut((p) => ({ ...p, display_name: e.target.value })) }), _jsx("button", { className: "btn-primary", onClick: onCreateOutgoing, style: { width: "auto", padding: "0 14px", height: 38 }, children: "\uC0DD\uC131" })] }), _jsxs("ul", { className: "integrations-list", children: [outgoing.length === 0 && _jsx("li", { className: "chat-empty", style: { padding: 12 }, children: "\uC544\uC6C3\uACE0\uC789 \uC6F9\uD6C5 \uC5C6\uC74C." }), outgoing.map((hk) => (_jsxs("li", { className: "integrations-row", children: [_jsxs("div", { style: { flex: 1 }, children: [_jsx("div", { style: { fontWeight: 600 }, children: hk.display_name || "(이름없음)" }), _jsxs("div", { style: { color: "var(--muted)", fontSize: 12 }, children: ["\uD2B8\uB9AC\uAC70: ", hk.trigger_words.join(", ") || "(없음)", " \u00B7 \uCF5C\uBC31 ", hk.callback_urls.length, "\uAC1C"] })] }), _jsx("button", { className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }, onClick: () => onDeleteOutgoing(hk.id), children: "\uC0AD\uC81C" })] }, hk.id)))] })] })), tab === "emoji" && (_jsxs("div", { className: "integrations-body", children: [_jsxs("div", { className: "integrations-create", children: [_jsx("input", { className: "field-input", placeholder: "\uC774\uB984 (\uC601\uC18C\uBB38\uC790/\uC22B\uC790/_/-)", value: newEmojiName, onChange: (e) => setNewEmojiName(e.target.value.toLowerCase()), style: { flex: "1 1 180px" } }), _jsx("input", { type: "file", accept: "image/png,image/jpeg,image/gif,image/webp", onChange: (e) => setNewEmojiFile(e.target.files?.[0] ?? null) }), _jsx("button", { className: "btn-primary", onClick: onCreateEmoji, disabled: !newEmojiName || !newEmojiFile, style: { width: "auto", padding: "0 14px", height: 38 }, children: "\uC5C5\uB85C\uB4DC" })] }), _jsxs("ul", { className: "integrations-list emoji-grid", children: [emojis.length === 0 && _jsx("li", { className: "chat-empty", style: { padding: 12 }, children: "\uB4F1\uB85D\uB41C \uC774\uBAA8\uC9C0\uAC00 \uC5C6\uC2B5\uB2C8\uB2E4." }), emojis.map((e) => (_jsxs("li", { className: "emoji-tile", children: [_jsx("img", { src: api.emojiImageURL(token ?? "", e.id), alt: e.name }), _jsxs("div", { className: "emoji-tile-name", title: `:${e.name}:`, children: [":", e.name, ":"] }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 8px", height: 26, color: "var(--danger)", fontSize: 11 }, onClick: () => onDeleteEmoji(e.id), children: "\uC0AD\uC81C" })] }, e.id)))] })] })), tab === "invites" && (_jsx("div", { className: "integrations-body", children: !currentTeamId ? (_jsx("div", { className: "chat-empty", style: { padding: 12 }, children: "\uD300\uC744 \uBA3C\uC800 \uC120\uD0DD\uD558\uBA74 \uCD08\uB300 \uB9C1\uD06C\uB97C \uBC1C\uAE09\uD560 \uC218 \uC788\uC2B5\uB2C8\uB2E4." })) : (_jsxs(_Fragment, { children: [_jsxs("div", { className: "integrations-create", children: [_jsxs("label", { style: { display: "flex", alignItems: "center", gap: 6, fontSize: 13 }, children: ["\uCD5C\uB300 \uC0AC\uC6A9 \uD69F\uC218", _jsxs("select", { className: "field-input", style: { width: 120 }, value: inviteMaxUses, onChange: (e) => setInviteMaxUses(Number(e.target.value)), children: [_jsx("option", { value: 1, children: "1\uD68C" }), _jsx("option", { value: 5, children: "5\uD68C" }), _jsx("option", { value: 25, children: "25\uD68C" }), _jsx("option", { value: 0, children: "\uBB34\uC81C\uD55C" })] })] }), _jsxs("label", { style: { display: "flex", alignItems: "center", gap: 6, fontSize: 13 }, children: ["\uB9CC\uB8CC", _jsx("select", { className: "field-input", style: { width: 120 }, value: inviteTTLSeconds, onChange: (e) => setInviteTTLSeconds(Number(e.target.value)), children: INVITE_TTL_CHOICES.map((c) => (_jsx("option", { value: c.seconds, children: c.label }, c.seconds))) })] }), _jsx("button", { className: "btn-primary", onClick: onCreateInvite, style: { width: "auto", padding: "0 14px", height: 38 }, children: "\uCD08\uB300 \uB9C1\uD06C \uC0DD\uC131" })] }), _jsxs("ul", { className: "integrations-list", children: [invites.length === 0 && (_jsx("li", { className: "chat-empty", style: { padding: 12 }, children: "\uD65C\uC131 \uCD08\uB300 \uB9C1\uD06C\uAC00 \uC5C6\uC2B5\uB2C8\uB2E4." })), invites.map((inv) => {
                                            const remaining = inv.max_uses === 0
                                                ? "무제한"
                                                : `${inv.max_uses - inv.use_count} / ${inv.max_uses}`;
                                            const expires = new Date(inv.expires_at).toLocaleString();
                                            return (_jsxs("li", { className: "integrations-row", children: [_jsxs("div", { style: { flex: 1, minWidth: 0 }, children: [_jsx("div", { style: { fontWeight: 600, fontSize: 12, wordBreak: "break-all" }, children: inv.url }), _jsxs("div", { style: { color: "var(--muted)", fontSize: 12, marginTop: 2 }, children: ["\uB0A8\uC740 \uC0AC\uC6A9 ", remaining, " \u00B7 \uB9CC\uB8CC ", expires] })] }), _jsxs("div", { style: { display: "flex", gap: 6, flexWrap: "wrap", justifyContent: "flex-end" }, children: [_jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30 }, onClick: () => onCopyInvite(inv.url), children: "\uBCF5\uC0AC" }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }, onClick: () => onRevokeInvite(inv.id), children: "\uBB34\uD6A8\uD654" })] })] }, inv.id));
                                        })] })] })) })), tab === "users" && (_jsx("div", { className: "integrations-body", children: _jsxs("ul", { className: "integrations-list", children: [users.length === 0 && (_jsx("li", { className: "chat-empty", style: { padding: 12 }, children: "\uB4F1\uB85D\uB41C \uC0AC\uC6A9\uC790\uAC00 \uC5C6\uC2B5\uB2C8\uB2E4." })), users.map((u) => {
                                    const inactive = (u.delete_at ?? 0) > 0;
                                    return (_jsxs("li", { className: "integrations-row", style: inactive ? { opacity: 0.55 } : undefined, children: [_jsxs("div", { style: { flex: 1 }, children: [_jsxs("div", { style: { fontWeight: 600 }, children: ["@", u.username, inactive && (_jsx("span", { style: { marginLeft: 8, color: "var(--danger)", fontSize: 11 }, children: "\uBE44\uD65C\uC131" }))] }), _jsxs("div", { style: { color: "var(--muted)", fontSize: 12 }, children: [u.email, " \u00B7 ", u.roles || "system_user"] })] }), inactive ? (_jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30 }, onClick: () => onReactivateUser(u.id), children: "\uD65C\uC131\uD654" })) : (_jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }, onClick: () => onDeactivateUser(u.id, u.username), children: "\uBE44\uD65C\uC131\uD654" }))] }, u.id));
                                })] }) })), tab === "audit" && (_jsxs("div", { className: "integrations-body", children: [_jsxs("div", { className: "integrations-create", children: [_jsxs("label", { style: { display: "flex", alignItems: "center", gap: 6, fontSize: 13 }, children: ["\uBD84\uB958", _jsx("select", { className: "field-input", style: { width: 140 }, value: auditPrefix, onChange: (e) => setAuditPrefix(e.target.value), children: AUDIT_PREFIXES.map((p) => (_jsx("option", { value: p.value, children: p.label }, p.value || "all"))) })] }), _jsx("input", { className: "field-input", placeholder: "\uD589\uC704\uC790 username (\uC635\uC158)", value: auditActor, onChange: (e) => setAuditActor(e.target.value), style: { flex: "1 1 180px" } }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 12px", height: 38 }, onClick: refresh, children: "\uC0C8\uB85C\uACE0\uCE68" })] }), _jsxs("ul", { className: "integrations-list", children: [auditRows.length === 0 && (_jsx("li", { className: "chat-empty", style: { padding: 12 }, children: "\uC870\uAC74\uC5D0 \uB9DE\uB294 \uAC10\uC0AC \uB85C\uADF8\uAC00 \uC5C6\uC2B5\uB2C8\uB2E4." })), auditRows.map((row) => {
                                        // Payload can be anything the action logger wrote — we stringify
                                        // so the admin can eyeball it without unfolding a JSON tree.
                                        // Empty payload shows as "—".
                                        let payload = "";
                                        try {
                                            payload =
                                                row.payload == null || (typeof row.payload === "object" && Object.keys(row.payload).length === 0)
                                                    ? "—"
                                                    : JSON.stringify(row.payload);
                                        }
                                        catch {
                                            payload = String(row.payload);
                                        }
                                        return (_jsx("li", { className: "integrations-row", style: { alignItems: "flex-start" }, children: _jsxs("div", { style: { flex: 1, minWidth: 0 }, children: [_jsx("div", { style: { fontWeight: 600, fontSize: 13 }, children: row.action }), _jsxs("div", { style: { color: "var(--muted)", fontSize: 11, marginTop: 2 }, children: [new Date(row.create_at).toLocaleString(), row.actor_id && ` · 행위자 ${row.actor_id.slice(0, 8)}`, row.target && ` · 대상 ${row.target}`] }), payload !== "—" && (_jsx("pre", { style: {
                                                            margin: "4px 0 0",
                                                            padding: "4px 6px",
                                                            background: "rgba(255,255,255,0.04)",
                                                            borderRadius: 4,
                                                            fontSize: 11,
                                                            whiteSpace: "pre-wrap",
                                                            wordBreak: "break-all",
                                                        }, children: payload }))] }) }, row.id));
                                    })] })] }))] }), confirmer.render()] }));
}
