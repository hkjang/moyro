import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import type { RootState } from "@/store";
import { clearAuth, setAuth } from "@/store/authSlice";
import {
  api,
  compatApi,
  customProfileApi,
  notifyApi,
  prefsApi,
  sidebarApi,
  type Channel,
  type ChannelNotifyProps,
  type ChannelStats,
  type CustomProfileField,
  type CustomProfileValues,
  type FileInfo,
  type OrderedSidebarCategories,
  type Post,
  type PostList,
  type Preference,
  type Reaction,
  type SessionRow,
  type SidebarCategory,
  type Team,
  type User,
  type UserNotifyProps,
  type UserStatusValue,
} from "@/api/client";
import { EmojiPicker, customEmojiByName } from "@/components/EmojiPicker";
import { AuthenticatedImage, downloadAuthenticatedMedia, isExternalImageURL } from "@/components/AuthenticatedMedia";
import { Lightbox } from "@/components/Lightbox";
import { MessageBody } from "@/components/MessageBody";
import { useMentionAutocomplete } from "@/components/MentionPicker";
import { BrandMark } from "@/components/brand/BrandMark";
import { useEscClose, useConfirm } from "@/components/shared";
import { useWebsocket } from "@/hooks/useWebsocket";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";

type UnreadEntry = { msg: number; mention: number };

// Quick reaction palette. The server accepts any emoji name; these are
// the one-click buttons exposed in the UI.
const QUICK_EMOJIS = ["+1", "heart", "tada", "laughing", "eyes", "rocket"];

type UsersMap = Record<string, User>;
type StatusMap = Record<string, UserStatusValue>;
type ReactionMap = Record<string, Reaction[]>; // postID -> reactions
type FilesMap = Record<string, FileInfo>;     // fileID -> info

