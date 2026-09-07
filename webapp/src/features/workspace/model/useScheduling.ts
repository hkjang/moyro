import { useCallback, useState } from "react";
import { api, type Post, type ScheduledPost } from "@/api/client";

type ScheduleSource = "root" | "thread";

export type ScheduleTarget = {
  channelId: string;
  message: string;
  fileIds: string[];
  /** Set when scheduled from a thread so the post lands back in it. */
  rootId?: string;
  source: ScheduleSource;
};

export type Scheduling = {
  /** The pending schedule dialog, or null when closed. */
  target: ScheduleTarget | null;
  closeSchedule: () => void;
  /** Opens the dialog for the root composer. */
  openForRoot: (message: string, fileIds: string[]) => void;
  /** Returns an opener bound to a thread root for the thread composer. */
  openForThread: (rootId: string) => (message: string, fileIds: string[]) => void;
  /** Creates the scheduled post; true on success. */
  confirm: (sendAt: number) => Promise<boolean>;
  /** Post whose reminder popover is open, or null. */
  reminderForPostId: string | null;
  setReminderForPostId: (postId: string | null) => void;
  createReminder: (postId: string, when: number) => Promise<boolean>;
  /** Kept for socket handlers; the count itself is surfaced by My Work. */
  setScheduledList: React.Dispatch<React.SetStateAction<ScheduledPost[]>>;
};

export type SchedulingOptions = {
  token: string | null;
  currentChannelId: string | null;
  posts: Post[];
  threadPosts: Post[];
  onError: (message: string) => void;
  onNotice: (message: string) => void;
  /** Called after a successful schedule so the originating composer resets. */
  onScheduled: (source: ScheduleSource) => void;
};

/**
 * Scheduled messages and post reminders. Scheduling from a thread resolves
 * the channel from the already loaded root post so the dialog never waits on
 * a fetch; a successful schedule tells the host which composer to clear so
 * the typed text cannot be Enter-sent a second time.
 */
export function useScheduling({
  token,
  currentChannelId,
  posts,
  threadPosts,
  onError,
  onNotice,
  onScheduled,
}: SchedulingOptions): Scheduling {
  const [target, setTarget] = useState<ScheduleTarget | null>(null);
  const [reminderForPostId, setReminderForPostId] = useState<string | null>(null);
  const [, setScheduledList] = useState<ScheduledPost[]>([]);

  const open = useCallback((source: ScheduleSource, message: string, fileIds: string[], rootId?: string) => {
    let channelId: string | null = currentChannelId;
    if (source === "thread" && rootId) {
      const root = threadPosts.find((p) => p.id === rootId) ?? posts.find((p) => p.id === rootId);
      channelId = root?.channel_id ?? currentChannelId;
    }
    if (!channelId) return;
    const trimmed = message.trim();
    if (!trimmed && fileIds.length === 0) {
      onError("메시지를 먼저 입력하세요.");
      return;
    }
    setTarget({ channelId, message: trimmed, fileIds, rootId, source });
  }, [currentChannelId, posts, threadPosts, onError]);

  const openForRoot = useCallback((message: string, fileIds: string[]) => open("root", message, fileIds), [open]);
  const openForThread = useCallback(
    (rootId: string) => (message: string, fileIds: string[]) => open("thread", message, fileIds, rootId),
    [open],
  );

  const confirm = useCallback(async (sendAt: number): Promise<boolean> => {
    if (!token || !target) return false;
    try {
      const scheduled = await api.createScheduledPost(token, {
        channel_id: target.channelId,
        root_id: target.rootId,
        message: target.message,
        file_ids: target.fileIds,
        send_at: sendAt,
      });
      setScheduledList((prev) => [...prev, scheduled].sort((a, b) => a.send_at - b.send_at));
      onScheduled(target.source);
      setTarget(null);
      onNotice("메시지를 예약했습니다.");
      return true;
    } catch (e) {
      onError(e instanceof Error ? e.message : "예약 실패");
      return false;
    }
  }, [token, target, onScheduled, onNotice, onError]);

  const createReminder = useCallback(async (postId: string, when: number): Promise<boolean> => {
    if (!token) return false;
    try {
      await api.createPostReminder(token, postId, when);
      setReminderForPostId(null);
      onNotice("리마인더를 설정했습니다.");
      return true;
    } catch (e) {
      onError(e instanceof Error ? e.message : "리마인더 생성 실패");
      return false;
    }
  }, [token, onNotice, onError]);

  return {
    target,
    closeSchedule: () => setTarget(null),
    openForRoot,
    openForThread,
    confirm,
    reminderForPostId,
    setReminderForPostId,
    createReminder,
    setScheduledList,
  };
}
