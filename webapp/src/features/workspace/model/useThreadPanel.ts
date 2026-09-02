import { type Dispatch, type MutableRefObject, type SetStateAction, useCallback, useEffect, useRef, useState } from "react";
import { api, type Post, type Reaction } from "@/api/client";

export type ThreadPanel = {
  rootId: string | null;
  posts: Post[];
  loading: boolean;
  /** Current root id for handlers that must not read stale render state. */
  rootIdRef: MutableRefObject<string | null>;
  setPosts: Dispatch<SetStateAction<Post[]>>;
  /** Opens a thread by root id and loads it. */
  open: (rootId: string) => Promise<void>;
  /** Clears thread state and invalidates any load in flight. */
  reset: () => void;
  /** Posts a reply into the open thread; false when it could not be sent. */
  reply: (message: string, fileIds: string[]) => Promise<boolean>;
};

export type ThreadPanelOptions = {
  token: string | null;
  currentChannelId: string | null;
  currentChannelIdRef: MutableRefObject<string | null>;
  hydrateUsers: (ids: string[]) => void;
  hydrateFiles: (ids: string[]) => void;
  setReactionsByPost: Dispatch<SetStateAction<Record<string, Reaction[]>>>;
  /** Invoked before loading so the host can show the panel. */
  onOpen: () => void;
  onError: (message: string) => void;
};

/**
 * Owns the thread side panel: which root is open, its posts, and the reply
 * action. Every load carries a generation counter and re-checks the root it
 * was issued for, so switching threads mid-flight cannot install another
 * thread's replies.
 */
export function useThreadPanel({
  token,
  currentChannelId,
  currentChannelIdRef,
  hydrateUsers,
  hydrateFiles,
  setReactionsByPost,
  onOpen,
  onError,
}: ThreadPanelOptions): ThreadPanel {
  const [rootId, setRootId] = useState<string | null>(null);
  const [posts, setPosts] = useState<Post[]>([]);
  const [loading, setLoading] = useState(false);
  const rootIdRef = useRef<string | null>(null);
  const generationRef = useRef(0);
  useEffect(() => { rootIdRef.current = rootId; }, [rootId]);

  const reset = useCallback(() => {
    generationRef.current += 1;
    rootIdRef.current = null;
    setRootId(null);
    setPosts([]);
    setLoading(false);
  }, []);

  const open = useCallback(async (nextRootId: string) => {
    if (!token) return;
    onOpen();
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    rootIdRef.current = nextRootId;
    setRootId(nextRootId);
    setPosts([]);
    setLoading(true);
    const stillCurrent = () => generationRef.current === generation && rootIdRef.current === nextRootId;
    try {
      const list = await api.listThread(token, nextRootId);
      if (!stillCurrent()) return;
      const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
      setPosts((current) => {
        const merged = new Map(ordered.map((post) => [post.id, post]));
        current
          .filter((post) => (post.root_id || post.id) === nextRootId)
          .forEach((post) => merged.set(post.id, post));
        return Array.from(merged.values()).sort((left, right) => left.create_at - right.create_at);
      });
      hydrateUsers(Array.from(new Set(ordered.map((p) => p.user_id))));
      hydrateFiles(Array.from(new Set(ordered.flatMap((p) => p.file_ids ?? []))));
      ordered.forEach((p) => {
        api.listReactions(token, p.id)
          .then((rs) => { if (stillCurrent()) setReactionsByPost((prev) => ({ ...prev, [p.id]: rs ?? [] })); })
          .catch(() => { /* ignore */ });
      });
    } catch (e) {
      if (stillCurrent()) onError(e instanceof Error ? e.message : "스레드 로드 실패");
    } finally {
      if (stillCurrent()) setLoading(false);
    }
  }, [token, onOpen, hydrateUsers, hydrateFiles, setReactionsByPost, onError]);

  const reply = useCallback(async (message: string, fileIds: string[]): Promise<boolean> => {
    if (!token || !currentChannelId || !rootId) return false;
    const rootPost = posts.find((post) => post.id === rootId);
    const channelID = rootPost?.channel_id;
    // The open root must belong to the channel on screen; otherwise a reply
    // would pair a stale root with the new channel.
    if (!channelID || channelID !== currentChannelId) return false;
    const trimmed = message.trim();
    if (!trimmed && fileIds.length === 0) return false;
    try {
      const p = await api.createPost(token, channelID, trimmed, rootId, fileIds);
      // The `posted` event also delivers this reply; show it now in case our
      // own broadcast arrives later.
      if (currentChannelIdRef.current === channelID && rootIdRef.current === rootId) {
        setPosts((prev) => (prev.some((x) => x.id === p.id) ? prev : [...prev, p]));
      }
      return true;
    } catch (e) {
      onError(e instanceof Error ? e.message : "스레드 전송 실패");
      return false;
    }
  }, [token, currentChannelId, currentChannelIdRef, rootId, posts, onError]);

  return { rootId, posts, loading, rootIdRef, setPosts, open, reset, reply };
}