export function ChatView() {
  const user = useSelector((s: RootState) => s.auth.user);
  const token = useSelector((s: RootState) => s.auth.token);
  const dispatch = useDispatch();
  const navigate = useNavigate();
  const location = useLocation();
  const { teamId: routeTeamId, channelId: routeChannelId, view: routeView } = useParams<{
    teamId?: string;
    channelId?: string;
    view?: string;
  }>();
  const systemInfo = useSystemInfo();
  const adminAccess = useAdminAccess();
  const applyingRouteRef = useRef(true);
  // Phase 20 — shared confirm dialog in place of native window.confirm.
  // render() is spilled into the chat-shell div at the bottom so its
  // z-index stacks above every other modal.
  const confirmer = useConfirm();

  const [teams, setTeams] = useState<Team[]>([]);
  const [currentTeamId, setCurrentTeamId] = useState<string | null>(routeTeamId ?? null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [currentChannelId, setCurrentChannelId] = useState<string | null>(routeChannelId ?? null);
  const [posts, setPosts] = useState<Post[]>([]);
  const [loadingPosts, setLoadingPosts] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [users, setUsers] = useState<UsersMap>({});
  const [statuses, setStatuses] = useState<StatusMap>({});
  const [reactionsByPost, setReactionsByPost] = useState<ReactionMap>({});
  const [filesByID, setFilesByID] = useState<FilesMap>({});

  const [typingUsers, setTypingUsers] = useState<Record<string, number>>({});
  const [unread, setUnread] = useState<Record<string, UnreadEntry>>({});
  // Per-channel notify_props (loaded lazily when the settings menu opens
  // or when a channel's member row is hydrated at login). Desktop pref
  // from the WS `unread_updated` event is also folded in so we can make
  // notification decisions without a round-trip.
  const [channelNotify, setChannelNotify] = useState<Record<string, ChannelNotifyProps>>({});
  const channelNotifyRef = useRef<Record<string, ChannelNotifyProps>>({});
  useEffect(() => { channelNotifyRef.current = channelNotify; }, [channelNotify]);
  const [searchTerm, setSearchTerm] = useState("");
  const [searchResults, setSearchResults] = useState<Post[] | null>(null);
  const [showStartDM, setShowStartDM] = useState(false);

  // Phase 16 — session-management modal. We lazy-fetch the list when the
  // modal opens; the list is short-lived and stale data (e.g. a session
  // that just expired) would just 404 on the revoke call which we handle.
  const [showSessions, setShowSessions] = useState(false);
  const [sessions, setSessions] = useState<SessionRow[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);

  // Phase 16 — archived channel visibility toggle. Off by default so the
  // sidebar stays lean. When on we re-fetch channels with include_deleted
  // so soft-deleted channels appear dimmed in the sidebar.
  const [showArchived, setShowArchived] = useState(false);

  // Phase 16 — archived-channel list (only populated while showArchived is
  // true). Kept separate from `channels` so the main list stays focused on
  // active rows; rendering merges the two below.
  const [archivedChannels, setArchivedChannels] = useState<Channel[]>([]);
  const [myStatus, setMyStatus] = useState<UserStatusValue>("online");
  // Profile picture upload — ref hits the hidden <input type="file">, flag
  // disables the button while the multipart upload is in flight so a second
  // click can't stack a duplicate request.
  const avatarFileRef = useRef<HTMLInputElement | null>(null);
  const [uploadingAvatar, setUploadingAvatar] = useState(false);

  // Phase 17 — email digest opt-in. Loaded once at mount; the toggle updates
  // optimistically and rolls back on server error. `null` = not yet loaded,
  // which disables the checkbox briefly on first paint.
  const [digestEnabled, setDigestEnabled] = useState<boolean | null>(null);

  // Phase 18 — saved-posts set of post ids. Hydrated lazily per channel
  // render via `savedPostsByIds` and patched on WS `saved_post_changed`.
  // A plain Set keeps the MessageRow render O(1) per post.
  const [savedIds, setSavedIds] = useState<Set<string>>(new Set());
  // When true the main pane renders the 저장됨 pseudo-channel view
  // (separate list of bookmarked posts across all channels).
  const [savedView, setSavedView] = useState(routeView === "saved");
  const [savedPosts, setSavedPosts] = useState<Post[]>([]);
  const [savedLoading, setSavedLoading] = useState(false);

  // Phase 18 — 채널 탐색 modal toggle. Lists public channels not yet
  // joined so users can discover them without an admin invite.
  const [showDiscover, setShowDiscover] = useState(false);

  // Phase 19 — scheduled messages. `scheduledList` is the cached pending
  // queue for the sidebar 예약됨 pseudo-channel; invalidated on create/
  // delete/send WS events. `scheduledView` flips the main pane into the
  // list layout; `scheduleModalFor` remembers which channel the compose-
  // then-schedule action came from so the modal persists the target.
  const [scheduledView, setScheduledView] = useState(routeView === "scheduled");
  const [scheduledList, setScheduledList] = useState<import("@/api/client").ScheduledPost[]>([]);
  const [scheduledLoading, setScheduledLoading] = useState(false);
  const [scheduleModalFor, setScheduleModalFor] = useState<
    | null
    | {
        channelId: string;
        message: string;
        fileIds: string[];
        // Phase 20 (F7) — when the 🕐 is clicked inside a thread, we persist
        // the rootId so the scheduled post is routed back to the same
        // thread at send time. Undefined for root-pane composes.
        rootId?: string;
        // Phase 20 (F3) — which composer to reset after successful schedule.
        // "root" clears the main-pane composer; "thread" clears the
        // ThreadPanel's reply composer. Avoids wiping the wrong textarea
        // when the user schedules from one surface while the other has
        // unrelated in-flight text.
        source: "root" | "thread";
      }
  >(null);
  // Phase 20 (F3) — bump-to-reset counters per composer surface. Passed
  // into <Composer resetSeq=… />; the Composer only reacts to *changes*,
  // so initial-mount rehydrate is preserved.
  const [rootComposerResetSeq, setRootComposerResetSeq] = useState(0);
  const [threadComposerResetSeq, setThreadComposerResetSeq] = useState(0);

  // Phase 19 — post reminders. Popover anchored to a MessageRow via post id;
  // only one open at a time so we render a single overlay. `reminderToasts`
  // is a short stack of incoming reminder_fired WS events; each entry is
  // auto-dismissed by a per-id timer but can also be clicked to jump.
  const [reminderForPostId, setReminderForPostId] = useState<string | null>(null);
  type ReminderToast = {
    id: string;
    postId: string;
    channelId: string;
    excerpt: string;
  };
  const [reminderToasts, setReminderToasts] = useState<ReminderToast[]>([]);

  const selectTeam = useCallback((teamId: string) => {
    setCurrentTeamId(teamId);
    setCurrentChannelId(null);
    setSavedView(false);
    setScheduledView(false);
    navigate(`/workspace/${encodeURIComponent(teamId)}`);
  }, [navigate]);

  const selectChannel = useCallback((channelId: string) => {
    if (!currentTeamId) return;
    setSavedView(false);
    setScheduledView(false);
    setCurrentChannelId(channelId);
    navigate(`/workspace/${encodeURIComponent(currentTeamId)}/channel/${encodeURIComponent(channelId)}`);
  }, [currentTeamId, navigate]);

  // The URL is the durable navigation state. Browser back/forward and a full
  // refresh restore the selected team, channel, or pseudo-channel view.
  useEffect(() => {
    if (routeTeamId && routeView && routeView !== "saved" && routeView !== "scheduled") {
      navigate(`/workspace/${encodeURIComponent(routeTeamId)}`, { replace: true });
      return;
    }
    applyingRouteRef.current = true;
    if (routeTeamId) setCurrentTeamId(routeTeamId);
    if (routeChannelId) setCurrentChannelId(routeChannelId);
    else if (routeTeamId && routeView !== "saved" && routeView !== "scheduled") setCurrentChannelId(null);
    setSavedView(routeView === "saved");
    setScheduledView(routeView === "scheduled");
  }, [routeTeamId, routeChannelId, routeView, navigate]);

  // Internal events can also change the active entity. Canonicalize those
  // changes back into a route without adding noisy history entries.
  useEffect(() => {
    if (applyingRouteRef.current) {
      applyingRouteRef.current = false;
      return;
    }
    if (!currentTeamId) return;
    const teamPath = `/workspace/${encodeURIComponent(currentTeamId)}`;
    const target = savedView
      ? `${teamPath}/saved`
      : scheduledView
        ? `${teamPath}/scheduled`
        : currentChannelId
          ? `${teamPath}/channel/${encodeURIComponent(currentChannelId)}`
          : teamPath;
    if (location.pathname !== target) navigate(target, { replace: true });
  }, [currentTeamId, currentChannelId, savedView, scheduledView, location.pathname, navigate]);

  // Phase 21 — Mattermost-shaped preferences. Theme value is one of
  // "light" | "dark" | "system"; default is "system" (follows the OS prefers-
  // color-scheme media query). Stored in preferences.category=display_settings,
  // name=theme so any official client points at the same row.
  type ThemeChoice = "light" | "dark" | "system";
  const [theme, setThemeState] = useState<ThemeChoice>(() => {
    // Hydrate from localStorage so first paint matches the user's last
    // saved theme — the prefs API call later in the mount cycle reconciles
    // if the server has a different value.
    const cached = typeof window !== "undefined"
      ? window.localStorage.getItem("moyro:theme") || ""
      : "";
    return cached === "light" || cached === "dark" || cached === "system" ? cached : "system";
  });
  // Apply the theme to <html data-theme=…> so CSS variables can branch.
  useEffect(() => {
    const root = document.documentElement;
    const apply = () => {
      let resolved: "light" | "dark" = "light";
      if (theme === "system") {
        resolved = window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
      } else {
        resolved = theme;
      }
      root.setAttribute("data-theme", resolved);
    };
    apply();
    if (theme === "system" && window.matchMedia) {
      const mq = window.matchMedia("(prefers-color-scheme: dark)");
      mq.addEventListener("change", apply);
      return () => mq.removeEventListener("change", apply);
    }
    return undefined;
  }, [theme]);
  // Cache the choice locally so the next first-paint is correct without
  // waiting on the network. Server is still source-of-truth.
  useEffect(() => {
    try { window.localStorage.setItem("moyro:theme", theme); } catch { /* ignore */ }
  }, [theme]);
  // Pull the canonical theme from preferences once at login.
  useEffect(() => {
    if (!token || !user?.id) return;
    let cancelled = false;
    prefsApi
      .listCategory(token, "display_settings", user.id)
      .then((prefs) => {
        if (cancelled) return;
        const t = prefs.find((p) => p.name === "theme")?.value;
        if (t === "light" || t === "dark" || t === "system") setThemeState(t);
      })
      .catch(() => { /* server may not have prefs yet — leave local default */ });
    return () => { cancelled = true; };
  }, [token, user?.id]);
  const setTheme = useCallback(async (next: ThemeChoice) => {
    setThemeState(next);
    if (!token || !user?.id) return;
    const pref: Preference = {
      user_id: user.id,
      category: "display_settings",
      name: "theme",
      value: next,
    };
    try { await prefsApi.upsert(token, [pref], user.id); } catch { /* keep local choice */ }
  }, [token, user?.id]);

  // Phase 21 — Quick Switcher (Cmd+K / Ctrl+K). Mattermost's keyboard-first
  // navigation surface. Combines channel autocomplete + user autocomplete so
  // a user can jump to a channel or open a DM in one shortcut.
  const [showQuickSwitcher, setShowQuickSwitcher] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && (e.key === "k" || e.key === "K")) {
        // Skip if the user is mid-text-input ⇢ we'd kidnap their typing.
        // …no, actually the whole point is to grab focus from anywhere.
        e.preventDefault();
        setShowQuickSwitcher(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Phase 21 — channel stats (member/pinned/files counts). Fetched lazily
  // when the active channel changes; a ChannelStats object is cached per id
  // so back-and-forth navigation doesn't re-fetch. The chip in the header
  // renders only when the count is known.
  const [channelStatsByID, setChannelStatsByID] = useState<Record<string, ChannelStats>>({});
  useEffect(() => {
    if (!token || !currentChannelId) return;
    if (channelStatsByID[currentChannelId]) return;
    let cancelled = false;
    compatApi
      .channelStats(token, currentChannelId)
      .then((s) => {
        if (cancelled) return;
        setChannelStatsByID((prev) => ({ ...prev, [currentChannelId]: s }));
      })
      .catch(() => { /* non-fatal — chip just stays hidden */ });
    return () => { cancelled = true; };
    // channelStatsByID intentionally not in deps: cache lookup is the guard.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, currentChannelId]);

  // Phase 22 — Mattermost sidebar categories. Loaded per (user, team) and
  // refreshed on team switch + WS-driven reload events. The categories
  // drive sidebar grouping (즐겨찾기/채널/DM/사용자 정의) and ordering. We
  // never fall back to the flat-render path while categories are loading —
  // the sidebar just shows a skeleton row to avoid a sort flicker.
  const [sidebarCats, setSidebarCats] = useState<OrderedSidebarCategories | null>(null);
  const [showNotifyPrefs, setShowNotifyPrefs] = useState(false);
  // Phase 33 — custom profile attributes drawer. Admin-defined fields with
  // per-user values. Lazily opened from the sidebar footer.
  const [showProfileFields, setShowProfileFields] = useState(false);
  // Phase 34 — Mattermost-v11-style user menu. Avatar trigger lives in the
  // chat-header right; clicking opens a dropdown with all account/profile
  // controls (avatar upload, theme, notify, profile fields, sessions, email
  // digest, status, logout). The hamburger to the left of the brand is a
  // direct opener for the admin "운영 관리" panel — only rendered for
  // admins, single click skips a dropdown that would only have one item.
  const [showUserMenu, setShowUserMenu] = useState(false);

  // Reload categories whenever the team changes. Server auto-bootstraps the
  // three defaults on first call, so a freshly-joined team renders correctly
  // without any client-side seeding.
  useEffect(() => {
    if (!token || !currentTeamId) {
      setSidebarCats(null);
      return;
    }
    let cancelled = false;
    sidebarApi
      .list(token, currentTeamId)
      .then((res) => { if (!cancelled) setSidebarCats(res); })
      .catch(() => { if (!cancelled) setSidebarCats(null); });
    return () => { cancelled = true; };
  }, [token, currentTeamId]);

  // Channel-id → favorite? lookup, derived from the favorites category. The
  // memo lets the sidebar render in O(1) per channel during the map phase.
  const favoriteChannelIds = useMemo(() => {
    const set = new Set<string>();
    if (!sidebarCats) return set;
    const fav = sidebarCats.categories.find((c) => c.type === "favorites");
    if (fav) for (const id of fav.channel_ids) set.add(id);
    return set;
  }, [sidebarCats]);

  // Toggle favorite: adds/removes the channel from the favorites category.
  // We optimistically patch sidebarCats then call the bulk-update endpoint;
  // a failed call rolls back via the next list reload.
  const onToggleFavorite = useCallback(async (channelId: string) => {
    if (!token || !currentTeamId || !sidebarCats) return;
    const fav = sidebarCats.categories.find((c) => c.type === "favorites");
    if (!fav) return;
    const isFav = fav.channel_ids.includes(channelId);
    const nextFavIds = isFav
      ? fav.channel_ids.filter((id) => id !== channelId)
      : [...fav.channel_ids, channelId];
    const patched: SidebarCategory = { ...fav, channel_ids: nextFavIds };
    setSidebarCats({
      ...sidebarCats,
      categories: sidebarCats.categories.map((c) => c.id === fav.id ? patched : c),
    });
    try {
      await sidebarApi.update(token, currentTeamId, patched);
    } catch {
      // Re-fetch on error so the UI doesn't drift from the server.
      try {
        const reloaded = await sidebarApi.list(token, currentTeamId);
        setSidebarCats(reloaded);
      } catch { /* swallowed — leave optimistic state */ }
    }
  }, [token, currentTeamId, sidebarCats]);

  // Phase 18 — search filters lifted out of the free-form terms. The
  // search input still accepts `from:` / `in:` / `before:` / `after:` /
  // `has:file` / `has:link`; we parse them into this state, re-emit the
  // plain terms in the query, and pass filters as explicit POST fields.
  const [searchFilters, setSearchFilters] = useState<import("@/api/client").SearchFilters>({});
  const [searchTotal, setSearchTotal] = useState<number>(0);
  const [searchPage, setSearchPage] = useState<number>(0);

  // The server returns effective RBAC permissions for the current session.
  // This keeps delegated administrators discoverable in the normal UI while
  // route and API authorization remain the enforcement boundary.
  const isAdmin = adminAccess.loaded && adminAccess.hasAdminAccess;

  // Thread sidebar: rootId is the live open thread, threadPosts is the
  // ordered list (oldest-first, root included) and threadLoading mirrors
  // the fetch state. Ref mirrors let the WS handler decide fast whether
  // an inbound post concerns the open thread without re-rendering on
  // every event.
  const [threadRootId, setThreadRootId] = useState<string | null>(null);
  const [threadPosts, setThreadPosts] = useState<Post[]>([]);
  const [threadLoading, setThreadLoading] = useState(false);
  const threadRootIdRef = useRef<string | null>(null);
  useEffect(() => { threadRootIdRef.current = threadRootId; }, [threadRootId]);

  const currentChannelIdRef = useRef<string | null>(null);
  useEffect(() => { currentChannelIdRef.current = currentChannelId; }, [currentChannelId]);

  // ---- Load teams ----
  useEffect(() => {
    if (!token) return;
    api.listTeams(token)
      .then((t) => {
        setTeams(t ?? []);
        setCurrentTeamId((prev) => prev && (t ?? []).some((team) => team.id === prev)
          ? prev
          : (t?.[0]?.id ?? null));
      })
      .catch((e) => setError(e.message));
  }, [token]);

  // ---- Load channels when team changes (also include DM channels) ----
  const loadChannels = useCallback(async () => {
    if (!token || !currentTeamId) return;
    try {
      const c = await api.listChannels(token, currentTeamId);
      setChannels(c ?? []);
      setCurrentChannelId((prev) => {
        if (prev && (c ?? []).some((x) => x.id === prev)) return prev;
        return (c ?? [])[0]?.id ?? null;
      });
      // Hydrate per-channel unread counts + notify_props in one shot so
      // badges survive reloads without a per-channel fetch storm.
      try {
        const members = await api.listMyChannelMembers(token, currentTeamId);
        const unreadNext: Record<string, UnreadEntry> = {};
        const notifyNext: Record<string, ChannelNotifyProps> = {};
        for (const m of members) {
          unreadNext[m.channel_id] = { msg: m.msg_count, mention: m.mention_count };
          if (m.notify_props) notifyNext[m.channel_id] = m.notify_props;
        }
        setUnread(unreadNext);
        setChannelNotify(notifyNext);
      } catch { /* ignore — badges will rebuild from WS events */ }
    } catch (e) {
      setError(e instanceof Error ? e.message : "채널 로드 실패");
    }
  }, [token, currentTeamId]);
  useEffect(() => { loadChannels(); }, [loadChannels]);

  // Ask for browser notification permission once per session. No-op if
  // the user has already decided. Stays best-effort — a denied permission
  // just means notifications silently don't fire.
  useEffect(() => {
    if (!("Notification" in window)) return;
    if (Notification.permission === "default") {
      Notification.requestPermission().catch(() => { /* ignore */ });
    }
  }, []);

  // Phase 17 — load email digest preference once per session. The server
  // defaults to `digest_enabled=true` so first-time users see the checkbox
  // ticked. A fetch error leaves the checkbox disabled rather than lying.
  useEffect(() => {
    if (!token) { setDigestEnabled(null); return; }
    api.getEmailPrefs(token)
      .then((p) => setDigestEnabled(!!p.digest_enabled))
      .catch(() => setDigestEnabled(null));
  }, [token]);

  // ---- Load posts (+ reactions + file infos) when channel changes ----
  useEffect(() => {
    if (!token || !currentChannelId) { setPosts([]); return; }
    setLoadingPosts(true);
    api.listPosts(token, currentChannelId)
      .then(async (list: PostList) => {
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
            .catch(() => { /* ignore */ });
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
                  if (isSaved) next.add(id); else next.delete(id);
                });
                return next;
              });
            })
            .catch(() => { /* ignore — star stays outline */ });
        }

        // Mark viewed to clear unread.
        api.viewChannel(token, currentChannelId).catch(() => undefined);
        setUnread((u) => ({ ...u, [currentChannelId]: { msg: 0, mention: 0 } }));
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoadingPosts(false));
  }, [token, currentChannelId]);

  async function hydrateUsers(ids: string[]) {
    if (!token) return;
    const missing = ids.filter((id) => id && !users[id]);
    if (missing.length === 0) return;
    const results = await Promise.all(
      missing.map((id) => api.getUser(token, id).catch(() => null)),
    );
    setUsers((prev) => {
      const next = { ...prev };
      results.forEach((u) => { if (u) next[u.id] = u; });
      return next;
    });
    try {
      const st = await api.getUserStatusesByIDs(token, missing);
      setStatuses((prev) => {
        const next = { ...prev };
        st.forEach((s) => { next[s.user_id] = s.status; });
        return next;
      });
    } catch { /* ignore */ }
  }

  async function hydrateFiles(ids: string[]) {
    if (!token) return;
    const missing = ids.filter((id) => id && !filesByID[id]);
    if (missing.length === 0) return;
    const results = await Promise.all(
      missing.map((id) => api.fileInfo(token, id).catch(() => null)),
    );
    setFilesByID((prev) => {
      const next = { ...prev };
      results.forEach((f) => { if (f) next[f.id] = f; });
      return next;
    });
  }

  // ---- Load my status once ----
  useEffect(() => {
    if (!token || !user) return;
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
  const handleWSMessage = useCallback((ev: MessageEvent) => {
    try {
      const payload = JSON.parse(ev.data as string);
      handleWSEvent(payload);
    } catch { /* ignore malformed frames */ }
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
    if (wsReconnectSeq === 0) return;
    if (!token) return;
    // (1) channels + unread + notify_props: loadChannels already does both.
    if (currentTeamId) {
      loadChannels();
    }
    // (2) posts in the currently open channel — merge by id so optimistic
    // edits / reactions we may have applied locally don't get clobbered.
    const chanID = currentChannelId;
    if (chanID) {
      api.listPosts(token, chanID).then((list: PostList) => {
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

  function handleWSEvent(payload: { event?: string; data?: Record<string, unknown> }) {
    const { event, data } = payload;
    if (!event || !data) return;
    switch (event) {
      case "posted": {
        const p: Post = JSON.parse(String(data.post ?? "{}"));
        if (!p.id) return;
        hydrateUsers([p.user_id]);
        hydrateFiles(p.file_ids ?? []);
        if (p.channel_id === currentChannelIdRef.current) {
          setPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
          api.viewChannel(token!, p.channel_id).catch(() => undefined);
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
        if (
          !authorIsMe &&
          !inFocus &&
          typeof Notification !== "undefined" &&
          Notification.permission === "granted" &&
          pref !== "none" &&
          (pref === "all" || mentionsMe || isDM)
        ) {
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
              setSavedView(false);
              setScheduledView(false);
              setCurrentChannelId(p.channel_id);
              n.close();
            };
          } catch { /* some browsers reject in background tabs — no-op */ }
        }
        return;
      }
      case "post_edited": {
        const p: Post = JSON.parse(String(data.post ?? "{}"));
        setPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
        setThreadPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
        return;
      }
      case "post_deleted": {
        const pid = String(data.post_id ?? "");
        setPosts((prev) => prev.filter((x) => x.id !== pid));
        setThreadPosts((prev) => prev.filter((x) => x.id !== pid));
        if (threadRootIdRef.current === pid) closeThread();
        return;
      }
      case "post_pinned":
      case "post_unpinned": {
        const p: Post = JSON.parse(String(data.post ?? "{}"));
        setPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
        setThreadPosts((prev) => prev.map((x) => x.id === p.id ? p : x));
        return;
      }
      case "reaction_added": {
        const r: Reaction = JSON.parse(String(data.reaction ?? "{}"));
        setReactionsByPost((prev) => {
          const cur = prev[r.post_id] ?? [];
          if (cur.some((x) => x.user_id === r.user_id && x.emoji_name === r.emoji_name)) return prev;
          return { ...prev, [r.post_id]: [...cur, r] };
        });
        return;
      }
      case "reaction_removed": {
        const r: Reaction = JSON.parse(String(data.reaction ?? "{}"));
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
        if (!ch || !uid || ch !== currentChannelIdRef.current) return;
        setTypingUsers((prev) => ({ ...prev, [uid]: Date.now() }));
        return;
      }
      case "status_change": {
        const uid = String(data.user_id ?? "");
        const st = String(data.status ?? "") as UserStatusValue;
        if (uid) setStatuses((prev) => ({ ...prev, [uid]: st }));
        return;
      }
      case "channel_viewed": {
        const ch = String(data.channel_id ?? "");
        if (ch) setUnread((u) => ({ ...u, [ch]: { msg: 0, mention: 0 } }));
        return;
      }
      case "unread_updated": {
        const ch = String(data.channel_id ?? "");
        if (!ch) return;
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
            if (cur.desktop === desktop) return prev;
            return { ...prev, [ch]: { ...cur, desktop } };
          });
        }
        return;
      }
      case "channel_updated": {
        const c: Channel = JSON.parse(String(data.channel ?? "{}"));
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
        if (!channelID) return;
        setChannels((prev) => {
          const next = prev.filter((c) => c.id !== channelID);
          setCurrentChannelId((cur) => {
            if (cur !== channelID) return cur;
            // Prefer a non-DM channel (sidebar main group) over a DM so
            // the viewer lands somewhere that "makes sense" after losing
            // the archived channel. Falls through to the first DM if
            // that's all that remains.
            const firstPublic = next.find((c) => c.type !== "D" && c.type !== "G");
            return firstPublic?.id ?? next[0]?.id ?? null;
          });
          return next;
        });
        if (showArchived) loadArchivedChannels();
        return;
      }
      case "channel_restored": {
        // Restored elsewhere — refresh the live list so it reappears. If
        // the archived panel is open, drop it from the archived list.
        loadChannels();
        if (showArchived) loadArchivedChannels();
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
        if (!postId) return;
        setSavedIds((prev) => {
          const next = new Set(prev);
          if (nowSaved) next.add(postId); else next.delete(postId);
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
            .catch(() => { /* ignore — next change will resync */ });
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
        if (!rid) return;
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
        const next: Record<string, number> = {};
        let changed = false;
        for (const [uid, ts] of Object.entries(prev)) {
          if (ts > cutoff) next[uid] = ts; else changed = true;
        }
        return changed ? next : prev;
      });
    }, 1500);
    return () => clearInterval(t);
  }, []);

  // ---- Derived ----
  const currentTeam = useMemo(
    () => teams.find((t) => t.id === currentTeamId) ?? null,
    [teams, currentTeamId],
  );
  const currentChannel = useMemo(
    () => channels.find((c) => c.id === currentChannelId) ?? null,
    [channels, currentChannelId],
  );
  const publicChannels = useMemo(() => channels.filter((c) => c.type !== "D"), [channels]);
  const dmChannels = useMemo(() => channels.filter((c) => c.type === "D"), [channels]);
  // Phase 22 — favorites cross both public and DM lists. Channels in the
  // favorites category get hoisted into a top section so they're a single
  // click away even when the user has dozens of channels.
  const favoriteChannels = useMemo(
    () => channels.filter((c) => favoriteChannelIds.has(c.id)),
    [channels, favoriteChannelIds],
  );
  const nonFavoritePublic = useMemo(
    () => publicChannels.filter((c) => !favoriteChannelIds.has(c.id)),
    [publicChannels, favoriteChannelIds],
  );
  const nonFavoriteDM = useMemo(
    () => dmChannels.filter((c) => !favoriteChannelIds.has(c.id)),
    [dmChannels, favoriteChannelIds],
  );

  // ---- Actions ----
  async function onCreateTeam() {
    if (!token) return;
    const display = prompt("팀 이름을 입력하세요 (표시용)");
    if (!display) return;
    try {
      const t = await api.createTeam(token, slug(display), display);
      setTeams((prev) => [...prev, t]);
      selectTeam(t.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : "팀 생성 실패");
    }
  }

  async function onCreateChannel() {
    if (!token || !currentTeamId) return;
    const display = prompt("채널 이름");
    if (!display) return;
    try {
      const c = await api.createChannel(token, currentTeamId, slug(display), display);
      setChannels((prev) => [...prev, c]);
      selectChannel(c.id);
    } catch (e) {
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
  async function onArchiveChannel(channelId: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "채널 보관",
      message: "이 채널을 보관 처리할까요? 멤버의 사이드바에서 사라집니다.",
      confirmLabel: "보관",
      destructive: true,
    });
    if (!ok) return;
    try {
      await api.archiveChannel(token, channelId);
      // Optimistic local drop; the server's WS broadcast will still fire
      // shortly after and becomes a no-op if we already removed the row.
      setChannels((prev) => prev.filter((c) => c.id !== channelId));
      if (currentChannelId === channelId) setCurrentChannelId(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "채널 보관 실패");
    }
  }

  async function onRestoreChannel(channelId: string) {
    if (!token) return;
    try {
      await api.restoreChannel(token, channelId);
      // The restore itself doesn't return the channel row, so refetch.
      loadChannels();
      if (showArchived) loadArchivedChannels();
    } catch (e) {
      setError(e instanceof Error ? e.message : "채널 복원 실패");
    }
  }

  // Fetch the archived-only slice. Re-runs whenever `showArchived` flips
  // on or the current team changes. We filter client-side to keep only
  // rows with a non-zero delete_at since `include_deleted=true` returns
  // both active and archived in one list.
  const loadArchivedChannels = useCallback(async () => {
    if (!token || !currentTeamId) return;
    try {
      const all = await api.listChannels(token, currentTeamId, true);
      setArchivedChannels((all ?? []).filter((c) => (c.delete_at ?? 0) > 0));
    } catch (e) {
      setError(e instanceof Error ? e.message : "보관 채널 로드 실패");
    }
  }, [token, currentTeamId]);

  useEffect(() => {
    if (!showArchived) { setArchivedChannels([]); return; }
    loadArchivedChannels();
  }, [showArchived, loadArchivedChannels]);

  // ---- Phase 16: session management ----
  const openSessionModal = useCallback(async () => {
    if (!token) return;
    setShowSessions(true);
    setSessionsLoading(true);
    try {
      const list = await api.listMySessions(token);
      // Newest first so the current-device row typically sits up top.
      list.sort((a, b) => b.create_at - a.create_at);
      setSessions(list);
    } catch (e) {
      setError(e instanceof Error ? e.message : "세션 조회 실패");
    } finally {
      setSessionsLoading(false);
    }
  }, [token]);

  async function onRevokeOneSession(sessionId: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "세션 종료",
      message: "이 세션을 종료할까요?",
      confirmLabel: "종료",
      destructive: true,
    });
    if (!ok) return;
    try {
      await api.revokeSession(token, sessionId);
      setSessions((prev) => prev.filter((s) => s.id !== sessionId));
      // If the user just killed their own current session, sign out
      // locally so the app lands on the login screen.
      const killedCurrent = sessions.find((s) => s.id === sessionId)?.is_current;
      if (killedCurrent) dispatch(clearAuth());
    } catch (e) {
      setError(e instanceof Error ? e.message : "세션 종료 실패");
    }
  }

  async function onRevokeOtherSessions() {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "다른 기기 로그아웃",
      message: "다른 모든 기기에서 로그아웃할까요? 이 기기의 세션은 유지됩니다.",
      confirmLabel: "로그아웃",
      destructive: true,
    });
    if (!ok) return;
    try {
      await api.revokeOtherSessions(token);
      setSessions((prev) => prev.filter((s) => s.is_current));
    } catch (e) {
      setError(e instanceof Error ? e.message : "다른 세션 종료 실패");
    }
  }

  // Slash-command output rendered as a transient banner above the composer.
  const [cmdNotice, setCmdNotice] = useState<string | null>(null);

  async function onSendPost(message: string, fileIds: string[]) {
    if (!token || !currentChannelId) return;
    const trimmed = message.trim();
    if (!trimmed && fileIds.length === 0) return;
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
      } catch (e) {
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
    } catch (e) {
      setError(e instanceof Error ? e.message : "전송 실패");
    }
  }

  async function onEditPost(postId: string, message: string) {
    if (!token) return;
    try {
      await api.updatePost(token, postId, message);
      // State refreshes via post_edited WS event; no local mutation needed.
    } catch (e) {
      setError(e instanceof Error ? e.message : "수정 실패");
    }
  }

  async function onDeletePost(postId: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "메시지 삭제",
      message: "이 메시지를 삭제할까요? 되돌릴 수 없습니다.",
      confirmLabel: "삭제",
      destructive: true,
    });
    if (!ok) return;
    try {
      await api.deletePost(token, postId);
    } catch (e) {
      setError(e instanceof Error ? e.message : "삭제 실패");
    }
  }

  // Phase 18 — saved posts toggle. Optimistic patch on the savedIds set;
  // server emits `saved_post_changed` which will re-reconcile. We skip
  // the optimistic update only when the server call errors.
  async function onToggleSaved(post: Post) {
    if (!token) return;
    const wasSaved = savedIds.has(post.id);
    setSavedIds((prev) => {
      const next = new Set(prev);
      if (wasSaved) next.delete(post.id); else next.add(post.id);
      return next;
    });
    try {
      if (wasSaved) await api.unsavePost(token, post.id);
      else await api.savePost(token, post.id);
    } catch (e) {
      // Roll back on error so the star reflects server truth.
      setSavedIds((prev) => {
        const next = new Set(prev);
        if (wasSaved) next.add(post.id); else next.delete(post.id);
        return next;
      });
      setError(e instanceof Error ? e.message : "저장 실패");
    }
  }

  // Phase 18 — load the 저장됨 pseudo-channel. Keeps its own list state
  // so switching between channels doesn't blow away the bookmarked list.
  const loadSavedPosts = useCallback(async () => {
    if (!token) return;
    setSavedLoading(true);
    try {
      const res = await api.listSavedPosts(token, 50, 0);
      const ordered = (res.order ?? []).map((id) => res.posts[id]).filter(Boolean);
      setSavedPosts(ordered);
      setSavedIds(new Set(ordered.map((p) => p.id)));
      hydrateUsers(Array.from(new Set(ordered.map((p) => p.user_id))));
    } catch (e) {
      setError(e instanceof Error ? e.message : "저장된 메시지 로드 실패");
    } finally {
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
    if (!token) return;
    setScheduledLoading(true);
    try {
      const list = await api.listMyScheduledPosts(token);
      setScheduledList(list ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : "예약 메시지 로드 실패");
    } finally {
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

  async function onCancelScheduled(id: string) {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "예약 취소",
      message: "예약된 메시지를 취소할까요?",
      confirmLabel: "취소",
      destructive: true,
    });
    if (!ok) return;
    try {
      await api.deleteScheduledPost(token, id);
      setScheduledList((prev) => prev.filter((s) => s.id !== id));
    } catch (e) {
      setError(e instanceof Error ? e.message : "예약 취소 실패");
    }
  }

  // Open the schedule modal from the Composer. Captures the current
  // compose state + active channel so the submission path has what it
  // needs without re-reading DOM. Phase 20 (F7): the thread composer
  // calls this with source="thread" + a rootId so the scheduled post
  // lands back in the same thread at send time.
  function onOpenScheduleModal(message: string, fileIds: string[]) {
    onOpenScheduleModalFor("root", message, fileIds, undefined);
  }
  function onOpenScheduleModalFromThread(rootId: string) {
    return (message: string, fileIds: string[]) =>
      onOpenScheduleModalFor("thread", message, fileIds, rootId);
  }
  function onOpenScheduleModalFor(
    source: "root" | "thread",
    message: string,
    fileIds: string[],
    rootId: string | undefined,
  ) {
    // For thread scheduling the channelId comes from the root post —
    // we look it up in the already-loaded thread post list so we don't
    // block the UI on a fetch.
    let channelId: string | null = currentChannelId;
    if (source === "thread" && rootId) {
      const root = threadPosts.find((p) => p.id === rootId) ?? posts.find((p) => p.id === rootId);
      channelId = root?.channel_id ?? currentChannelId;
    }
    if (!channelId) return;
    const trimmed = message.trim();
    if (!trimmed && fileIds.length === 0) {
      setError("메시지를 먼저 입력하세요.");
      return;
    }
    setScheduleModalFor({ channelId, message: trimmed, fileIds, rootId, source });
  }

  async function onConfirmSchedule(sendAt: number): Promise<boolean> {
    if (!token || !scheduleModalFor) return false;
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
      } else {
        setRootComposerResetSeq((n) => n + 1);
      }
      setScheduleModalFor(null);
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : "예약 실패");
      return false;
    }
  }

  // Phase 19 — create a reminder for the given post. `when` is the epoch-ms
  // target. Closes the popover on success; on error shows an inline error
  // and keeps the popover open so the user can retry a different time.
  async function onCreateReminder(postId: string, when: number): Promise<boolean> {
    if (!token) return false;
    try {
      await api.createPostReminder(token, postId, when);
      setReminderForPostId(null);
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : "리마인더 생성 실패");
      return false;
    }
  }

  function onJumpFromReminder(channelId: string) {
    if (!channelId) return;
    setSearchResults(null);
    selectChannel(channelId);
  }

  async function onToggleReaction(post: Post, emoji: string) {
    if (!token || !user) return;
    const existing = (reactionsByPost[post.id] ?? []).find(
      (r) => r.user_id === user.id && r.emoji_name === emoji,
    );
    try {
      if (existing) {
        await api.removeReaction(token, post.id, user.id, emoji);
      } else {
        await api.addReaction(token, post.id, user.id, emoji);
      }
      // WS events apply the change.
    } catch (e) {
      setError(e instanceof Error ? e.message : "리액션 실패");
    }
  }

  async function onUploadFiles(files: File[]): Promise<FileInfo[]> {
    if (!token || !currentChannelId) return [];
    try {
      const res = await api.uploadFiles(token, currentChannelId, files);
      setFilesByID((prev) => {
        const next = { ...prev };
        res.file_infos.forEach((fi) => { next[fi.id] = fi; });
        return next;
      });
      return res.file_infos;
    } catch (e) {
      setError(e instanceof Error ? e.message : "업로드 실패");
      return [];
    }
  }

  function sendTyping() {
    if (!currentChannelId) return;
    wsSend(JSON.stringify({
      seq: Date.now(),
      action: "user_typing",
      data: { channel_id: currentChannelId },
    }));
  }

  async function onStartDirect(otherId: string) {
    if (!token || !user) return;
    try {
      const c = await api.createDirectChannel(token, [user.id, otherId]);
      setChannels((prev) => prev.some((x) => x.id === c.id) ? prev : [...prev, c]);
      selectChannel(c.id);
      setShowStartDM(false);
    } catch (e) {
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
  function parseSearchFilters(raw: string): { terms: string; filters: import("@/api/client").SearchFilters } {
    const tokens = raw.split(/\s+/);
    const filters: import("@/api/client").SearchFilters = {};
    const residual: string[] = [];
    for (const tok of tokens) {
      if (!tok) continue;
      const mFrom = tok.match(/^from:(\S+)$/i);
      if (mFrom) {
        const uname = mFrom[1];
        const hit = Object.values(users).find((u) => u.username === uname);
        if (hit) { filters.from_user_id = hit.id; continue; }
        residual.push(tok);
        continue;
      }
      const mIn = tok.match(/^in:(\S+)$/i);
      if (mIn) {
        const cname = mIn[1];
        const ch = channels.find((c) => c.name === cname || c.display_name === cname);
        if (ch) { filters.in_channel_id = ch.id; continue; }
        residual.push(tok);
        continue;
      }
      const mAfter = tok.match(/^after:(\d{4}-\d{2}-\d{2})$/i);
      if (mAfter) {
        const t = Date.parse(mAfter[1]);
        if (!isNaN(t)) { filters.after = t; continue; }
      }
      const mBefore = tok.match(/^before:(\d{4}-\d{2}-\d{2})$/i);
      if (mBefore) {
        // Treat before: as "end of that day" so `before:2026-01-01` includes
        // posts from Jan 1 themselves — users rarely want strict "<midnight".
        const t = Date.parse(mBefore[1]);
        if (!isNaN(t)) { filters.before = t + 24 * 60 * 60 * 1000; continue; }
      }
      if (/^has:file$/i.test(tok)) { filters.has_file = true; continue; }
      if (/^has:link$/i.test(tok)) { filters.has_link = true; continue; }
      residual.push(tok);
    }
    return { terms: residual.join(" "), filters };
  }

  async function onSearch(page = 0) {
    if (!token || !currentTeamId) return;
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
    } catch (e) {
      setError(e instanceof Error ? e.message : "검색 실패");
    }
  }

  async function onChangeMyStatus(s: UserStatusValue) {
    if (!token) return;
    setMyStatus(s);
    try { await api.updateMyStatus(token, s, true); }
    catch (e) { setError(e instanceof Error ? e.message : "상태 변경 실패"); }
  }

  // ---- Per-channel notify props ----
  // Optimistically applies the change so the menu reflects the new state
  // immediately, then reverts on error. We persist the *full* props bag so
  // the server doesn't have to merge — easier to reason about.
  const onChangeNotify = useCallback(
    async (channelId: string, patch: Partial<ChannelNotifyProps>) => {
      if (!token) return;
      const cur = channelNotifyRef.current[channelId] ?? { desktop: "all", mark_unread: "all" };
      const next: ChannelNotifyProps = { ...cur, ...patch };
      setChannelNotify((prev) => ({ ...prev, [channelId]: next }));
      try {
        const saved = await api.setMyChannelNotifyProps(token, channelId, next);
        setChannelNotify((prev) => ({ ...prev, [channelId]: saved ?? next }));
      } catch (e) {
        setChannelNotify((prev) => ({ ...prev, [channelId]: cur }));
        setError(e instanceof Error ? e.message : "알림 설정 저장 실패");
      }
    },
    [token],
  );

  // ---- Thread actions ----
  const openThread = useCallback(async (rootId: string) => {
    if (!token) return;
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
          .catch(() => { /* ignore */ });
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : "스레드 로드 실패");
    } finally {
      setThreadLoading(false);
    }
  }, [token]);

  function closeThread() {
    setThreadRootId(null);
    setThreadPosts([]);
  }

  async function onReplyInThread(message: string, fileIds: string[]) {
    if (!token || !currentChannelId || !threadRootId) return;
    const trimmed = message.trim();
    if (!trimmed && fileIds.length === 0) return;
    try {
      const p = await api.createPost(token, currentChannelId, trimmed, threadRootId, fileIds);
      // Thread panel updates via the `posted` WS event; keep the reply
      // visible immediately in case our own broadcast arrives later.
      setThreadPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
    } catch (e) {
      setError(e instanceof Error ? e.message : "스레드 전송 실패");
    }
  }

  // ---- Render ----
  return (
    <div className="chat-shell">
      <aside className="chat-side">
        {/* Phase 34.5 — sidebar header. Mattermost-v11 keeps team identity
            tight in the top-left. For admins the hamburger to the left of
            the brand opens the operations panel directly (single click —
            no intermediate dropdown for a single destination); for regular
            users the brand sits flush left without a placeholder spacer
            that would otherwise look like a missing affordance. */}
        <div className={`side-brand ${isAdmin ? "" : "side-brand-no-burger"}`}>
          {isAdmin && (
            <button
              type="button"
              className="side-hamburger"
              aria-label="운영 관리 열기"
              title="운영 관리 · 시스템 · 플러그인 · 역할 · 작업"
              onClick={() => navigate("/admin/overview")}
            >
              <span /><span /><span />
            </button>
          )}
          <div className="side-brand-name">
            <BrandMark className="side-brand-logo" size={30} />
            <strong>moyro</strong>
          </div>
        </div>

        <SectionTitle>팀</SectionTitle>
        <div className="item-list">
          {teams.map((t) => (
            <button
              key={t.id}
              className={`item ${t.id === currentTeamId ? "item-active" : ""}`}
              onClick={() => selectTeam(t.id)}
            >
              <span className="item-badge" style={{ background: color(t.id) }}>
                {t.display_name[0]?.toUpperCase() ?? "?"}
              </span>
              {t.display_name}
            </button>
          ))}
          <button className="item item-muted" onClick={onCreateTeam}>＋ 새 팀</button>
        </div>

        {currentTeamId && (
          <>
            {/* Phase 18 — saved-posts pseudo-channel. Sits above the real
                channels so the star view is a single click from anywhere. */}
            <div className="item-list" style={{ marginBottom: 4 }}>
              <button
                type="button"
                className={`item ${savedView ? "item-active" : ""}`}
                onClick={openSavedView}
                title="북마크한 메시지 모아보기"
              >
                ⭐ 저장됨
              </button>
              {/* Phase 19 — scheduled pseudo-channel. Count badge mirrors
                  the saved-view treatment so the sidebar rhythm stays
                  consistent; a zero count renders a plain label. */}
              <button
                type="button"
                className={`item ${scheduledView ? "item-active" : ""}`}
                onClick={openScheduledView}
                title="예약된 메시지"
                style={{ display: "flex", alignItems: "center", gap: 6 }}
              >
                <span style={{ flex: 1 }}>🕐 예약됨</span>
                {scheduledList.length > 0 && (
                  <span className="unread-badge" aria-label={`예약 ${scheduledList.length}건`}>
                    {scheduledList.length}
                  </span>
                )}
              </button>
            </div>
            {/* Phase 22 — favorites section. Renders only when at least one
                channel is starred so the sidebar doesn't grow an empty
                header on a fresh team. The star toggle on every row in
                the channel/DM lists below feeds this section. */}
            {favoriteChannels.length > 0 && (
              <>
                <SectionTitle>⭐ 즐겨찾기</SectionTitle>
                <div className="item-list">
                  {favoriteChannels.map((c) => {
                    if (c.type === "D") {
                      const otherId = dmCounterpart(c.name, user?.id ?? "");
                      const u = users[otherId];
                      const ue = unread[c.id] ?? { msg: 0, mention: 0 };
                      return (
                        <button
                          key={c.id}
                          className={`item ${!savedView && !scheduledView && c.id === currentChannelId ? "item-active" : ""}`}
                          onClick={() => selectChannel(c.id)}
                        >
                          <Avatar token={token} id={otherId} name={u?.username ?? otherId.slice(0, 8)} status={statuses[otherId]} size={22} picture={u?.picture} updateAt={u?.update_at} />
                          <span style={{ marginLeft: 2, flex: 1, textAlign: "left" }}>{u?.username ?? otherId.slice(0, 8)}</span>
                          <span
                            role="button"
                            className="channel-fav is-fav"
                            title="즐겨찾기 해제"
                            onClick={(e) => { e.stopPropagation(); onToggleFavorite(c.id); }}
                            onMouseDown={(e) => e.stopPropagation()}
                          >★</span>
                          {ue.mention > 0
                            ? <span className="mention-badge">{ue.mention}</span>
                            : ue.msg > 0
                              ? <span className="unread">{ue.msg}</span>
                              : null}
                        </button>
                      );
                    }
                    return (
                      <ChannelRow
                        key={c.id}
                        channel={c}
                        active={!savedView && !scheduledView && c.id === currentChannelId}
                        unread={unread[c.id] ?? { msg: 0, mention: 0 }}
                        onClick={() => selectChannel(c.id)}
                        isFavorite
                        onToggleFavorite={onToggleFavorite}
                      />
                    );
                  })}
                </div>
              </>
            )}
            <SectionTitle>채널</SectionTitle>
            <div className="item-list">
              {nonFavoritePublic.map((c) => (
                <ChannelRow
                  key={c.id}
                  channel={c}
                  active={!savedView && !scheduledView && c.id === currentChannelId}
                  unread={unread[c.id] ?? { msg: 0, mention: 0 }}
                  onClick={() => selectChannel(c.id)}
                  isFavorite={false}
                  onToggleFavorite={onToggleFavorite}
                />
              ))}
              <button className="item item-muted" onClick={onCreateChannel}>＋ 새 채널</button>
              {/* Phase 18 — 채널 탐색 opens a modal listing public channels
                  the user hasn't joined yet. Distinct from "새 채널" which
                  creates one from scratch. */}
              <button
                className="item item-muted"
                onClick={() => setShowDiscover(true)}
                title="가입 가능한 공개 채널 찾아보기"
              >
                🔍 채널 탐색
              </button>
              {/* Phase 16 — archived channel visibility. Off by default;
                  flipping it on triggers a separate include_deleted fetch
                  (see loadArchivedChannels effect). Admin-only restore is
                  gated in-row below. */}
              <button
                className="item item-muted"
                onClick={() => setShowArchived((v) => !v)}
                title="보관된 채널 표시/숨김"
              >
                {showArchived ? "▴ 보관된 채널 숨기기" : "▾ 보관된 채널 보기"}
              </button>
              {showArchived && archivedChannels.map((c) => (
                <div
                  key={c.id}
                  className="item"
                  style={{ opacity: 0.55, display: "flex", alignItems: "center", gap: 6 }}
                >
                  <span style={{ flex: 1, fontStyle: "italic" }}>
                    # {c.display_name}
                  </span>
                  {isAdmin && (
                    <button
                      type="button"
                      className="action-btn"
                      title="복원"
                      onClick={() => onRestoreChannel(c.id)}
                    >↺</button>
                  )}
                </div>
              ))}
              {showArchived && archivedChannels.length === 0 && (
                <div className="item item-muted" style={{ fontSize: 13 }}>
                  보관된 채널이 없습니다.
                </div>
              )}
            </div>

            <SectionTitle>다이렉트 메시지</SectionTitle>
            <div className="item-list">
              {nonFavoriteDM.map((c) => {
                const otherId = dmCounterpart(c.name, user?.id ?? "");
                const u = users[otherId];
                const ue = unread[c.id] ?? { msg: 0, mention: 0 };
                return (
                  <button
                    key={c.id}
                    className={`item ${!savedView && !scheduledView && c.id === currentChannelId ? "item-active" : ""}`}
                    onClick={() => selectChannel(c.id)}
                  >
                    <Avatar token={token} id={otherId} name={u?.username ?? otherId.slice(0, 8)} status={statuses[otherId]} size={22} picture={u?.picture} updateAt={u?.update_at} />
                    <span style={{ marginLeft: 2, flex: 1, textAlign: "left" }}>{u?.username ?? otherId.slice(0, 8)}</span>
                    <span
                      role="button"
                      className="channel-fav"
                      title="즐겨찾기에 추가"
                      onClick={(e) => { e.stopPropagation(); onToggleFavorite(c.id); }}
                      onMouseDown={(e) => e.stopPropagation()}
                    >☆</span>
                    {ue.mention > 0
                      ? <span className="mention-badge">{ue.mention}</span>
                      : ue.msg > 0
                        ? <span className="unread">{ue.msg}</span>
                        : null}
                  </button>
                );
              })}
              <button className="item item-muted" onClick={() => setShowStartDM(true)}>＋ 새 DM</button>
            </div>
          </>
        )}

      </aside>

      <main className="chat-main">
        {wsStatus === "reconnecting" && (
          <div className="ws-reconnect-banner" role="status">
            재연결 중… (시도 {wsAttempts}회)
          </div>
        )}
        {savedView ? (
          <SavedPostsView
            posts={savedPosts}
            users={users}
            statuses={statuses}
            reactionsByPost={reactionsByPost}
            filesByID={filesByID}
            currentUserId={user?.id ?? ""}
            token={token ?? ""}
            channels={channels}
            loading={savedLoading}
            onClose={closeSavedView}
            onReload={loadSavedPosts}
            onToggleReaction={onToggleReaction}
            onEdit={onEditPost}
            onDelete={onDeletePost}
            onOpenThread={openThread}
            isSaved={(postId) => savedIds.has(postId)}
            onToggleSaved={onToggleSaved}
            onJumpToChannel={selectChannel}
          />
        ) : scheduledView ? (
          <ScheduledPostsView
            items={scheduledList}
            channels={channels}
            loading={scheduledLoading}
            onClose={closeScheduledView}
            onReload={loadScheduledList}
            onCancel={onCancelScheduled}
            onJumpToChannel={selectChannel}
          />
        ) : currentChannel ? (
          <>
            <header className="chat-header">
              <div className="chat-header-left">
                <div className="chat-header-team">{currentTeam?.display_name}</div>
                <h2 className="chat-header-title">
                  {currentChannel.type === "D" ? (
                    <>
                      <Avatar
                        token={token}
                        id={dmCounterpart(currentChannel.name, user?.id ?? "")}
                        name=""
                        status={statuses[dmCounterpart(currentChannel.name, user?.id ?? "")]}
                        size={22}
                        picture={users[dmCounterpart(currentChannel.name, user?.id ?? "")]?.picture}
                        updateAt={users[dmCounterpart(currentChannel.name, user?.id ?? "")]?.update_at}
                      />
                      {" "}
                      {users[dmCounterpart(currentChannel.name, user?.id ?? "")]?.username ?? "다이렉트 메시지"}
                    </>
                  ) : (
                    <><span className="channel-hash">#</span>{currentChannel.display_name}</>
                  )}
                  {/* Phase 21 — channel member-count chip. Fed by the
                      compat /channels/{id}/stats endpoint. Hidden for DMs
                      (member count is always 2 there) and until the lazy
                      stats fetch resolves. */}
                  {currentChannel.type !== "D" && channelStatsByID[currentChannel.id] && (
                    <span
                      className="channel-stats-chip"
                      title={`멤버 ${channelStatsByID[currentChannel.id].member_count}명 · 고정 ${channelStatsByID[currentChannel.id].pinnedpost_count}개 · 파일 ${channelStatsByID[currentChannel.id].files_count}개`}
                    >
                      👥 {channelStatsByID[currentChannel.id].member_count}
                    </span>
                  )}
                  <ChannelSettingsMenu
                    props={channelNotify[currentChannel.id] ?? { desktop: "all", mark_unread: "all" }}
                    onChange={(patch) => onChangeNotify(currentChannel.id, patch)}
                  />
                  {/* Phase 16 — archive affordance. Admin-only and only on
                      regular (O/P) channels; DMs and group DMs aren't
                      archivable server-side so we hide the button there. */}
                  {isAdmin && currentChannel.type !== "D" && currentChannel.type !== "G" && (
                    <button
                      type="button"
                      className="action-btn"
                      title="채널 보관"
                      style={{ marginLeft: 6 }}
                      onClick={() => onArchiveChannel(currentChannel.id)}
                    >🗄️</button>
                  )}
                </h2>
              </div>
              <div className="chat-header-right">
                <form
                  className="search-form"
                  onSubmit={(e) => { e.preventDefault(); onSearch(0); }}
                >
                  <span className="search-icon" aria-hidden>🔍</span>
                  <input
                    className="search-input"
                    placeholder="메시지 검색"
                    value={searchTerm}
                    onChange={(e) => setSearchTerm(e.target.value)}
                    title="from:username, in:channel, before:YYYY-MM-DD, after:YYYY-MM-DD, has:file, has:link"
                  />
                  {searchResults && (
                    <button
                      type="button"
                      className="search-clear"
                      title="검색 닫기"
                      onClick={() => { setSearchResults(null); setSearchTerm(""); setSearchFilters({}); setSearchTotal(0); setSearchPage(0); }}
                    >×</button>
                  )}
                </form>
                {/* Phase 34.5 — visual separator + user-menu trigger. The
                    1px hairline between the search and the trigger keeps
                    the two clusters from feeling like one cramped blob;
                    matches Mattermost-v11 header rhythm. */}
                <span className="chat-header-divider" aria-hidden />
                <button
                  type="button"
                  className="user-menu-trigger"
                  aria-label="계정 메뉴 열기"
                  aria-expanded={showUserMenu}
                  aria-haspopup="menu"
                  onClick={() => setShowUserMenu((v) => !v)}
                  title="계정 · 프로필 · 환경설정"
                >
                  <Avatar
                    token={token}
                    id={user?.id ?? ""}
                    name={user?.username ?? ""}
                    status={myStatus}
                    size={28}
                    picture={user?.picture}
                    updateAt={user?.update_at}
                  />
                  <span className="user-menu-caret" aria-hidden>▾</span>
                </button>
              </div>
            </header>

            {searchResults ? (
              <div className="chat-messages">
                <div className="search-filter-bar">
                  <div>
                    "{searchTerm}" 검색결과 {" "}
                    <strong>{searchTotal}</strong>건 (페이지 {searchPage + 1})
                  </div>
                  <div className="search-filter-chips">
                    {searchFilters.from_user_id && (
                      <span className="search-chip">
                        from: {users[searchFilters.from_user_id]?.username ?? searchFilters.from_user_id.slice(0, 6)}
                      </span>
                    )}
                    {searchFilters.in_channel_id && (
                      <span className="search-chip">
                        in: {channels.find((c) => c.id === searchFilters.in_channel_id)?.display_name ?? searchFilters.in_channel_id.slice(0, 6)}
                      </span>
                    )}
                    {searchFilters.after && (
                      <span className="search-chip">after: {new Date(searchFilters.after).toISOString().slice(0, 10)}</span>
                    )}
                    {searchFilters.before && (
                      <span className="search-chip">before: {new Date(searchFilters.before - 1).toISOString().slice(0, 10)}</span>
                    )}
                    {searchFilters.has_file && <span className="search-chip">has:file</span>}
                    {searchFilters.has_link && <span className="search-chip">has:link</span>}
                  </div>
                </div>
                {searchResults.map((p) => (
                  <MessageRow
                    key={p.id}
                    post={p}
                    isMe={p.user_id === user?.id}
                    author={users[p.user_id]}
                    status={statuses[p.user_id]}
                    reactions={reactionsByPost[p.id] ?? []}
                    currentUserId={user?.id ?? ""}
                    files={(p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
                    token={token ?? ""}
                    onToggleReaction={(emoji) => onToggleReaction(p, emoji)}
                    onEdit={onEditPost}
                    onDelete={onDeletePost}
                    onOpenThread={openThread}
                    isSaved={savedIds.has(p.id)}
                    onToggleSaved={() => onToggleSaved(p)}
                    compact
                    channelLabel={
                      channels.find((c) => c.id === p.channel_id)?.display_name
                    }
                    onJumpToChannel={() => selectChannel(p.channel_id)}
                  />
                ))}
                {searchTotal > searchResults.length + searchPage * 20 && (
                  <div style={{ display: "flex", justifyContent: "center", padding: 10 }}>
                    <button
                      type="button"
                      className="btn-ghost"
                      style={{ width: "auto", padding: "0 14px", height: 32 }}
                      onClick={() => onSearch(searchPage + 1)}
                    >
                      다음 페이지
                    </button>
                  </div>
                )}
              </div>
            ) : (
              <div className="chat-messages">
                {loadingPosts ? (
                  <div className="chat-empty">불러오는 중…</div>
                ) : posts.length === 0 ? (
                  <div className="chat-empty">첫 메시지를 남겨보세요.</div>
                ) : (
                  posts.map((p) => (
                    <MessageRow
                      key={p.id}
                      post={p}
                      isMe={p.user_id === user?.id}
                      author={users[p.user_id]}
                      status={statuses[p.user_id]}
                      reactions={reactionsByPost[p.id] ?? []}
                      currentUserId={user?.id ?? ""}
                      files={(p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
                      token={token ?? ""}
                      isSaved={savedIds.has(p.id)}
                      onToggleSaved={() => onToggleSaved(p)}
                      onToggleReaction={(emoji) => onToggleReaction(p, emoji)}
                      onEdit={onEditPost}
                      onDelete={onDeletePost}
                      onOpenThread={openThread}
                      onRemindMe={() => setReminderForPostId(p.id)}
                    />
                  ))
                )}
              </div>
            )}

            {!searchResults && (
              <>
                {cmdNotice && (
                  <div className="cmd-notice">
                    <span>{cmdNotice}</span>
                    <button type="button" className="action-btn" onClick={() => setCmdNotice(null)}>✕</button>
                  </div>
                )}
                <TypingIndicator
                  typingUsers={Object.keys(typingUsers).filter((uid) => uid !== user?.id)}
                  users={users}
                />
                <Composer
                  token={token ?? ""}
                  channelID={currentChannelId}
                  onSend={onSendPost}
                  onTyping={sendTyping}
                  onUpload={onUploadFiles}
                  onSchedule={onOpenScheduleModal}
                  userId={user?.id}
                  rootId={null}
                  resetSeq={rootComposerResetSeq}
                />
              </>
            )}
          </>
        ) : (
          <div className="chat-empty" style={{ paddingTop: 80 }}>
            {currentTeam ? "채널을 만들어 시작하세요." : "먼저 팀을 만들어주세요."}
          </div>
        )}

        {error && <div className="login-error" style={{ margin: 12 }}>{error}</div>}
      </main>

      {threadRootId && (
        <ThreadPanel
          rootId={threadRootId}
          posts={threadPosts}
          loading={threadLoading}
          users={users}
          statuses={statuses}
          reactionsByPost={reactionsByPost}
          filesByID={filesByID}
          currentUserId={user?.id ?? ""}
          token={token ?? ""}
          onToggleReaction={onToggleReaction}
          onEdit={onEditPost}
          onDelete={onDeletePost}
          onReply={onReplyInThread}
          onUpload={onUploadFiles}
          onSchedule={onOpenScheduleModalFromThread(threadRootId)}
          composerResetSeq={threadComposerResetSeq}
          onClose={closeThread}
        />
      )}

      {showStartDM && token && user && (
        <StartDirectModal
          token={token}
          currentUserId={user.id}
          onClose={() => setShowStartDM(false)}
          onPick={onStartDirect}
        />
      )}

      {showSessions && (
        <SessionManagerModal
          sessions={sessions}
          loading={sessionsLoading}
          onRevoke={onRevokeOneSession}
          onRevokeOthers={onRevokeOtherSessions}
          onClose={() => setShowSessions(false)}
        />
      )}

      {/* Phase 18 — 채널 탐색. Opens via the sidebar button; on join we
          add the channel to the local list (so the user can switch to it
          without waiting on the next channels fetch) and close the modal. */}
      {showDiscover && currentTeamId && token && (
        <ChannelDiscoverModal
          token={token}
          teamId={currentTeamId}
          onClose={() => setShowDiscover(false)}
          onJoined={(chId) => {
            // Re-pull so we have the full Channel record + membership
            // roles correct. If the fetch fails the user will still see
            // the channel after their next reconnect-driven refresh.
            loadChannels();
            selectChannel(chId);
            setShowDiscover(false);
          }}
        />
      )}

      {/* Phase 19 — schedule modal. Opens from the Composer 🕐 button;
          onConfirm returns a bool so the modal can block itself while the
          server round-trip is in flight and surface errors inline. */}
      {scheduleModalFor && (
        <ScheduleModal
          channelName={channels.find((c) => c.id === scheduleModalFor.channelId)?.display_name ?? ""}
          messagePreview={scheduleModalFor.message}
          onCancel={() => setScheduleModalFor(null)}
          onConfirm={onConfirmSchedule}
        />
      )}

      {/* Phase 19 — reminder popover. Anchored to the center for now — a
          fixed centered card is cheap to build and keyboard-reachable
          without accessibility gymnastics around viewport clipping. */}
      {reminderForPostId && (
        <ReminderPopover
          postId={reminderForPostId}
          onCancel={() => setReminderForPostId(null)}
          onConfirm={onCreateReminder}
        />
      )}

      {/* Phase 19 — reminder toast stack. Fires when reminder_fired WS
          event arrives; each toast auto-dismisses after 20s but a click on
          "이동" jumps to the target channel immediately. */}
      {reminderToasts.length > 0 && (
        <div className="reminder-toast-stack">
          {reminderToasts.map((t) => (
            <div key={t.id} className="reminder-toast">
              <div className="reminder-toast-title">🔔 리마인더</div>
              {t.excerpt && (
                <div className="reminder-toast-body">{t.excerpt}</div>
              )}
              <div className="reminder-toast-actions">
                <button
                  type="button"
                  className="btn-primary"
                  style={{ width: "auto", padding: "0 12px", height: 28 }}
                  onClick={() => {
                    onJumpFromReminder(t.channelId);
                    setReminderToasts((prev) => prev.filter((x) => x.id !== t.id));
                  }}
                >이동</button>
                <button
                  type="button"
                  className="btn-ghost"
                  style={{ width: "auto", padding: "0 10px", height: 28 }}
                  onClick={() => setReminderToasts((prev) => prev.filter((x) => x.id !== t.id))}
                >닫기</button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Phase 21 — Quick Switcher (Cmd+K). Channel + user autocomplete in
          one combined list; arrow keys cycle, Enter selects. On user pick
          we open or create a DM channel. */}
      {showQuickSwitcher && token && (
        <QuickSwitcherModal
          token={token}
          teamId={currentTeamId}
          channels={channels}
          users={users}
          meId={user?.id ?? ""}
          onClose={() => setShowQuickSwitcher(false)}
          onPickChannel={(chId) => {
            selectChannel(chId);
            setShowQuickSwitcher(false);
          }}
          onPickUser={async (peer) => {
            setShowQuickSwitcher(false);
            if (!token || !peer.id || peer.id === user?.id) return;
            try {
              const ch = await api.createDirectChannel(token, [peer.id]);
              setUsers((prev) => ({ ...prev, [peer.id]: peer }));
              await loadChannels();
              selectChannel(ch.id);
            } catch (err) {
              setError((err as Error).message || "DM 열기 실패");
            }
          }}
        />
      )}

      {/* Phase 22 — user-level notify_props panel. Persists through PUT
          /users/me/notify_props. */}
      {showNotifyPrefs && token && (
        <NotifyPrefsModal token={token} onClose={() => setShowNotifyPrefs(false)} />
      )}

      {/* Phase 33 — custom profile attributes drawer. Renders the admin's
          field defs as a form, persists per-field values via PATCH
          /users/me/custom_profile_attributes. Admins additionally see a
          "필드 관리" toggle to define/rename/remove fields globally. */}
      {showProfileFields && token && (
        <CustomProfileFieldsModal
          token={token}
          isAdmin={false}
          onClose={() => setShowProfileFields(false)}
        />
      )}

      {/* Phase 34.5 — avatar file input for the user-menu's "프로필 사진
          변경" entry. Parked at chat-shell root (no longer in the sidebar)
          so it survives sidebar layout changes and the user-menu open/close
          cycle. We reset .value after each run so re-selecting the same
          file still fires onChange. */}
      <input
        ref={avatarFileRef}
        type="file"
        accept="image/*"
        style={{ display: "none" }}
        onChange={async (e) => {
          const file = e.target.files?.[0];
          if (!file || !token) { e.target.value = ""; return; }
          setUploadingAvatar(true);
          try {
            const updated = await api.uploadProfileImage(token, file);
            dispatch(setAuth({ token, user: updated }));
          } catch (err) {
            setError((err as Error).message || "프로필 사진 업로드 실패");
          } finally {
            setUploadingAvatar(false);
            e.target.value = "";
          }
        }}
      />

      {/* Phase 34 — user-menu dropdown. Mounted at the chat-shell root
          so the absolute-positioned dropdown isn't clipped by the
          chat-header's overflow. Click-outside to close is handled by a
          full-viewport invisible backdrop directly behind the menu. */}
      {showUserMenu && (
        <UserMenuOverlay
          username={user?.username ?? ""}
          email={user?.email ?? ""}
          status={myStatus}
          onChangeStatus={onChangeMyStatus}
          theme={theme}
          onChangeTheme={setTheme}
          digestEnabled={digestEnabled}
          onToggleDigest={async (next) => {
            if (!token) return;
            const prev = digestEnabled;
            setDigestEnabled(next);
            try {
              await api.updateEmailPrefs(token, { digest_enabled: next });
            } catch (err) {
              setDigestEnabled(prev);
              setError((err as Error).message || "이메일 설정 저장 실패");
            }
          }}
          uploadingAvatar={uploadingAvatar}
          onUploadAvatar={() => avatarFileRef.current?.click()}
          onOpenProfileFields={() => { setShowUserMenu(false); setShowProfileFields(true); }}
          onOpenNotifyPrefs={() => { setShowUserMenu(false); setShowNotifyPrefs(true); }}
          onOpenSessions={() => { setShowUserMenu(false); openSessionModal(); }}
          onOpenQuickSwitcher={() => { setShowUserMenu(false); setShowQuickSwitcher(true); }}
          onOpenPersonalSettings={() => { setShowUserMenu(false); navigate("/settings/profile"); }}
          onOpenMyApprovals={() => { setShowUserMenu(false); navigate("/settings/approvals/mine"); }}
          onOpenApprovalReviews={() => { setShowUserMenu(false); navigate("/settings/approvals/review"); }}
          onOpenAdmin={() => { setShowUserMenu(false); navigate("/admin/overview"); }}
          isAdmin={isAdmin}
          approvalEnabled={systemInfo.approval_enabled === true}
          version={displayVersion(systemInfo.version)}
          buildHash={systemInfo.build_hash}
          onLogout={async () => {
            setShowUserMenu(false);
            if (token) { try { await api.logout(token); } catch { /* best-effort */ } }
            dispatch(clearAuth());
          }}
          onClose={() => setShowUserMenu(false)}
        />
      )}

      {/* Phase 20 — shared confirm dialog. Rendered last so its backdrop
          and z-index stack above every other modal in the shell. */}
      {confirmer.render()}
    </div>
  );
}

// ---- Subcomponents ----

function SectionTitle({ children }: { children: React.ReactNode }) {
  return <div className="section-title">{children}</div>;
}

function ChannelRow({
  channel, active, unread, onClick, isFavorite, onToggleFavorite,
}: {
  channel: Channel;
  active: boolean;
  unread: UnreadEntry;
  onClick: () => void;
  // Phase 22 — favorites toggle. Optional so existing call sites that don't
  // care (e.g. archived list) compile unchanged. When provided, a star icon
  // appears on hover (or always for already-favorited rows).
  isFavorite?: boolean;
  onToggleFavorite?: (channelId: string) => void;
}) {
  return (
    <button className={`item ${active ? "item-active" : ""}`} onClick={onClick}>
      <span className="channel-hash">#</span>
      <span style={{ flex: 1, textAlign: "left" }}>{channel.display_name}</span>
      {onToggleFavorite && (
        <span
          role="button"
          aria-label={isFavorite ? "즐겨찾기 해제" : "즐겨찾기"}
          title={isFavorite ? "즐겨찾기 해제" : "즐겨찾기에 추가"}
          className={`channel-fav ${isFavorite ? "is-fav" : ""}`}
          onClick={(e) => { e.stopPropagation(); onToggleFavorite(channel.id); }}
          onMouseDown={(e) => e.stopPropagation()}
        >
          {isFavorite ? "★" : "☆"}
        </span>
      )}
      {unread.mention > 0
        ? <span className="mention-badge">{unread.mention}</span>
        : unread.msg > 0
          ? <span className="unread">{unread.msg}</span>
          : null}
    </button>
  );
}

// `picture` (optional) is the raw value from User.picture — either an
// external URL or a bare file_id. When provided and non-empty, we fetch
// the image through `/api/v4/users/{id}/image?v={updateAt}`. On network
// failure (404 from empty, CORS from a stale external URL) we fall back
// to the initial-tile render via an onError handler.
function Avatar({
  token, id, name, status, size = 28, picture, updateAt,
}: {
  token: string | null;
  id: string;
  name: string;
  status?: UserStatusValue;
  size?: number;
  picture?: string;
  updateAt?: number;
}) {
  const bg = color(id || name || "?");
  const initial = (name || id || "?")[0]?.toUpperCase() ?? "?";
  const [imgFailed, setImgFailed] = useState(false);
  const externalPicture = isExternalImageURL(picture);
  const showImg = !!picture && !imgFailed && !!id && (externalPicture || !!token);
  useEffect(() => setImgFailed(false), [picture, updateAt, token]);
  return (
    <span
      className="avatar"
      style={{
        width: size,
        height: size,
        background: showImg ? "transparent" : bg,
        fontSize: size * 0.45,
      }}
    >
      {showImg ? (
        externalPicture ? (
          <img
            src={picture}
            alt=""
            referrerPolicy="no-referrer"
            onError={() => setImgFailed(true)}
          />
        ) : (
          <AuthenticatedImage
            token={token ?? ""}
            path={api.userImagePath(id, updateAt ?? picture)}
            alt=""
            onFetchError={() => setImgFailed(true)}
            onError={() => setImgFailed(true)}
          />
        )
      ) : (
        initial
      )}
      {status && <span className={`status-dot status-${status}`} />}
    </span>
  );
}

type MessageRowProps = {
  post: Post;
  isMe: boolean;
  author?: User;
  status?: UserStatusValue;
  reactions: Reaction[];
  currentUserId: string;
  files: FileInfo[];
  token: string;
  onToggleReaction: (emoji: string) => void;
  onEdit: (postId: string, message: string) => void;
  onDelete: (postId: string) => void;
  onOpenThread?: (rootId: string) => void;
  compact?: boolean;
  // Hide the "open thread" action (e.g. when already rendering inside
  // the thread sidebar — a nested open would just re-open the same root).
  hideThreadAction?: boolean;
  // Phase 18 — bookmark affordance. Undefined disables the star button
  // entirely (used in thread panel where save-from-thread adds clutter).
  isSaved?: boolean;
  onToggleSaved?: () => void;
  // Phase 18 — when rendering a post outside its own channel (search
  // results / saved list), show the channel name and give the row a
  // "jump to channel" affordance.
  channelLabel?: string;
  onJumpToChannel?: () => void;
  // Phase 19 — optional reminder hook. When present, the hover action bar
  // shows a 🔔 button that opens a popover (rendered by the parent) for
  // picking a remind-at time.
  onRemindMe?: () => void;
};

function MessageRow(props: MessageRowProps) {
  const { post, isMe, author, status, reactions, currentUserId, files, token, onToggleReaction, onEdit, onDelete, onOpenThread, compact, hideThreadAction, isSaved, onToggleSaved, channelLabel, onJumpToChannel, onRemindMe } = props;
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(post.message);
  const [pickerOpen, setPickerOpen] = useState(false);
  const editRef = useRef<HTMLTextAreaElement>(null);
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
    ? `moyro:draft:edit:${currentUserId}:${post.id}`
    : null;
  const editDraft = useDraft(editDraftKey, draft, setDraft);

  const grouped = useMemo(() => {
    const m: Record<string, Reaction[]> = {};
    reactions.forEach((r) => { (m[r.emoji_name] ||= []).push(r); });
    return m;
  }, [reactions]);

  const edited = post.update_at > post.create_at;

  return (
    <div className={`msg ${isMe ? "msg-me" : ""} ${compact ? "msg-compact" : ""}`}>
      <div className="msg-meta">
        <Avatar token={token} id={post.user_id} name={author?.username ?? ""} status={status} size={20} picture={author?.picture} updateAt={author?.update_at} />
        <span className="msg-author">{author?.username ?? (isMe ? "나" : post.user_id.slice(0, 8))}</span>
        <time className="msg-time">{formatTime(post.create_at)}</time>
        {edited && <span className="msg-edited">(편집됨)</span>}
        {post.is_pinned && <span className="msg-pinned">📌</span>}
        {channelLabel && (
          <button
            type="button"
            className="msg-channel-chip"
            onClick={onJumpToChannel}
            title="이 채널로 이동"
          >
            #{channelLabel}
          </button>
        )}
      </div>

      {editing ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (draft.trim()) {
              onEdit(post.id, draft.trim());
              editDraft.clearSaved();
              setEditing(false);
            }
          }}
        >
          <div className="mention-picker-host">
            <textarea
              ref={editRef}
              className="composer-input"
              value={draft}
              onChange={(e) => {
                setDraft(e.target.value);
                editMentions.onChange(e);
              }}
              rows={2}
              autoFocus
              onKeyDown={(e) => {
                if (editMentions.handleKeyDown(e)) return;
                // Phase 20 (F8) — Escape discards the edit and also clears
                // any auto-saved edit draft in localStorage so re-opening
                // the editor starts fresh instead of rehydrating the
                // abandoned garbage.
                if (e.key === "Escape") {
                  editDraft.clearSaved();
                  setEditing(false);
                  setDraft(post.message);
                }
              }}
            />
            {editMentions.render()}
          </div>
          <div style={{ display: "flex", gap: 8, marginTop: 6 }}>
            <button type="submit" className="btn-primary" style={{ width: "auto", height: 32, padding: "0 12px" }}>저장</button>
            <button type="button" className="btn-ghost" style={{ width: "auto", height: 32, padding: "0 12px" }}
              onClick={() => {
                // Phase 20 (F8) — same as Escape: drop the auto-saved draft
                // when the user explicitly cancels the edit.
                editDraft.clearSaved();
                setEditing(false);
                setDraft(post.message);
              }}>취소</button>
          </div>
        </form>
      ) : (
        <>
          {post.message && (
            <MessageBody
              source={post.message}
              token={token}
              linkMetadata={post.link_metadata}
            />
          )}
          {files.length > 0 && (
            <div className="msg-files">
              {files.map((f) => <FileChip key={f.id} file={f} token={token} />)}
            </div>
          )}
        </>
      )}

      {Object.keys(grouped).length > 0 && (
        <div className="reactions">
          {Object.entries(grouped).map(([emoji, rs]) => {
            const mine = rs.some((r) => r.user_id === currentUserId);
            // Custom emoji lookup: if the name matches a loaded custom
            // emoji, render its image inline; otherwise fall through to
            // the built-in short-code → unicode map.
            const custom = customEmojiByName(emoji);
            return (
              <button
                key={emoji}
                type="button"
                className={`reaction-chip ${mine ? "reaction-mine" : ""}`}
                onClick={() => onToggleReaction(emoji)}
                title={rs.map((r) => r.user_id).join(", ")}
              >
                {custom ? (
                  <AuthenticatedImage
                    token={token}
                    path={api.emojiImagePath(custom.id)}
                    className="emoji-img"
                    alt={emoji}
                  />
                ) : (
                  <span>{emojiChar(emoji)}</span>
                )}
                <span className="reaction-count">{rs.length}</span>
              </button>
            );
          })}
        </div>
      )}

      {!editing && !compact && (
        <div className="msg-actions">
          <button type="button" className="action-btn" onClick={() => setPickerOpen((v) => !v)} title="리액션">😊</button>
          {!hideThreadAction && onOpenThread && (
            <button
              type="button"
              className="action-btn"
              onClick={() => onOpenThread(post.root_id || post.id)}
              title="스레드 열기"
            >💬</button>
          )}
          {onToggleSaved && (
            <button
              type="button"
              className={`action-btn ${isSaved ? "action-saved" : ""}`}
              onClick={onToggleSaved}
              title={isSaved ? "저장 해제" : "저장"}
            >
              {isSaved ? "★" : "☆"}
            </button>
          )}
          {onRemindMe && (
            <button
              type="button"
              className="action-btn"
              onClick={onRemindMe}
              title="나중에 알림"
            >🔔</button>
          )}
          {isMe && <button type="button" className="action-btn" onClick={() => setEditing(true)} title="편집">✎</button>}
          {isMe && <button type="button" className="action-btn" onClick={() => onDelete(post.id)} title="삭제">🗑</button>}
          {pickerOpen && (
            <EmojiPicker
              token={token}
              quick={QUICK_EMOJIS}
              onPick={(name) => { onToggleReaction(name); setPickerOpen(false); }}
              onClose={() => setPickerOpen(false)}
            />
          )}
        </div>
      )}
      {/* Compact variant (search results / saved list) still exposes the
          star inline since it's the primary interaction in those views. */}
      {!editing && compact && onToggleSaved && (
        <div className="msg-actions" style={{ opacity: 1 }}>
          <button
            type="button"
            className={`action-btn ${isSaved ? "action-saved" : ""}`}
            onClick={onToggleSaved}
            title={isSaved ? "저장 해제" : "저장"}
          >
            {isSaved ? "★" : "☆"}
          </button>
        </div>
      )}
    </div>
  );
}

function FileChip({ file, token }: { file: FileInfo; token: string }) {
  const [lightbox, setLightbox] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [downloadFailed, setDownloadFailed] = useState(false);
  const filePath = api.fileDownloadPath(file.id);
  const isImage = file.mime_type?.startsWith("image/");
  if (isImage) {
    // Prefer the server-generated thumbnail when one exists. When the
    // upload is still being processed (has_thumbnail=false), fall back to
    // the full-size image — correct, just slower. Both cases open the
    // full-res lightbox on click.
    const thumbnailPath = file.has_thumbnail
      ? api.fileThumbnailPath(file.id)
      : filePath;
    return (
      <>
        <button
          type="button"
          className="file-image"
          onClick={() => setLightbox(true)}
          aria-label={`이미지 확대: ${file.name}`}
        >
          <AuthenticatedImage token={token} path={thumbnailPath} alt={file.name} loading="lazy" />
        </button>
        {lightbox && (
          <Lightbox token={token} path={filePath} alt={file.name} onClose={() => setLightbox(false)} />
        )}
      </>
    );
  }

  async function onDownload() {
    if (downloading) return;
    setDownloading(true);
    setDownloadFailed(false);
    try {
      await downloadAuthenticatedMedia(token, filePath, file.name);
    } catch {
      setDownloadFailed(true);
    } finally {
      setDownloading(false);
    }
  }

  return (
    <button
      type="button"
      className="file-chip"
      onClick={onDownload}
      disabled={downloading}
      title={downloadFailed ? "파일을 다운로드하지 못했습니다." : undefined}
    >
      <span className="file-icon">📎</span>
      <span className="file-name">{file.name}</span>
      <span className="file-size">
        {downloading ? "받는 중…" : downloadFailed ? "실패 — 다시 시도" : humanSize(file.size)}
      </span>
    </button>
  );
}

type ComposerProps = {
  token: string;
  // channelID scopes the @mention autocomplete to the right channel. May
  // be null when no channel is focused yet (e.g. brand-new workspace);
  // the hook simply short-circuits into a no-op.
  channelID: string | null;
  onSend: (message: string, fileIds: string[]) => void;
  onTyping: () => void;
  onUpload: (files: File[]) => Promise<FileInfo[]>;
  // Phase 19/20 — optional scheduling affordance. The 🕐 button is rendered
  // whenever onSchedule is wired; root and thread composers both use this
  // path, and the caller decides whether a root_id should be attached.
  onSchedule?: (message: string, fileIds: string[]) => void;
  // Phase 19 — used to build the per-user/channel/thread draft key. When
  // any of these is falsy, the draft hook short-circuits and no auto-save
  // happens (safer than saving to a shared key).
  userId?: string;
  rootId?: string | null;
  // Phase 20 (F3) — bump-to-reset hook. Parent increments this seq after
  // a successful schedule so the textarea/pending files/draft clear
  // without leaving the "deploy notes" text ready to re-send on Enter.
  // useEffect watches the seq; initial mount is skipped via a ref so
  // we don't wipe a rehydrated draft on first render.
  resetSeq?: number;
};

function Composer({ token, channelID, onSend, onTyping, onUpload, onSchedule, userId, rootId, resetSeq }: ComposerProps) {
  const [value, setValue] = useState("");
  const [pending, setPending] = useState<FileInfo[]>([]);
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
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
    ? `moyro:draft:${userId}:${channelID}:${rootId || "root"}`
    : null;
  const draft = useDraft(draftKey, value, setValue);

  function submit() {
    const trimmed = value.trim();
    if (!trimmed && pending.length === 0) return;
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
    if (resetSeqRef.current === resetSeq) return;
    resetSeqRef.current = resetSeq;
    setValue("");
    setPending([]);
    draft.clearSaved();
  }, [resetSeq, draft]);

  // Phase 20 (F5) — auto-focus the textarea when the channel changes so
  // the user can immediately type after clicking a channel in the
  // sidebar. Skipped on the very first mount to avoid yanking focus from
  // whatever else the app may be doing at startup (e.g. modal, login).
  const prevChannelRef = useRef<string | null>(channelID);
  useEffect(() => {
    if (prevChannelRef.current === channelID) return;
    prevChannelRef.current = channelID;
    if (channelID) textareaRef.current?.focus();
  }, [channelID]);

  async function onFilesSelected(files: FileList | null) {
    if (!files || files.length === 0) return;
    setUploading(true);
    const uploaded = await onUpload(Array.from(files));
    setPending((prev) => [...prev, ...uploaded]);
    setUploading(false);
    if (fileInputRef.current) fileInputRef.current.value = "";
  }

  function notifyTyping() {
    const now = Date.now();
    if (now - typingAtRef.current > 1500) {
      typingAtRef.current = now;
      onTyping();
    }
  }

  return (
    <form
      className="composer"
      onSubmit={(e) => { e.preventDefault(); submit(); }}
    >
      <div style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 6 }}>
        {pending.length > 0 && (
          <div className="msg-files" style={{ marginBottom: 0 }}>
            {pending.map((f) => (
              <div key={f.id} className="file-chip">
                <span className="file-icon">📎</span>
                <span className="file-name">{f.name}</span>
                <button
                  type="button"
                  className="action-btn"
                  onClick={() => setPending((prev) => prev.filter((x) => x.id !== f.id))}
                >✕</button>
              </div>
            ))}
          </div>
        )}
        <div style={{ display: "flex", minWidth: 0, gap: 8 }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: 40, height: 40, padding: 0, flex: "0 0 auto" }}
            onClick={() => fileInputRef.current?.click()}
            title="파일 첨부"
          >📎</button>
          {onSchedule && (
            <button
              type="button"
              className="btn-ghost"
              style={{ width: 40, height: 40, padding: 0, flex: "0 0 auto" }}
              onClick={() => onSchedule(value, pending.map((f) => f.id))}
              title="메시지 예약 전송"
            >🕐</button>
          )}
          <input
            ref={fileInputRef}
            type="file"
            multiple
            style={{ display: "none" }}
            onChange={(e) => onFilesSelected(e.target.files)}
          />
          <div className="mention-picker-host" style={{ flex: 1, minWidth: 0, display: "flex" }}>
            <textarea
              ref={textareaRef}
              className="composer-input"
              rows={1}
              placeholder={uploading ? "업로드 중…" : "메시지를 입력하세요… (Shift+Enter 줄바꿈)"}
              value={value}
              onChange={(e) => {
                setValue(e.target.value);
                mentions.onChange(e);
                notifyTyping();
              }}
              onKeyDown={(e) => {
                // Give the mention picker first crack at arrow/Enter/Tab/Escape
                // keys when it's open; otherwise fall through to submit-on-Enter.
                if (mentions.handleKeyDown(e)) return;
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  submit();
                }
              }}
            />
            {mentions.render()}
          </div>
          <button type="submit" className="btn-primary" style={{ width: 88, height: 40, flex: "0 0 auto" }}>
            전송
          </button>
        </div>
        {draft.hasSaved && (
          <div className="draft-badge">
            <span>초안 저장됨</span>
            <button
              type="button"
              className="draft-clear"
              onClick={draft.clear}
              title="저장된 초안 지우기"
            >지우기</button>
          </div>
        )}
      </div>
      <input type="hidden" value={token} />
    </form>
  );
}

