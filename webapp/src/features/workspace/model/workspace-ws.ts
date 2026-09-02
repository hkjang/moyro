import type { Dispatch, MutableRefObject, SetStateAction } from "react";
import {
  api,
  type Channel,
  type ChannelNotifyProps,
  type Post,
  type Reaction,
  type ScheduledPost,
  type User,
  type UserStatusValue,
} from "@/api/client";
import { parseMentionIDs } from "@/features/workspace/model/workspace-helpers";
import { appendLivePost } from "@/features/workspace/model/post-window";
import {
  DEFAULT_INBOX_PREFERENCES,
  inboxNotificationsAllowed,
  isPriorityActivity,
  type InboxPreferences,
} from "@/api/inbox-preferences";
import type {
  ReactionMap,
  ReminderToast,
  StatusMap,
  UnreadEntry,
  UsersMap,
} from "@/features/workspace/model/types";

export type WorkspaceWebSocketEvent = {
  event?: string;
  data?: Record<string, unknown>;
};

type Setter<T> = Dispatch<SetStateAction<T>>;

export type WorkspaceWebSocketEventInput = {
  token: string | null;
  user?: User | null;
  channels: Channel[];
  users: UsersMap;
  currentChannelIdRef: MutableRefObject<string | null>;
  threadRootIdRef: MutableRefObject<string | null>;
  channelNotifyRef: MutableRefObject<Record<string, ChannelNotifyProps>>;
  inboxPreferences?: InboxPreferences;
  showArchived: boolean;
  hydrateUsers: (ids: string[]) => void;
  hydrateFiles: (ids: string[]) => void;
  closeThread: () => void;
  loadChannels: () => void;
  loadArchivedChannels: () => void;
  setPosts: Setter<Post[]>;
  setThreadPosts: Setter<Post[]>;
  setReactionsByPost: Setter<ReactionMap>;
  setTypingUsers: Setter<Record<string, number>>;
  setStatuses: Setter<StatusMap>;
  setUnread: Setter<Record<string, UnreadEntry>>;
  setChannelNotify: Setter<Record<string, ChannelNotifyProps>>;
  setChannels: Setter<Channel[]>;
  setCurrentChannelId: Setter<string | null>;
  setSavedIds: Setter<Set<string>>;
  setScheduledList: Setter<ScheduledPost[]>;
  setReminderToasts: Setter<ReminderToast[]>;
};

export function handleWorkspaceWebSocketEvent(
  input: WorkspaceWebSocketEventInput,
  payload: WorkspaceWebSocketEvent,
) {
  const {
    token,
    user,
    channels,
    users,
    currentChannelIdRef,
    threadRootIdRef,
    channelNotifyRef,
    inboxPreferences = DEFAULT_INBOX_PREFERENCES,
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
  } = input;
  const { event, data } = payload;
  if (!event || !data) return;
  switch (event) {
    case "posted": {
      const p: Post = JSON.parse(String(data.post ?? "{}"));
      if (!p.id) return;
      hydrateUsers([p.user_id]);
      hydrateFiles(p.file_ids ?? []);
      if (p.channel_id === currentChannelIdRef.current) {
        setPosts((prev) => appendLivePost(prev, p));
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
      const inboxEventType = isDM ? "direct_message" : mentionsMe ? "mention" : "plugin_event";
      const priority = isPriorityActivity(inboxPreferences, p.user_id, inboxEventType);
      const inFocus = !document.hidden && p.channel_id === currentChannelIdRef.current;
      const pref = channelNotifyRef.current[p.channel_id]?.desktop ?? "all";
      if (
        !authorIsMe &&
        !inFocus &&
        typeof Notification !== "undefined" &&
        Notification.permission === "granted" &&
        pref !== "none" &&
        (pref === "all" || mentionsMe || isDM) &&
        inboxNotificationsAllowed(inboxPreferences, new Date(), priority)
      ) {
        const author = users[p.user_id]?.username ?? "새 메시지";
        const channelLabel = isDM
          ? author
          : (channel ? `#${channel.display_name}` : "채널");
        try {
          const n = new Notification(channelLabel, {
            body: p.message?.slice(0, 140) || "",
            tag: inboxPreferences.bundle_by === "none"
              ? p.id
              : inboxPreferences.bundle_by === "type"
                ? `moyro-${inboxEventType}`
                : p.channel_id,
            icon: "/favicon.ico",
          });
          n.onclick = () => {
            window.focus();
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
      // sockets. The global 내 업무 view owns its own list; workspace only
      // keeps the per-message saved markers synchronized.
      const postId = String(data.post_id ?? "");
      const nowSaved = !!data.saved;
      if (!postId) return;
      setSavedIds((prev) => {
        const next = new Set(prev);
        if (nowSaved) next.add(postId); else next.delete(postId);
        return next;
      });
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
