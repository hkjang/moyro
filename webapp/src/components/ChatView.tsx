import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import type { RootState } from "@/store";
import { clearAuth, setAuth } from "@/store/authSlice";
import { setCurrentChannel, upsertChannel } from "@/store/channelsSlice";
import {
  api,
  compatApi,
  sidebarApi,
  type Channel,
  type ChannelNotifyProps,
  type ChannelStats,
  type FileInfo,
  type OrderedSidebarCategories,
  type Post,
  type PostList,
  type SessionRow,
  type SidebarCategory,
  type Team,
  type UserStatusValue,
} from "@/api/client";
import { useConfirm } from "@/components/shared";
import { useWebsocket } from "@/hooks/useWebsocket";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import { useThemePreference } from "@/features/theme/ThemePreferenceProvider";
import {
  ContextPanel,
  type WorkspaceContextTab,
} from "@/features/workspace/context/ContextPanel";
import {
  ChannelFilesView,
  ChannelInfoView,
  ChannelSummaryView,
  EmptyThreadView,
} from "@/features/workspace/context/ChannelContextViews";
import { ChannelHeader } from "@/features/workspace/header/ChannelHeader";
import { MessageComposer } from "@/features/workspace/composer/MessageComposer";
import { clearMoyroDraftsForUser } from "@/features/workspace/composer/useDraft";
import { MessageItem } from "@/features/workspace/messages/MessageItem";
import type {
  FilesMap,
  ReactionMap,
  StatusMap,
  ReminderToast,
  UnreadEntry,
  UsersMap,
} from "@/features/workspace/model/types";
import { workspaceSlug } from "@/features/workspace/model/workspace-helpers";
import { parseWorkspaceSearchFilters } from "@/features/workspace/model/search";
import { selectChannelFileEntries } from "@/features/workspace/model/selectors";
import { useChannelSummary } from "@/features/workspace/model/useChannelSummary";
import { useTypingExpiry } from "@/features/workspace/model/useTypingExpiry";
import { useInboxPreferences } from "@/features/workspace/model/useInboxPreferences";
import {
  handleWorkspaceWebSocketEvent,
  type WorkspaceWebSocketEvent,
} from "@/features/workspace/model/workspace-ws";
import {
  ChannelDiscoverModal,
  CustomProfileFieldsModal,
  NotifyPrefsModal,
  QuickSwitcherModal,
  SessionManagerModal,
  StartDirectModal,
  UserMenuOverlay,
} from "@/features/workspace/dialogs/WorkspaceDialogs";
import { WorkspaceShell } from "@/features/workspace/shell/WorkspaceShell";
import { ReminderPopover, ScheduleModal } from "@/features/workspace/scheduling/SchedulingDialogs";
import { WorkspaceSidebar } from "@/features/workspace/sidebar/WorkspaceSidebar";
import { ThreadPanel, TypingIndicator } from "@/features/workspace/thread/ThreadPanel";
import { PluginRHSPanel } from "@/plugins/PluginRHSPanel";
import {
  dispatchPluginWebSocketEvent,
  hideActivePluginRHS,
  usePluginRegistryState,
} from "@/plugins/registry";
import { mattermostPluginStore } from "@/plugins/runtime";