function TypingIndicator({ typingUsers, users }: { typingUsers: string[]; users: UsersMap }) {
  if (typingUsers.length === 0) return null;
  const names = typingUsers.map((uid) => users[uid]?.username ?? uid.slice(0, 6)).slice(0, 3);
  const label = names.length === 1
    ? `${names[0]}님이 입력 중…`
    : names.length <= 3
      ? `${names.join(", ")}님이 입력 중…`
      : "여러 명이 입력 중…";
  return <div className="typing-indicator">{label}</div>;
}

type ThreadPanelProps = {
  rootId: string;
  posts: Post[];
  loading: boolean;
  users: UsersMap;
  statuses: StatusMap;
  reactionsByPost: ReactionMap;
  filesByID: FilesMap;
  currentUserId: string;
  token: string;
  onToggleReaction: (post: Post, emoji: string) => void;
  onEdit: (postId: string, message: string) => void;
  onDelete: (postId: string) => void;
  onReply: (message: string, fileIds: string[]) => void;
  onUpload: (files: File[]) => Promise<FileInfo[]>;
  // Phase 20 (F7) — thread schedule parity. Thread replies can now be
  // scheduled because the server already supports root_id on
  // scheduled_posts; we just had to pipe it through.
  onSchedule?: (message: string, fileIds: string[]) => void;
  composerResetSeq?: number;
  onClose: () => void;
};

