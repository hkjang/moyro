import { type Dispatch, type MutableRefObject, type SetStateAction, useCallback, useState } from "react";
import { api, type Post } from "@/api/client";
import { appendLivePost } from "@/features/workspace/model/post-window";

type Confirmer = {
  confirm: (options: {
    title: string;
    message: string;
    confirmLabel: string;
    destructive?: boolean;
  }) => Promise<boolean>;
};

/** A message the server refused, kept so the reader can retry or drop it. */
export type FailedSend = {
  id: string;
  channelId: string;
  message: string;
  fileIds: string[];
  reason: string;
  /** True while a retry is in flight. */
  retrying: boolean;
};

export type PostActions = {
  /** Transient slash-command output rendered above the composer. */
  commandNotice: string | null;
  /** Dismisses that banner before its timeout elapses. */
  dismissCommandNotice: () => void;
  /**
   * Returns true when the composer may clear — either the message was sent,
   * or it was captured as a retryable failure shown in the timeline. False
   * means nothing was recorded anywhere and the composer must keep the text.
   */
  send: (message: string, fileIds: string[]) => Promise<boolean>;
  /** Messages the server refused, awaiting a retry in the open channel. */
  failedSends: FailedSend[];
  retryFailed: (id: string) => Promise<void>;
  discardFailed: (id: string) => void;
  /** Sends an emoticon: the caption is the message text, the id rides in props. */
  sendSticker: (stickerId: string, caption: string) => Promise<boolean>;
  edit: (postId: string, message: string) => Promise<boolean>;
  remove: (postId: string) => Promise<void>;
  toggleSaved: (post: Post) => Promise<void>;
};

export type PostActionsOptions = {
  token: string | null;
  teamId: string | null;
  channelId: string | null;
  /**
   * The channel the user is looking at *now*. A send that resolves after the
   * user has switched channels must not append its post to the new channel's
   * list, so the check reads a ref rather than the captured value.
   */
  currentChannelIdRef: MutableRefObject<string | null>;
  setPosts: Dispatch<SetStateAction<Post[]>>;
  savedIds: Set<string>;
  setSavedIds: Dispatch<SetStateAction<Set<string>>>;
  confirmer: Confirmer;
  onError: (message: string) => void;
  /** Optional confirmation channel for actions that otherwise finish silently. */
  onNotice?: (message: string) => void;
};

/** How long the ephemeral slash-command banner stays up. */
const COMMAND_NOTICE_MS = 6000;

/**
 * Message-level actions: send (including slash commands), edit, delete, and
 * the saved-post toggle.
 *
 * Edits and deletes deliberately mutate nothing locally — the server's
 * `post_edited` / `post_deleted` WebSocket events are the single source of
 * truth for those, so applying them here too would risk drift.
 */