export function ChatView() {
  const user = useSelector((s: RootState) => s.auth.user);
  const token = useSelector((s: RootState) => s.auth.token);
  const dispatch = useDispatch();
  const navigate = useNavigate();
  const location = useLocation();
  const { teamId: routeTeamId, channelId: routeChannelId } = useParams<{
    teamId?: string;
    channelId?: string;
  }>();
  const systemInfo = useSystemInfo();
  const adminAccess = useAdminAccess();
  const isAdmin = adminAccess.loaded && adminAccess.hasAdminAccess;
  const hasAIPermission = adminAccess.loaded && adminAccess.can("use_ai");
  const { theme, setTheme } = useThemePreference();
  const pluginRegistry = usePluginRegistryState();
  const activePluginRHS = pluginRegistry.rhsComponents.find(
    (entry) => entry.id === pluginRegistry.activeRhsComponentId,
  );
  const navigationFocusPostID = (() => {
    if (!location.state || typeof location.state !== "object") return "";
    const candidate = (location.state as { focusPostId?: unknown }).focusPostId;
    return typeof candidate === "string" ? candidate : "";
  })();
  const navigationPostLoadRef = useRef("");
  const navigationPostFocusedRef = useRef("");
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
  const inboxPreferences = useInboxPreferences(token);

  const [users, setUsers] = useState<UsersMap>({});
  const [statuses, setStatuses] = useState<StatusMap>({});
  const [reactionsByPost, setReactionsByPost] = useState<ReactionMap>({});
  const [filesByID, setFilesByID] = useState<FilesMap>({});
  const currentTeam = useMemo(
    () => teams.find((team) => team.id === currentTeamId) ?? null,
    [teams, currentTeamId],
  );
  const currentChannel = useMemo(
    () => channels.find((channel) => channel.id === currentChannelId) ?? null,
    [channels, currentChannelId],
  );

  useEffect(() => {
    mattermostPluginStore.updateContext({
      teams,
      currentTeamId,
      users,
      posts,
    });
  }, [currentTeamId, posts, teams, users]);

  useEffect(() => {
    const onPluginPostUpdated = (event: Event) => {
      const post = (event as CustomEvent<unknown>).detail as Post | undefined;
      if (!post?.id || !post.channel_id) return;
      setPosts((current) => current.some((item) => item.id === post.id)
        ? current.map((item) => item.id === post.id ? { ...item, ...post } : item)
        : current);
      setThreadPosts((current) => current.some((item) => item.id === post.id)
        ? current.map((item) => item.id === post.id ? { ...item, ...post } : item)
        : current);
    };
    window.addEventListener("moyro:plugin-post-updated", onPluginPostUpdated);
    return () => window.removeEventListener("moyro:plugin-post-updated", onPluginPostUpdated);
  }, []);

  // Mattermost web plugins receive a read-only-shaped facade backed by these
  // native slices. Keep it synchronized with the workspace's local data.
  useEffect(() => {
    for (const channel of channels) dispatch(upsertChannel(channel));
  }, [channels, dispatch]);
  useEffect(() => {
    dispatch(setCurrentChannel(currentChannelId));
  }, [currentChannelId, dispatch]);

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
  const searchRequestGenerationRef = useRef(0);
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
  const archivedChannelsGenerationRef = useRef(0);
  const [myStatus, setMyStatus] = useState<UserStatusValue>("online");
  // Profile picture upload — ref hits the hidden <input type="file">, flag
  // disables the button while the multipart upload is in flight so a second
  // click can't stack a duplicate request.
  const avatarFileRef = useRef<HTMLInputElement | null>(null);
  const [uploadingAvatar, setUploadingAvatar] = useState(false);

  // `null` keeps the digest toggle disabled until its initial load completes.
  const [digestEnabled, setDigestEnabled] = useState<boolean | null>(null);

  // A Set keeps each saved-post lookup O(1).
  const [savedIds, setSavedIds] = useState<Set<string>>(new Set());
  // Phase 18 — 채널 탐색 modal toggle. Lists public channels not yet
  // joined so users can discover them without an admin invite.
  const [showDiscover, setShowDiscover] = useState(false);

  // Scheduled messages stay global while the modal remembers its composer.
  const [scheduledList, setScheduledList] = useState<import("@/api/client").ScheduledPost[]>([]);
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
  // into <MessageComposer resetSeq=… />; the composer only reacts to *changes*,
  // so initial-mount rehydrate is preserved.
  const [rootComposerResetSeq, setRootComposerResetSeq] = useState(0);
  const [threadComposerResetSeq, setThreadComposerResetSeq] = useState(0);

  // Phase 19 — post reminders. Popover anchored to a MessageItem via post id;
  // only one open at a time so we render a single overlay. `reminderToasts`
  // is a short stack of incoming reminder_fired WS events; each entry is
  // auto-dismissed by a per-id timer but can also be clicked to jump.
  const [reminderForPostId, setReminderForPostId] = useState<string | null>(null);
  const [reminderToasts, setReminderToasts] = useState<ReminderToast[]>([]);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);

  // The right context is deliberately closed whenever the primary workspace
  // target changes. Keeping an old thread open while currentChannelId moves
  // would pair the old root with the new channel for replies and uploads.
  const [threadRootId, setThreadRootId] = useState<string | null>(null);
  const [threadPosts, setThreadPosts] = useState<Post[]>([]);
  const [threadLoading, setThreadLoading] = useState(false);
  const [activeContext, setActiveContext] = useState<WorkspaceContextTab | null>(null);
  const {
    output: channelSummary,
    sources: channelSummarySources,
    generatedAt: channelSummaryGeneratedAt,
    streaming: channelSummaryStreaming,
    error: channelSummaryError,
    preferences: aiPreferences,
    availabilityLoaded: aiAvailabilityLoaded,
    canUseAI,
    statusLabel: aiStatusLabel,
    candidatePosts: summaryCandidatePosts,
    run: runChannelSummary,
    stop: stopChannelSummary,
    reset: resetChannelSummary,
  } = useChannelSummary({
    token,
    channel: currentChannel,
    posts,
    users,
    permissionStateLoaded: adminAccess.loaded,
    hasPermission: hasAIPermission,
  });
  const threadRootIdRef = useRef<string | null>(null);
  const threadLoadGenerationRef = useRef(0);
  useEffect(() => { threadRootIdRef.current = threadRootId; }, [threadRootId]);
  const closeContext = useCallback(() => {
    threadLoadGenerationRef.current += 1;
    threadRootIdRef.current = null;
    resetChannelSummary();
    setActiveContext(null);
    setThreadRootId(null);
    setThreadPosts([]);
    setThreadLoading(false);
  }, [resetChannelSummary]);

  const selectTeam = useCallback((teamId: string) => {
    searchRequestGenerationRef.current += 1;
    archivedChannelsGenerationRef.current += 1;
    setSearchResults(null);
    setSearchFilters({});
    setSearchTotal(0);
    setSearchPage(0);
    setArchivedChannels([]);
    closeContext();
    setMobileSidebarOpen(false);
    setCurrentTeamId(teamId);
    setCurrentChannelId(null);
    navigate(`/workspace/${encodeURIComponent(teamId)}`);
  }, [closeContext, navigate]);

  const selectChannel = useCallback((channelId: string) => {
    if (!currentTeamId) return;
    closeContext();
    setMobileSidebarOpen(false);
    setCurrentChannelId(channelId);
    navigate(`/workspace/${encodeURIComponent(currentTeamId)}/channel/${encodeURIComponent(channelId)}`);
  }, [closeContext, currentTeamId, navigate]);

  // The URL is the durable workspace navigation state. Personal saved and
  // scheduled lists are global /my-work routes and never become pseudo panes.
  useEffect(() => {
    applyingRouteRef.current = true;
    if (routeTeamId) setCurrentTeamId(routeTeamId);
    if (routeChannelId) setCurrentChannelId(routeChannelId);
    else if (routeTeamId) setCurrentChannelId(null);
  }, [routeTeamId, routeChannelId]);

  // Internal events can also change the active entity. Canonicalize those
  // changes back into a route without adding noisy history entries.
  useEffect(() => {
    // ProductShell owns navigation outside the workspace. During a route
    // transition React may run an effect queued by the previous workspace
    // render before ChatView unmounts. Read the browser's current location,
    // rather than that stale render's closure, so a late channel load cannot
    // pull a global destination back to the workspace.
    const currentPathname = window.location.pathname;
    if (!/^\/workspace(?:\/|$)/.test(currentPathname)) return;
    if (applyingRouteRef.current) {
      applyingRouteRef.current = false;
      return;
    }
    if (!currentTeamId) return;
    const teamPath = `/workspace/${encodeURIComponent(currentTeamId)}`;
    const target = currentChannelId
      ? `${teamPath}/channel/${encodeURIComponent(currentChannelId)}`
      : teamPath;
    if (currentPathname !== target) navigate(target, { replace: true });
  }, [currentTeamId, currentChannelId, location.pathname, navigate]);

  // Browser back/forward can change the route without going through one of
  // the selection callbacks above. Reconcile transient workspace surfaces too.
  useEffect(() => {
    closeContext();
    setMobileSidebarOpen(false);
  }, [closeContext, routeTeamId, routeChannelId]);

  // Not every channel transition originates in the router (notification
  // clicks and membership refreshes can also replace the active channel).
  // Track the resolved workspace scope so those paths cannot retain stale
  // thread, summary, file or info state either.
  const workspaceScope = `${currentTeamId ?? ""}:${currentChannelId ?? ""}`;
  const workspaceScopeRef = useRef(workspaceScope);
  useEffect(() => {
    if (workspaceScopeRef.current === workspaceScope) return;
    workspaceScopeRef.current = workspaceScope;
    closeContext();
    setMobileSidebarOpen(false);
  }, [closeContext, workspaceScope]);

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

  const currentChannelIdRef = useRef<string | null>(null);
  useEffect(() => { currentChannelIdRef.current = currentChannelId; }, [currentChannelId]);
  const currentTeamIdRef = useRef<string | null>(currentTeamId);
  useEffect(() => {
    if (currentTeamIdRef.current !== currentTeamId) {
      currentTeamIdRef.current = currentTeamId;
      searchRequestGenerationRef.current += 1;
    }
  }, [currentTeamId]);
  const channelsLoadGenerationRef = useRef(0);
  const postsLoadGenerationRef = useRef(0);

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
    const generation = channelsLoadGenerationRef.current + 1;
    channelsLoadGenerationRef.current = generation;
    if (!token || !currentTeamId) return;
    const teamID = currentTeamId;
    try {
      const c = await api.listChannels(token, teamID);
      if (channelsLoadGenerationRef.current !== generation) return;
      setChannels(c ?? []);
      setCurrentChannelId((prev) => {
        if (prev && (c ?? []).some((x) => x.id === prev)) return prev;
        return (c ?? [])[0]?.id ?? null;
      });
      // Hydrate per-channel unread counts + notify_props in one shot so
      // badges survive reloads without a per-channel fetch storm.
      try {
        const members = await api.listMyChannelMembers(token, teamID);
        if (channelsLoadGenerationRef.current !== generation) return;
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
      if (channelsLoadGenerationRef.current === generation) {
        setError(e instanceof Error ? e.message : "채널 로드 실패");
      }
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
    if (!token || !systemInfo.loaded) { setDigestEnabled(null); return; }
    if (systemInfo.capabilities?.email_digest?.enabled !== true) {
      setDigestEnabled(false);
      return;
    }
    api.getEmailPrefs(token)
      .then((p) => setDigestEnabled(!!p.digest_enabled))
      .catch(() => setDigestEnabled(null));
  }, [systemInfo.capabilities?.email_digest?.enabled, systemInfo.loaded, token]);

  // ---- Load posts (+ reactions + file infos) when channel changes ----
  useEffect(() => {
    const generation = postsLoadGenerationRef.current + 1;
    postsLoadGenerationRef.current = generation;
    if (!token || !currentChannelId) {
      setPosts([]);
      setLoadingPosts(false);
      return undefined;
    }
    const channelID = currentChannelId;
    setPosts([]);
    setLoadingPosts(true);
    api.listPosts(token, channelID)
      .then((list: PostList) => {
        if (postsLoadGenerationRef.current !== generation) return;
        const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
        ordered.reverse(); // newest-first → oldest-first
        setPosts((current) => {
          const merged = new Map(ordered.map((post) => [post.id, post]));
          current
            .filter((post) => post.channel_id === channelID)
            .forEach((post) => merged.set(post.id, post));
          return Array.from(merged.values()).sort((left, right) => left.create_at - right.create_at);
        });

        // Collect unique user IDs and file IDs for a single round-trip each.
        const userIds = Array.from(new Set(ordered.map((p) => p.user_id)));
        const fileIds = Array.from(new Set(ordered.flatMap((p) => p.file_ids ?? [])));
        hydrateUsers(userIds);
        hydrateFiles(fileIds);

        // Pull reactions per post (small N) — fire-and-forget per message.
        ordered.forEach((p) => {
          api.listReactions(token, p.id)
            .then((rs) => {
              if (postsLoadGenerationRef.current === generation) {
                setReactionsByPost((prev) => ({ ...prev, [p.id]: rs ?? [] }));
              }
            })
            .catch(() => { /* ignore */ });
        });

        // Phase 18 — hydrate bookmarked state for the loaded posts in one
        // batch call so the star icon renders filled where applicable.
        // Merges into savedIds (additive) so other channels' state
        // survives the per-channel load.
        if (ordered.length > 0) {
          api.savedPostsByIds(token, ordered.map((p) => p.id))
            .then((m) => {
              if (postsLoadGenerationRef.current !== generation) return;
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
        api.viewChannel(token, channelID).catch(() => undefined);
        setUnread((u) => ({ ...u, [channelID]: { msg: 0, mention: 0 } }));
      })
      .catch((e) => {
        if (postsLoadGenerationRef.current === generation) setError(e.message);
      })
      .finally(() => {
        if (postsLoadGenerationRef.current === generation) setLoadingPosts(false);
      });
    return () => {
      if (postsLoadGenerationRef.current === generation) {
        postsLoadGenerationRef.current += 1;
      }
    };
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
  const handleWSEventRef = useRef(handleWSEvent);
  useLayoutEffect(() => {
    handleWSEventRef.current = handleWSEvent;
  });
  const handleWSMessage = useCallback((ev: MessageEvent) => {
    try {
      const payload = JSON.parse(ev.data as string);
      dispatchPluginWebSocketEvent(payload);
      handleWSEventRef.current(payload);
    } catch { /* ignore malformed frames */ }
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
    let active = true;
    // (1) channels + unread + notify_props: loadChannels already does both.
    if (currentTeamId) {
      loadChannels();
    }
    // (2) posts in the currently open channel — merge by id so optimistic
    // edits / reactions we may have applied locally don't get clobbered.
    const chanID = currentChannelId;
    if (chanID) {
      api.listPosts(token, chanID).then((list: PostList) => {
        if (!active || currentChannelIdRef.current !== chanID) return;
        const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
        ordered.reverse();
        setPosts((prev) => {
          const byId = new Map(
            prev.filter((post) => post.channel_id === chanID).map((post) => [post.id, post]),
          );
          ordered.forEach((p) => byId.set(p.id, p));
          return Array.from(byId.values()).sort((a, b) => a.create_at - b.create_at);
        });
      }).catch(() => undefined);
    }
    return () => { active = false; };
  }, [wsReconnectSeq, token, currentTeamId, currentChannelId, loadChannels]);

  function handleWSEvent(payload: WorkspaceWebSocketEvent) {
    handleWorkspaceWebSocketEvent({
      token,
      user,
      channels,
      users,
      currentChannelIdRef,
      threadRootIdRef,
      channelNotifyRef,
      inboxPreferences,
      showArchived,
      hydrateUsers,
      hydrateFiles,
      closeThread,
      loadChannels,
      loadArchivedChannels,
      setPosts,
      setThreadPosts,
      setReactionsByPost,
      setTypingUsers,
      setStatuses,
      setUnread,
      setChannelNotify,
      setChannels,
      setCurrentChannelId,
      setSavedIds,
      setScheduledList,
      setReminderToasts,
    }, payload);
  }

  useTypingExpiry(setTypingUsers);

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
  const channelFileEntries = useMemo(
    () => selectChannelFileEntries(posts, currentChannelId, filesByID, users),
    [currentChannelId, filesByID, posts, users],
  );

  function openChannelContext(tab: Exclude<WorkspaceContextTab, "thread">) {
    if (!currentChannel) return;
    hideActivePluginRHS();
    setActiveContext(tab);
  }

  function jumpToChannelPost(postId: string) {
    setSearchResults(null);
    // On mobile the context is full-screen, so hide it before locating the
    // source. Preserve the generated summary until an actual scope change or
    // explicit panel close so the user can return to it from the header.
    setActiveContext(null);
    window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        const target = document.getElementById(`channel-post-${postId}`);
        target?.scrollIntoView({ behavior: "smooth", block: "center" });
        target?.focus({ preventScroll: true });
      });
    });
  }

  // Flow surfaces carry a post id in router state. Normal channel paging may
  // not contain an older source, so fetch that exact accessible post before
  // locating it. The server still enforces channel membership on /posts/ids.
  useEffect(() => {
    if (!navigationFocusPostID || !token || !currentChannelId || loadingPosts) return;
    const key = `${currentChannelId}:${navigationFocusPostID}`;
    const target = posts.find((post) => post.id === navigationFocusPostID);
    if (target) {
      if (target.channel_id !== currentChannelId) return;
      if (navigationPostFocusedRef.current === key) return;
      navigationPostLoadRef.current = key;
      navigationPostFocusedRef.current = key;
      jumpToChannelPost(target.id);
      return;
    }
    if (navigationPostLoadRef.current === key) return;
    navigationPostLoadRef.current = key;
    const channelID = currentChannelId;
    let active = true;
    let settled = false;
    void compatApi.postsByIds(token, [navigationFocusPostID]).then(
      (found) => {
        if (!active || currentChannelIdRef.current !== channelID) return;
        settled = true;
        const exact = found.find((post) => post.id === navigationFocusPostID);
        if (!exact || exact.channel_id !== channelID) {
          setError("이 채널에서 원문 메시지를 찾을 수 없습니다.");
          return;
        }
        setPosts((current) => current.some((post) => post.id === exact.id)
          ? current
          : [...current, exact].sort((left, right) => left.create_at - right.create_at));
        hydrateUsers([exact.user_id]);
        hydrateFiles(exact.file_ids ?? []);
      },
      (cause: unknown) => {
        if (active && currentChannelIdRef.current === channelID) {
          settled = true;
          setError(cause instanceof Error ? cause.message : "원문 메시지를 불러오지 못했습니다.");
        }
      },
    );
    return () => {
      active = false;
      // The channel-list effect flips loadingPosts immediately after mount.
      // If that render supersedes this exact lookup before it settles, allow
      // the post-load render to retry instead of leaving the key permanently
      // marked as loaded with no target in state.
      if (!settled && navigationPostLoadRef.current === key) {
        navigationPostLoadRef.current = "";
      }
    };
  }, [currentChannelId, loadingPosts, navigationFocusPostID, posts, token]);


  // ---- Actions ----
  async function onCreateTeam() {
    if (!token) return;
    const display = prompt("팀 이름을 입력하세요 (표시용)");
    if (!display) return;
    try {
      const t = await api.createTeam(token, workspaceSlug(display), display);
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
      const c = await api.createChannel(token, currentTeamId, workspaceSlug(display), display);
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
    const generation = archivedChannelsGenerationRef.current + 1;
    archivedChannelsGenerationRef.current = generation;
    if (!token || !currentTeamId) return;
    const teamID = currentTeamId;
    try {
      const all = await api.listChannels(token, teamID, true);
      if (
        archivedChannelsGenerationRef.current !== generation
        || currentTeamIdRef.current !== teamID
      ) return;
      setArchivedChannels((all ?? []).filter((c) => (c.delete_at ?? 0) > 0));
    } catch (e) {
      if (
        archivedChannelsGenerationRef.current === generation
        && currentTeamIdRef.current === teamID
      ) setError(e instanceof Error ? e.message : "보관 채널 로드 실패");
    }
  }, [token, currentTeamId]);

  useEffect(() => {
    if (!showArchived) {
      archivedChannelsGenerationRef.current += 1;
      setArchivedChannels([]);
      return;
    }
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
      if (killedCurrent) {
        if (user?.id && systemInfo.capabilities?.drafts?.clear_on_logout !== false) {
          clearMoyroDraftsForUser(user.id);
        }
        dispatch(clearAuth());
      }
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

  async function onSendPost(message: string, fileIds: string[]): Promise<boolean> {
    if (!token || !currentChannelId) return false;
    const channelID = currentChannelId;
    const trimmed = message.trim();
    if (!trimmed && fileIds.length === 0) return false;
    // Slash command path — only when there are no attachments and the message
    // starts with "/". Falls back to regular post on the unknown-command 404.
    if (trimmed.startsWith("/") && fileIds.length === 0) {
      try {
        const resp = await api.executeCommand(token, currentTeamId ?? "", channelID, trimmed);
        if (resp.response_type === "ephemeral") {
          setCmdNotice(resp.text);
          setTimeout(() => setCmdNotice(null), 6000);
        }
        // in_channel commands produce a server-side post + WS broadcast; nothing else to do.
        return true;
      } catch (e) {
        const msg = e instanceof Error ? e.message : "명령 실행 실패";
        // If the server says the command is unknown, send the line as a normal message.
        if (!msg.includes("unknown")) {
          setError(msg);
          return false;
        }
      }
    }
    try {
      const p = await api.createPost(token, channelID, trimmed, "", fileIds);
      if (currentChannelIdRef.current === channelID) {
        setPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
      }
      // Pre-hydrate file infos we just uploaded (already in filesByID from uploader).
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : "전송 실패");
      return false;
    }
  }

  async function onEditPost(postId: string, message: string): Promise<boolean> {
    if (!token) return false;
    try {
      await api.updatePost(token, postId, message);
      // State refreshes via post_edited WS event; no local mutation needed.
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : "수정 실패");
      return false;
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

  // Phase 19 — scheduled-posts loader keeps the sidebar badge current.
  const loadScheduledList = useCallback(async () => {
    if (!token) return;
    try {
      const list = await api.listMyScheduledPosts(token);
      setScheduledList(list ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : "예약 메시지 로드 실패");
    }
  }, [token]);

  // Pull the pending queue once at mount so the sidebar count is accurate
  // before the user opens the 예약됨 view. Subsequent changes arrive via
  // the scheduled_post_* WS events wired above.
  useEffect(() => { loadScheduledList(); }, [loadScheduledList]);

  // Open the schedule modal from the MessageComposer. Captures the current
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
      // MessageComposer clears its value/pending/draft after a successful
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
      const uploadError = e instanceof Error ? e : new Error("업로드 실패");
      setError(uploadError.message);
      throw uploadError;
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

  async function onSearch(page = 0) {
    const generation = searchRequestGenerationRef.current + 1;
    searchRequestGenerationRef.current = generation;
    if (!token || !currentTeamId) return;
    const teamID = currentTeamId;
    const q = searchTerm.trim();
    if (!q) {
      setSearchResults(null);
      setSearchFilters({});
      setSearchTotal(0);
      setSearchPage(0);
      return;
    }
    const { terms, filters } = parseWorkspaceSearchFilters(q, users, channels);
    // Searching for only filter tokens (no residual terms) isn't something
    // plainto_tsquery can handle — fall back to the already-filtered
    // listing by stuffing a single space so the server's trim check passes
    // only when there are real terms. Otherwise require one residual word.
    if (!terms.trim()) {
      setError("검색어를 입력하세요 (필터만으로는 검색할 수 없습니다).");
      return;
    }
    try {
      const res = await api.searchPosts(token, teamID, terms, {
        page,
        perPage: 20,
        filters,
      });
      if (
        searchRequestGenerationRef.current !== generation
        || currentTeamIdRef.current !== teamID
      ) return;
      const ordered = (res.order ?? []).map((id) => res.posts[id]).filter(Boolean);
      setSearchResults(ordered);
      setSearchFilters(filters);
      setSearchTotal(res.total_hits ?? ordered.length);
      setSearchPage(page);
      hydrateUsers(Array.from(new Set(ordered.map((p) => p.user_id))));
    } catch (e) {
      if (
        searchRequestGenerationRef.current === generation
        && currentTeamIdRef.current === teamID
      ) setError(e instanceof Error ? e.message : "검색 실패");
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
    hideActivePluginRHS();
    const generation = threadLoadGenerationRef.current + 1;
    threadLoadGenerationRef.current = generation;
    threadRootIdRef.current = rootId;
    setActiveContext("thread");
    setThreadRootId(rootId);
    setThreadPosts([]);
    setThreadLoading(true);
    try {
      const list = await api.listThread(token, rootId);
      if (
        threadLoadGenerationRef.current !== generation
        || threadRootIdRef.current !== rootId
      ) return;
      const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
      setThreadPosts((current) => {
        const merged = new Map(ordered.map((post) => [post.id, post]));
        current
          .filter((post) => (post.root_id || post.id) === rootId)
          .forEach((post) => merged.set(post.id, post));
        return Array.from(merged.values()).sort((left, right) => left.create_at - right.create_at);
      });
      hydrateUsers(Array.from(new Set(ordered.map((p) => p.user_id))));
      hydrateFiles(Array.from(new Set(ordered.flatMap((p) => p.file_ids ?? []))));
      ordered.forEach((p) => {
        api.listReactions(token, p.id)
          .then((rs) => {
            if (
              threadLoadGenerationRef.current === generation
              && threadRootIdRef.current === rootId
            ) {
              setReactionsByPost((prev) => ({ ...prev, [p.id]: rs ?? [] }));
            }
          })
          .catch(() => { /* ignore */ });
      });
    } catch (e) {
      if (
        threadLoadGenerationRef.current === generation
        && threadRootIdRef.current === rootId
      ) setError(e instanceof Error ? e.message : "스레드 로드 실패");
    } finally {
      if (
        threadLoadGenerationRef.current === generation
        && threadRootIdRef.current === rootId
      ) setThreadLoading(false);
    }
  }, [token]);

  function closeThread() {
    closeContext();
  }

  async function onReplyInThread(message: string, fileIds: string[]): Promise<boolean> {
    if (!token || !currentChannelId || !threadRootId) return false;
    const rootID = threadRootId;
    const rootPost = threadPosts.find((post) => post.id === rootID);
    const channelID = rootPost?.channel_id;
    if (!channelID || channelID !== currentChannelId) return false;
    const trimmed = message.trim();
    if (!trimmed && fileIds.length === 0) return false;
    try {
      const p = await api.createPost(token, channelID, trimmed, rootID, fileIds);
      // Thread panel updates via the `posted` WS event; keep the reply
      // visible immediately in case our own broadcast arrives later.
      if (
        currentChannelIdRef.current === channelID
        && threadRootIdRef.current === rootID
      ) {
        setThreadPosts((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
      }
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : "스레드 전송 실패");
      return false;
    }
  }

  // ---- Render ----
  return (
    <WorkspaceShell
      mobileSidebarOpen={mobileSidebarOpen}
      onOpenMobileSidebar={() => setMobileSidebarOpen(true)}
      onCloseMobileSidebar={() => setMobileSidebarOpen(false)}
      sidebar={(
        <WorkspaceSidebar
          token={token}
          currentUser={user ?? null}
          teams={teams}
          currentTeamId={currentTeamId}
          currentChannelId={currentChannelId}
          favoriteChannels={favoriteChannels}
          publicChannels={nonFavoritePublic}
          directChannels={nonFavoriteDM}
          archivedChannels={archivedChannels}
          users={users}
          statuses={statuses}
          unread={unread}
          scheduledCount={scheduledList.length}
          showArchived={showArchived}
          isAdmin={isAdmin}
          onSelectTeam={selectTeam}
          onSelectChannel={selectChannel}
          onCreateTeam={onCreateTeam}
          onCreateChannel={onCreateChannel}
          onOpenSaved={() => navigate("/my-work/saved")}
          onOpenScheduled={() => navigate("/my-work/scheduled")}
          onOpenDiscover={() => setShowDiscover(true)}
          onToggleArchived={() => setShowArchived((value) => !value)}
          onRestoreChannel={onRestoreChannel}
          onOpenDirect={() => setShowStartDM(true)}
          onToggleFavorite={onToggleFavorite}
          onOpenAdmin={() => navigate("/admin/overview")}
          onCloseMobile={() => setMobileSidebarOpen(false)}
        />
      )}
      main={(
      <main className="chat-main workspace-main">
        {wsStatus === "reconnecting" && (
          <div className="ws-reconnect-banner" role="status">
            재연결 중… (시도 {wsAttempts}회)
          </div>
        )}
        {currentChannel ? (
          <>
            <ChannelHeader
              token={token}
              currentUser={user ?? null}
              team={currentTeam}
              channel={currentChannel}
              users={users}
              statuses={statuses}
              status={myStatus}
              stats={channelStatsByID[currentChannel.id]}
              notifyProps={channelNotify[currentChannel.id] ?? { desktop: "all", mark_unread: "all" }}
              isAdmin={isAdmin}
              searchTerm={searchTerm}
              searchOpen={searchResults !== null}
              accountMenuOpen={showUserMenu}
              activeContext={activeContext}
              onChangeNotify={(patch) => onChangeNotify(currentChannel.id, patch)}
              onArchive={() => onArchiveChannel(currentChannel.id)}
              onSearchTermChange={setSearchTerm}
              onSearch={() => onSearch(0)}
              onClearSearch={() => {
                searchRequestGenerationRef.current += 1;
                setSearchResults(null);
                setSearchTerm("");
                setSearchFilters({});
                setSearchTotal(0);
                setSearchPage(0);
              }}
              onToggleAccountMenu={() => setShowUserMenu((value) => !value)}
              onOpenContext={openChannelContext}
            />

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
                  <MessageItem
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
                    <MessageItem
                      key={p.id}
                      post={p}
                      isMe={p.user_id === user?.id}
                      author={users[p.user_id]}
                      status={statuses[p.user_id]}
                      reactions={reactionsByPost[p.id] ?? []}
                      currentUserId={user?.id ?? ""}
                      files={(p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
                      token={token ?? ""}
                      domAnchorId={`channel-post-${p.id}`}
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
                <MessageComposer
                  token={token ?? ""}
                  channelID={currentChannelId}
                  destinationLabel={currentChannel ? `#${currentChannel.display_name}에 전송` : "채널에 전송"}
                  canUseAI={canUseAI}
                  aiPermissionLoaded={aiAvailabilityLoaded}
                  aiStatusLabel={aiStatusLabel}
                  aiPreferences={aiPreferences}
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
      )}
      context={activePluginRHS ? (
        <PluginRHSPanel registration={activePluginRHS} onClose={hideActivePluginRHS} />
      ) : activeContext && currentChannel ? (
        <ContextPanel
          activeTab={activeContext}
          onTabChange={setActiveContext}
          onClose={closeThread}
          panels={{
            thread: threadRootId ? (
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
                destinationLabel={`#${currentChannel.display_name} · 스레드에 답글`}
                canUseAI={canUseAI}
                aiPermissionLoaded={aiAvailabilityLoaded}
                aiStatusLabel={aiStatusLabel}
                aiPreferences={aiPreferences}
              />
            ) : <EmptyThreadView />,
            summary: (
              <ChannelSummaryView
                permissionLoaded={aiAvailabilityLoaded}
                canUseAI={canUseAI}
                unavailableReason={aiStatusLabel}
                availableMessageCount={summaryCandidatePosts.length}
                output={channelSummary}
                sources={channelSummarySources}
                generatedAt={channelSummaryGeneratedAt}
                streaming={channelSummaryStreaming}
                error={channelSummaryError}
                onRun={() => void runChannelSummary()}
                onStop={stopChannelSummary}
                onJumpToPost={jumpToChannelPost}
              />
            ),
            files: (
              <ChannelFilesView
                token={token ?? ""}
                entries={channelFileEntries}
                onJumpToPost={jumpToChannelPost}
              />
            ),
            info: (
              <ChannelInfoView
                channel={currentChannel}
                team={currentTeam}
                stats={channelStatsByID[currentChannel.id]}
              />
            ),
          }}
        />
      ) : undefined}
    >

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

      {/* Phase 19 — schedule modal. Opens from the MessageComposer schedule button;
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

      {/* The hidden input stays mounted while the account menu opens and closes. */}
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

      {/* Render outside the header so the account menu is not clipped. */}
      {showUserMenu && (
        <UserMenuOverlay
          username={user?.username ?? ""}
          email={user?.email ?? ""}
          status={myStatus}
          onChangeStatus={onChangeMyStatus}
          theme={theme}
          onChangeTheme={setTheme}
          digestEnabled={digestEnabled}
          digestAvailable={systemInfo.capabilities?.email_digest?.enabled === true}
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
          onOpenMyApprovals={() => { setShowUserMenu(false); navigate("/approvals/mine"); }}
          onOpenApprovalReviews={() => { setShowUserMenu(false); navigate("/approvals/review"); }}
          onOpenAdmin={() => { setShowUserMenu(false); navigate("/admin/overview"); }}
          isAdmin={isAdmin}
          approvalEnabled={systemInfo.approval_enabled === true}
          version={displayVersion(systemInfo.version)}
          buildHash={systemInfo.build_hash}
          onLogout={async () => {
            setShowUserMenu(false);
            if (token) { try { await api.logout(token); } catch { /* best-effort */ } }
			if (user?.id && systemInfo.capabilities?.drafts?.clear_on_logout !== false) {
				clearMoyroDraftsForUser(user.id);
			}
            dispatch(clearAuth());
          }}
          onClose={() => setShowUserMenu(false)}
        />
      )}

      {/* Phase 20 — shared confirm dialog. Rendered last so its backdrop
          and z-index stack above every other modal in the shell. */}
      {confirmer.render()}
    </WorkspaceShell>
  );
}
