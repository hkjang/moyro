import type { Post } from "@/api/client";
import { formatDayLabel, isSameDay } from "@/lib/time";

/**
 * Consecutive messages from one author inside this window collapse into a
 * single visual group: the follow-ups drop their avatar, name, and time and
 * read as continuation lines. Five minutes is the convention Mattermost and
 * Slack users already expect.
 */
export const CONTINUATION_WINDOW_MS = 5 * 60_000;

export type TimelineItem =
  | { kind: "date"; key: string; at: number; label: string }
  | { kind: "unread"; key: string }
  | { kind: "post"; key: string; post: Post; continuation: boolean };

export type BuildTimelineOptions = {
  /**
   * The reader's `last_viewed_at` captured when the channel was opened. Posts
   * by other people after this instant sit below a "new messages" marker.
   * Zero (or undefined) suppresses the marker.
   */
  unreadSince?: number;
  /** The reader's own id — their own posts never count as unread. */
  currentUserId?: string;
  /** Injectable clock for the "오늘 / 어제" labels. */
  now?: number;
};

function presentedAs(post: Post): string {
  // A webhook or bot can post under an overridden name; two such posts share
  // a user id but are not the same speaker to the reader.
  const props = post.props ?? {};
  return `${post.user_id}|${String(props.override_username ?? "")}|${String(props.from_webhook ?? "")}`;
}

function continues(previous: Post, next: Post): boolean {
  return (
    presentedAs(previous) === presentedAs(next)
    && next.create_at - previous.create_at >= 0
    && next.create_at - previous.create_at < CONTINUATION_WINDOW_MS
    && (previous.root_id || "") === (next.root_id || "")
    && isSameDay(previous.create_at, next.create_at)
  );
}

/**
 * Turns an oldest-first post list into the rows the channel view renders:
 * date separators at every day boundary, at most one unread marker, and each
 * post flagged as a continuation of the previous one where the grouping rule
 * applies. A separator always breaks a group, so a message after midnight or
 * after the unread marker starts fresh.
 */
export function buildTimeline(posts: Post[], options: BuildTimelineOptions = {}): TimelineItem[] {
  const now = options.now ?? Date.now();
  const unreadSince = options.unreadSince ?? 0;
  const items: TimelineItem[] = [];
  let previous: Post | undefined;
  let unreadPlaced = false;

  for (const post of posts) {
    let brokeGroup = false;

    if (!previous || !isSameDay(previous.create_at, post.create_at)) {
      items.push({
        kind: "date",
        key: `date-${post.id}`,
        at: post.create_at,
        label: formatDayLabel(post.create_at, now),
      });
      brokeGroup = true;
    }

    if (
      !unreadPlaced
      && unreadSince > 0
      && post.create_at > unreadSince
      && post.user_id !== options.currentUserId
    ) {
      items.push({ kind: "unread", key: `unread-${post.id}` });
      unreadPlaced = true;
      brokeGroup = true;
    }

    items.push({
      kind: "post",
      key: post.id,
      post,
      continuation: !brokeGroup && previous !== undefined && continues(previous, post),
    });
    previous = post;
  }

  return items;
}
