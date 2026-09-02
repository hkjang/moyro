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
  type SidebarCategory,
  type Team,
  type UserStatusValue,
} from "@/api/client";
import { useConfirm } from "@/components/shared";
import { useToast } from "@/components/feedback/ToastProvider";
import { useWebsocket } from "@/hooks/useWebsocket";
import { useUnreadTitle } from "@/hooks/useUnreadTitle";
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
import { DateDivider, JumpToLatestButton, UnreadDivider } from "@/features/workspace/messages/TimelineDividers";
import { buildTimeline } from "@/features/workspace/model/timeline";
import { useTimelineScroll } from "@/features/workspace/model/useTimelineScroll";
import { useOlderPosts } from "@/features/workspace/model/useOlderPosts";
import { useThreadPanel } from "@/features/workspace/model/useThreadPanel";
import { TimelineSkeleton } from "@/features/workspace/messages/TimelineSkeleton";
import { ShortcutHelpModal } from "@/features/workspace/dialogs/ShortcutHelpModal";
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
import { boundPostWindow } from "@/features/workspace/model/post-window";
import { useChannelSummary } from "@/features/workspace/model/useChannelSummary";
import { useTypingExpiry } from "@/features/workspace/model/useTypingExpiry";
import { useInboxPreferences } from "@/features/workspace/model/useInboxPreferences";
import { useSessionManager } from "@/features/workspace/model/useSessionManager";
import { useArchivedChannels } from "@/features/workspace/model/useArchivedChannels";
import { usePostActions } from "@/features/workspace/model/usePostActions";
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
  const toast = useToast();

  const [teams, setTeams] = useState<Team[]>([]);
  const [currentTeamId, setCurrentTeamId] = useState<string | null>(routeTeamId ?? null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [channelsLoading, setChannelsLoading] = useState(false);
  const [currentChannelId, setCurrentChannelId] = useState<string | null>(routeChannelId ?? null);
  const [posts, setPosts] = useState<Post[]>([]);
  const [loadingPosts, setLoadingPosts] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Every existing setError call keeps working; the message now surfaces in
  // the shared toast instead of a login-styled banner pinned under the list.
  useEffect(() => {
    if (!error) return;
    toast.error(error);
    setError(null);
  }, [error, toast]);
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
      thread.setPosts((current) => current.some((item) => item.id === post.id)
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
  // The reader's last_viewed_at per channel, refreshed whenever this client
  // marks a channel viewed. Read through a ref when a channel opens so the
  // "새 메시지" marker reflects the moment before viewing zeroes the counters.
  const lastViewedRef = useRef<Record<string, number>>({});
  const unreadRef = useRef(unread);
  useEffect(() => { unreadRef.current = unread; }, [unread]);
  const [unreadMarkerAt, setUnreadMarkerAt] = useState(0);
  useUnreadTitle(
    Object.values(unread).reduce((sum, entry) => sum + entry.mention, 0),
    Object.values(unread).filter((entry) => entry.msg > 0).length,
  );
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

  // Phase 16 — session-management modal, owned by its own hook.
  const sessionManager = useSessionManager({
    token,
    userId: user?.id,
    clearDraftsOnLogout: systemInfo.capabilities?.drafts?.clear_on_logout !== false,
    confirmer,
    onError: setError,
  });

  // Phase 16 — archived channel visibility toggle. Off by default so the
  // sidebar stays lean. When on we re-fetch channels with include_deleted
  // so soft-deleted channels appear dimmed in the sidebar.
  const [showArchived, setShowArchived] = useState(false);

  // Phase 16 — archived-channel list, owned by its own hook. Kept separate
  // from `channels` so the main list stays focused on active rows; rendering
  // merges the two below.
  const { channels: archivedChannels, reload: loadArchivedChannels } = useArchivedChannels({
    token,
    teamId: currentTeamId,
    enabled: showArchived,
    onError: setError,
  });
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
  // The pending-schedule list is kept only so websocket/schedule handlers can
  // update it; the count itself is surfaced by My Work rather than the sidebar.
  const [, setScheduledList] = useState<import("@/api/client").ScheduledPost[]>([]);
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
  const [activeContext, setActiveContext] = useState<WorkspaceContextTab | null>(null);
  const openThreadPanel = useCallback(() => {
    hideActivePluginRHS();
    setActiveContext("thread");
  }, []);
  const currentChannelIdRef = useRef<string | null>(null);
  const thread = useThreadPanel({
    token,
    currentChannelId,
    currentChannelIdRef,
    hydrateUsers: (ids) => { void hydrateUsers(ids); },
    hydrateFiles: (ids) => { void hydrateFiles(ids); },
    setReactionsByPost,
    onOpen: openThreadPanel,
    onError: setError,
  });
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
  const closeContext = useCallback(() => {
    thread.reset();
    resetChannelSummary();
    setActiveContext(null);
  }, [thread.reset, resetChannelSummary]);

  const selectTeam = useCallback((teamId: string) => {
    searchRequestGenerationRef.current += 1;
    setSearchResults(null);
    setSearchFilters({});
    setSearchTotal(0);
    setSearchPage(0);
    closeContext();
    setMobileSidebarOpen(false);
    setCurrentTeamId(teamId);
    setCurrentChannelId(null);
    navigate(`/workspace/${encodeURIComponent(teamId)}`);
  }, [closeContext, navigate]);

  const selectChannelRef = useRef<((channelId: string) => void) | null>(null);
  const selectChannel = useCallback((channelId: string) => {
    if (!currentTeamId) return;
    closeContext();
    setMobileSidebarOpen(false);
    setCurrentChannelId(channelId);
    navigate(`/workspace/${encodeURIComponent(currentTeamId)}/channel/${encodeURIComponent(channelId)}`);
  }, [closeContext, currentTeamId, navigate]);
  useEffect(() => { selectChannelRef.current = selectChannel; }, [selectChannel]);

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
        return;
      }
      const target = e.target as HTMLElement | null;
      const typing = Boolean(target && (
        target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable
      ));
      // "?" is a plain character in a text field; only outside one is it the
      // help shortcut.
      if (e.key === "?" && !typing && !mod && !e.altKey) {
        e.preventDefault();
        setShowShortcutHelp(true);
        return;
      }
      if (e.altKey && !mod && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
        const order = channelOrderRef.current;
        const index = order.indexOf(currentChannelIdRef.current ?? "");
        if (order.length === 0) return;
        const next = e.key === "ArrowUp"
          ? order[(index <= 0 ? order.length : index) - 1]
          : order[(index + 1) % order.length];
        if (next && next !== currentChannelIdRef.current) {
          e.preventDefault();
          selectChannelRef.current?.(next);
        }
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
    setChannelsLoading(true);
    try {
      const c = await api.listChannels(token, teamID);
      if (channelsLoadGenerationRef.current !== generation) return;
      setChannels(c ?? []);
      setChannelsLoading(false);
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
          lastViewedRef.current[m.channel_id] = m.last_viewed_at;
        }
        setUnread(unreadNext);
        setChannelNotify(notifyNext);
      } catch { /* ignore — badges will rebuild from WS events */ }
    } catch (e) {
      if (channelsLoadGenerationRef.current === generation) {
        setChannelsLoading(false);
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
    // Freeze the unread boundary now: the view call below resets the counters,
    // and the marker must reflect what was unread when the reader arrived.
    const hadUnread = (unreadRef.current[channelID]?.msg ?? 0) > 0;
    setUnreadMarkerAt(hadUnread ? (lastViewedRef.current[channelID] ?? 0) : 0);
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
          return boundPostWindow(
            Array.from(merged.values()).sort((left, right) => left.create_at - right.create_at),
          );
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

        // Mark viewed to clear unread. Record the moment locally so a later
        // return to this channel places its marker after what was seen now.
        api.viewChannel(token, channelID).catch(() => undefined);
        lastViewedRef.current[channelID] = Date.now();
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
          return boundPostWindow(Array.from(byId.values()).sort((a, b) => a.create_at - b.create_at));
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
      threadRootIdRef: thread.rootIdRef,
      channelNotifyRef,
      inboxPreferences,
      showArchived,
      hydrateUsers,
      hydrateFiles,
      closeThread,
      loadChannels,
      loadArchivedChannels,
      setPosts,
      setThreadPosts: thread.setPosts,
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
  const timeline = useMemo(
    () => buildTimeline(posts, { unreadSince: unreadMarkerAt, currentUserId: user?.id }),
    [posts, unreadMarkerAt, user?.id],
  );
  const timelinePostIds = useMemo(() => posts.map((post) => post.id), [posts]);
  const olderPosts = useOlderPosts({
    token,
    channelId: currentChannelId,
    currentChannelIdRef,
    posts,
    setPosts,
    hydrateUsers: (ids) => { void hydrateUsers(ids); },
    hydrateFiles: (ids) => { void hydrateFiles(ids); },
    onError: setError,
  });
  const timelineScroll = useTimelineScroll({
    channelId: currentChannelId,
    postIds: timelinePostIds,
    latestAuthorId: posts[posts.length - 1]?.user_id,
    currentUserId: user?.id,
    loading: loadingPosts,
    suspended: Boolean(navigationFocusPostID),
    onReachTop: olderPosts.loadOlder,
  });

  // ↑ on an empty composer opens the author's latest message for editing.
  const [editRequest, setEditRequest] = useState<{ postId: string; seq: number }>({ postId: "", seq: 0 });
  const onEditLastMessage = useCallback((): boolean => {
    if (!user) return false;
    const mine = [...posts].reverse().find((post) => post.user_id === user.id && !post.root_id);
    if (!mine) return false;
    setEditRequest((current) => ({ postId: mine.id, seq: current.seq + 1 }));
    return true;
  }, [posts, user]);

  // Alt+↑/↓ walk the sidebar in its rendered order; read through a ref so the
  // global key handler never closes over a stale list.
  const channelOrderRef = useRef<string[]>([]);
  useEffect(() => {
    channelOrderRef.current = [...favoriteChannels, ...nonFavoritePublic, ...nonFavoriteDM].map((c) => c.id);
  }, [favoriteChannels, nonFavoritePublic, nonFavoriteDM]);
  const [showShortcutHelp, setShowShortcutHelp] = useState(false);

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
      toast.success("채널을 보관했습니다.");
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
      toast.success("채널을 복원했습니다.");
    } catch (e) {
      setError(e instanceof Error ? e.message : "채널 복원 실패");
    }
  }

  // Message-level actions (send incl. slash commands, edit, delete, save)
  // live in their own hook.
  const postActions = usePostActions({
    token,
    teamId: currentTeamId,
    channelId: currentChannelId,
    currentChannelIdRef,
    setPosts,
    savedIds,
    setSavedIds,
    confirmer,
    onError: setError,
    onNotice: toast.success,
  });


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
      const root = thread.posts.find((p) => p.id === rootId) ?? posts.find((p) => p.id === rootId);
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
      toast.success("메시지를 예약했습니다.");
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
      toast.success("리마인더를 설정했습니다.");
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
  const openThread = thread.open;
  function closeThread() {
    closeContext();
  }
  const onReplyInThread = thread.reply;

  // ---- Render ----
  return (
    <WorkspaceShell
      mobileSidebarOpen={mobileSidebarOpen}
      onOpenMobileSidebar={() => setMobileSidebarOpen(true)}
      onCloseMobileSidebar={() => setMobileSidebarOpen(false)}
      sidebar={(
        <WorkspaceSidebar
          loading={channelsLoading}
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
          showArchived={showArchived}
          isAdmin={isAdmin}
          onSelectTeam={selectTeam}
          onSelectChannel={selectChannel}
          onCreateTeam={onCreateTeam}
          onCreateChannel={onCreateChannel}
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
                    onEdit={postActions.edit}
                    onDelete={postActions.remove}
                    onOpenThread={openThread}
                    isSaved={savedIds.has(p.id)}
                    onToggleSaved={() => postActions.toggleSaved(p)}
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
              <div className="chat-timeline-host">
                <div className="chat-messages chat-messages-grouped" ref={timelineScroll.containerRef}>
                  {loadingPosts ? (
                    <TimelineSkeleton />
                  ) : posts.length === 0 ? (
                    <div className="chat-empty">첫 메시지를 남겨보세요.</div>
                  ) : (
                    <>
                    {olderPosts.loading && (
                      <div className="timeline-history-edge" role="status">이전 메시지를 불러오는 중…</div>
                    )}
                    {olderPosts.exhausted && !olderPosts.loading && (
                      <div className="timeline-history-edge">대화의 시작입니다</div>
                    )}
                    {timeline.map((item) => {
                      if (item.kind === "date") return <DateDivider key={item.key} label={item.label} at={item.at} />;
                      if (item.kind === "unread") return <UnreadDivider key={item.key} />;
                      const p = item.post;
                      return (
                        <MessageItem
                          key={p.id}
                          post={p}
                          continuation={item.continuation}
                          isMe={p.user_id === user?.id}
                          author={users[p.user_id]}
                          status={statuses[p.user_id]}
                          reactions={reactionsByPost[p.id] ?? []}
                          currentUserId={user?.id ?? ""}
                          files={(p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
                          token={token ?? ""}
                          domAnchorId={`channel-post-${p.id}`}
                          isSaved={savedIds.has(p.id)}
                          onToggleSaved={() => postActions.toggleSaved(p)}
                          onToggleReaction={(emoji) => onToggleReaction(p, emoji)}
                          onEdit={postActions.edit}
                          onDelete={postActions.remove}
                          onOpenThread={openThread}
                          onRemindMe={() => setReminderForPostId(p.id)}
                          editRequestSeq={editRequest.postId === p.id ? editRequest.seq : 0}
                        />
                      );
                    })}
                    </>
                  )}
                </div>
                {(timelineScroll.scrolledUp || timelineScroll.pendingCount > 0) && posts.length > 0 && (
                  <JumpToLatestButton count={timelineScroll.pendingCount} onClick={timelineScroll.jumpToLatest} />
                )}
              </div>
            )}

            {!searchResults && (
              <>
                {postActions.commandNotice && (
                  <div className="cmd-notice">
                    <span>{postActions.commandNotice}</span>
                    <button type="button" className="action-btn" onClick={postActions.dismissCommandNotice}>✕</button>
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
                  onSend={postActions.send}
                  onEditLast={onEditLastMessage}
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
            thread: thread.rootId ? (
              <ThreadPanel
                rootId={thread.rootId}
                posts={thread.posts}
                loading={thread.loading}
                users={users}
                statuses={statuses}
                reactionsByPost={reactionsByPost}
                filesByID={filesByID}
                currentUserId={user?.id ?? ""}
                token={token ?? ""}
                onToggleReaction={onToggleReaction}
                onEdit={postActions.edit}
                onDelete={postActions.remove}
                onReply={onReplyInThread}
                onUpload={onUploadFiles}
                onSchedule={onOpenScheduleModalFromThread(thread.rootId)}
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

      {sessionManager.visible && (
        <SessionManagerModal
          sessions={sessionManager.sessions}
          loading={sessionManager.loading}
          onRevoke={sessionManager.revoke}
          onRevokeOthers={sessionManager.revokeOthers}
          onClose={sessionManager.close}
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
      {showShortcutHelp && <ShortcutHelpModal onClose={() => setShowShortcutHelp(false)} />}
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
          onOpenSessions={() => { setShowUserMenu(false); sessionManager.open(); }}
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
