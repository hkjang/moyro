import { type Dispatch, type MutableRefObject, type SetStateAction, useCallback, useEffect, useRef, useState } from "react";
import { api, type Post } from "@/api/client";

/** How many older posts one page brings in. Matches the initial page size. */
export const HISTORY_PAGE_SIZE = 60;

export type OlderPosts = {
  /** Requests the page before the oldest loaded post. Safe to call repeatedly. */
  loadOlder: () => void;
  /** True while a page is in flight; the view shows a small indicator. */
  loading: boolean;
  /** True once the server returned a short page — the conversation's start. */
  exhausted: boolean;
};

export type OlderPostsOptions = {
  token: string | null;
  channelId: string | null;
  currentChannelIdRef: MutableRefObject<string | null>;
  posts: Post[];
  setPosts: Dispatch<SetStateAction<Post[]>>;
  hydrateUsers: (ids: string[]) => void;
  hydrateFiles: (ids: string[]) => void;
  onError: (message: string) => void;
};

/**
 * Pages older history into the channel view.
 *
 * The channel opens with one page; before this hook, that page was the
 * limit — scrolling up simply stopped. Each request anchors on the oldest
 * post currently loaded, so live arrivals at the tail never shift the
 * cursor. A short page marks the start of the conversation and stops
 * further requests; switching channels resets everything.
 */
export function useOlderPosts({
  token,
  channelId,
  currentChannelIdRef,
  posts,
  setPosts,
  hydrateUsers,
  hydrateFiles,
  onError,
}: OlderPostsOptions): OlderPosts {
  const [loading, setLoading] = useState(false);
  const [exhausted, setExhausted] = useState(false);
  const inFlightRef = useRef<string | null>(null);
  const oldestIdRef = useRef<string | undefined>(undefined);
  oldestIdRef.current = posts[0]?.id;

  useEffect(() => {
    inFlightRef.current = null;
    setLoading(false);
    setExhausted(false);
  }, [channelId]);

  const loadOlder = useCallback(() => {
    const anchor = oldestIdRef.current;
    if (!token || !channelId || !anchor || exhausted) return;
    // One page at a time, and never the same anchor twice: a reader who
    // keeps the top in view would otherwise queue duplicate requests.
    if (inFlightRef.current) return;
    inFlightRef.current = anchor;
    setLoading(true);
    const requestedChannel = channelId;
    api.listPostsBefore(token, requestedChannel, anchor, HISTORY_PAGE_SIZE)
      .then((list) => {
        if (currentChannelIdRef.current !== requestedChannel) return;
        const older = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
        older.reverse(); // newest-first cursor order → oldest-first
        if (older.length < HISTORY_PAGE_SIZE) setExhausted(true);
        if (older.length === 0) return;
        setPosts((current) => {
          const known = new Set(current.map((post) => post.id));
          const fresh = older.filter((post) => !known.has(post.id));
          return fresh.length ? [...fresh, ...current] : current;
        });
        hydrateUsers(Array.from(new Set(older.map((post) => post.user_id))));
        hydrateFiles(Array.from(new Set(older.flatMap((post) => post.file_ids ?? []))));
      })
      .catch((error: unknown) => {
        if (currentChannelIdRef.current !== requestedChannel) return;
        onError(error instanceof Error ? error.message : "이전 메시지를 불러오지 못했습니다.");
      })
      .finally(() => {
        if (inFlightRef.current === anchor) inFlightRef.current = null;
        if (currentChannelIdRef.current === requestedChannel) setLoading(false);
      });
  }, [token, channelId, exhausted, currentChannelIdRef, setPosts, hydrateUsers, hydrateFiles, onError]);

  return { loadOlder, loading, exhausted };
}