function ThreadPanel(props: ThreadPanelProps) {
  const {
    rootId, posts, loading, users, statuses, reactionsByPost, filesByID,
    currentUserId, token, onToggleReaction, onEdit, onDelete, onReply, onUpload,
    onSchedule, composerResetSeq, onClose,
  } = props;

  const root = posts.find((p) => p.id === rootId) ?? null;
  const replies = posts.filter((p) => p.id !== rootId);

  return (
    <aside className="thread-panel">
      <header className="thread-header">
        <strong>스레드</strong>
        <button type="button" className="action-btn" onClick={onClose} title="닫기">✕</button>
      </header>
      <div className="thread-body">
        {loading && posts.length === 0 ? (
          <div className="chat-empty">불러오는 중…</div>
        ) : !root ? (
          <div className="chat-empty">원본 메시지를 찾을 수 없습니다.</div>
        ) : (
          <>
            <MessageRow
              post={root}
              isMe={root.user_id === currentUserId}
              author={users[root.user_id]}
              status={statuses[root.user_id]}
              reactions={reactionsByPost[root.id] ?? []}
              currentUserId={currentUserId}
              files={(root.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
              token={token}
              onToggleReaction={(emoji) => onToggleReaction(root, emoji)}
              onEdit={onEdit}
              onDelete={onDelete}
              hideThreadAction
            />
            <div className="thread-divider">답글 {replies.length}개</div>
            {replies.map((p) => (
              <MessageRow
                key={p.id}
                post={p}
                isMe={p.user_id === currentUserId}
                author={users[p.user_id]}
                status={statuses[p.user_id]}
                reactions={reactionsByPost[p.id] ?? []}
                currentUserId={currentUserId}
                files={(p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
                token={token}
                onToggleReaction={(emoji) => onToggleReaction(p, emoji)}
                onEdit={onEdit}
                onDelete={onDelete}
                hideThreadAction
              />
            ))}
          </>
        )}
      </div>
      <Composer
        token={token}
        // Thread replies belong to the root post's channel; fall back to
        // null if the root hasn't loaded yet so the autocomplete hook
        // stays dormant instead of querying an empty channelID.
        channelID={root?.channel_id ?? null}
        onSend={onReply}
        onTyping={() => { /* typing in threads is best-effort; skip for now */ }}
        onUpload={onUpload}
        userId={currentUserId}
        rootId={rootId}
        onSchedule={onSchedule}
        resetSeq={composerResetSeq}
      />
    </aside>
  );
}

// ---- Phase 18: SavedPostsView ----
//
// Renders the caller's bookmarked posts in the main pane. Reuses MessageRow
// in compact mode so the visual language matches search results; the
// "Jump to channel" chip sets currentChannelId and flips savedView off.
type SavedPostsViewProps = {
  posts: Post[];
  users: UsersMap;
  statuses: StatusMap;
  reactionsByPost: ReactionMap;
  filesByID: FilesMap;
  currentUserId: string;
  token: string;
  channels: Channel[];
  loading: boolean;
  onClose: () => void;
  onReload: () => void;
  onToggleReaction: (post: Post, emoji: string) => void;
  onEdit: (postId: string, message: string) => void;
  onDelete: (postId: string) => void;
  onOpenThread: (rootId: string) => void;
  isSaved: (postId: string) => boolean;
  onToggleSaved: (post: Post) => void;
  onJumpToChannel: (channelId: string) => void;
};

function SavedPostsView(props: SavedPostsViewProps) {
  const {
    posts, users, statuses, reactionsByPost, filesByID, currentUserId, token,
    channels, loading, onClose, onReload, onToggleReaction, onEdit, onDelete,
    onOpenThread, isSaved, onToggleSaved, onJumpToChannel,
  } = props;
  return (
    <>
      <header className="chat-header">
        <div className="chat-header-left">
          <h2 className="chat-header-title">⭐ 저장됨</h2>
        </div>
        <div style={{ display: "flex", gap: 6 }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 12px", height: 32 }}
            onClick={onReload}
            title="목록 새로고침"
          >
            ↻
          </button>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 12px", height: 32 }}
            onClick={onClose}
          >
            닫기
          </button>
        </div>
      </header>
      <div className="chat-messages">
        {loading ? (
          <div className="chat-empty">불러오는 중…</div>
        ) : posts.length === 0 ? (
          <div className="chat-empty">
            저장된 메시지가 없습니다. 메시지 위에 마우스를 올려 ☆ 버튼을 눌러보세요.
          </div>
        ) : (
          posts.map((p) => (
            <MessageRow
              key={p.id}
              post={p}
              isMe={p.user_id === currentUserId}
              author={users[p.user_id]}
              status={statuses[p.user_id]}
              reactions={reactionsByPost[p.id] ?? []}
              currentUserId={currentUserId}
              files={(p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
              token={token}
              onToggleReaction={(emoji) => onToggleReaction(p, emoji)}
              onEdit={onEdit}
              onDelete={onDelete}
              onOpenThread={onOpenThread}
              isSaved={isSaved(p.id)}
              onToggleSaved={() => onToggleSaved(p)}
              compact
              channelLabel={channels.find((c) => c.id === p.channel_id)?.display_name}
              onJumpToChannel={() => onJumpToChannel(p.channel_id)}
            />
          ))
        )}
      </div>
    </>
  );
}

// ---- Phase 18: ChannelDiscoverModal ----
//
// Lists public channels in the current team that the user hasn't joined.
// Debounced text search; clicking 참여 calls joinChannel and removes the
// row optimistically. Pagination is an "더 보기" button rather than infinite
// scroll — the list is bounded and a single expansion is usually enough.
type ChannelDiscoverModalProps = {
  token: string;
  teamId: string;
  onClose: () => void;
  onJoined: (channelId: string) => void;
};

function ChannelDiscoverModal({ token, teamId, onClose, onJoined }: ChannelDiscoverModalProps) {
  useEscClose(true, onClose);
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<Channel[]>([]);
  const [loading, setLoading] = useState(false);
  const [offset, setOffset] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [joining, setJoining] = useState<string | null>(null);

  const doFetch = useCallback(async (reset: boolean) => {
    setLoading(true);
    setErr(null);
    try {
      const nextOffset = reset ? 0 : offset;
      const list = await api.discoverChannels(token, teamId, {
        q: q.trim(),
        limit: 20,
        offset: nextOffset,
      });
      if (reset) setRows(list);
      else setRows((prev) => [...prev, ...list]);
      setHasMore((list?.length ?? 0) >= 20);
      setOffset(nextOffset + (list?.length ?? 0));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "채널 탐색 실패");
    } finally {
      setLoading(false);
    }
  }, [token, teamId, q, offset]);

  // Debounce the initial + query-change load so typing doesn't spam the API.
  useEffect(() => {
    const t = setTimeout(() => { doFetch(true); }, q ? 180 : 0);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q]);

  async function onJoin(ch: Channel) {
    if (joining) return;
    setJoining(ch.id);
    try {
      await api.joinChannel(token, ch.id);
      setRows((prev) => prev.filter((c) => c.id !== ch.id));
      onJoined(ch.id);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "채널 참여 실패");
    } finally {
      setJoining(null);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal-card channel-discover-modal"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 style={{ margin: "0 0 12px" }}>🔍 채널 탐색</h3>
        <input
          className="field-input"
          autoFocus
          placeholder="이름 또는 표시 이름으로 검색"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          style={{ marginBottom: 10 }}
        />
        {err && <div className="login-error">{err}</div>}
        <div className="discover-list">
          {rows.length === 0 && !loading && (
            <div className="chat-empty" style={{ padding: 16 }}>
              {q ? "검색 결과가 없습니다." : "참여할 수 있는 공개 채널이 없습니다."}
            </div>
          )}
          {rows.map((c) => (
            <div key={c.id} className="discover-row">
              <div className="discover-row-main">
                <div className="discover-row-title">
                  <span className="channel-hash">#</span>
                  {c.display_name}
                </div>
                <div className="discover-row-name">{c.name}</div>
                {c.purpose && <div className="discover-row-purpose">{c.purpose}</div>}
              </div>
              <button
                type="button"
                className="btn-primary"
                style={{ width: "auto", padding: "0 14px", height: 32 }}
                disabled={joining === c.id}
                onClick={() => onJoin(c)}
              >
                {joining === c.id ? "참여 중…" : "참여"}
              </button>
            </div>
          ))}
        </div>
        <div style={{ display: "flex", justifyContent: "space-between", marginTop: 10 }}>
          <button type="button" className="btn-ghost" onClick={onClose}>닫기</button>
          {hasMore && (
            <button
              type="button"
              className="btn-ghost"
              disabled={loading}
              onClick={() => doFetch(false)}
            >
              {loading ? "불러오는 중…" : "더 보기"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function StartDirectModal({
  token, currentUserId, onClose, onPick,
}: {
  token: string;
  currentUserId: string;
  onClose: () => void;
  onPick: (userId: string) => void;
}) {
  useEscClose(true, onClose);
  const [q, setQ] = useState("");
  const [results, setResults] = useState<User[]>([]);

  useEffect(() => {
    const t = setTimeout(async () => {
      try {
        if (q.trim()) {
          const list = await api.searchUsers(token, q.trim(), 20);
          setResults(list.filter((u) => u.id !== currentUserId));
        } else {
          const list = await api.listUsers(token, 0, 20);
          setResults(list.filter((u) => u.id !== currentUserId));
        }
      } catch { /* ignore */ }
    }, 200);
    return () => clearTimeout(t);
  }, [q, token, currentUserId]);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()}>
        <h3 style={{ margin: "0 0 12px" }}>새 다이렉트 메시지</h3>
        <input
          className="field-input"
          autoFocus
          placeholder="사용자 검색…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <div className="user-picker">
          {results.length === 0 ? (
            <div className="chat-empty" style={{ padding: 16 }}>결과 없음</div>
          ) : results.map((u) => (
            <button key={u.id} className="item" onClick={() => onPick(u.id)}>
              <Avatar token={token} id={u.id} name={u.username} size={22} picture={u.picture} updateAt={u.update_at} />
              <span style={{ marginLeft: 2 }}>{u.username}</span>
              <span style={{ color: "var(--muted)", fontSize: 13, marginLeft: "auto" }}>{u.email}</span>
            </button>
          ))}
        </div>
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 12 }}>
          <button className="btn-ghost" style={{ width: "auto", padding: "0 14px", height: 34 }} onClick={onClose}>닫기</button>
        </div>
      </div>
    </div>
  );
}

type DesktopPref = "all" | "mentions" | "none";
type MarkUnreadPref = "all" | "mention";

function ChannelSettingsMenu({
  props, onChange,
}: {
  props: ChannelNotifyProps;
  onChange: (patch: Partial<ChannelNotifyProps>) => void;
}) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);

  // Close on outside click. Mounting the listener only while open keeps
  // the global listener cost zero in the common case.
  useEffect(() => {
    if (!open) return;
    function onDoc(e: MouseEvent) {
      if (!wrapRef.current) return;
      if (!wrapRef.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [open]);

  const desktop = (props.desktop ?? "all") as DesktopPref;
  const markUnread = (props.mark_unread ?? "all") as MarkUnreadPref;

  return (
    <span className="settings-wrap" ref={wrapRef}>
      <button
        type="button"
        className="settings-gear"
        title="채널 알림 설정"
        aria-label="채널 알림 설정"
        onClick={() => setOpen((v) => !v)}
      >⚙</button>
      {open && (
        <div className="notify-menu" role="dialog" aria-label="알림 설정">
          <div className="notify-section-title">데스크톱 알림</div>
          {(["all", "mentions", "none"] as DesktopPref[]).map((v) => (
            <label key={v} className="notify-radio">
              <input
                type="radio"
                name="desktop"
                checked={desktop === v}
                onChange={() => onChange({ desktop: v })}
              />
              <span>{
                v === "all" ? "모든 새 메시지" :
                v === "mentions" ? "@멘션 또는 DM만" :
                "끄기"
              }</span>
            </label>
          ))}
          <div className="notify-section-title" style={{ marginTop: 10 }}>읽지 않음 표시</div>
          {(["all", "mention"] as MarkUnreadPref[]).map((v) => (
            <label key={v} className="notify-radio">
              <input
                type="radio"
                name="mark_unread"
                checked={markUnread === v}
                onChange={() => onChange({ mark_unread: v })}
              />
              <span>{v === "all" ? "모든 메시지" : "멘션만 (음소거)"}</span>
            </label>
          ))}
        </div>
      )}
    </span>
  );
}

// ---- Phase 19: useDraft (localStorage auto-save) ----
//
// Debounced 500ms auto-save of a controlled textarea value to localStorage.
// Rehydrates on mount if a saved draft exists. Null key disables the hook
// so callers can safely pass `null` when they lack context (e.g. no user
// logged in, no channel focused). `clear` both wipes storage and resets
// the controlled value; `clearSaved` only wipes storage (used on successful
// send where the textarea is being cleared anyway).
function useDraft(
  key: string | null,
  value: string,
  setValue: (v: string) => void,
) {
  const [hasSaved, setHasSaved] = useState(false);
  // Rehydrate on mount / key change. Skipped while the key is null to
  // avoid accidentally overwriting a fresh compose with stale state.
  useEffect(() => {
    if (!key) { setHasSaved(false); return; }
    try {
      const saved = localStorage.getItem(key);
      if (saved) { setValue(saved); setHasSaved(true); }
      else { setHasSaved(false); }
    } catch { /* ignore — localStorage may be disabled */ }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  // Debounced write. Empty value clears the entry so localStorage doesn't
  // fill up with stale keys after the user wipes their draft manually.
  useEffect(() => {
    if (!key) return;
    const t = setTimeout(() => {
      try {
        if (value.trim()) {
          localStorage.setItem(key, value);
          setHasSaved(true);
        } else {
          localStorage.removeItem(key);
          setHasSaved(false);
        }
      } catch { /* ignore */ }
    }, 500);
    return () => clearTimeout(t);
  }, [key, value]);
  function clearSaved() {
    if (!key) return;
    try { localStorage.removeItem(key); } catch { /* ignore */ }
    setHasSaved(false);
  }
  function clear() {
    clearSaved();
    setValue("");
  }
  return { hasSaved, clear, clearSaved };
}

// ---- Phase 19: ScheduledPostsView ----
//
// Pseudo-channel listing the caller's pending scheduled messages. Each
// row shows target channel + send_at + excerpt; cancel removes the row
// optimistically (the backend WS event refreshes the list authoritatively).
type ScheduledPostsViewProps = {
  items: import("@/api/client").ScheduledPost[];
  channels: Channel[];
  loading: boolean;
  onClose: () => void;
  onReload: () => void;
  onCancel: (id: string) => void;
  onJumpToChannel: (channelId: string) => void;
};

function ScheduledPostsView(props: ScheduledPostsViewProps) {
  const { items, channels, loading, onClose, onReload, onCancel, onJumpToChannel } = props;
  const sorted = [...items].sort((a, b) => a.send_at - b.send_at);
  return (
    <>
      <header className="chat-header">
        <div className="chat-header-left">
          <h2 className="chat-header-title">🕐 예약됨</h2>
        </div>
        <div style={{ display: "flex", gap: 6 }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 12px", height: 32 }}
            onClick={onReload}
            title="목록 새로고침"
          >↻</button>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 12px", height: 32 }}
            onClick={onClose}
          >닫기</button>
        </div>
      </header>
      <div className="chat-messages">
        {loading ? (
          <div className="chat-empty">불러오는 중…</div>
        ) : sorted.length === 0 ? (
          <div className="chat-empty">
            예약된 메시지가 없습니다. 메시지 입력창의 🕐 버튼으로 예약할 수 있습니다.
          </div>
        ) : (
          sorted.map((s) => {
            const ch = channels.find((c) => c.id === s.channel_id);
            const when = new Date(s.send_at);
            return (
              <div key={s.id} className="scheduled-row">
                <div className="scheduled-row-head">
                  <button
                    type="button"
                    className="msg-channel-chip"
                    onClick={() => onJumpToChannel(s.channel_id)}
                    title="이 채널로 이동"
                  >
                    #{ch?.display_name ?? s.channel_id.slice(0, 8)}
                  </button>
                  <time className="scheduled-row-time">
                    {when.toLocaleString()}
                  </time>
                </div>
                <div className="scheduled-row-body">{s.message}</div>
                {s.error_text && (
                  <div className="scheduled-row-error">
                    전송 실패: {s.error_text}
                  </div>
                )}
                <div className="scheduled-row-actions">
                  <button
                    type="button"
                    className="btn-ghost"
                    style={{ width: "auto", padding: "0 12px", height: 28 }}
                    onClick={() => onCancel(s.id)}
                  >취소</button>
                </div>
              </div>
            );
          })
        )}
      </div>
    </>
  );
}

// ---- Phase 19: ScheduleModal ----
//
// Quick-preset buttons plus a free-form <input type="datetime-local"> for
// custom targets. Server enforces a must-be-in-future check (with 30s
// skew); we mirror that client-side so the error isn't a round-trip away.
type ScheduleModalProps = {
  channelName: string;
  messagePreview: string;
  onCancel: () => void;
  onConfirm: (sendAtMs: number) => Promise<boolean>;
};

function ScheduleModal({ channelName, messagePreview, onCancel, onConfirm }: ScheduleModalProps) {
  useEscClose(true, onCancel);
  const [custom, setCustom] = useState<string>(() => {
    // Seed the datetime-local with now + 15 min rounded so the first
    // interaction is "just submit" — avoids the empty-input trap.
    const d = new Date(Date.now() + 15 * 60 * 1000);
    d.setSeconds(0, 0);
    // datetime-local expects YYYY-MM-DDTHH:mm in local time
    const pad = (n: number) => n.toString().padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Preset helpers. "Tomorrow 9am" / "Next Monday 9am" are computed off
  // the user's local clock — the server stores absolute epoch-ms so tz
  // drift between client and server doesn't matter for correctness.
  function tomorrow9am(): number {
    const d = new Date();
    d.setDate(d.getDate() + 1);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }
  function nextMonday9am(): number {
    const d = new Date();
    // 0 = Sunday, 1 = Monday, ... Add 1..7 days to land on next Monday.
    const delta = ((1 - d.getDay() + 7) % 7) || 7;
    d.setDate(d.getDate() + delta);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }
  function hoursFromNow(h: number): number {
    return Date.now() + h * 3600_000;
  }

  async function send(target: number) {
    if (busy) return;
    if (target <= Date.now() - 30_000) {
      setErr("미래 시각을 선택하세요.");
      return;
    }
    setBusy(true);
    setErr(null);
    const ok = await onConfirm(target);
    if (!ok) { setErr("예약 생성에 실패했습니다. 잠시 후 다시 시도하세요."); setBusy(false); }
    // On success the parent unmounts the modal; no local state to reset.
  }

  function onCustomSubmit() {
    const t = new Date(custom).getTime();
    if (Number.isNaN(t)) { setErr("올바른 날짜/시간을 입력하세요."); return; }
    send(t);
  }

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onCancel}>
      <div className="modal-card schedule-modal" onClick={(e) => e.stopPropagation()}>
        <h3 style={{ margin: "0 0 8px" }}>🕐 메시지 예약</h3>
        <div className="schedule-target">
          <span className="channel-hash">#</span>{channelName || "채널"}
        </div>
        <div className="schedule-preview">{messagePreview}</div>
        <div className="schedule-presets">
          <button type="button" className="btn-ghost" disabled={busy}
            onClick={() => send(hoursFromNow(1))}>1시간 후</button>
          <button type="button" className="btn-ghost" disabled={busy}
            onClick={() => send(tomorrow9am())}>내일 오전 9시</button>
          <button type="button" className="btn-ghost" disabled={busy}
            onClick={() => send(nextMonday9am())}>다음 주 월요일 오전 9시</button>
        </div>
        <div className="schedule-custom">
          <label>사용자 지정</label>
          <input
            type="datetime-local"
            className="field-input"
            value={custom}
            onChange={(e) => setCustom(e.target.value)}
          />
          <button
            type="button"
            className="btn-primary"
            style={{ width: "auto", padding: "0 14px", height: 36 }}
            onClick={onCustomSubmit}
            disabled={busy}
          >{busy ? "예약 중…" : "예약"}</button>
        </div>
        {err && <div className="login-error">{err}</div>}
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 10 }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 14px", height: 34 }}
            onClick={onCancel}
            disabled={busy}
          >닫기</button>
        </div>
      </div>
    </div>
  );
}

// ---- Phase 19: ReminderPopover ----
//
// Fixed center card with preset buttons for common remind-at targets
// plus a datetime-local fallback. Calls onConfirm with epoch-ms;
// onConfirm returns a bool so we can keep the popover open on server
// failure.
type ReminderPopoverProps = {
  postId: string;
  onCancel: () => void;
  onConfirm: (postId: string, remindAtMs: number) => Promise<boolean>;
};

function ReminderPopover({ postId, onCancel, onConfirm }: ReminderPopoverProps) {
  useEscClose(true, onCancel);
  const [custom, setCustom] = useState<string>(() => {
    const d = new Date(Date.now() + 60 * 60 * 1000);
    d.setSeconds(0, 0);
    const pad = (n: number) => n.toString().padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function minutesFromNow(m: number): number {
    return Date.now() + m * 60_000;
  }
  function tomorrow9am(): number {
    const d = new Date();
    d.setDate(d.getDate() + 1);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }
  function nextMonday9am(): number {
    const d = new Date();
    const delta = ((1 - d.getDay() + 7) % 7) || 7;
    d.setDate(d.getDate() + delta);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }

  async function send(target: number) {
    if (busy) return;
    if (target <= Date.now() - 30_000) {
      setErr("미래 시각을 선택하세요.");
      return;
    }
    setBusy(true);
    setErr(null);
    const ok = await onConfirm(postId, target);
    if (!ok) { setErr("리마인더 생성에 실패했습니다."); setBusy(false); }
  }

  function onCustomSubmit() {
    const t = new Date(custom).getTime();
    if (Number.isNaN(t)) { setErr("올바른 날짜/시간을 입력하세요."); return; }
    send(t);
  }

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onCancel}>
      <div className="modal-card reminder-popover" onClick={(e) => e.stopPropagation()}>
        <h3 style={{ margin: "0 0 10px" }}>🔔 리마인더 설정</h3>
        <div className="schedule-presets">
          <button type="button" className="btn-ghost" disabled={busy}
            onClick={() => send(minutesFromNow(30))}>30분 후</button>
          <button type="button" className="btn-ghost" disabled={busy}
            onClick={() => send(minutesFromNow(60))}>1시간 후</button>
          <button type="button" className="btn-ghost" disabled={busy}
            onClick={() => send(tomorrow9am())}>내일 오전 9시</button>
          <button type="button" className="btn-ghost" disabled={busy}
            onClick={() => send(nextMonday9am())}>다음 주 월요일 오전 9시</button>
        </div>
        <div className="schedule-custom">
          <label>사용자 지정</label>
          <input
            type="datetime-local"
            className="field-input"
            value={custom}
            onChange={(e) => setCustom(e.target.value)}
          />
          <button
            type="button"
            className="btn-primary"
            style={{ width: "auto", padding: "0 14px", height: 36 }}
            onClick={onCustomSubmit}
            disabled={busy}
          >{busy ? "설정 중…" : "설정"}</button>
        </div>
        {err && <div className="login-error">{err}</div>}
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 10 }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 14px", height: 34 }}
            onClick={onCancel}
            disabled={busy}
          >닫기</button>
        </div>
      </div>
    </div>
  );
}

// ---- Helpers ----

function slug(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40) || `x-${Date.now()}`;
}

function formatTime(ms: number): string {
  const d = new Date(ms);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function color(id: string): string {
  const palette = ["#6366f1", "#8b5cf6", "#ec4899", "#f59e0b", "#10b981", "#06b6d4"];
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return palette[h % palette.length];
}

function humanSize(bytes: number): string {
  if (!bytes) return "";
  const units = ["B", "KB", "MB", "GB"];
  let n = bytes;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)}${units[i]}`;
}

// `posted` event delivers `mentions` as a JSON-encoded string of user IDs
// (server-side convention to keep the envelope flat). Parse defensively.
function parseMentionIDs(raw: unknown): string[] {
  if (!raw) return [];
  if (Array.isArray(raw)) return raw.map(String);
  if (typeof raw === "string") {
    try {
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) return parsed.map(String);
    } catch { /* ignore */ }
  }
  return [];
}

// DM channels are named "<sortedIdA>__<sortedIdB>"; return the other participant.
function dmCounterpart(name: string, me: string): string {
  const [a, b] = name.split("__");
  if (!b) return a ?? "";
  if (a === me) return b;
  return a;
}

// Map a subset of emoji names to characters for the quick palette.
const EMOJI_MAP: Record<string, string> = {
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
function emojiChar(name: string): string {
  return EMOJI_MAP[name] ?? `:${name}:`;
}

// Phase 16 — session management drawer. Lists the user's live sessions
// (IP-ish device_id + expiry) with revoke buttons per-row and a "kill all
// other devices" catch-all at the bottom. The current row is tagged by
// the server via `is_current` (matches the JWT behind the request); we
// don't ship the bearer token to the client for comparison.
function SessionManagerModal({
  sessions,
  loading,
  onRevoke,
  onRevokeOthers,
  onClose,
}: {
  sessions: SessionRow[];
  loading: boolean;
  onRevoke: (sessionId: string) => void;
  onRevokeOthers: () => void;
  onClose: () => void;
}) {
  useEscClose(true, onClose);
  const others = sessions.filter((s) => !s.is_current).length;
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 520 }}>
        <header className="integrations-header">
          <h3 style={{ margin: 0 }}>세션 관리</h3>
          <button type="button" className="action-btn" onClick={onClose} title="닫기">✕</button>
        </header>
        <div style={{ padding: "4px 0 10px", color: "var(--muted)", fontSize: 13 }}>
          이 계정으로 로그인한 모든 기기의 세션입니다. 의심스러운 세션이 있다면 즉시 종료하세요.
        </div>
        {loading ? (
          <div className="chat-empty" style={{ padding: 14 }}>불러오는 중…</div>
        ) : sessions.length === 0 ? (
          <div className="chat-empty" style={{ padding: 14 }}>활성 세션이 없습니다.</div>
        ) : (
          <ul className="integrations-list">
            {sessions.map((s) => (
              <li key={s.id} className="integrations-row">
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontWeight: 600 }}>
                    {s.is_current ? "이 기기" : (s.device_id || "알 수 없는 기기")}
                  </div>
                  <div style={{ color: "var(--muted)", fontSize: 13 }}>
                    생성 {new Date(s.create_at).toLocaleString()}
                    {" · 만료 "}
                    {new Date(s.expires_at).toLocaleString()}
                  </div>
                </div>
                <button
                  type="button"
                  className="btn-ghost"
                  style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                  onClick={() => onRevoke(s.id)}
                >종료</button>
              </li>
            ))}
          </ul>
        )}
        <div style={{ marginTop: 12, display: "flex", justifyContent: "flex-end" }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 14px", height: 36, color: "var(--danger)" }}
            onClick={onRevokeOthers}
            disabled={others === 0}
            title={others === 0 ? "다른 기기 세션이 없습니다" : ""}
          >
            다른 모든 기기 로그아웃{others > 0 ? ` (${others})` : ""}
          </button>
        </div>
      </div>
    </div>
  );
}

// ---- Phase 21: Quick Switcher (Cmd+K) ----
//
// Mattermost-style keyboard switcher. Two parallel autocomplete sources
// (channels in the current team + users globally) merged into one selectable
// list. Empty input shows the user's most recent channels so the modal is
// useful even before typing.
//
// Implementation details:
//   - 120ms debounce on the input. Keeps requests sane while still feeling
//     instant under typical typing.
//   - Sequence-guarded: only the latest in-flight result lands in state.
//   - Arrow keys cycle, Enter selects, Esc closes (via useEscClose).
//   - Already-known users from the parent's `users` map render with avatars
//     even before the autocomplete result populates.
type QuickSwitcherEntry =
  | { kind: "channel"; channel: Channel }
  | { kind: "user"; user: User };

function QuickSwitcherModal({
  token,
  teamId,
  channels,
  users,
  meId,
  onClose,
  onPickChannel,
  onPickUser,
}: {
  token: string;
  teamId: string | null;
  channels: Channel[];
  users: UsersMap;
  meId: string;
  onClose: () => void;
  onPickChannel: (channelId: string) => void;
  onPickUser: (user: User) => void;
}) {
  useEscClose(true, onClose);
  const [query, setQuery] = useState("");
  const [channelHits, setChannelHits] = useState<Channel[]>([]);
  const [userHits, setUserHits] = useState<User[]>([]);
  const [activeIdx, setActiveIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const reqSeqRef = useRef(0);

  // Initial focus + initial channel suggestions (recent joined channels).
  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  // Debounced fetch. Empty query falls back to the local channel list so
  // the modal isn't blank on open.
  useEffect(() => {
    const term = query.trim();
    if (!term) {
      // Up to 8 most recent channels the user belongs to.
      setChannelHits(channels.slice(0, 8));
      setUserHits([]);
      setActiveIdx(0);
      return;
    }
    const seq = ++reqSeqRef.current;
    const handle = setTimeout(async () => {
      try {
        const [chs, ures] = await Promise.all([
          teamId ? compatApi.autocompleteChannels(token, teamId, term).catch(() => []) : Promise.resolve([] as Channel[]),
          compatApi.autocompleteUsers(token, term, 10).catch(() => ({ users: [] as User[], out_of_channel: [] as User[] })),
        ]);
        if (reqSeqRef.current !== seq) return;
        setChannelHits(chs);
        setUserHits(ures.users.filter((u) => u.id !== meId));
        setActiveIdx(0);
      } catch {
        if (reqSeqRef.current !== seq) return;
        setChannelHits([]);
        setUserHits([]);
      }
    }, 120);
    return () => clearTimeout(handle);
  }, [query, token, teamId, channels, meId]);

  const entries = useMemo<QuickSwitcherEntry[]>(() => {
    return [
      ...channelHits.map((c) => ({ kind: "channel" as const, channel: c })),
      ...userHits.map((u) => ({ kind: "user" as const, user: u })),
    ];
  }, [channelHits, userHits]);

  // Clamp the cursor whenever the result list shrinks under it.
  useEffect(() => {
    if (activeIdx >= entries.length) setActiveIdx(Math.max(0, entries.length - 1));
  }, [entries.length, activeIdx]);

  const choose = useCallback(
    (e: QuickSwitcherEntry) => {
      if (e.kind === "channel") onPickChannel(e.channel.id);
      else onPickUser(e.user);
    },
    [onPickChannel, onPickUser],
  );

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal-card quick-switcher"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="빠른 이동"
      >
        <input
          ref={inputRef}
          className="quick-switcher-input"
          type="text"
          placeholder="채널이나 사용자를 입력하세요…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setActiveIdx((i) => Math.min(entries.length - 1, i + 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setActiveIdx((i) => Math.max(0, i - 1));
            } else if (e.key === "Enter") {
              e.preventDefault();
              const entry = entries[activeIdx];
              if (entry) choose(entry);
            }
          }}
        />
        <ul className="quick-switcher-list" role="listbox">
          {entries.length === 0 && (
            <li className="quick-switcher-empty">결과 없음</li>
          )}
          {entries.map((entry, idx) => {
            const active = idx === activeIdx;
            if (entry.kind === "channel") {
              const c = entry.channel;
              const symbol = c.type === "P" ? "🔒" : c.type === "D" ? "👤" : c.type === "G" ? "👥" : "#";
              return (
                <li
                  key={"ch-" + c.id}
                  className={"quick-switcher-row" + (active ? " active" : "")}
                  role="option"
                  aria-selected={active}
                  onMouseEnter={() => setActiveIdx(idx)}
                  onMouseDown={(ev) => { ev.preventDefault(); choose(entry); }}
                >
                  <span className="quick-switcher-icon">{symbol}</span>
                  <span className="quick-switcher-name">{c.display_name || c.name}</span>
                  <span className="quick-switcher-sub">채널</span>
                </li>
              );
            }
            const u = entry.user;
            const cached = users[u.id];
            const display = u.username + (cached?.username && cached.username !== u.username ? ` (${cached.username})` : "");
            return (
              <li
                key={"u-" + u.id}
                className={"quick-switcher-row" + (active ? " active" : "")}
                role="option"
                aria-selected={active}
                onMouseEnter={() => setActiveIdx(idx)}
                onMouseDown={(ev) => { ev.preventDefault(); choose(entry); }}
              >
                <span className="quick-switcher-icon">@</span>
                <span className="quick-switcher-name">{display}</span>
                <span className="quick-switcher-sub">DM</span>
              </li>
            );
          })}
        </ul>
        <div className="quick-switcher-hint">
          ↑↓ 이동 · Enter 선택 · Esc 닫기
        </div>
      </div>
    </div>
  );
}

// Phase 22 — user-level notify_props panel. Persists through PUT
// /users/me/notify_props which writes the full map atomically. Each row in
// the form is a string→string entry — Mattermost's contract intentionally
// never types the values past TEXT so future provider plugins can extend
// the map without a migration. We surface the four most actioned keys with
// dropdowns and let everything else round-trip untouched.
function NotifyPrefsModal({ token, onClose }: { token: string; onClose: () => void }) {
  const [props, setProps] = useState<UserNotifyProps>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEscClose(true, onClose);
  useEffect(() => {
    let cancelled = false;
    notifyApi
      .get(token)
      .then((p) => { if (!cancelled) setProps(p ?? {}); })
      .catch((e) => { if (!cancelled) setError((e as Error).message); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [token]);
  const update = (key: string, value: string) =>
    setProps((prev) => ({ ...prev, [key]: value }));
  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const next = await notifyApi.put(token, props);
      setProps(next ?? props);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal>
        <h3 style={{ margin: "0 0 16px" }}>알림 설정</h3>
        {loading ? (
          <div style={{ color: "var(--muted)" }}>불러오는 중…</div>
        ) : (
          <div style={{ display: "grid", gap: 12 }}>
            <label className="notify-row">
              <span>데스크톱 알림</span>
              <select value={props.desktop ?? "mention"} onChange={(e) => update("desktop", e.target.value)}>
                <option value="all">모든 메시지</option>
                <option value="mention">멘션 + DM만</option>
                <option value="none">받지 않기</option>
              </select>
            </label>
            <label className="notify-row">
              <span>알림음</span>
              <select value={props.desktop_sound ?? "true"} onChange={(e) => update("desktop_sound", e.target.value)}>
                <option value="true">사용</option>
                <option value="false">사용 안 함</option>
              </select>
            </label>
            <label className="notify-row">
              <span>이메일 알림</span>
              <select value={props.email ?? "true"} onChange={(e) => update("email", e.target.value)}>
                <option value="true">사용</option>
                <option value="false">사용 안 함</option>
              </select>
            </label>
            <label className="notify-row">
              <span>이름 멘션 강조</span>
              <select value={props.first_name ?? "false"} onChange={(e) => update("first_name", e.target.value)}>
                <option value="true">사용</option>
                <option value="false">사용 안 함</option>
              </select>
            </label>
            <label className="notify-row">
              <span>채널 전체 호출 (@channel)</span>
              <select value={props.channel ?? "true"} onChange={(e) => update("channel", e.target.value)}>
                <option value="true">받기</option>
                <option value="false">무시</option>
              </select>
            </label>
            <label className="notify-row">
              <span>강조 키워드 (쉼표 구분)</span>
              <input
                type="text"
                value={props.mention_keys ?? ""}
                onChange={(e) => update("mention_keys", e.target.value)}
                placeholder="배포, 긴급"
                style={{ flex: 1 }}
              />
            </label>
          </div>
        )}
        {error && (
          <div style={{ color: "var(--danger)", marginTop: 12, fontSize: 13 }}>{error}</div>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 16 }}>
          <button type="button" className="btn-ghost" onClick={onClose}>취소</button>
          <button
            type="button"
            className="btn-primary"
            onClick={save}
            disabled={loading || saving}
          >
            {saving ? "저장 중…" : "저장"}
          </button>
        </div>
      </div>
    </div>
  );
}

// Phase 33 — Custom profile attributes drawer. Renders the admin-curated
// field defs and the caller's per-field values. Two views side-by-side:
//
//   Left:  the user's own value form (always visible).
//   Right: the admin field-management form (only visible to system_admins).
//
// Field defs are global so a non-admin user only sees the value form. The
// modal lazy-loads on open and refetches both fields and values together
// so a freshly-added admin field shows up immediately for users who happen
// to have the modal open.
function CustomProfileFieldsModal({
  token,
  isAdmin,
  onClose,
}: {
  token: string;
  isAdmin: boolean;
  onClose: () => void;
}) {
  const [fields, setFields] = useState<CustomProfileField[]>([]);
  const [values, setValues] = useState<CustomProfileValues>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Admin-mode toggle: when true, render the field-defs editor instead of
  // (or rather, alongside) the value form. Hidden entirely for non-admins.
  const [adminMode, setAdminMode] = useState(false);
  // New-field form. Stored separately so the user can type a name + pick a
  // type before the row is committed.
  const [newName, setNewName] = useState("");
  const [newType, setNewType] = useState<string>("text");
  useEscClose(true, onClose);

  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const [fs, vs] = await Promise.all([
        customProfileApi.listFields(token),
        customProfileApi.getUserValues(token),
      ]);
      setFields(Array.isArray(fs) ? fs : []);
      setValues(vs ?? {});
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [fs, vs] = await Promise.all([
          customProfileApi.listFields(token),
          customProfileApi.getUserValues(token),
        ]);
        if (cancelled) return;
        setFields(Array.isArray(fs) ? fs : []);
        setValues(vs ?? {});
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [token]);

  const updateValue = (fieldId: string, raw: unknown) =>
    setValues((prev) => ({ ...prev, [fieldId]: raw }));

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      // Only PATCH the keys that map to current field defs — drops orphan
      // entries from a since-deleted field so the next reload starts clean.
      const filtered: CustomProfileValues = {};
      for (const f of fields) {
        if (Object.prototype.hasOwnProperty.call(values, f.id)) {
          filtered[f.id] = values[f.id];
        }
      }
      const next = await customProfileApi.patchMyValues(token, filtered);
      setValues(next ?? filtered);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const createField = async () => {
    const name = newName.trim();
    if (!name) return;
    setSaving(true);
    try {
      await customProfileApi.createField(token, { name, type: newType });
      setNewName("");
      setNewType("text");
      await reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const deleteField = async (id: string) => {
    setSaving(true);
    try {
      await customProfileApi.deleteField(token, id);
      await reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal style={{ maxWidth: 560 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 12 }}>
          <h3 style={{ margin: 0 }}>프로필 항목</h3>
          {isAdmin && (
            <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13 }}>
              <input
                type="checkbox"
                checked={adminMode}
                onChange={(e) => setAdminMode(e.target.checked)}
              />
              필드 관리 (관리자)
            </label>
          )}
        </div>
        {loading ? (
          <div style={{ color: "var(--muted)" }}>불러오는 중…</div>
        ) : adminMode ? (
          <div style={{ display: "grid", gap: 10 }}>
            {fields.length === 0 ? (
              <div style={{ color: "var(--muted)", fontSize: 13 }}>
                정의된 항목이 없습니다. 아래에서 새 항목을 추가하세요.
              </div>
            ) : (
              fields.map((f) => (
                <div key={f.id} style={{ display: "flex", alignItems: "center", gap: 8 }}>
                  <strong style={{ flex: 1 }}>{f.name}</strong>
                  <span style={{ color: "var(--muted)", fontSize: 13 }}>{f.type}</span>
                  <button
                    type="button"
                    className="btn-ghost"
                    style={{ fontSize: 13 }}
                    onClick={() => void deleteField(f.id)}
                    disabled={saving}
                  >
                    삭제
                  </button>
                </div>
              ))
            )}
            <div style={{ marginTop: 12, paddingTop: 12, borderTop: "1px solid var(--border)" }}>
              <div style={{ fontSize: 13, color: "var(--muted)", marginBottom: 6 }}>새 항목 추가</div>
              <div style={{ display: "flex", gap: 6 }}>
                <input
                  type="text"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="이름 (예: 부서)"
                  style={{ flex: 1 }}
                />
                <select
                  value={newType}
                  onChange={(e) => setNewType(e.target.value)}
                  style={{ width: 110 }}
                >
                  <option value="text">텍스트</option>
                  <option value="url">URL</option>
                  <option value="phone">전화</option>
                  <option value="select">선택</option>
                  <option value="date">날짜</option>
                </select>
                <button
                  type="button"
                  className="btn-primary"
                  onClick={() => void createField()}
                  disabled={saving || newName.trim() === ""}
                >
                  추가
                </button>
              </div>
            </div>
          </div>
        ) : fields.length === 0 ? (
          <div style={{ color: "var(--muted)", fontSize: 13 }}>
            관리자가 정의한 프로필 항목이 없습니다.
          </div>
        ) : (
          <div style={{ display: "grid", gap: 12 }}>
            {fields.map((f) => {
              const cur = values[f.id];
              const asString = typeof cur === "string" ? cur : cur == null ? "" : String(cur);
              const inputType =
                f.type === "url" ? "url" :
                f.type === "phone" ? "tel" :
                f.type === "date" ? "date" :
                "text";
              return (
                <label key={f.id} className="notify-row">
                  <span>{f.name}</span>
                  <input
                    type={inputType}
                    value={asString}
                    onChange={(e) => updateValue(f.id, e.target.value)}
                    style={{ flex: 1 }}
                  />
                </label>
              );
            })}
          </div>
        )}
        {error && (
          <div style={{ color: "var(--danger)", marginTop: 12, fontSize: 13 }}>{error}</div>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 16 }}>
          <button type="button" className="btn-ghost" onClick={onClose}>닫기</button>
          {!adminMode && (
            <button
              type="button"
              className="btn-primary"
              onClick={() => void save()}
              disabled={loading || saving || fields.length === 0}
            >
              {saving ? "저장 중…" : "저장"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// Phase 34 — Mattermost-v11-style user menu rendered as an overlay so it
// floats above the chat-header. The trigger lives in the chat-header right;
// this component handles the dropdown panel + click-outside backdrop +
// Esc-to-close. Every interactive entry is a real button so keyboard
// navigation (Tab + Enter) works out of the box.
function UserMenuOverlay(props: {
  username: string;
  email: string;
  status: UserStatusValue;
  onChangeStatus: (next: UserStatusValue) => void;
  theme: "light" | "dark" | "system";
  onChangeTheme: (next: "light" | "dark" | "system") => void;
  digestEnabled: boolean | null;
  onToggleDigest: (next: boolean) => void;
  uploadingAvatar: boolean;
  onUploadAvatar: () => void;
  onOpenProfileFields: () => void;
  onOpenNotifyPrefs: () => void;
  onOpenSessions: () => void;
  onOpenQuickSwitcher: () => void;
  onOpenPersonalSettings: () => void;
  onOpenMyApprovals: () => void;
  onOpenApprovalReviews: () => void;
  onOpenAdmin: () => void;
  isAdmin: boolean;
  approvalEnabled: boolean;
  version: string;
  buildHash?: string;
  onLogout: () => void;
  onClose: () => void;
}) {
  useEscClose(true, props.onClose);
  const statusLabel: Record<UserStatusValue, string> = {
    online: "온라인",
    away: "자리비움",
    dnd: "방해금지",
    offline: "오프라인",
  };
  return (
    <div className="user-menu-layer" role="presentation">
      {/* Invisible backdrop catches clicks outside the panel and closes
          the menu. Pointer-events on the panel itself stay enabled so
          clicks inside don't propagate to the backdrop. */}
      <button
        type="button"
        className="user-menu-backdrop"
        aria-label="메뉴 닫기"
        onClick={props.onClose}
      />
      <div className="user-menu" role="menu" aria-label="계정 메뉴">
        <div className="user-menu-head">
          <div className="user-menu-name">{props.username || "사용자"}</div>
          {props.email && <div className="user-menu-email">{props.email}</div>}
          <div className="user-menu-current-status" aria-live="polite">
            <span className={`status-dot status-${props.status}`} aria-hidden />
            <span>{statusLabel[props.status]}</span>
          </div>
        </div>

        <div className="user-menu-section">
          <div className="user-menu-section-label">상태 변경</div>
          <div className="user-menu-status-row">
            {(["online", "away", "dnd", "offline"] as UserStatusValue[]).map((s) => (
              <button
                key={s}
                type="button"
                className={`user-menu-status-pill ${props.status === s ? "is-active" : ""} status-${s}`}
                onClick={() => props.onChangeStatus(s)}
                role="menuitemradio"
                aria-checked={props.status === s}
              >
                <span className={`status-dot status-${s}`} aria-hidden />
                <span>{statusLabel[s]}</span>
                {props.status === s && <span className="user-menu-check" aria-hidden>✓</span>}
              </button>
            ))}
          </div>
        </div>

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenPersonalSettings}
        >
          <span className="user-menu-icon">⚙️</span>
          <span className="user-menu-label">
            내 설정
            <small>프로필 · 개인 키 · AI 설정</small>
          </span>
        </button>
        {props.approvalEnabled && (
          <>
            <button
              type="button"
              className="user-menu-item"
              role="menuitem"
              onClick={props.onOpenMyApprovals}
            >
              <span className="user-menu-icon">📋</span>
              <span className="user-menu-label">
                내 승인 요청
                <small>검토 상태와 실행 결과</small>
              </span>
            </button>
            <button
              type="button"
              className="user-menu-item"
              role="menuitem"
              onClick={props.onOpenApprovalReviews}
            >
              <span className="user-menu-icon">✅</span>
              <span className="user-menu-label">
                검토 대기
                <small>승인 · 반려 결정</small>
              </span>
            </button>
          </>
        )}
        {props.isAdmin && (
          <button
            type="button"
            className="user-menu-item"
            role="menuitem"
            onClick={props.onOpenAdmin}
          >
            <span className="user-menu-icon">🛡️</span>
            <span className="user-menu-label">
              서비스 관리
              <small>SSO · AI · 키 정책 · 승인</small>
            </span>
          </button>
        )}

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onUploadAvatar}
          disabled={props.uploadingAvatar}
        >
          <span className="user-menu-icon">🖼️</span>
          <span className="user-menu-label">
            {props.uploadingAvatar ? "업로드 중…" : "프로필 사진 변경"}
            <small>JPG/PNG · 최대 512KB</small>
          </span>
        </button>
        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenProfileFields}
        >
          <span className="user-menu-icon">🪪</span>
          <span className="user-menu-label">
            프로필 항목
            <small>부서 · 전화번호 등 사용자 정의 필드</small>
          </span>
        </button>

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenNotifyPrefs}
        >
          <span className="user-menu-icon">🔔</span>
          <span className="user-menu-label">
            알림 설정
            <small>데스크톱 · 멘션 · 이메일</small>
          </span>
        </button>
        <label className="user-menu-row" role="menuitemcheckbox" aria-checked={props.digestEnabled === true}>
          <span className="user-menu-icon">📧</span>
          <span className="user-menu-label">
            이메일 알림 수신
            <small>하루 한 번 놓친 멘션 요약</small>
          </span>
          <input
            type="checkbox"
            checked={props.digestEnabled === true}
            disabled={props.digestEnabled === null}
            onChange={(e) => props.onToggleDigest(e.target.checked)}
          />
        </label>

        <div className="user-menu-divider" />

        <div className="user-menu-row" role="menuitem">
          <span className="user-menu-icon">🎨</span>
          <span className="user-menu-label">테마</span>
          <select
            className="user-menu-select"
            value={props.theme}
            onChange={(e) => props.onChangeTheme(e.target.value as "light" | "dark" | "system")}
            aria-label="테마 변경"
          >
            <option value="system">시스템</option>
            <option value="light">밝게</option>
            <option value="dark">어둡게</option>
          </select>
        </div>

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenQuickSwitcher}
        >
          <span className="user-menu-icon">🔎</span>
          <span className="user-menu-label">
            빠른 이동
            <small>채널 · 사용자 전환 (Ctrl+K)</small>
          </span>
        </button>
        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenSessions}
        >
          <span className="user-menu-icon">🔐</span>
          <span className="user-menu-label">
            세션 관리
            <small>로그인된 기기 보기 · 종료</small>
          </span>
        </button>

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item user-menu-item-danger"
          role="menuitem"
          onClick={props.onLogout}
        >
          <span className="user-menu-icon">↩</span>
          <span className="user-menu-label">로그아웃</span>
        </button>
        <div className="user-menu-version" aria-label={`서비스 버전 ${props.version}`}>
          <span className="user-menu-version-brand"><BrandMark size={20} />moyro {props.version}</span>
          {props.buildHash && <span>build {props.buildHash.slice(0, 8)}</span>}
        </div>
      </div>
    </div>
  );
}
