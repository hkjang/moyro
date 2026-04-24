import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import { clearAuth, setAuth } from "@/store/authSlice";
import { api, } from "@/api/client";
import { IntegrationsPanel } from "@/components/IntegrationsPanel";
import { EmojiPicker, customEmojiByName } from "@/components/EmojiPicker";
import { Lightbox } from "@/components/Lightbox";
import { MessageBody } from "@/components/MessageBody";
import { useMentionAutocomplete } from "@/components/MentionPicker";
import { useEscClose, useConfirm } from "@/components/shared";
import { useWebsocket } from "@/hooks/useWebsocket";
// Quick reaction palette. The server accepts any emoji name; these are
// the one-click buttons exposed in the UI.
const QUICK_EMOJIS = ["+1", "heart", "tada", "laughing", "eyes", "rocket"];
export function ChatView() {
    const user = useSelector((s) => s.auth.user);
    const token = useSelector((s) => s.auth.token);
    const dispatch = useDispatch();
    // Phase 20 — shared confirm dialog in place of native window.confirm.
    // render() is spilled into the chat-shell div at the bottom so its
    // z-index stacks above every other modal.
    const confirmer = useConfirm();
    const [teams, setTeams] = useState([]);
    const [currentTeamId, setCurrentTeamId] = useState(null);
    const [channels, setChannels] = useState([]);
    const [currentChannelId, setCurrentChannelId] = useState(null);
    const [posts, setPosts] = useState([]);
    const [loadingPosts, setLoadingPosts] = useState(false);
    const [error, setError] = useState(null);
    const [users, setUsers] = useState({});
    const [statuses, setStatuses] = useState({});
    const [reactionsByPost, setReactionsByPost] = useState({});
    const [filesByID, setFilesByID] = useState({});
    const [typingUsers, setTypingUsers] = useState({});
    const [unread, setUnread] = useState({});
    // Per-channel notify_props (loaded lazily when the settings menu opens
    // or when a channel's member row is hydrated at login). Desktop pref
    // from the WS `unread_updated` event is also folded in so we can make
    // notification decisions without a round-trip.
    const [channelNotify, setChannelNotify] = useState({});
    const channelNotifyRef = useRef({});
    useEffect(() => { channelNotifyRef.current = channelNotify; }, [channelNotify]);
    const [searchTerm, setSearchTerm] = useState("");
    const [searchResults, setSearchResults] = useState(null);
    const [showStartDM, setShowStartDM] = useState(false);
    const [showIntegrations, setShowIntegrations] = useState(false);
    // Phase 16 — session-management modal. We lazy-fetch the list when the
    // modal opens; the list is short-lived and stale data (e.g. a session
    // that just expired) would just 404 on the revoke call which we handle.
    const [showSessions, setShowSessions] = useState(false);
    const [sessions, setSessions] = useState([]);
    const [sessionsLoading, setSessionsLoading] = useState(false);
    // Phase 16 — archived channel visibility toggle. Off by default so the
    // sidebar stays lean. When on we re-fetch channels with include_deleted
    // so soft-deleted channels appear dimmed in the sidebar.
    const [showArchived, setShowArchived] = useState(false);
    // Phase 16 — archived-channel list (only populated while showArchived is
    // true). Kept separate from `channels` so the main list stays focused on
    // active rows; rendering merges the two below.
    const [archivedChannels, setArchivedChannels] = useState([]);
    const [myStatus, setMyStatus] = useState("online");
    // Profile picture upload — ref hits the hidden <input type="file">, flag
    // disables the button while the multipart upload is in flight so a second
    // click can't stack a duplicate request.
    const avatarFileRef = useRef(null);
    const [uploadingAvatar, setUploadingAvatar] = useState(false);
    // Phase 17 — email digest opt-in. Loaded once at mount; the toggle updates
    // optimistically and rolls back on server error. `null` = not yet loaded,
    // which disables the checkbox briefly on first paint.
    const [digestEnabled, setDigestEnabled] = useState(null);
    // Phase 18 — saved-posts set of post ids. Hydrated lazily per channel
    // render via `savedPostsByIds` and patched on WS `saved_post_changed`.
    // A plain Set keeps the MessageRow render O(1) per post.
    const [savedIds, setSavedIds] = useState(new Set());
    // When true the main pane renders the 저장됨 pseudo-channel view
    // (separate list of bookmarked posts across all channels).
    const [savedView, setSavedView] = useState(false);
    const [savedPosts, setSavedPosts] = useState([]);
    const [savedLoading, setSavedLoading] = useState(false);
    // Phase 18 — 채널 탐색 modal toggle. Lists public channels not yet
    // joined so users can discover them without an admin invite.
    const [showDiscover, setShowDiscover] = useState(false);
    // Phase 19 — scheduled messages. `scheduledList` is the cached pending
    // queue for the sidebar 예약됨 pseudo-channel; invalidated on create/
    // delete/send WS events. `scheduledView` flips the main pane into the
    // list layout; `scheduleModalFor` remembers which channel the compose-
    // then-schedule action came from so the modal persists the target.
    const [scheduledView, setScheduledView] = useState(false);
    const [scheduledList, setScheduledList] = useState([]);
    const [scheduledLoading, setScheduledLoading] = useState(false);
    const [scheduleModalFor, setScheduleModalFor] = useState(null);
    // Phase 20 (F3) — bump-to-reset counters per composer surface. Passed
    // into <Composer resetSeq=… />; the Composer only reacts to *changes*,
    // so initial-mount rehydrate is preserved.
    const [rootComposerResetSeq, setRootComposerResetSeq] = useState(0);
    const [threadComposerResetSeq, setThreadComposerResetSeq] = useState(0);
    // Phase 19 — post reminders. Popover anchored to a MessageRow via post id;
    // only one open at a time so we render a single overlay. `reminderToasts`
    // is a short stack of incoming reminder_fired WS events; each entry is
    // auto-dismissed by a per-id timer but can also be clicked to jump.
    const [reminderForPostId, setReminderForPostId] = useState(null);
    const [reminderToasts, setReminderToasts] = useState([]);
    // Phase 18 — search filters lifted out of the free-form terms. The
    // search input still accepts `from:` / `in:` / `before:` / `after:` /
    // `has:file` / `has:link`; we parse them into this state, re-emit the
    // plain terms in the query, and pass filters as explicit POST fields.
    const [searchFilters, setSearchFilters] = useState({});
    const [searchTotal, setSearchTotal] = useState(0);
    const [searchPage, setSearchPage] = useState(0);
    // Admin gate — roles is a space-separated list from the server. We only
    // render the integrations entry-point for system_admin; the server also
    // enforces this on every route, so a forged check here only grants a UI
    // button with no teeth.
    const isAdmin = useMemo(() => (user?.roles ?? "").split(/\s+/).includes("system_admin"), [user]);
    // Thread sidebar: rootId is the live open thread, threadPosts is the
    // ordered list (oldest-first, root included) and threadLoading mirrors
    // the fetch state. Ref mirrors let the WS handler decide fast whether
    // an inbound post concerns the open thread without re-rendering on
    // every event.
    const [threadRootId, setThreadRootId] = useState(null);
    const [threadPosts, setThreadPosts] = useState([]);
    const [threadLoading, setThreadLoading] = useState(false);
    const threadRootIdRef = useRef(null);
    useEffect(() => { threadRootIdRef.current = threadRootId; }, [threadRootId]);
    const currentChannelIdRef = useRef(null);
    useEffect(() => { currentChannelIdRef.current = currentChannelId; }, [currentChannelId]);
    // ---- Load teams ----
    useEffect(() => {
        if (!token)
            return;
        api.listTeams(token)
            .then((t) => {
            setTeams(t ?? []);
            setCurrentTeamId((prev) => prev ?? (t?.[0]?.id ?? null));
        })
            .catch((e) => setError(e.message));
    }, [token]);
    // ---- Load channels when team changes (also include DM channels) ----
    const loadChannels = useCallback(async () => {
        if (!token || !currentTeamId)
            return;
        try {
            const c = await api.listChannels(token, currentTeamId);
            setChannels(c ?? []);
            setCurrentChannelId((prev) => {
                if (prev && (c ?? []).some((x) => x.id === prev))
                    return prev;
                return (c ?? [])[0]?.id ?? null;
            });
            // Hydrate per-channel unread counts + notify_props in one shot so
            // badges survive reloads without a per-channel fetch storm.
            try {
                const members = await api.listMyChannelMembers(token, currentTeamId);
                const unreadNext = {};
                const notifyNext = {};
                for (const m of members) {
                    unreadNext[m.channel_id] = { msg: m.msg_count, mention: m.mention_count };
                    if (m.notify_props)
                        notifyNext[m.channel_id] = m.notify_props;
                }
                setUnread(unreadNext);
                setChannelNotify(notifyNext);
            }
            catch { /* ignore — badges will rebuild from WS events */ }
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "채널 로드 실패");
        }
    }, [token, currentTeamId]);
    useEffect(() => { loadChannels(); }, [loadChannels]);
    // Ask for browser notification permission once per session. No-op if
    // the user has already decided. Stays best-effort — a denied permission
    // just means notifications silently don't fire.
    useEffect(() => {
        if (!("Notification" in window))
            return;
        if (Notification.permission === "default") {
            Notification.requestPermission().catch(() => { });
        }
    }, []);
    // Phase 17 — load email digest preference once per session. The server
    // defaults to `digest_enabled=true` so first-time users see the checkbox
    // ticked. A fetch error leaves the checkbox disabled rather than lying.
    useEffect(() => {
        if (!token) {
            setDigestEnabled(null);
            return;
        }
        api.getEmailPrefs(token)
            .then((p) => setDigestEnabled(!!p.digest_enabled))
            .catch(() => setDigestEnabled(null));
    }, [token]);
    // ---- Load posts (+ reactions + file infos) when channel changes ----
    useEffect(() => {
        if (!token || !currentChannelId) {
            setPosts([]);
            return;
        }
        setLoadingPosts(true);
        api.listPosts(token, currentChannelId)
            .then(async (list) => {
            const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
            ordered.reverse(); // newest-first → oldest-first
            setPosts(ordered);
            // Collect unique user IDs and file IDs for a single round-trip each.
            const userIds = Array.from(new Set(ordered.map((p) => p.user_id)));
            const fileIds = Array.from(new Set(ordered.flatMap((p) => p.file_ids ?? [])));
            hydrateUsers(userIds);
            hydrateFiles(fileIds);
            // Pull reactions per post (small N) — fire-and-forget per message.
            ordered.forEach((p) => {
                api.listReactions(token, p.id)
                    .then((rs) => setReactionsByPost((prev) => ({ ...prev, [p.id]: rs ?? [] })))
                    .catch(() => { });
            });
            // Phase 18 — hydrate bookmarked state for the loaded posts in one
            // batch call so the star icon renders filled where applicable.
            // Merges into savedIds (additive) so other channels' state
            // survives the per-channel load.
            if (ordered.length > 0) {
                api.savedPostsByIds(token, ordered.map((p) => p.id))
                    .then((m) => {
                    setSavedIds((prev) => {
                        const next = new Set(prev);
                        Object.entries(m).forEach(([id, isSaved]) => {
                            if (isSaved)
                                next.add(id);
                            else
                                next.delete(id);
                        });
                        return next;
                    });
                })
                    .catch(() => { });
            }
            // Mark viewed to clear unread.
            api.viewChannel(token, currentChannelId).catch(() => undefined);
            setUnread((u) => ({ ...u, [currentChannelId]: { msg: 0, mention: 0 } }));
        })
            .catch((e) => setError(e.message))
            .finally(() => setLoadingPosts(false));
    }, [token, currentChannelId]);
    async function hydrateUsers(ids) {
        if (!token)
            return;
        const missing = ids.filter((id) => id && !users[id]);
        if (missing.length === 0)
            return;
        const results = await Promise.all(missing.map((id) => api.getUser(token, id).catch(() => null)));
        setUsers((prev) => {
            const next = { ...prev };
            results.forEach((u) => { if (u)
                next[u.id] = u; });
            return next;
        });
        try {
            const st = await api.getUserStatusesByIDs(token, missing);
            setStatuses((prev) => {
                const next = { ...prev };
                st.forEach((s) => { next[s.user_id] = s.status; });
                return next;
            });
        }
        catch { /* ignore */ }
    }
    async function hydrateFiles(ids) {
        if (!token)
            return;
        const missing = ids.filter((id) => id && !filesByID[id]);
        if (missing.length === 0)
            return;
        const results = await Promise.all(missing.map((id) => api.fileInfo(token, id).catch(() => null)));
        setFilesByID((prev) => {
            const next = { ...prev };
            results.forEach((f) => { if (f)
                next[f.id] = f; });
            return next;
        });
    }
    // ---- Load my status once ----
    useEffect(() => {
        if (!token || !user)
            return;
        api.getUserStatus(token, user.id)
            .then((s) => setMyStatus(s.status))
            .catch(() => undefined);
    }, [token, user]);
    // ---- WebSocket (with reconnect + reconciler) ----
    //
    // The hook handles backoff/reopen internally; we just hand it a token
    // and a message callback. `reconnectSeq` bumps on every successful
    // *reopen* (not the initial connect), which triggers the reconciler
    // below to refetch anything that might have drifted during the gap.
    const handleWSMessage = useCallback((ev) => {
        try {
            const payload = JSON.parse(ev.data);
            handleWSEvent(payload);
        }
        catch { /* ignore malformed frames */ }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);
    const ws = useWebsocket(token, handleWSMessage);
    const wsStatus = ws.status;
    const wsAttempts = ws.attempts;
    const wsReconnectSeq = ws.reconnectSeq;
    const wsSend = ws.send;
    // Phase 17 — reconnect reconciler. When the socket reopens after a drop,
    // refetch enough state to catch up on anything that may have happened in
    // the gap: channel membership changes (create/archive/restore), unread +
    // mention counters, and the current channel's post stream. All merge by
    // id into existing state — no full page reload.
    useEffect(() => {
        if (wsReconnectSeq === 0)
            return;
        if (!token)
            return;
        // (1) channels + unread + notify_props: loadChannels already does both.
        if (currentTeamId) {
            loadChannels();
        }
        // (2) posts in the currently open channel — merge by id so optimistic
        // edits / reactions we may have applied locally don't get clobbered.
        const chanID = currentChannelId;
        if (chanID) {
            api.listPosts(token, chanID).then((list) => {
                const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
                ordered.reverse();
                setPosts((prev) => {
                    const byId = new Map(prev.map((p) => [p.id, p]));
                    ordered.forEach((p) => byId.set(p.id, p));
                    return Array.from(byId.values()).sort((a, b) => a.create_at - b.create_at);
                });
            }).catch(() => undefined);
        }
    }, [wsReconnectSeq, token, currentTeamId, currentChannelId, loadChannels]);
    function handleWSEvent(payload) {
        const { event, data } = payload;
        if (!event || !data)
            return;
        switch (event) {
            case "posted": {
                const p = JSON.parse(String(data.post ?? "{}"));
                if (!p.id)
                    return;
                hydrateUsers([p.user_id]);
                hydrateFiles(p.file_ids ?? []);
                if (p.channel_id === currentChannelIdRef.current) {
                    setPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
                    api.viewChannel(token, p.channel_id).catch(() => undefined);
                }
                // `unread_updated` WS event arrives alongside `posted` for non-author
                // recipients and is the authoritative source for badge counts; we no
                // longer optimistically bump unread here.
                // Thread sidebar also needs this post if it belongs to the open root.
                if (threadRootIdRef.current && p.root_id === threadRootIdRef.current) {
                    setThreadPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
                }
                // Desktop notification — only when:
                //   - author is someone else
                //   - the tab is not in focus OR the channel isn't the open one
                //   - browser permission is granted
                //   - channel's desktop preference is not "none"
                //   - desktop=mentions requires this post to mention us (or be a DM)
                const authorIsMe = user && p.user_id === user.id;
                const channel = channels.find((c) => c.id === p.channel_id);
                const isDM = channel?.type === "D";
                const mentionIDs = parseMentionIDs(data.mentions);
                const mentionsMe = !!user && mentionIDs.includes(user.id);
                const inFocus = !document.hidden && p.channel_id === currentChannelIdRef.current;
                const pref = channelNotifyRef.current[p.channel_id]?.desktop ?? "all";
                if (!authorIsMe &&
                    !inFocus &&
                    typeof Notification !== "undefined" &&
                    Notification.permission === "granted" &&
                    pref !== "none" &&
                    (pref === "all" || mentionsMe || isDM)) {
                    const author = users[p.user_id]?.username ?? "새 메시지";
                    const channelLabel = isDM
                        ? author
                        : (channel ? `#${channel.display_name}` : "채널");
                    try {
                        const n = new Notification(channelLabel, {
                            body: p.message?.slice(0, 140) || "",
                            tag: p.channel_id,
                            icon: "/favicon.ico",
                        });
                        n.onclick = () => {
                            window.focus();
                            setCurrentChannelId(p.channel_id);
                            n.close();
                        };
                    }
                    catch { /* some browsers reject in background tabs — no-op */ }
                }
                return;
            }
            case "post_edited": {
                const p = JSON.parse(String(data.post ?? "{}"));
                setPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
                setThreadPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
                return;
            }
            case "post_deleted": {
                const pid = String(data.post_id ?? "");
                setPosts((prev) => prev.filter((x) => x.id !== pid));
                setThreadPosts((prev) => prev.filter((x) => x.id !== pid));
                if (threadRootIdRef.current === pid)
                    closeThread();
                return;
            }
            case "post_pinned":
            case "post_unpinned": {
                const p = JSON.parse(String(data.post ?? "{}"));
                setPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
                setThreadPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
                return;
            }
            case "reaction_added": {
                const r = JSON.parse(String(data.reaction ?? "{}"));
                setReactionsByPost((prev) => {
                    const cur = prev[r.post_id] ?? [];
                    if (cur.some((x) => x.user_id === r.user_id && x.emoji_name === r.emoji_name))
                        return prev;
                    return { ...prev, [r.post_id]: [...cur, r] };
                });
                return;
            }
            case "reaction_removed": {
                const r = JSON.parse(String(data.reaction ?? "{}"));
                setReactionsByPost((prev) => {
                    const cur = prev[r.post_id] ?? [];
                    return {
                        ...prev,
                        [r.post_id]: cur.filter((x) => !(x.user_id === r.user_id && x.emoji_name === r.emoji_name)),
                    };
                });
                return;
            }
            case "typing": {
                const ch = String(data.channel_id ?? "");
                const uid = String(data.user_id ?? "");
                if (!ch || !uid || ch !== currentChannelIdRef.current)
                    return;
                setTypingUsers((prev) => ({ ...prev, [uid]: Date.now() }));
                return;
            }
            case "status_change": {
                const uid = String(data.user_id ?? "");
                const st = String(data.status ?? "");
                if (uid)
                    setStatuses((prev) => ({ ...prev, [uid]: st }));
                return;
            }
            case "channel_viewed": {
                const ch = String(data.channel_id ?? "");
                if (ch)
                    setUnread((u) => ({ ...u, [ch]: { msg: 0, mention: 0 } }));
                return;
            }
            case "unread_updated": {
                const ch = String(data.channel_id ?? "");
                if (!ch)
                    return;
                // Server-authoritative: replace rather than increment. Desktop
                // pref also rides along so we can cache without a GET.
                const msg = Number(data.msg_count ?? 0);
                const mention = Number(data.mention_count ?? 0);
                const desktop = typeof data.desktop === "string" ? data.desktop : undefined;
                // Suppress the sidebar bump while the channel is currently open — the
                // corresponding viewChannel call will zero it out on the next tick,
                // so incrementing would flash a stale 1.
                if (ch !== currentChannelIdRef.current) {
                    setUnread((u) => ({ ...u, [ch]: { msg, mention } }));
                }
                if (desktop) {
                    setChannelNotify((prev) => {
                        const cur = prev[ch] ?? {};
                        if (cur.desktop === desktop)
                            return prev;
                        return { ...prev, [ch]: { ...cur, desktop } };
                    });
                }
                return;
            }
            case "channel_updated": {
                const c = JSON.parse(String(data.channel ?? "{}"));
                setChannels((prev) => prev.map((x) => x.id === c.id ? c : x));
                return;
            }
            case "channel_deleted": {
                // Phase 16: an admin archived this channel somewhere else. Drop it
                // from the active sidebar; the archived-channels panel (if open)
                // will pull a fresh list. Phase 20 (F4): if the user was *in* that
                // channel, roll them forward to the next remaining non-archived
                // channel instead of dumping them into the "채널을 만들어 시작하세요"
                // empty state. Only set null when there's genuinely nothing left.
                const channelID = String(data.channel_id ?? "");
                if (!channelID)
                    return;
                setChannels((prev) => {
                    const next = prev.filter((c) => c.id !== channelID);
                    setCurrentChannelId((cur) => {
                        if (cur !== channelID)
                            return cur;
                        // Prefer a non-DM channel (sidebar main group) over a DM so
                        // the viewer lands somewhere that "makes sense" after losing
                        // the archived channel. Falls through to the first DM if
                        // that's all that remains.
                        const firstPublic = next.find((c) => c.type !== "D" && c.type !== "G");
                        return firstPublic?.id ?? next[0]?.id ?? null;
                    });
                    return next;
                });
                if (showArchived)
                    loadArchivedChannels();
                return;
            }
            case "channel_restored": {
                // Restored elsewhere — refresh the live list so it reappears. If
                // the archived panel is open, drop it from the archived list.
                loadChannels();
                if (showArchived)
                    loadArchivedChannels();
                return;
            }
            case "mention": {
                // Stored on the post already via `posted`; could ring a notification.
                return;
            }
            case "saved_post_changed": {
                // Phase 18 — multi-tab sync. Fired only to the acting user's own
                // sockets. We patch the savedIds set and, if the 저장됨 view is
                // open, drop the unsaved post from the list immediately.
                const postId = String(data.post_id ?? "");
                const nowSaved = !!data.saved;
                if (!postId)
                    return;
                setSavedIds((prev) => {
                    const next = new Set(prev);
                    if (nowSaved)
                        next.add(postId);
                    else
                        next.delete(postId);
                    return next;
                });
                if (!nowSaved) {
                    setSavedPosts((prev) => prev.filter((p) => p.id !== postId));
                }
                return;
            }
            case "scheduled_post_created":
            case "scheduled_post_sent":
            case "scheduled_post_deleted": {
                // Phase 19 — refetch the pending queue on any change. Keeps the
                // sidebar count + 예약됨 list authoritative across tabs. Cheap:
                // the list is bounded by the user's pending count.
                if (token) {
                    api.listMyScheduledPosts(token)
                        .then(setScheduledList)
                        .catch(() => { });
                }
                return;
            }
            case "reminder_fired": {
                // Phase 19 — push a toast. Auto-dismissed after 20s. The 이동
                // button (rendered below) sets currentChannelId to the recorded
                // channel so a click hops straight there.
                const rid = String(data.reminder_id ?? "");
                const pid = String(data.post_id ?? "");
                const cid = String(data.channel_id ?? "");
                const excerpt = String(data.excerpt ?? "");
                if (!rid)
                    return;
                // Phase 20 (F6) — cap the visible stack at 5. A burst of
                // reminder_fired events (e.g. many due at once after the server
                // comes back from a pause) would otherwise stack unbounded and
                // cover the viewport. We keep the newest 5 and drop the oldest;
                // the dismiss timer for the dropped id still fires harmlessly
                // (filter on an id that's no longer present is a no-op).
                setReminderToasts((prev) => {
                    const next = [...prev, { id: rid, postId: pid, channelId: cid, excerpt }];
                    return next.length > 5 ? next.slice(next.length - 5) : next;
                });
                window.setTimeout(() => {
                    setReminderToasts((prev) => prev.filter((t) => t.id !== rid));
                }, 20_000);
                return;
            }
            case "reminder_created":
            case "reminder_deleted": {
                // No list view for reminders (they're per-post), so we just ignore.
                // The popover UI is modal; state is refreshed when re-opened.
                return;
            }
        }
    }
    // Expire typing indicators after 4s.
    useEffect(() => {
        const t = setInterval(() => {
            const cutoff = Date.now() - 4000;
            setTypingUsers((prev) => {
                const next = {};
                let changed = false;
                for (const [uid, ts] of Object.entries(prev)) {
                    if (ts > cutoff)
                        next[uid] = ts;
                    else
                        changed = true;
                }
                return changed ? next : prev;
            });
        }, 1500);
        return () => clearInterval(t);
    }, []);
    // ---- Derived ----
    const currentTeam = useMemo(() => teams.find((t) => t.id === currentTeamId) ?? null, [teams, currentTeamId]);
    const currentChannel = useMemo(() => channels.find((c) => c.id === currentChannelId) ?? null, [channels, currentChannelId]);
    const publicChannels = useMemo(() => channels.filter((c) => c.type !== "D"), [channels]);
    const dmChannels = useMemo(() => channels.filter((c) => c.type === "D"), [channels]);
    // ---- Actions ----
    async function onCreateTeam() {
        if (!token)
            return;
        const display = prompt("팀 이름을 입력하세요 (표시용)");
        if (!display)
            return;
        try {
            const t = await api.createTeam(token, slug(display), display);
            setTeams((prev) => [...prev, t]);
            setCurrentTeamId(t.id);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "팀 생성 실패");
        }
    }
    async function onCreateChannel() {
        if (!token || !currentTeamId)
            return;
        const display = prompt("채널 이름");
        if (!display)
            return;
        try {
            const c = await api.createChannel(token, currentTeamId, slug(display), display);
            setChannels((prev) => [...prev, c]);
            setCurrentChannelId(c.id);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "채널 생성 실패");
        }
    }
    // ---- Phase 16: channel archive / restore ----
    //
    // Archive stamps `delete_at` on the server and broadcasts a WS event
    // (`channel_deleted`) so every connected client drops the row from its
    // sidebar. Restore zeroes `delete_at` and broadcasts `channel_restored`.
    // The local optimistic update keeps the UI responsive; the broadcast
    // reconciles any drift (or informs other tabs).
    async function onArchiveChannel(channelId) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "채널 보관",
            message: "이 채널을 보관 처리할까요? 멤버의 사이드바에서 사라집니다.",
            confirmLabel: "보관",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await api.archiveChannel(token, channelId);
            // Optimistic local drop; the server's WS broadcast will still fire
            // shortly after and becomes a no-op if we already removed the row.
            setChannels((prev) => prev.filter((c) => c.id !== channelId));
            if (currentChannelId === channelId)
                setCurrentChannelId(null);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "채널 보관 실패");
        }
    }
    async function onRestoreChannel(channelId) {
        if (!token)
            return;
        try {
            await api.restoreChannel(token, channelId);
            // The restore itself doesn't return the channel row, so refetch.
            loadChannels();
            if (showArchived)
                loadArchivedChannels();
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "채널 복원 실패");
        }
    }
    // Fetch the archived-only slice. Re-runs whenever `showArchived` flips
    // on or the current team changes. We filter client-side to keep only
    // rows with a non-zero delete_at since `include_deleted=true` returns
    // both active and archived in one list.
    const loadArchivedChannels = useCallback(async () => {
        if (!token || !currentTeamId)
            return;
        try {
            const all = await api.listChannels(token, currentTeamId, true);
            setArchivedChannels((all ?? []).filter((c) => (c.delete_at ?? 0) > 0));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "보관 채널 로드 실패");
        }
    }, [token, currentTeamId]);
    useEffect(() => {
        if (!showArchived) {
            setArchivedChannels([]);
            return;
        }
        loadArchivedChannels();
    }, [showArchived, loadArchivedChannels]);
    // ---- Phase 16: session management ----
    const openSessionModal = useCallback(async () => {
        if (!token)
            return;
        setShowSessions(true);
        setSessionsLoading(true);
        try {
            const list = await api.listMySessions(token);
            // Newest first so the current-device row typically sits up top.
            list.sort((a, b) => b.create_at - a.create_at);
            setSessions(list);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "세션 조회 실패");
        }
        finally {
            setSessionsLoading(false);
        }
    }, [token]);
    async function onRevokeOneSession(sessionId) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "세션 종료",
            message: "이 세션을 종료할까요?",
            confirmLabel: "종료",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await api.revokeSession(token, sessionId);
            setSessions((prev) => prev.filter((s) => s.id !== sessionId));
            // If the user just killed their own current session, sign out
            // locally so the app lands on the login screen.
            const killedCurrent = sessions.find((s) => s.id === sessionId)?.is_current;
            if (killedCurrent)
                dispatch(clearAuth());
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "세션 종료 실패");
        }
    }
    async function onRevokeOtherSessions() {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "다른 기기 로그아웃",
            message: "다른 모든 기기에서 로그아웃할까요? 이 기기의 세션은 유지됩니다.",
            confirmLabel: "로그아웃",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await api.revokeOtherSessions(token);
            setSessions((prev) => prev.filter((s) => s.is_current));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "다른 세션 종료 실패");
        }
    }
    // Slash-command output rendered as a transient banner above the composer.
    const [cmdNotice, setCmdNotice] = useState(null);
    async function onSendPost(message, fileIds) {
        if (!token || !currentChannelId)
            return;
        const trimmed = message.trim();
        if (!trimmed && fileIds.length === 0)
            return;
        // Slash command path — only when there are no attachments and the message
        // starts with "/". Falls back to regular post on the unknown-command 404.
        if (trimmed.startsWith("/") && fileIds.length === 0) {
            try {
                const resp = await api.executeCommand(token, currentTeamId ?? "", currentChannelId, trimmed);
                if (resp.response_type === "ephemeral") {
                    setCmdNotice(resp.text);
                    setTimeout(() => setCmdNotice(null), 6000);
                }
                // in_channel commands produce a server-side post + WS broadcast; nothing else to do.
                return;
            }
            catch (e) {
                const msg = e instanceof Error ? e.message : "명령 실행 실패";
                // If the server says the command is unknown, send the line as a normal message.
                if (!msg.includes("unknown")) {
                    setError(msg);
                    return;
                }
            }
        }
        try {
            const p = await api.createPost(token, currentChannelId, trimmed, "", fileIds);
            setPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
            // Pre-hydrate file infos we just uploaded (already in filesByID from uploader).
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "전송 실패");
        }
    }
    async function onEditPost(postId, message) {
        if (!token)
            return;
        try {
            await api.updatePost(token, postId, message);
            // State refreshes via post_edited WS event; no local mutation needed.
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "수정 실패");
        }
    }
    async function onDeletePost(postId) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "메시지 삭제",
            message: "이 메시지를 삭제할까요? 되돌릴 수 없습니다.",
            confirmLabel: "삭제",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await api.deletePost(token, postId);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "삭제 실패");
        }
    }
    // Phase 18 — saved posts toggle. Optimistic patch on the savedIds set;
    // server emits `saved_post_changed` which will re-reconcile. We skip
    // the optimistic update only when the server call errors.
    async function onToggleSaved(post) {
        if (!token)
            return;
        const wasSaved = savedIds.has(post.id);
        setSavedIds((prev) => {
            const next = new Set(prev);
            if (wasSaved)
                next.delete(post.id);
            else
                next.add(post.id);
            return next;
        });
        try {
            if (wasSaved)
                await api.unsavePost(token, post.id);
            else
                await api.savePost(token, post.id);
        }
        catch (e) {
            // Roll back on error so the star reflects server truth.
            setSavedIds((prev) => {
                const next = new Set(prev);
                if (wasSaved)
                    next.add(post.id);
                else
                    next.delete(post.id);
                return next;
            });
            setError(e instanceof Error ? e.message : "저장 실패");
        }
    }
    // Phase 18 — load the 저장됨 pseudo-channel. Keeps its own list state
    // so switching between channels doesn't blow away the bookmarked list.
    const loadSavedPosts = useCallback(async () => {
        if (!token)
            return;
        setSavedLoading(true);
        try {
            const res = await api.listSavedPosts(token, 50, 0);
            const ordered = (res.order ?? []).map((id) => res.posts[id]).filter(Boolean);
            setSavedPosts(ordered);
            setSavedIds(new Set(ordered.map((p) => p.id)));
            hydrateUsers(Array.from(new Set(ordered.map((p) => p.user_id))));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "저장된 메시지 로드 실패");
        }
        finally {
            setSavedLoading(false);
        }
    }, [token]);
    // Enter / leave the 저장됨 view. Clears currentChannelId so the header
    // and composer code paths branch into the "no channel" layout, then
    // hydrates the list.
    function openSavedView() {
        setSavedView(true);
        setScheduledView(false);
        setSearchResults(null);
        loadSavedPosts();
    }
    function closeSavedView() {
        setSavedView(false);
    }
    // Phase 19 — scheduled-posts loader. Matches the saved-posts pattern;
    // fires on mount (via WS sync) or when the sidebar entry is clicked.
    const loadScheduledList = useCallback(async () => {
        if (!token)
            return;
        setScheduledLoading(true);
        try {
            const list = await api.listMyScheduledPosts(token);
            setScheduledList(list ?? []);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "예약 메시지 로드 실패");
        }
        finally {
            setScheduledLoading(false);
        }
    }, [token]);
    // Pull the pending queue once at mount so the sidebar count is accurate
    // before the user opens the 예약됨 view. Subsequent changes arrive via
    // the scheduled_post_* WS events wired above.
    useEffect(() => { loadScheduledList(); }, [loadScheduledList]);
    function openScheduledView() {
        setScheduledView(true);
        setSavedView(false);
        setSearchResults(null);
        loadScheduledList();
    }
    function closeScheduledView() {
        setScheduledView(false);
    }
    async function onCancelScheduled(id) {
        if (!token)
            return;
        const ok = await confirmer.confirm({
            title: "예약 취소",
            message: "예약된 메시지를 취소할까요?",
            confirmLabel: "취소",
            destructive: true,
        });
        if (!ok)
            return;
        try {
            await api.deleteScheduledPost(token, id);
            setScheduledList((prev) => prev.filter((s) => s.id !== id));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "예약 취소 실패");
        }
    }
    // Open the schedule modal from the Composer. Captures the current
    // compose state + active channel so the submission path has what it
    // needs without re-reading DOM. Phase 20 (F7): the thread composer
    // calls this with source="thread" + a rootId so the scheduled post
    // lands back in the same thread at send time.
    function onOpenScheduleModal(message, fileIds) {
        onOpenScheduleModalFor("root", message, fileIds, undefined);
    }
    function onOpenScheduleModalFromThread(rootId) {
        return (message, fileIds) => onOpenScheduleModalFor("thread", message, fileIds, rootId);
    }
    function onOpenScheduleModalFor(source, message, fileIds, rootId) {
        // For thread scheduling the channelId comes from the root post —
        // we look it up in the already-loaded thread post list so we don't
        // block the UI on a fetch.
        let channelId = currentChannelId;
        if (source === "thread" && rootId) {
            const root = threadPosts.find((p) => p.id === rootId) ?? posts.find((p) => p.id === rootId);
            channelId = root?.channel_id ?? currentChannelId;
        }
        if (!channelId)
            return;
        const trimmed = message.trim();
        if (!trimmed && fileIds.length === 0) {
            setError("메시지를 먼저 입력하세요.");
            return;
        }
        setScheduleModalFor({ channelId, message: trimmed, fileIds, rootId, source });
    }
    async function onConfirmSchedule(sendAt) {
        if (!token || !scheduleModalFor)
            return false;
        try {
            const sp = await api.createScheduledPost(token, {
                channel_id: scheduleModalFor.channelId,
                root_id: scheduleModalFor.rootId,
                message: scheduleModalFor.message,
                file_ids: scheduleModalFor.fileIds,
                send_at: sendAt,
            });
            setScheduledList((prev) => [...prev, sp].sort((a, b) => a.send_at - b.send_at));
            // Phase 20 (F3) — bump the right reset counter so the originating
            // Composer clears its value/pending/draft after a successful
            // schedule. Without this the user's typed text stays in the
            // textarea and can be accidentally Enter-sent a second time.
            if (scheduleModalFor.source === "thread") {
                setThreadComposerResetSeq((n) => n + 1);
            }
            else {
                setRootComposerResetSeq((n) => n + 1);
            }
            setScheduleModalFor(null);
            return true;
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "예약 실패");
            return false;
        }
    }
    // Phase 19 — create a reminder for the given post. `when` is the epoch-ms
    // target. Closes the popover on success; on error shows an inline error
    // and keeps the popover open so the user can retry a different time.
    async function onCreateReminder(postId, when) {
        if (!token)
            return false;
        try {
            await api.createPostReminder(token, postId, when);
            setReminderForPostId(null);
            return true;
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "리마인더 생성 실패");
            return false;
        }
    }
    function onJumpFromReminder(channelId) {
        if (!channelId)
            return;
        setSavedView(false);
        setScheduledView(false);
        setSearchResults(null);
        setCurrentChannelId(channelId);
    }
    async function onToggleReaction(post, emoji) {
        if (!token || !user)
            return;
        const existing = (reactionsByPost[post.id] ?? []).find((r) => r.user_id === user.id && r.emoji_name === emoji);
        try {
            if (existing) {
                await api.removeReaction(token, post.id, user.id, emoji);
            }
            else {
                await api.addReaction(token, post.id, user.id, emoji);
            }
            // WS events apply the change.
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "리액션 실패");
        }
    }
    async function onUploadFiles(files) {
        if (!token || !currentChannelId)
            return [];
        try {
            const res = await api.uploadFiles(token, currentChannelId, files);
            setFilesByID((prev) => {
                const next = { ...prev };
                res.file_infos.forEach((fi) => { next[fi.id] = fi; });
                return next;
            });
            return res.file_infos;
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "업로드 실패");
            return [];
        }
    }
    function sendTyping() {
        if (!currentChannelId)
            return;
        wsSend(JSON.stringify({
            seq: Date.now(),
            action: "user_typing",
            data: { channel_id: currentChannelId },
        }));
    }
    async function onStartDirect(otherId) {
        if (!token || !user)
            return;
        try {
            const c = await api.createDirectChannel(token, [user.id, otherId]);
            setChannels((prev) => prev.some((x) => x.id === c.id) ? prev : [...prev, c]);
            setCurrentChannelId(c.id);
            setShowStartDM(false);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "DM 생성 실패");
        }
    }
    // Phase 18 — parse filter tokens out of the free-form search string.
    // Returns the residual query (filters stripped) plus the resolved filter
    // object. Unknown tokens stay in the query so the ts_rank search still
    // matches them literally. Usernames and channel names are resolved via
    // the cached users/channels maps — if a user/channel isn't in the cache
    // we pass the token through as a terms word and the server just returns
    // empty results, which is acceptable.
    function parseSearchFilters(raw) {
        const tokens = raw.split(/\s+/);
        const filters = {};
        const residual = [];
        for (const tok of tokens) {
            if (!tok)
                continue;
            const mFrom = tok.match(/^from:(\S+)$/i);
            if (mFrom) {
                const uname = mFrom[1];
                const hit = Object.values(users).find((u) => u.username === uname);
                if (hit) {
                    filters.from_user_id = hit.id;
                    continue;
                }
                residual.push(tok);
                continue;
            }
            const mIn = tok.match(/^in:(\S+)$/i);
            if (mIn) {
                const cname = mIn[1];
                const ch = channels.find((c) => c.name === cname || c.display_name === cname);
                if (ch) {
                    filters.in_channel_id = ch.id;
                    continue;
                }
                residual.push(tok);
                continue;
            }
            const mAfter = tok.match(/^after:(\d{4}-\d{2}-\d{2})$/i);
            if (mAfter) {
                const t = Date.parse(mAfter[1]);
                if (!isNaN(t)) {
                    filters.after = t;
                    continue;
                }
            }
            const mBefore = tok.match(/^before:(\d{4}-\d{2}-\d{2})$/i);
            if (mBefore) {
                // Treat before: as "end of that day" so `before:2026-01-01` includes
                // posts from Jan 1 themselves — users rarely want strict "<midnight".
                const t = Date.parse(mBefore[1]);
                if (!isNaN(t)) {
                    filters.before = t + 24 * 60 * 60 * 1000;
                    continue;
                }
            }
            if (/^has:file$/i.test(tok)) {
                filters.has_file = true;
                continue;
            }
            if (/^has:link$/i.test(tok)) {
                filters.has_link = true;
                continue;
            }
            residual.push(tok);
        }
        return { terms: residual.join(" "), filters };
    }
    async function onSearch(page = 0) {
        if (!token || !currentTeamId)
            return;
        const q = searchTerm.trim();
        if (!q) {
            setSearchResults(null);
            setSearchFilters({});
            setSearchTotal(0);
            setSearchPage(0);
            return;
        }
        const { terms, filters } = parseSearchFilters(q);
        // Searching for only filter tokens (no residual terms) isn't something
        // plainto_tsquery can handle — fall back to the already-filtered
        // listing by stuffing a single space so the server's trim check passes
        // only when there are real terms. Otherwise require one residual word.
        if (!terms.trim()) {
            setError("검색어를 입력하세요 (필터만으로는 검색할 수 없습니다).");
            return;
        }
        try {
            const res = await api.searchPosts(token, currentTeamId, terms, {
                page,
                perPage: 20,
                filters,
            });
            const ordered = (res.order ?? []).map((id) => res.posts[id]).filter(Boolean);
            setSearchResults(ordered);
            setSearchFilters(filters);
            setSearchTotal(res.total_hits ?? ordered.length);
            setSearchPage(page);
            hydrateUsers(Array.from(new Set(ordered.map((p) => p.user_id))));
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "검색 실패");
        }
    }
    async function onChangeMyStatus(s) {
        if (!token)
            return;
        setMyStatus(s);
        try {
            await api.updateMyStatus(token, s, true);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "상태 변경 실패");
        }
    }
    // ---- Per-channel notify props ----
    // Optimistically applies the change so the menu reflects the new state
    // immediately, then reverts on error. We persist the *full* props bag so
    // the server doesn't have to merge — easier to reason about.
    const onChangeNotify = useCallback(async (channelId, patch) => {
        if (!token)
            return;
        const cur = channelNotifyRef.current[channelId] ?? { desktop: "all", mark_unread: "all" };
        const next = { ...cur, ...patch };
        setChannelNotify((prev) => ({ ...prev, [channelId]: next }));
        try {
            const saved = await api.setMyChannelNotifyProps(token, channelId, next);
            setChannelNotify((prev) => ({ ...prev, [channelId]: saved ?? next }));
        }
        catch (e) {
            setChannelNotify((prev) => ({ ...prev, [channelId]: cur }));
            setError(e instanceof Error ? e.message : "알림 설정 저장 실패");
        }
    }, [token]);
    // ---- Thread actions ----
    const openThread = useCallback(async (rootId) => {
        if (!token)
            return;
        setThreadRootId(rootId);
        setThreadLoading(true);
        try {
            const list = await api.listThread(token, rootId);
            const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
            setThreadPosts(ordered);
            hydrateUsers(Array.from(new Set(ordered.map((p) => p.user_id))));
            hydrateFiles(Array.from(new Set(ordered.flatMap((p) => p.file_ids ?? []))));
            ordered.forEach((p) => {
                api.listReactions(token, p.id)
                    .then((rs) => setReactionsByPost((prev) => ({ ...prev, [p.id]: rs ?? [] })))
                    .catch(() => { });
            });
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "스레드 로드 실패");
        }
        finally {
            setThreadLoading(false);
        }
    }, [token]);
    function closeThread() {
        setThreadRootId(null);
        setThreadPosts([]);
    }
    async function onReplyInThread(message, fileIds) {
        if (!token || !currentChannelId || !threadRootId)
            return;
        const trimmed = message.trim();
        if (!trimmed && fileIds.length === 0)
            return;
        try {
            const p = await api.createPost(token, currentChannelId, trimmed, threadRootId, fileIds);
            // Thread panel updates via the `posted` WS event; keep the reply
            // visible immediately in case our own broadcast arrives later.
            setThreadPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
        }
        catch (e) {
            setError(e instanceof Error ? e.message : "스레드 전송 실패");
        }
    }
    // ---- Render ----
    return (_jsxs("div", { className: "chat-shell", children: [_jsxs("aside", { className: "chat-side", children: [_jsxs("div", { className: "login-brand", style: { marginBottom: 16 }, children: [_jsx("div", { className: "login-logo", "aria-hidden": true, children: "M" }), _jsx("strong", { children: "Moddle" })] }), _jsx(SectionTitle, { children: "\uD300" }), _jsxs("div", { className: "item-list", children: [teams.map((t) => (_jsxs("button", { className: `item ${t.id === currentTeamId ? "item-active" : ""}`, onClick: () => setCurrentTeamId(t.id), children: [_jsx("span", { className: "item-badge", style: { background: color(t.id) }, children: t.display_name[0]?.toUpperCase() ?? "?" }), t.display_name] }, t.id))), _jsx("button", { className: "item item-muted", onClick: onCreateTeam, children: "\uFF0B \uC0C8 \uD300" })] }), currentTeamId && (_jsxs(_Fragment, { children: [_jsxs("div", { className: "item-list", style: { marginBottom: 4 }, children: [_jsx("button", { type: "button", className: `item ${savedView ? "item-active" : ""}`, onClick: openSavedView, title: "\uBD81\uB9C8\uD06C\uD55C \uBA54\uC2DC\uC9C0 \uBAA8\uC544\uBCF4\uAE30", children: "\u2B50 \uC800\uC7A5\uB428" }), _jsxs("button", { type: "button", className: `item ${scheduledView ? "item-active" : ""}`, onClick: openScheduledView, title: "\uC608\uC57D\uB41C \uBA54\uC2DC\uC9C0", style: { display: "flex", alignItems: "center", gap: 6 }, children: [_jsx("span", { style: { flex: 1 }, children: "\uD83D\uDD50 \uC608\uC57D\uB428" }), scheduledList.length > 0 && (_jsx("span", { className: "unread-badge", "aria-label": `예약 ${scheduledList.length}건`, children: scheduledList.length }))] })] }), _jsx(SectionTitle, { children: "\uCC44\uB110" }), _jsxs("div", { className: "item-list", children: [publicChannels.map((c) => (_jsx(ChannelRow, { channel: c, active: !savedView && !scheduledView && c.id === currentChannelId, unread: unread[c.id] ?? { msg: 0, mention: 0 }, onClick: () => { setSavedView(false); setScheduledView(false); setCurrentChannelId(c.id); } }, c.id))), _jsx("button", { className: "item item-muted", onClick: onCreateChannel, children: "\uFF0B \uC0C8 \uCC44\uB110" }), _jsx("button", { className: "item item-muted", onClick: () => setShowDiscover(true), title: "\uAC00\uC785 \uAC00\uB2A5\uD55C \uACF5\uAC1C \uCC44\uB110 \uCC3E\uC544\uBCF4\uAE30", children: "\uD83D\uDD0D \uCC44\uB110 \uD0D0\uC0C9" }), _jsx("button", { className: "item item-muted", onClick: () => setShowArchived((v) => !v), title: "\uBCF4\uAD00\uB41C \uCC44\uB110 \uD45C\uC2DC/\uC228\uAE40", children: showArchived ? "▴ 보관된 채널 숨기기" : "▾ 보관된 채널 보기" }), showArchived && archivedChannels.map((c) => (_jsxs("div", { className: "item", style: { opacity: 0.55, display: "flex", alignItems: "center", gap: 6 }, children: [_jsxs("span", { style: { flex: 1, fontStyle: "italic" }, children: ["# ", c.display_name] }), isAdmin && (_jsx("button", { type: "button", className: "action-btn", title: "\uBCF5\uC6D0", onClick: () => onRestoreChannel(c.id), children: "\u21BA" }))] }, c.id))), showArchived && archivedChannels.length === 0 && (_jsx("div", { className: "item item-muted", style: { fontSize: 12 }, children: "\uBCF4\uAD00\uB41C \uCC44\uB110\uC774 \uC5C6\uC2B5\uB2C8\uB2E4." }))] }), _jsx(SectionTitle, { children: "\uB2E4\uC774\uB809\uD2B8 \uBA54\uC2DC\uC9C0" }), _jsxs("div", { className: "item-list", children: [dmChannels.map((c) => {
                                        const otherId = dmCounterpart(c.name, user?.id ?? "");
                                        const u = users[otherId];
                                        const ue = unread[c.id] ?? { msg: 0, mention: 0 };
                                        return (_jsxs("button", { className: `item ${!savedView && !scheduledView && c.id === currentChannelId ? "item-active" : ""}`, onClick: () => { setSavedView(false); setScheduledView(false); setCurrentChannelId(c.id); }, children: [_jsx(Avatar, { id: otherId, name: u?.username ?? otherId.slice(0, 8), status: statuses[otherId], size: 22, picture: u?.picture, updateAt: u?.update_at }), _jsx("span", { style: { marginLeft: 2 }, children: u?.username ?? otherId.slice(0, 8) }), ue.mention > 0
                                                    ? _jsx("span", { className: "mention-badge", children: ue.mention })
                                                    : ue.msg > 0
                                                        ? _jsx("span", { className: "unread", children: ue.msg })
                                                        : null] }, c.id));
                                    }), _jsx("button", { className: "item item-muted", onClick: () => setShowStartDM(true), children: "\uFF0B \uC0C8 DM" })] })] })), _jsxs("div", { style: { marginTop: "auto", paddingTop: 16 }, children: [_jsx("div", { style: { color: "var(--muted)", fontSize: 12 }, children: "\uC811\uC18D \uC911" }), _jsxs("div", { style: { display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }, children: [_jsx(Avatar, { id: user?.id ?? "", name: user?.username ?? "", status: myStatus, size: 24, picture: user?.picture, updateAt: user?.update_at }), _jsx("div", { style: { fontWeight: 600 }, children: user?.username })] }), _jsxs("select", { className: "status-select", value: myStatus, onChange: (e) => onChangeMyStatus(e.target.value), children: [_jsx("option", { value: "online", children: "\uD83D\uDFE2 \uC628\uB77C\uC778" }), _jsx("option", { value: "away", children: "\uD83C\uDF19 \uC790\uB9AC\uBE44\uC6C0" }), _jsx("option", { value: "dnd", children: "\u26D4 \uBC29\uD574\uAE08\uC9C0" }), _jsx("option", { value: "offline", children: "\u26AB \uC624\uD504\uB77C\uC778" })] }), isAdmin && (_jsx("button", { type: "button", className: "btn-ghost", style: { marginTop: 8 }, onClick: () => setShowIntegrations(true), title: "\uBD07 \u00B7 \uC6F9\uD6C5 \u00B7 \uD1A0\uD070 \uAD00\uB9AC", children: "\uD83D\uDD27 \uD1B5\uD569 \uAD00\uB9AC" })), _jsx("input", { ref: avatarFileRef, type: "file", accept: "image/*", style: { display: "none" }, onChange: async (e) => {
                                    const file = e.target.files?.[0];
                                    if (!file || !token) {
                                        e.target.value = "";
                                        return;
                                    }
                                    setUploadingAvatar(true);
                                    try {
                                        const updated = await api.uploadProfileImage(token, file);
                                        dispatch(setAuth({ token, user: updated }));
                                    }
                                    catch (err) {
                                        setError(err.message || "프로필 사진 업로드 실패");
                                    }
                                    finally {
                                        setUploadingAvatar(false);
                                        e.target.value = "";
                                    }
                                } }), _jsx("button", { type: "button", className: "btn-ghost", style: { marginTop: 8 }, onClick: () => avatarFileRef.current?.click(), disabled: uploadingAvatar, title: "JPG/PNG, \uCD5C\uB300 512KB", children: uploadingAvatar ? "업로드 중…" : "🖼️ 프로필 사진 변경" }), _jsxs("label", { className: "email-prefs-toggle", style: { marginTop: 8, display: "flex", alignItems: "center", gap: 6, fontSize: 13 }, title: "\uD558\uB8E8\uC5D0 \uD55C \uBC88, \uB193\uCE5C \uBA58\uC158\uC744 \uC774\uBA54\uC77C\uB85C \uBCF4\uB0B4\uB4DC\uB9BD\uB2C8\uB2E4", children: [_jsx("input", { type: "checkbox", checked: digestEnabled === true, disabled: digestEnabled === null || !token, onChange: async (e) => {
                                            if (!token)
                                                return;
                                            const next = e.target.checked;
                                            const prev = digestEnabled;
                                            setDigestEnabled(next);
                                            try {
                                                await api.updateEmailPrefs(token, { digest_enabled: next });
                                            }
                                            catch (err) {
                                                setDigestEnabled(prev);
                                                setError(err.message || "이메일 설정 저장 실패");
                                            }
                                        } }), "\uC774\uBA54\uC77C \uC54C\uB9BC \uC218\uC2E0"] }), _jsx("button", { type: "button", className: "btn-ghost", style: { marginTop: 8 }, onClick: openSessionModal, title: "\uC774 \uACC4\uC815\uC758 \uB85C\uADF8\uC778 \uC138\uC158 \uBAA9\uB85D", children: "\uD83D\uDD10 \uC138\uC158 \uAD00\uB9AC" }), _jsx("button", { type: "button", className: "btn-ghost", style: { marginTop: 8 }, onClick: async () => {
                                    if (token) {
                                        try {
                                            await api.logout(token);
                                        }
                                        catch { /* best-effort */ }
                                    }
                                    dispatch(clearAuth());
                                }, children: "\uB85C\uADF8\uC544\uC6C3" })] })] }), _jsxs("main", { className: "chat-main", children: [wsStatus === "reconnecting" && (_jsxs("div", { className: "ws-reconnect-banner", role: "status", children: ["\uC7AC\uC5F0\uACB0 \uC911\u2026 (\uC2DC\uB3C4 ", wsAttempts, "\uD68C)"] })), savedView ? (_jsx(SavedPostsView, { posts: savedPosts, users: users, statuses: statuses, reactionsByPost: reactionsByPost, filesByID: filesByID, currentUserId: user?.id ?? "", token: token ?? "", channels: channels, loading: savedLoading, onClose: closeSavedView, onReload: loadSavedPosts, onToggleReaction: onToggleReaction, onEdit: onEditPost, onDelete: onDeletePost, onOpenThread: openThread, isSaved: (postId) => savedIds.has(postId), onToggleSaved: onToggleSaved, onJumpToChannel: (chId) => { setSavedView(false); setCurrentChannelId(chId); } })) : scheduledView ? (_jsx(ScheduledPostsView, { items: scheduledList, channels: channels, loading: scheduledLoading, onClose: closeScheduledView, onReload: loadScheduledList, onCancel: onCancelScheduled, onJumpToChannel: (chId) => { setScheduledView(false); setCurrentChannelId(chId); } })) : currentChannel ? (_jsxs(_Fragment, { children: [_jsxs("header", { className: "chat-header", children: [_jsxs("div", { className: "chat-header-left", children: [_jsx("div", { className: "chat-header-team", children: currentTeam?.display_name }), _jsxs("h2", { className: "chat-header-title", children: [currentChannel.type === "D" ? (_jsxs(_Fragment, { children: [_jsx(Avatar, { id: dmCounterpart(currentChannel.name, user?.id ?? ""), name: "", status: statuses[dmCounterpart(currentChannel.name, user?.id ?? "")], size: 22, picture: users[dmCounterpart(currentChannel.name, user?.id ?? "")]?.picture, updateAt: users[dmCounterpart(currentChannel.name, user?.id ?? "")]?.update_at }), " ", users[dmCounterpart(currentChannel.name, user?.id ?? "")]?.username ?? "다이렉트 메시지"] })) : (_jsxs(_Fragment, { children: [_jsx("span", { className: "channel-hash", children: "#" }), currentChannel.display_name] })), _jsx(ChannelSettingsMenu, { props: channelNotify[currentChannel.id] ?? { desktop: "all", mark_unread: "all" }, onChange: (patch) => onChangeNotify(currentChannel.id, patch) }), isAdmin && currentChannel.type !== "D" && currentChannel.type !== "G" && (_jsx("button", { type: "button", className: "action-btn", title: "\uCC44\uB110 \uBCF4\uAD00", style: { marginLeft: 6 }, onClick: () => onArchiveChannel(currentChannel.id), children: "\uD83D\uDDC4\uFE0F" }))] })] }), _jsxs("form", { className: "search-form", onSubmit: (e) => { e.preventDefault(); onSearch(0); }, children: [_jsx("input", { className: "search-input", placeholder: "\uBA54\uC2DC\uC9C0 \uAC80\uC0C9  (\uC608: \uBC30\uD3EC from:alice in:general has:link)", value: searchTerm, onChange: (e) => setSearchTerm(e.target.value), title: "from:username, in:channel, before:YYYY-MM-DD, after:YYYY-MM-DD, has:file, has:link" }), searchResults && (_jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 32, marginLeft: 6 }, onClick: () => { setSearchResults(null); setSearchTerm(""); setSearchFilters({}); setSearchTotal(0); setSearchPage(0); }, children: "\uB2EB\uAE30" }))] })] }), searchResults ? (_jsxs("div", { className: "chat-messages", children: [_jsxs("div", { className: "search-filter-bar", children: [_jsxs("div", { children: ["\"", searchTerm, "\" \uAC80\uC0C9\uACB0\uACFC ", " ", _jsx("strong", { children: searchTotal }), "\uAC74 (\uD398\uC774\uC9C0 ", searchPage + 1, ")"] }), _jsxs("div", { className: "search-filter-chips", children: [searchFilters.from_user_id && (_jsxs("span", { className: "search-chip", children: ["from: ", users[searchFilters.from_user_id]?.username ?? searchFilters.from_user_id.slice(0, 6)] })), searchFilters.in_channel_id && (_jsxs("span", { className: "search-chip", children: ["in: ", channels.find((c) => c.id === searchFilters.in_channel_id)?.display_name ?? searchFilters.in_channel_id.slice(0, 6)] })), searchFilters.after && (_jsxs("span", { className: "search-chip", children: ["after: ", new Date(searchFilters.after).toISOString().slice(0, 10)] })), searchFilters.before && (_jsxs("span", { className: "search-chip", children: ["before: ", new Date(searchFilters.before - 1).toISOString().slice(0, 10)] })), searchFilters.has_file && _jsx("span", { className: "search-chip", children: "has:file" }), searchFilters.has_link && _jsx("span", { className: "search-chip", children: "has:link" })] })] }), searchResults.map((p) => (_jsx(MessageRow, { post: p, isMe: p.user_id === user?.id, author: users[p.user_id], status: statuses[p.user_id], reactions: reactionsByPost[p.id] ?? [], currentUserId: user?.id ?? "", files: (p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean), token: token ?? "", onToggleReaction: (emoji) => onToggleReaction(p, emoji), onEdit: onEditPost, onDelete: onDeletePost, onOpenThread: openThread, isSaved: savedIds.has(p.id), onToggleSaved: () => onToggleSaved(p), compact: true, channelLabel: channels.find((c) => c.id === p.channel_id)?.display_name, onJumpToChannel: () => setCurrentChannelId(p.channel_id) }, p.id))), searchTotal > searchResults.length + searchPage * 20 && (_jsx("div", { style: { display: "flex", justifyContent: "center", padding: 10 }, children: _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 14px", height: 32 }, onClick: () => onSearch(searchPage + 1), children: "\uB2E4\uC74C \uD398\uC774\uC9C0" }) }))] })) : (_jsx("div", { className: "chat-messages", children: loadingPosts ? (_jsx("div", { className: "chat-empty", children: "\uBD88\uB7EC\uC624\uB294 \uC911\u2026" })) : posts.length === 0 ? (_jsx("div", { className: "chat-empty", children: "\uCCAB \uBA54\uC2DC\uC9C0\uB97C \uB0A8\uACA8\uBCF4\uC138\uC694." })) : (posts.map((p) => (_jsx(MessageRow, { post: p, isMe: p.user_id === user?.id, author: users[p.user_id], status: statuses[p.user_id], reactions: reactionsByPost[p.id] ?? [], currentUserId: user?.id ?? "", files: (p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean), token: token ?? "", isSaved: savedIds.has(p.id), onToggleSaved: () => onToggleSaved(p), onToggleReaction: (emoji) => onToggleReaction(p, emoji), onEdit: onEditPost, onDelete: onDeletePost, onOpenThread: openThread, onRemindMe: () => setReminderForPostId(p.id) }, p.id)))) })), !searchResults && (_jsxs(_Fragment, { children: [cmdNotice && (_jsxs("div", { className: "cmd-notice", children: [_jsx("span", { children: cmdNotice }), _jsx("button", { type: "button", className: "action-btn", onClick: () => setCmdNotice(null), children: "\u2715" })] })), _jsx(TypingIndicator, { typingUsers: Object.keys(typingUsers).filter((uid) => uid !== user?.id), users: users }), _jsx(Composer, { token: token ?? "", channelID: currentChannelId, onSend: onSendPost, onTyping: sendTyping, onUpload: onUploadFiles, onSchedule: onOpenScheduleModal, userId: user?.id, rootId: null, resetSeq: rootComposerResetSeq })] }))] })) : (_jsx("div", { className: "chat-empty", style: { paddingTop: 80 }, children: currentTeam ? "채널을 만들어 시작하세요." : "먼저 팀을 만들어주세요." })), error && _jsx("div", { className: "login-error", style: { margin: 12 }, children: error })] }), threadRootId && (_jsx(ThreadPanel, { rootId: threadRootId, posts: threadPosts, loading: threadLoading, users: users, statuses: statuses, reactionsByPost: reactionsByPost, filesByID: filesByID, currentUserId: user?.id ?? "", token: token ?? "", onToggleReaction: onToggleReaction, onEdit: onEditPost, onDelete: onDeletePost, onReply: onReplyInThread, onUpload: onUploadFiles, onSchedule: onOpenScheduleModalFromThread(threadRootId), composerResetSeq: threadComposerResetSeq, onClose: closeThread })), showStartDM && token && user && (_jsx(StartDirectModal, { token: token, currentUserId: user.id, onClose: () => setShowStartDM(false), onPick: onStartDirect })), showIntegrations && isAdmin && (_jsx(IntegrationsPanel, { channels: channels, currentTeamId: currentTeamId, onClose: () => setShowIntegrations(false) })), showSessions && (_jsx(SessionManagerModal, { sessions: sessions, loading: sessionsLoading, onRevoke: onRevokeOneSession, onRevokeOthers: onRevokeOtherSessions, onClose: () => setShowSessions(false) })), showDiscover && currentTeamId && token && (_jsx(ChannelDiscoverModal, { token: token, teamId: currentTeamId, onClose: () => setShowDiscover(false), onJoined: (chId) => {
                    // Re-pull so we have the full Channel record + membership
                    // roles correct. If the fetch fails the user will still see
                    // the channel after their next reconnect-driven refresh.
                    loadChannels();
                    setCurrentChannelId(chId);
                    setShowDiscover(false);
                } })), scheduleModalFor && (_jsx(ScheduleModal, { channelName: channels.find((c) => c.id === scheduleModalFor.channelId)?.display_name ?? "", messagePreview: scheduleModalFor.message, onCancel: () => setScheduleModalFor(null), onConfirm: onConfirmSchedule })), reminderForPostId && (_jsx(ReminderPopover, { postId: reminderForPostId, onCancel: () => setReminderForPostId(null), onConfirm: onCreateReminder })), reminderToasts.length > 0 && (_jsx("div", { className: "reminder-toast-stack", children: reminderToasts.map((t) => (_jsxs("div", { className: "reminder-toast", children: [_jsx("div", { className: "reminder-toast-title", children: "\uD83D\uDD14 \uB9AC\uB9C8\uC778\uB354" }), t.excerpt && (_jsx("div", { className: "reminder-toast-body", children: t.excerpt })), _jsxs("div", { className: "reminder-toast-actions", children: [_jsx("button", { type: "button", className: "btn-primary", style: { width: "auto", padding: "0 12px", height: 28 }, onClick: () => {
                                        onJumpFromReminder(t.channelId);
                                        setReminderToasts((prev) => prev.filter((x) => x.id !== t.id));
                                    }, children: "\uC774\uB3D9" }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 28 }, onClick: () => setReminderToasts((prev) => prev.filter((x) => x.id !== t.id)), children: "\uB2EB\uAE30" })] })] }, t.id))) })), confirmer.render()] }));
}
// ---- Subcomponents ----
function SectionTitle({ children }) {
    return _jsx("div", { className: "section-title", children: children });
}
function ChannelRow({ channel, active, unread, onClick, }) {
    return (_jsxs("button", { className: `item ${active ? "item-active" : ""}`, onClick: onClick, children: [_jsx("span", { className: "channel-hash", children: "#" }), _jsx("span", { style: { flex: 1, textAlign: "left" }, children: channel.display_name }), unread.mention > 0
                ? _jsx("span", { className: "mention-badge", children: unread.mention })
                : unread.msg > 0
                    ? _jsx("span", { className: "unread", children: unread.msg })
                    : null] }));
}
// `picture` (optional) is the raw value from User.picture — either an
// external URL or a bare file_id. When provided and non-empty, we fetch
// the image through `/api/v4/users/{id}/image?v={updateAt}`. On network
// failure (404 from empty, CORS from a stale external URL) we fall back
// to the initial-tile render via an onError handler.
function Avatar({ id, name, status, size = 28, picture, updateAt, }) {
    const bg = color(id || name || "?");
    const initial = (name || id || "?")[0]?.toUpperCase() ?? "?";
    const [imgFailed, setImgFailed] = useState(false);
    const showImg = !!picture && !imgFailed && !!id;
    return (_jsxs("span", { className: "avatar", style: {
            width: size,
            height: size,
            background: showImg ? "transparent" : bg,
            fontSize: size * 0.45,
        }, children: [showImg ? (_jsx("img", { src: api.userImageURL(id, updateAt ?? picture), alt: "", onError: () => setImgFailed(true) })) : (initial), status && _jsx("span", { className: `status-dot status-${status}` })] }));
}
function MessageRow(props) {
    const { post, isMe, author, status, reactions, currentUserId, files, token, onToggleReaction, onEdit, onDelete, onOpenThread, compact, hideThreadAction, isSaved, onToggleSaved, channelLabel, onJumpToChannel, onRemindMe } = props;
    const [editing, setEditing] = useState(false);
    const [draft, setDraft] = useState(post.message);
    const [pickerOpen, setPickerOpen] = useState(false);
    const editRef = useRef(null);
    // `@mention` autocomplete for the inline edit textarea. Scoped to the
    // post's own channel so suggestions match who the edited message will
    // ultimately notify.
    const editMentions = useMentionAutocomplete({
        token,
        channelID: post.channel_id,
        value: draft,
        setValue: setDraft,
        textareaRef: editRef,
    });
    // Phase 19 — draft auto-save for the inline edit textarea. Key is
    // scoped per user + post so two tabs editing the same post share a
    // draft; switching the editing toggle off without saving leaves the
    // draft intact so re-opening the editor restores it. The key is null
    // while the editor is closed so we don't accidentally rehydrate on
    // mount before the user clicks ✎.
    const editDraftKey = editing && currentUserId
        ? `moddle:draft:edit:${currentUserId}:${post.id}`
        : null;
    const editDraft = useDraft(editDraftKey, draft, setDraft);
    const grouped = useMemo(() => {
        const m = {};
        reactions.forEach((r) => { (m[r.emoji_name] ||= []).push(r); });
        return m;
    }, [reactions]);
    const edited = post.update_at > post.create_at;
    return (_jsxs("div", { className: `msg ${isMe ? "msg-me" : ""} ${compact ? "msg-compact" : ""}`, children: [_jsxs("div", { className: "msg-meta", children: [_jsx(Avatar, { id: post.user_id, name: author?.username ?? "", status: status, size: 20, picture: author?.picture, updateAt: author?.update_at }), _jsx("span", { className: "msg-author", children: author?.username ?? (isMe ? "나" : post.user_id.slice(0, 8)) }), _jsx("time", { className: "msg-time", children: formatTime(post.create_at) }), edited && _jsx("span", { className: "msg-edited", children: "(\uD3B8\uC9D1\uB428)" }), post.is_pinned && _jsx("span", { className: "msg-pinned", children: "\uD83D\uDCCC" }), channelLabel && (_jsxs("button", { type: "button", className: "msg-channel-chip", onClick: onJumpToChannel, title: "\uC774 \uCC44\uB110\uB85C \uC774\uB3D9", children: ["#", channelLabel] }))] }), editing ? (_jsxs("form", { onSubmit: (e) => {
                    e.preventDefault();
                    if (draft.trim()) {
                        onEdit(post.id, draft.trim());
                        editDraft.clearSaved();
                        setEditing(false);
                    }
                }, children: [_jsxs("div", { className: "mention-picker-host", children: [_jsx("textarea", { ref: editRef, className: "composer-input", value: draft, onChange: (e) => {
                                    setDraft(e.target.value);
                                    editMentions.onChange(e);
                                }, rows: 2, autoFocus: true, onKeyDown: (e) => {
                                    if (editMentions.handleKeyDown(e))
                                        return;
                                    // Phase 20 (F8) — Escape discards the edit and also clears
                                    // any auto-saved edit draft in localStorage so re-opening
                                    // the editor starts fresh instead of rehydrating the
                                    // abandoned garbage.
                                    if (e.key === "Escape") {
                                        editDraft.clearSaved();
                                        setEditing(false);
                                        setDraft(post.message);
                                    }
                                } }), editMentions.render()] }), _jsxs("div", { style: { display: "flex", gap: 8, marginTop: 6 }, children: [_jsx("button", { type: "submit", className: "btn-primary", style: { width: "auto", height: 32, padding: "0 12px" }, children: "\uC800\uC7A5" }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", height: 32, padding: "0 12px" }, onClick: () => {
                                    // Phase 20 (F8) — same as Escape: drop the auto-saved draft
                                    // when the user explicitly cancels the edit.
                                    editDraft.clearSaved();
                                    setEditing(false);
                                    setDraft(post.message);
                                }, children: "\uCDE8\uC18C" })] })] })) : (_jsxs(_Fragment, { children: [post.message && (_jsx(MessageBody, { source: post.message, token: token, linkMetadata: post.link_metadata })), files.length > 0 && (_jsx("div", { className: "msg-files", children: files.map((f) => _jsx(FileChip, { file: f, token: token }, f.id)) }))] })), Object.keys(grouped).length > 0 && (_jsx("div", { className: "reactions", children: Object.entries(grouped).map(([emoji, rs]) => {
                    const mine = rs.some((r) => r.user_id === currentUserId);
                    // Custom emoji lookup: if the name matches a loaded custom
                    // emoji, render its image inline; otherwise fall through to
                    // the built-in short-code → unicode map.
                    const custom = customEmojiByName(emoji);
                    return (_jsxs("button", { type: "button", className: `reaction-chip ${mine ? "reaction-mine" : ""}`, onClick: () => onToggleReaction(emoji), title: rs.map((r) => r.user_id).join(", "), children: [custom ? (_jsx("img", { className: "emoji-img", src: api.emojiImageURL(token, custom.id), alt: emoji })) : (_jsx("span", { children: emojiChar(emoji) })), _jsx("span", { className: "reaction-count", children: rs.length })] }, emoji));
                }) })), !editing && !compact && (_jsxs("div", { className: "msg-actions", children: [_jsx("button", { type: "button", className: "action-btn", onClick: () => setPickerOpen((v) => !v), title: "\uB9AC\uC561\uC158", children: "\uD83D\uDE0A" }), !hideThreadAction && onOpenThread && (_jsx("button", { type: "button", className: "action-btn", onClick: () => onOpenThread(post.root_id || post.id), title: "\uC2A4\uB808\uB4DC \uC5F4\uAE30", children: "\uD83D\uDCAC" })), onToggleSaved && (_jsx("button", { type: "button", className: `action-btn ${isSaved ? "action-saved" : ""}`, onClick: onToggleSaved, title: isSaved ? "저장 해제" : "저장", children: isSaved ? "★" : "☆" })), onRemindMe && (_jsx("button", { type: "button", className: "action-btn", onClick: onRemindMe, title: "\uB098\uC911\uC5D0 \uC54C\uB9BC", children: "\uD83D\uDD14" })), isMe && _jsx("button", { type: "button", className: "action-btn", onClick: () => setEditing(true), title: "\uD3B8\uC9D1", children: "\u270E" }), isMe && _jsx("button", { type: "button", className: "action-btn", onClick: () => onDelete(post.id), title: "\uC0AD\uC81C", children: "\uD83D\uDDD1" }), pickerOpen && (_jsx(EmojiPicker, { token: token, quick: QUICK_EMOJIS, onPick: (name) => { onToggleReaction(name); setPickerOpen(false); }, onClose: () => setPickerOpen(false) }))] })), !editing && compact && onToggleSaved && (_jsx("div", { className: "msg-actions", style: { opacity: 1 }, children: _jsx("button", { type: "button", className: `action-btn ${isSaved ? "action-saved" : ""}`, onClick: onToggleSaved, title: isSaved ? "저장 해제" : "저장", children: isSaved ? "★" : "☆" }) }))] }));
}
function FileChip({ file, token }) {
    const [lightbox, setLightbox] = useState(false);
    const href = api.fileDownloadURL(token, file.id);
    const isImage = file.mime_type?.startsWith("image/");
    if (isImage) {
        // Prefer the server-generated thumbnail when one exists. When the
        // upload is still being processed (has_thumbnail=false), fall back to
        // the full-size image — correct, just slower. Both cases open the
        // full-res lightbox on click.
        const thumbURL = file.has_thumbnail
            ? api.fileThumbnailURL(token, file.id)
            : href;
        return (_jsxs(_Fragment, { children: [_jsx("button", { type: "button", className: "file-image", onClick: () => setLightbox(true), "aria-label": `이미지 확대: ${file.name}`, children: _jsx("img", { src: thumbURL, alt: file.name, loading: "lazy" }) }), lightbox && (_jsx(Lightbox, { src: href, alt: file.name, onClose: () => setLightbox(false) }))] }));
    }
    return (_jsxs("a", { className: "file-chip", href: href, target: "_blank", rel: "noreferrer", download: file.name, children: [_jsx("span", { className: "file-icon", children: "\uD83D\uDCCE" }), _jsx("span", { className: "file-name", children: file.name }), _jsx("span", { className: "file-size", children: humanSize(file.size) })] }));
}
function Composer({ token, channelID, onSend, onTyping, onUpload, onSchedule, userId, rootId, resetSeq }) {
    const [value, setValue] = useState("");
    const [pending, setPending] = useState([]);
    const [uploading, setUploading] = useState(false);
    const fileInputRef = useRef(null);
    const textareaRef = useRef(null);
    const typingAtRef = useRef(0);
    const mentions = useMentionAutocomplete({
        token,
        channelID,
        value,
        setValue,
        textareaRef,
    });
    // Phase 19 — draft auto-save. Key namespaces per user/channel/root so
    // concurrent drafts in different channels or threads don't clobber each
    // other. Null key disables the hook entirely (no per-channel context).
    const draftKey = userId && channelID
        ? `moddle:draft:${userId}:${channelID}:${rootId || "root"}`
        : null;
    const draft = useDraft(draftKey, value, setValue);
    function submit() {
        const trimmed = value.trim();
        if (!trimmed && pending.length === 0)
            return;
        onSend(trimmed, pending.map((f) => f.id));
        setValue("");
        setPending([]);
        draft.clearSaved();
    }
    // Phase 20 (F3) — parent-driven reset. Fires when the parent bumps
    // `resetSeq` (e.g. right after a successful schedule confirm). Skipped
    // on first mount so an incoming rehydrated draft survives the initial
    // render — only responds to *changes* after that.
    const resetSeqRef = useRef(resetSeq);
    useEffect(() => {
        if (resetSeqRef.current === resetSeq)
            return;
        resetSeqRef.current = resetSeq;
        setValue("");
        setPending([]);
        draft.clearSaved();
    }, [resetSeq, draft]);
    // Phase 20 (F5) — auto-focus the textarea when the channel changes so
    // the user can immediately type after clicking a channel in the
    // sidebar. Skipped on the very first mount to avoid yanking focus from
    // whatever else the app may be doing at startup (e.g. modal, login).
    const prevChannelRef = useRef(channelID);
    useEffect(() => {
        if (prevChannelRef.current === channelID)
            return;
        prevChannelRef.current = channelID;
        if (channelID)
            textareaRef.current?.focus();
    }, [channelID]);
    async function onFilesSelected(files) {
        if (!files || files.length === 0)
            return;
        setUploading(true);
        const uploaded = await onUpload(Array.from(files));
        setPending((prev) => [...prev, ...uploaded]);
        setUploading(false);
        if (fileInputRef.current)
            fileInputRef.current.value = "";
    }
    function notifyTyping() {
        const now = Date.now();
        if (now - typingAtRef.current > 1500) {
            typingAtRef.current = now;
            onTyping();
        }
    }
    return (_jsxs("form", { className: "composer", onSubmit: (e) => { e.preventDefault(); submit(); }, children: [_jsxs("div", { style: { flex: 1, display: "flex", flexDirection: "column", gap: 6 }, children: [pending.length > 0 && (_jsx("div", { className: "msg-files", style: { marginBottom: 0 }, children: pending.map((f) => (_jsxs("div", { className: "file-chip", children: [_jsx("span", { className: "file-icon", children: "\uD83D\uDCCE" }), _jsx("span", { className: "file-name", children: f.name }), _jsx("button", { type: "button", className: "action-btn", onClick: () => setPending((prev) => prev.filter((x) => x.id !== f.id)), children: "\u2715" })] }, f.id))) })), _jsxs("div", { style: { display: "flex", gap: 8 }, children: [_jsx("button", { type: "button", className: "btn-ghost", style: { width: 40, height: 40, padding: 0, flex: "0 0 auto" }, onClick: () => fileInputRef.current?.click(), title: "\uD30C\uC77C \uCCA8\uBD80", children: "\uD83D\uDCCE" }), onSchedule && (_jsx("button", { type: "button", className: "btn-ghost", style: { width: 40, height: 40, padding: 0, flex: "0 0 auto" }, onClick: () => onSchedule(value, pending.map((f) => f.id)), title: "\uBA54\uC2DC\uC9C0 \uC608\uC57D \uC804\uC1A1", children: "\uD83D\uDD50" })), _jsx("input", { ref: fileInputRef, type: "file", multiple: true, style: { display: "none" }, onChange: (e) => onFilesSelected(e.target.files) }), _jsxs("div", { className: "mention-picker-host", style: { flex: 1, display: "flex" }, children: [_jsx("textarea", { ref: textareaRef, className: "composer-input", rows: 1, placeholder: uploading ? "업로드 중…" : "메시지를 입력하세요… (Shift+Enter 줄바꿈)", value: value, onChange: (e) => {
                                            setValue(e.target.value);
                                            mentions.onChange(e);
                                            notifyTyping();
                                        }, onKeyDown: (e) => {
                                            // Give the mention picker first crack at arrow/Enter/Tab/Escape
                                            // keys when it's open; otherwise fall through to submit-on-Enter.
                                            if (mentions.handleKeyDown(e))
                                                return;
                                            if (e.key === "Enter" && !e.shiftKey) {
                                                e.preventDefault();
                                                submit();
                                            }
                                        } }), mentions.render()] }), _jsx("button", { type: "submit", className: "btn-primary", style: { width: 88, height: 40 }, children: "\uC804\uC1A1" })] }), draft.hasSaved && (_jsxs("div", { className: "draft-badge", children: [_jsx("span", { children: "\uCD08\uC548 \uC800\uC7A5\uB428" }), _jsx("button", { type: "button", className: "draft-clear", onClick: draft.clear, title: "\uC800\uC7A5\uB41C \uCD08\uC548 \uC9C0\uC6B0\uAE30", children: "\uC9C0\uC6B0\uAE30" })] }))] }), _jsx("input", { type: "hidden", value: token })] }));
}
function TypingIndicator({ typingUsers, users }) {
    if (typingUsers.length === 0)
        return null;
    const names = typingUsers.map((uid) => users[uid]?.username ?? uid.slice(0, 6)).slice(0, 3);
    const label = names.length === 1
        ? `${names[0]}님이 입력 중…`
        : names.length <= 3
            ? `${names.join(", ")}님이 입력 중…`
            : "여러 명이 입력 중…";
    return _jsx("div", { className: "typing-indicator", children: label });
}
function ThreadPanel(props) {
    const { rootId, posts, loading, users, statuses, reactionsByPost, filesByID, currentUserId, token, onToggleReaction, onEdit, onDelete, onReply, onUpload, onSchedule, composerResetSeq, onClose, } = props;
    const root = posts.find((p) => p.id === rootId) ?? null;
    const replies = posts.filter((p) => p.id !== rootId);
    return (_jsxs("aside", { className: "thread-panel", children: [_jsxs("header", { className: "thread-header", children: [_jsx("strong", { children: "\uC2A4\uB808\uB4DC" }), _jsx("button", { type: "button", className: "action-btn", onClick: onClose, title: "\uB2EB\uAE30", children: "\u2715" })] }), _jsx("div", { className: "thread-body", children: loading && posts.length === 0 ? (_jsx("div", { className: "chat-empty", children: "\uBD88\uB7EC\uC624\uB294 \uC911\u2026" })) : !root ? (_jsx("div", { className: "chat-empty", children: "\uC6D0\uBCF8 \uBA54\uC2DC\uC9C0\uB97C \uCC3E\uC744 \uC218 \uC5C6\uC2B5\uB2C8\uB2E4." })) : (_jsxs(_Fragment, { children: [_jsx(MessageRow, { post: root, isMe: root.user_id === currentUserId, author: users[root.user_id], status: statuses[root.user_id], reactions: reactionsByPost[root.id] ?? [], currentUserId: currentUserId, files: (root.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean), token: token, onToggleReaction: (emoji) => onToggleReaction(root, emoji), onEdit: onEdit, onDelete: onDelete, hideThreadAction: true }), _jsxs("div", { className: "thread-divider", children: ["\uB2F5\uAE00 ", replies.length, "\uAC1C"] }), replies.map((p) => (_jsx(MessageRow, { post: p, isMe: p.user_id === currentUserId, author: users[p.user_id], status: statuses[p.user_id], reactions: reactionsByPost[p.id] ?? [], currentUserId: currentUserId, files: (p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean), token: token, onToggleReaction: (emoji) => onToggleReaction(p, emoji), onEdit: onEdit, onDelete: onDelete, hideThreadAction: true }, p.id)))] })) }), _jsx(Composer, { token: token, 
                // Thread replies belong to the root post's channel; fall back to
                // null if the root hasn't loaded yet so the autocomplete hook
                // stays dormant instead of querying an empty channelID.
                channelID: root?.channel_id ?? null, onSend: onReply, onTyping: () => { }, onUpload: onUpload, userId: currentUserId, rootId: rootId, onSchedule: onSchedule, resetSeq: composerResetSeq })] }));
}
function SavedPostsView(props) {
    const { posts, users, statuses, reactionsByPost, filesByID, currentUserId, token, channels, loading, onClose, onReload, onToggleReaction, onEdit, onDelete, onOpenThread, isSaved, onToggleSaved, onJumpToChannel, } = props;
    return (_jsxs(_Fragment, { children: [_jsxs("header", { className: "chat-header", children: [_jsx("div", { className: "chat-header-left", children: _jsx("h2", { className: "chat-header-title", children: "\u2B50 \uC800\uC7A5\uB428" }) }), _jsxs("div", { style: { display: "flex", gap: 6 }, children: [_jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 12px", height: 32 }, onClick: onReload, title: "\uBAA9\uB85D \uC0C8\uB85C\uACE0\uCE68", children: "\u21BB" }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 12px", height: 32 }, onClick: onClose, children: "\uB2EB\uAE30" })] })] }), _jsx("div", { className: "chat-messages", children: loading ? (_jsx("div", { className: "chat-empty", children: "\uBD88\uB7EC\uC624\uB294 \uC911\u2026" })) : posts.length === 0 ? (_jsx("div", { className: "chat-empty", children: "\uC800\uC7A5\uB41C \uBA54\uC2DC\uC9C0\uAC00 \uC5C6\uC2B5\uB2C8\uB2E4. \uBA54\uC2DC\uC9C0 \uC704\uC5D0 \uB9C8\uC6B0\uC2A4\uB97C \uC62C\uB824 \u2606 \uBC84\uD2BC\uC744 \uB20C\uB7EC\uBCF4\uC138\uC694." })) : (posts.map((p) => (_jsx(MessageRow, { post: p, isMe: p.user_id === currentUserId, author: users[p.user_id], status: statuses[p.user_id], reactions: reactionsByPost[p.id] ?? [], currentUserId: currentUserId, files: (p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean), token: token, onToggleReaction: (emoji) => onToggleReaction(p, emoji), onEdit: onEdit, onDelete: onDelete, onOpenThread: onOpenThread, isSaved: isSaved(p.id), onToggleSaved: () => onToggleSaved(p), compact: true, channelLabel: channels.find((c) => c.id === p.channel_id)?.display_name, onJumpToChannel: () => onJumpToChannel(p.channel_id) }, p.id)))) })] }));
}
function ChannelDiscoverModal({ token, teamId, onClose, onJoined }) {
    useEscClose(true, onClose);
    const [q, setQ] = useState("");
    const [rows, setRows] = useState([]);
    const [loading, setLoading] = useState(false);
    const [offset, setOffset] = useState(0);
    const [hasMore, setHasMore] = useState(false);
    const [err, setErr] = useState(null);
    const [joining, setJoining] = useState(null);
    const doFetch = useCallback(async (reset) => {
        setLoading(true);
        setErr(null);
        try {
            const nextOffset = reset ? 0 : offset;
            const list = await api.discoverChannels(token, teamId, {
                q: q.trim(),
                limit: 20,
                offset: nextOffset,
            });
            if (reset)
                setRows(list);
            else
                setRows((prev) => [...prev, ...list]);
            setHasMore((list?.length ?? 0) >= 20);
            setOffset(nextOffset + (list?.length ?? 0));
        }
        catch (e) {
            setErr(e instanceof Error ? e.message : "채널 탐색 실패");
        }
        finally {
            setLoading(false);
        }
    }, [token, teamId, q, offset]);
    // Debounce the initial + query-change load so typing doesn't spam the API.
    useEffect(() => {
        const t = setTimeout(() => { doFetch(true); }, q ? 180 : 0);
        return () => clearTimeout(t);
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [q]);
    async function onJoin(ch) {
        if (joining)
            return;
        setJoining(ch.id);
        try {
            await api.joinChannel(token, ch.id);
            setRows((prev) => prev.filter((c) => c.id !== ch.id));
            onJoined(ch.id);
        }
        catch (e) {
            setErr(e instanceof Error ? e.message : "채널 참여 실패");
        }
        finally {
            setJoining(null);
        }
    }
    return (_jsx("div", { className: "modal-backdrop", onClick: onClose, children: _jsxs("div", { className: "modal-card channel-discover-modal", onClick: (e) => e.stopPropagation(), children: [_jsx("h3", { style: { margin: "0 0 12px" }, children: "\uD83D\uDD0D \uCC44\uB110 \uD0D0\uC0C9" }), _jsx("input", { className: "field-input", autoFocus: true, placeholder: "\uC774\uB984 \uB610\uB294 \uD45C\uC2DC \uC774\uB984\uC73C\uB85C \uAC80\uC0C9", value: q, onChange: (e) => setQ(e.target.value), style: { marginBottom: 10 } }), err && _jsx("div", { className: "login-error", children: err }), _jsxs("div", { className: "discover-list", children: [rows.length === 0 && !loading && (_jsx("div", { className: "chat-empty", style: { padding: 16 }, children: q ? "검색 결과가 없습니다." : "참여할 수 있는 공개 채널이 없습니다." })), rows.map((c) => (_jsxs("div", { className: "discover-row", children: [_jsxs("div", { className: "discover-row-main", children: [_jsxs("div", { className: "discover-row-title", children: [_jsx("span", { className: "channel-hash", children: "#" }), c.display_name] }), _jsx("div", { className: "discover-row-name", children: c.name }), c.purpose && _jsx("div", { className: "discover-row-purpose", children: c.purpose })] }), _jsx("button", { type: "button", className: "btn-primary", style: { width: "auto", padding: "0 14px", height: 32 }, disabled: joining === c.id, onClick: () => onJoin(c), children: joining === c.id ? "참여 중…" : "참여" })] }, c.id)))] }), _jsxs("div", { style: { display: "flex", justifyContent: "space-between", marginTop: 10 }, children: [_jsx("button", { type: "button", className: "btn-ghost", onClick: onClose, children: "\uB2EB\uAE30" }), hasMore && (_jsx("button", { type: "button", className: "btn-ghost", disabled: loading, onClick: () => doFetch(false), children: loading ? "불러오는 중…" : "더 보기" }))] })] }) }));
}
function StartDirectModal({ token, currentUserId, onClose, onPick, }) {
    useEscClose(true, onClose);
    const [q, setQ] = useState("");
    const [results, setResults] = useState([]);
    useEffect(() => {
        const t = setTimeout(async () => {
            try {
                if (q.trim()) {
                    const list = await api.searchUsers(token, q.trim(), 20);
                    setResults(list.filter((u) => u.id !== currentUserId));
                }
                else {
                    const list = await api.listUsers(token, 0, 20);
                    setResults(list.filter((u) => u.id !== currentUserId));
                }
            }
            catch { /* ignore */ }
        }, 200);
        return () => clearTimeout(t);
    }, [q, token, currentUserId]);
    return (_jsx("div", { className: "modal-backdrop", onClick: onClose, children: _jsxs("div", { className: "modal-card", onClick: (e) => e.stopPropagation(), children: [_jsx("h3", { style: { margin: "0 0 12px" }, children: "\uC0C8 \uB2E4\uC774\uB809\uD2B8 \uBA54\uC2DC\uC9C0" }), _jsx("input", { className: "field-input", autoFocus: true, placeholder: "\uC0AC\uC6A9\uC790 \uAC80\uC0C9\u2026", value: q, onChange: (e) => setQ(e.target.value) }), _jsx("div", { className: "user-picker", children: results.length === 0 ? (_jsx("div", { className: "chat-empty", style: { padding: 16 }, children: "\uACB0\uACFC \uC5C6\uC74C" })) : results.map((u) => (_jsxs("button", { className: "item", onClick: () => onPick(u.id), children: [_jsx(Avatar, { id: u.id, name: u.username, size: 22, picture: u.picture, updateAt: u.update_at }), _jsx("span", { style: { marginLeft: 2 }, children: u.username }), _jsx("span", { style: { color: "var(--muted)", fontSize: 12, marginLeft: "auto" }, children: u.email })] }, u.id))) }), _jsx("div", { style: { display: "flex", justifyContent: "flex-end", marginTop: 12 }, children: _jsx("button", { className: "btn-ghost", style: { width: "auto", padding: "0 14px", height: 34 }, onClick: onClose, children: "\uB2EB\uAE30" }) })] }) }));
}
function ChannelSettingsMenu({ props, onChange, }) {
    const [open, setOpen] = useState(false);
    const wrapRef = useRef(null);
    // Close on outside click. Mounting the listener only while open keeps
    // the global listener cost zero in the common case.
    useEffect(() => {
        if (!open)
            return;
        function onDoc(e) {
            if (!wrapRef.current)
                return;
            if (!wrapRef.current.contains(e.target))
                setOpen(false);
        }
        document.addEventListener("mousedown", onDoc);
        return () => document.removeEventListener("mousedown", onDoc);
    }, [open]);
    const desktop = (props.desktop ?? "all");
    const markUnread = (props.mark_unread ?? "all");
    return (_jsxs("span", { className: "settings-wrap", ref: wrapRef, children: [_jsx("button", { type: "button", className: "settings-gear", title: "\uCC44\uB110 \uC54C\uB9BC \uC124\uC815", "aria-label": "\uCC44\uB110 \uC54C\uB9BC \uC124\uC815", onClick: () => setOpen((v) => !v), children: "\u2699" }), open && (_jsxs("div", { className: "notify-menu", role: "dialog", "aria-label": "\uC54C\uB9BC \uC124\uC815", children: [_jsx("div", { className: "notify-section-title", children: "\uB370\uC2A4\uD06C\uD1B1 \uC54C\uB9BC" }), ["all", "mentions", "none"].map((v) => (_jsxs("label", { className: "notify-radio", children: [_jsx("input", { type: "radio", name: "desktop", checked: desktop === v, onChange: () => onChange({ desktop: v }) }), _jsx("span", { children: v === "all" ? "모든 새 메시지" :
                                    v === "mentions" ? "@멘션 또는 DM만" :
                                        "끄기" })] }, v))), _jsx("div", { className: "notify-section-title", style: { marginTop: 10 }, children: "\uC77D\uC9C0 \uC54A\uC74C \uD45C\uC2DC" }), ["all", "mention"].map((v) => (_jsxs("label", { className: "notify-radio", children: [_jsx("input", { type: "radio", name: "mark_unread", checked: markUnread === v, onChange: () => onChange({ mark_unread: v }) }), _jsx("span", { children: v === "all" ? "모든 메시지" : "멘션만 (음소거)" })] }, v)))] }))] }));
}
// ---- Phase 19: useDraft (localStorage auto-save) ----
//
// Debounced 500ms auto-save of a controlled textarea value to localStorage.
// Rehydrates on mount if a saved draft exists. Null key disables the hook
// so callers can safely pass `null` when they lack context (e.g. no user
// logged in, no channel focused). `clear` both wipes storage and resets
// the controlled value; `clearSaved` only wipes storage (used on successful
// send where the textarea is being cleared anyway).
function useDraft(key, value, setValue) {
    const [hasSaved, setHasSaved] = useState(false);
    // Rehydrate on mount / key change. Skipped while the key is null to
    // avoid accidentally overwriting a fresh compose with stale state.
    useEffect(() => {
        if (!key) {
            setHasSaved(false);
            return;
        }
        try {
            const saved = localStorage.getItem(key);
            if (saved) {
                setValue(saved);
                setHasSaved(true);
            }
            else {
                setHasSaved(false);
            }
        }
        catch { /* ignore — localStorage may be disabled */ }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [key]);
    // Debounced write. Empty value clears the entry so localStorage doesn't
    // fill up with stale keys after the user wipes their draft manually.
    useEffect(() => {
        if (!key)
            return;
        const t = setTimeout(() => {
            try {
                if (value.trim()) {
                    localStorage.setItem(key, value);
                    setHasSaved(true);
                }
                else {
                    localStorage.removeItem(key);
                    setHasSaved(false);
                }
            }
            catch { /* ignore */ }
        }, 500);
        return () => clearTimeout(t);
    }, [key, value]);
    function clearSaved() {
        if (!key)
            return;
        try {
            localStorage.removeItem(key);
        }
        catch { /* ignore */ }
        setHasSaved(false);
    }
    function clear() {
        clearSaved();
        setValue("");
    }
    return { hasSaved, clear, clearSaved };
}
function ScheduledPostsView(props) {
    const { items, channels, loading, onClose, onReload, onCancel, onJumpToChannel } = props;
    const sorted = [...items].sort((a, b) => a.send_at - b.send_at);
    return (_jsxs(_Fragment, { children: [_jsxs("header", { className: "chat-header", children: [_jsx("div", { className: "chat-header-left", children: _jsx("h2", { className: "chat-header-title", children: "\uD83D\uDD50 \uC608\uC57D\uB428" }) }), _jsxs("div", { style: { display: "flex", gap: 6 }, children: [_jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 12px", height: 32 }, onClick: onReload, title: "\uBAA9\uB85D \uC0C8\uB85C\uACE0\uCE68", children: "\u21BB" }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 12px", height: 32 }, onClick: onClose, children: "\uB2EB\uAE30" })] })] }), _jsx("div", { className: "chat-messages", children: loading ? (_jsx("div", { className: "chat-empty", children: "\uBD88\uB7EC\uC624\uB294 \uC911\u2026" })) : sorted.length === 0 ? (_jsx("div", { className: "chat-empty", children: "\uC608\uC57D\uB41C \uBA54\uC2DC\uC9C0\uAC00 \uC5C6\uC2B5\uB2C8\uB2E4. \uBA54\uC2DC\uC9C0 \uC785\uB825\uCC3D\uC758 \uD83D\uDD50 \uBC84\uD2BC\uC73C\uB85C \uC608\uC57D\uD560 \uC218 \uC788\uC2B5\uB2C8\uB2E4." })) : (sorted.map((s) => {
                    const ch = channels.find((c) => c.id === s.channel_id);
                    const when = new Date(s.send_at);
                    return (_jsxs("div", { className: "scheduled-row", children: [_jsxs("div", { className: "scheduled-row-head", children: [_jsxs("button", { type: "button", className: "msg-channel-chip", onClick: () => onJumpToChannel(s.channel_id), title: "\uC774 \uCC44\uB110\uB85C \uC774\uB3D9", children: ["#", ch?.display_name ?? s.channel_id.slice(0, 8)] }), _jsx("time", { className: "scheduled-row-time", children: when.toLocaleString() })] }), _jsx("div", { className: "scheduled-row-body", children: s.message }), s.error_text && (_jsxs("div", { className: "scheduled-row-error", children: ["\uC804\uC1A1 \uC2E4\uD328: ", s.error_text] })), _jsx("div", { className: "scheduled-row-actions", children: _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 12px", height: 28 }, onClick: () => onCancel(s.id), children: "\uCDE8\uC18C" }) })] }, s.id));
                })) })] }));
}
function ScheduleModal({ channelName, messagePreview, onCancel, onConfirm }) {
    useEscClose(true, onCancel);
    const [custom, setCustom] = useState(() => {
        // Seed the datetime-local with now + 15 min rounded so the first
        // interaction is "just submit" — avoids the empty-input trap.
        const d = new Date(Date.now() + 15 * 60 * 1000);
        d.setSeconds(0, 0);
        // datetime-local expects YYYY-MM-DDTHH:mm in local time
        const pad = (n) => n.toString().padStart(2, "0");
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    });
    const [err, setErr] = useState(null);
    const [busy, setBusy] = useState(false);
    // Preset helpers. "Tomorrow 9am" / "Next Monday 9am" are computed off
    // the user's local clock — the server stores absolute epoch-ms so tz
    // drift between client and server doesn't matter for correctness.
    function tomorrow9am() {
        const d = new Date();
        d.setDate(d.getDate() + 1);
        d.setHours(9, 0, 0, 0);
        return d.getTime();
    }
    function nextMonday9am() {
        const d = new Date();
        // 0 = Sunday, 1 = Monday, ... Add 1..7 days to land on next Monday.
        const delta = ((1 - d.getDay() + 7) % 7) || 7;
        d.setDate(d.getDate() + delta);
        d.setHours(9, 0, 0, 0);
        return d.getTime();
    }
    function hoursFromNow(h) {
        return Date.now() + h * 3600_000;
    }
    async function send(target) {
        if (busy)
            return;
        if (target <= Date.now() - 30_000) {
            setErr("미래 시각을 선택하세요.");
            return;
        }
        setBusy(true);
        setErr(null);
        const ok = await onConfirm(target);
        if (!ok) {
            setErr("예약 생성에 실패했습니다. 잠시 후 다시 시도하세요.");
            setBusy(false);
        }
        // On success the parent unmounts the modal; no local state to reset.
    }
    function onCustomSubmit() {
        const t = new Date(custom).getTime();
        if (Number.isNaN(t)) {
            setErr("올바른 날짜/시간을 입력하세요.");
            return;
        }
        send(t);
    }
    return (_jsx("div", { className: "modal-backdrop", onClick: busy ? undefined : onCancel, children: _jsxs("div", { className: "modal-card schedule-modal", onClick: (e) => e.stopPropagation(), children: [_jsx("h3", { style: { margin: "0 0 8px" }, children: "\uD83D\uDD50 \uBA54\uC2DC\uC9C0 \uC608\uC57D" }), _jsxs("div", { className: "schedule-target", children: [_jsx("span", { className: "channel-hash", children: "#" }), channelName || "채널"] }), _jsx("div", { className: "schedule-preview", children: messagePreview }), _jsxs("div", { className: "schedule-presets", children: [_jsx("button", { type: "button", className: "btn-ghost", disabled: busy, onClick: () => send(hoursFromNow(1)), children: "1\uC2DC\uAC04 \uD6C4" }), _jsx("button", { type: "button", className: "btn-ghost", disabled: busy, onClick: () => send(tomorrow9am()), children: "\uB0B4\uC77C \uC624\uC804 9\uC2DC" }), _jsx("button", { type: "button", className: "btn-ghost", disabled: busy, onClick: () => send(nextMonday9am()), children: "\uB2E4\uC74C \uC8FC \uC6D4\uC694\uC77C \uC624\uC804 9\uC2DC" })] }), _jsxs("div", { className: "schedule-custom", children: [_jsx("label", { children: "\uC0AC\uC6A9\uC790 \uC9C0\uC815" }), _jsx("input", { type: "datetime-local", className: "field-input", value: custom, onChange: (e) => setCustom(e.target.value) }), _jsx("button", { type: "button", className: "btn-primary", style: { width: "auto", padding: "0 14px", height: 36 }, onClick: onCustomSubmit, disabled: busy, children: busy ? "예약 중…" : "예약" })] }), err && _jsx("div", { className: "login-error", children: err }), _jsx("div", { style: { display: "flex", justifyContent: "flex-end", marginTop: 10 }, children: _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 14px", height: 34 }, onClick: onCancel, disabled: busy, children: "\uB2EB\uAE30" }) })] }) }));
}
function ReminderPopover({ postId, onCancel, onConfirm }) {
    useEscClose(true, onCancel);
    const [custom, setCustom] = useState(() => {
        const d = new Date(Date.now() + 60 * 60 * 1000);
        d.setSeconds(0, 0);
        const pad = (n) => n.toString().padStart(2, "0");
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    });
    const [err, setErr] = useState(null);
    const [busy, setBusy] = useState(false);
    function minutesFromNow(m) {
        return Date.now() + m * 60_000;
    }
    function tomorrow9am() {
        const d = new Date();
        d.setDate(d.getDate() + 1);
        d.setHours(9, 0, 0, 0);
        return d.getTime();
    }
    function nextMonday9am() {
        const d = new Date();
        const delta = ((1 - d.getDay() + 7) % 7) || 7;
        d.setDate(d.getDate() + delta);
        d.setHours(9, 0, 0, 0);
        return d.getTime();
    }
    async function send(target) {
        if (busy)
            return;
        if (target <= Date.now() - 30_000) {
            setErr("미래 시각을 선택하세요.");
            return;
        }
        setBusy(true);
        setErr(null);
        const ok = await onConfirm(postId, target);
        if (!ok) {
            setErr("리마인더 생성에 실패했습니다.");
            setBusy(false);
        }
    }
    function onCustomSubmit() {
        const t = new Date(custom).getTime();
        if (Number.isNaN(t)) {
            setErr("올바른 날짜/시간을 입력하세요.");
            return;
        }
        send(t);
    }
    return (_jsx("div", { className: "modal-backdrop", onClick: busy ? undefined : onCancel, children: _jsxs("div", { className: "modal-card reminder-popover", onClick: (e) => e.stopPropagation(), children: [_jsx("h3", { style: { margin: "0 0 10px" }, children: "\uD83D\uDD14 \uB9AC\uB9C8\uC778\uB354 \uC124\uC815" }), _jsxs("div", { className: "schedule-presets", children: [_jsx("button", { type: "button", className: "btn-ghost", disabled: busy, onClick: () => send(minutesFromNow(30)), children: "30\uBD84 \uD6C4" }), _jsx("button", { type: "button", className: "btn-ghost", disabled: busy, onClick: () => send(minutesFromNow(60)), children: "1\uC2DC\uAC04 \uD6C4" }), _jsx("button", { type: "button", className: "btn-ghost", disabled: busy, onClick: () => send(tomorrow9am()), children: "\uB0B4\uC77C \uC624\uC804 9\uC2DC" }), _jsx("button", { type: "button", className: "btn-ghost", disabled: busy, onClick: () => send(nextMonday9am()), children: "\uB2E4\uC74C \uC8FC \uC6D4\uC694\uC77C \uC624\uC804 9\uC2DC" })] }), _jsxs("div", { className: "schedule-custom", children: [_jsx("label", { children: "\uC0AC\uC6A9\uC790 \uC9C0\uC815" }), _jsx("input", { type: "datetime-local", className: "field-input", value: custom, onChange: (e) => setCustom(e.target.value) }), _jsx("button", { type: "button", className: "btn-primary", style: { width: "auto", padding: "0 14px", height: 36 }, onClick: onCustomSubmit, disabled: busy, children: busy ? "설정 중…" : "설정" })] }), err && _jsx("div", { className: "login-error", children: err }), _jsx("div", { style: { display: "flex", justifyContent: "flex-end", marginTop: 10 }, children: _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 14px", height: 34 }, onClick: onCancel, disabled: busy, children: "\uB2EB\uAE30" }) })] }) }));
}
// ---- Helpers ----
function slug(s) {
    return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40) || `x-${Date.now()}`;
}
function formatTime(ms) {
    const d = new Date(ms);
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
function color(id) {
    const palette = ["#6366f1", "#8b5cf6", "#ec4899", "#f59e0b", "#10b981", "#06b6d4"];
    let h = 0;
    for (let i = 0; i < id.length; i++)
        h = (h * 31 + id.charCodeAt(i)) >>> 0;
    return palette[h % palette.length];
}
function humanSize(bytes) {
    if (!bytes)
        return "";
    const units = ["B", "KB", "MB", "GB"];
    let n = bytes;
    let i = 0;
    while (n >= 1024 && i < units.length - 1) {
        n /= 1024;
        i++;
    }
    return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)}${units[i]}`;
}
// `posted` event delivers `mentions` as a JSON-encoded string of user IDs
// (server-side convention to keep the envelope flat). Parse defensively.
function parseMentionIDs(raw) {
    if (!raw)
        return [];
    if (Array.isArray(raw))
        return raw.map(String);
    if (typeof raw === "string") {
        try {
            const parsed = JSON.parse(raw);
            if (Array.isArray(parsed))
                return parsed.map(String);
        }
        catch { /* ignore */ }
    }
    return [];
}
// DM channels are named "<sortedIdA>__<sortedIdB>"; return the other participant.
function dmCounterpart(name, me) {
    const [a, b] = name.split("__");
    if (!b)
        return a ?? "";
    if (a === me)
        return b;
    return a;
}
// Map a subset of emoji names to characters for the quick palette.
const EMOJI_MAP = {
    "+1": "👍",
    "-1": "👎",
    heart: "❤️",
    tada: "🎉",
    laughing: "😄",
    eyes: "👀",
    rocket: "🚀",
    fire: "🔥",
    clap: "👏",
    check: "✅",
};
function emojiChar(name) {
    return EMOJI_MAP[name] ?? `:${name}:`;
}
// Phase 16 — session management drawer. Lists the user's live sessions
// (IP-ish device_id + expiry) with revoke buttons per-row and a "kill all
// other devices" catch-all at the bottom. The current row is tagged by
// the server via `is_current` (matches the JWT behind the request); we
// don't ship the bearer token to the client for comparison.
function SessionManagerModal({ sessions, loading, onRevoke, onRevokeOthers, onClose, }) {
    useEscClose(true, onClose);
    const others = sessions.filter((s) => !s.is_current).length;
    return (_jsx("div", { className: "modal-backdrop", onClick: onClose, children: _jsxs("div", { className: "modal-card", onClick: (e) => e.stopPropagation(), style: { maxWidth: 520 }, children: [_jsxs("header", { className: "integrations-header", children: [_jsx("h3", { style: { margin: 0 }, children: "\uC138\uC158 \uAD00\uB9AC" }), _jsx("button", { type: "button", className: "action-btn", onClick: onClose, title: "\uB2EB\uAE30", children: "\u2715" })] }), _jsx("div", { style: { padding: "4px 0 10px", color: "var(--muted)", fontSize: 12 }, children: "\uC774 \uACC4\uC815\uC73C\uB85C \uB85C\uADF8\uC778\uD55C \uBAA8\uB4E0 \uAE30\uAE30\uC758 \uC138\uC158\uC785\uB2C8\uB2E4. \uC758\uC2EC\uC2A4\uB7EC\uC6B4 \uC138\uC158\uC774 \uC788\uB2E4\uBA74 \uC989\uC2DC \uC885\uB8CC\uD558\uC138\uC694." }), loading ? (_jsx("div", { className: "chat-empty", style: { padding: 14 }, children: "\uBD88\uB7EC\uC624\uB294 \uC911\u2026" })) : sessions.length === 0 ? (_jsx("div", { className: "chat-empty", style: { padding: 14 }, children: "\uD65C\uC131 \uC138\uC158\uC774 \uC5C6\uC2B5\uB2C8\uB2E4." })) : (_jsx("ul", { className: "integrations-list", children: sessions.map((s) => (_jsxs("li", { className: "integrations-row", children: [_jsxs("div", { style: { flex: 1, minWidth: 0 }, children: [_jsx("div", { style: { fontWeight: 600 }, children: s.is_current ? "이 기기" : (s.device_id || "알 수 없는 기기") }), _jsxs("div", { style: { color: "var(--muted)", fontSize: 12 }, children: ["\uC0DD\uC131 ", new Date(s.create_at).toLocaleString(), " · 만료 ", new Date(s.expires_at).toLocaleString()] })] }), _jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }, onClick: () => onRevoke(s.id), children: "\uC885\uB8CC" })] }, s.id))) })), _jsx("div", { style: { marginTop: 12, display: "flex", justifyContent: "flex-end" }, children: _jsxs("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 14px", height: 36, color: "var(--danger)" }, onClick: onRevokeOthers, disabled: others === 0, title: others === 0 ? "다른 기기 세션이 없습니다" : "", children: ["\uB2E4\uB978 \uBAA8\uB4E0 \uAE30\uAE30 \uB85C\uADF8\uC544\uC6C3", others > 0 ? ` (${others})` : ""] }) })] }) }));
}