export function usePostActions({
  token,
  teamId,
  channelId,
  currentChannelIdRef,
  setPosts,
  savedIds,
  setSavedIds,
  confirmer,
  onError,
  onNotice,
}: PostActionsOptions): PostActions {
  const [commandNotice, setCommandNotice] = useState<string | null>(null);
  const [failedSends, setFailedSends] = useState<FailedSend[]>([]);
  // A failure is scoped to the channel it happened in, so switching away and
  // back does not surface someone else's conversation's failed message.
  const visibleFailedSends = failedSends.filter((entry) => entry.channelId === channelId);

  const send = useCallback(
    async (message: string, fileIds: string[]): Promise<boolean> => {
      if (!token || !channelId) return false;
      const targetChannelId = channelId;
      const trimmed = message.trim();
      if (!trimmed && fileIds.length === 0) return false;

      // Slash-command path — only without attachments. An unknown command
      // falls through and is sent as an ordinary message.
      if (trimmed.startsWith("/") && fileIds.length === 0) {
        try {
          const response = await api.executeCommand(token, teamId ?? "", targetChannelId, trimmed);
          if (response.response_type === "ephemeral") {
            setCommandNotice(response.text);
            setTimeout(() => setCommandNotice(null), COMMAND_NOTICE_MS);
          }
          // in_channel commands produce a server-side post + WS broadcast.
          return true;
        } catch (e) {
          const failure = e instanceof Error ? e.message : "명령 실행 실패";
          if (!failure.includes("unknown")) {
            onError(failure);
            return false;
          }
        }
      }

      try {
        const post = await api.createPost(token, targetChannelId, trimmed, "", fileIds);
        if (currentChannelIdRef.current === targetChannelId) {
          setPosts((prev) => appendLivePost(prev, post));
        }
        return true;
      } catch (e) {
        // Keep the message where the reader can see and retry it rather than
        // leaving it as text in a composer they might navigate away from.
        setFailedSends((prev) => [...prev, {
          id: `failed-${Date.now()}-${prev.length}`,
          channelId: targetChannelId,
          message: trimmed,
          fileIds,
          reason: e instanceof Error ? e.message : "전송 실패",
          retrying: false,
        }]);
        return true;
      }
    },
    [token, teamId, channelId, currentChannelIdRef, setPosts],
  );

  const retryFailed = useCallback(async (id: string) => {
    const entry = failedSends.find((candidate) => candidate.id === id);
    if (!token || !entry || entry.retrying) return;
    setFailedSends((prev) => prev.map((c) => (c.id === id ? { ...c, retrying: true } : c)));
    try {
      const post = await api.createPost(token, entry.channelId, entry.message, "", entry.fileIds);
      setFailedSends((prev) => prev.filter((c) => c.id !== id));
      if (currentChannelIdRef.current === entry.channelId) {
        setPosts((prev) => appendLivePost(prev, post));
      }
    } catch (e) {
      setFailedSends((prev) => prev.map((c) => (c.id === id
        ? { ...c, retrying: false, reason: e instanceof Error ? e.message : "전송 실패" }
        : c)));
    }
  }, [token, failedSends, currentChannelIdRef, setPosts]);

  const discardFailed = useCallback((id: string) => {
    setFailedSends((prev) => prev.filter((entry) => entry.id !== id));
  }, []);

  const sendSticker = useCallback(
    async (stickerId: string, caption: string): Promise<boolean> => {
      if (!token || !channelId) return false;
      const targetChannelId = channelId;
      try {
        const post = await api.createPost(token, targetChannelId, caption, "", [], { sticker: stickerId });
        if (currentChannelIdRef.current === targetChannelId) {
          setPosts((prev) => appendLivePost(prev, post));
        }
        return true;
      } catch (e) {
        onError(e instanceof Error ? e.message : "이모티콘 전송 실패");
        return false;
      }
    },
    [token, channelId, currentChannelIdRef, setPosts, onError],
  );

  const edit = useCallback(
    async (postId: string, message: string): Promise<boolean> => {
      if (!token) return false;
      try {
        await api.updatePost(token, postId, message);
        return true;
      } catch (e) {
        onError(e instanceof Error ? e.message : "수정 실패");
        return false;
      }
    },
    [token, onError],
  );

  const remove = useCallback(
    async (postId: string) => {
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
        onError(e instanceof Error ? e.message : "삭제 실패");
      }
    },
    [token, confirmer, onError],
  );

  // Optimistic so the star responds immediately; the server emits
  // `saved_post_changed` which re-reconciles. Rolled back only on error.
  const toggleSaved = useCallback(
    async (post: Post) => {
      if (!token) return;
      const wasSaved = savedIds.has(post.id);
      const apply = (add: boolean) =>
        setSavedIds((prev) => {
          const next = new Set(prev);
          if (add) next.add(post.id);
          else next.delete(post.id);
          return next;
        });

      apply(!wasSaved);
      try {
        if (wasSaved) await api.unsavePost(token, post.id);
        else await api.savePost(token, post.id);
        onNotice?.(wasSaved ? "저장을 해제했습니다." : "메시지를 저장했습니다. 내 업무에서 볼 수 있습니다.");
      } catch (e) {
        apply(wasSaved);
        onError(e instanceof Error ? e.message : "저장 실패");
      }
    },
    [token, savedIds, setSavedIds, onError, onNotice],
  );

  const dismissCommandNotice = useCallback(() => setCommandNotice(null), []);

  return {
    commandNotice, dismissCommandNotice, send, sendSticker, edit, remove, toggleSaved,
    failedSends: visibleFailedSends, retryFailed, discardFailed,
  };
}
