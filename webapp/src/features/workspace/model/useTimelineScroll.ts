import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

/** How close to the end still counts as "reading the latest messages". */
const BOTTOM_THRESHOLD_PX = 48;

export type TimelineScroll = {
  /** Attach to the scrolling message container. */
  containerRef: React.RefObject<HTMLDivElement>;
  /** Messages arrived while the reader was scrolled up. */
  pendingCount: number;
  /** True while the reader is away from the end of the list. */
  scrolledUp: boolean;
  /** Scrolls to the newest message and clears the pending count. */
  jumpToLatest: () => void;
};

export type TimelineScrollOptions = {
  /** Resets the "pinned to bottom" state and scrolls to the end when it changes. */
  channelId: string | null;
  /** Ids of the posts in render order; growth at the tail means new messages. */
  postIds: string[];
  /** Id of the newest post's author — a reader's own message always scrolls. */
  latestAuthorId?: string;
  currentUserId?: string;
  /** True while the initial page for the channel is still loading. */
  loading: boolean;
  /**
   * Set while the view is being positioned on an exact post (a search hit or
   * a Flow source link). The hook must not fight that positioning by snapping
   * to the bottom.
   */
  suspended?: boolean;
};

/**
 * Keeps the channel view where a reader expects it.
 *
 * On open it lands on the newest message. While the reader stays at the end,
 * each incoming message keeps the view pinned there. Once they scroll up to
 * read history, new messages stop moving the view and are counted instead,
 * so a "N new messages" affordance can offer the way back. Sending a message
 * always scrolls, because the author wants to see it land.
 */
export function useTimelineScroll(options: TimelineScrollOptions): TimelineScroll {
  const { channelId, postIds, latestAuthorId, currentUserId, loading, suspended } = options;
  const containerRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef(true);
  const previousIdsRef = useRef<string[]>([]);
  const previousChannelRef = useRef<string | null>(null);
  const [pendingCount, setPendingCount] = useState(0);
  const [scrolledUp, setScrolledUp] = useState(false);

  const scrollToEnd = useCallback((behavior: ScrollBehavior = "auto") => {
    const node = containerRef.current;
    if (!node) return;
    node.scrollTo({ top: node.scrollHeight, behavior });
    pinnedRef.current = true;
    setScrolledUp(false);
    setPendingCount(0);
  }, []);

  // Track whether the reader is at the end. Passive so scrolling stays smooth.
  useEffect(() => {
    const node = containerRef.current;
    if (!node) return undefined;
    const onScroll = () => {
      const distance = node.scrollHeight - node.scrollTop - node.clientHeight;
      const atBottom = distance <= BOTTOM_THRESHOLD_PX;
      pinnedRef.current = atBottom;
      setScrolledUp(!atBottom);
      if (atBottom) setPendingCount(0);
    };
    node.addEventListener("scroll", onScroll, { passive: true });
    return () => node.removeEventListener("scroll", onScroll);
  }, [channelId]);

  // Position after the DOM has the new rows but before paint, so the reader
  // never sees the top of the list flash by.
  useLayoutEffect(() => {
    if (loading || suspended) return;

    const channelChanged = previousChannelRef.current !== channelId;
    const previousIds = previousIdsRef.current;
    previousIdsRef.current = postIds;
    previousChannelRef.current = channelId;

    if (channelChanged) {
      scrollToEnd();
      return;
    }

    const grewAtTail = postIds.length > previousIds.length
      && previousIds.every((id, index) => postIds[index] === id);
    if (!grewAtTail) return;

    const ownMessage = Boolean(currentUserId) && latestAuthorId === currentUserId;
    if (pinnedRef.current || ownMessage) {
      scrollToEnd(ownMessage ? "smooth" : "auto");
      return;
    }
    setPendingCount((count) => count + (postIds.length - previousIds.length));
  }, [channelId, postIds, latestAuthorId, currentUserId, loading, suspended, scrollToEnd]);

  const jumpToLatest = useCallback(() => scrollToEnd("smooth"), [scrollToEnd]);

  return { containerRef, pendingCount, scrolledUp, jumpToLatest };
}
