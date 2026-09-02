import type { Post } from "@/api/client";

/**
 * Maximum posts kept in the channel view.
 *
 * The channel loads one page (60) on open and then grows only as live messages
 * arrive. A session left open in a busy channel would otherwise accumulate
 * posts — and DOM nodes — without limit for as long as the tab is open.
 *
 * The bound is well above the initial page so ordinary reading never trims,
 * and above a screenful so scrolling back a little still works. Scrolling
 * further back is a history-paging concern, not something the live list is
 * meant to serve.
 */
export const MAX_RETAINED_POSTS = 400;

/**
 * Appends a live post, keeping the list ordered, free of duplicates, and
 * bounded to the newest {@link MAX_RETAINED_POSTS} entries.
 *
 * Returns the original array when the post is already present so React can
 * skip the re-render.
 */
export function appendLivePost(current: Post[], post: Post): Post[] {
  if (current.some((existing) => existing.id === post.id)) return current;
  const next = [...current, post];
  // History the reader paged in on purpose is kept: live arrivals never let
  // the list grow past the larger of the default window and its current
  // size, but they do not shrink a list the reader deliberately extended.
  const limit = Math.max(MAX_RETAINED_POSTS, current.length);
  return next.length > limit ? next.slice(next.length - limit) : next;
}

/**
 * Trims an already-ordered list to the newest {@link MAX_RETAINED_POSTS}
 * entries. Used where a list is rebuilt wholesale rather than appended to.
 */
export function boundPostWindow(posts: Post[]): Post[] {
  return posts.length > MAX_RETAINED_POSTS ? posts.slice(posts.length - MAX_RETAINED_POSTS) : posts;
}
