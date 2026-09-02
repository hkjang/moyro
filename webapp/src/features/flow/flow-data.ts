import {
  type Channel,
  type ChannelMemberWithCounts,
  type Post,
  type Team,
} from "@/api/client";

export type FlowChannelEntry = {
  channel: Channel;
  team: Team;
  membership?: ChannelMemberWithCounts;
};

export type FlowWorkspaceIndex = {
  teams: Team[];
  entries: FlowChannelEntry[];
  channelById: Record<string, FlowChannelEntry>;
  loading: boolean;
  error: string;
  warnings: string[];
  /** Changes when the durable activity feed should be reloaded. */
  activityRevision: number;
  /** Changes when a visible task or decision is created, updated, or removed. */
  workItemRevision: number;
  refresh: () => void;
};

export function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback;
}

export function channelPath(entry: FlowChannelEntry): string {
  return `/workspace/${encodeURIComponent(entry.team.id)}/channel/${encodeURIComponent(entry.channel.id)}`;
}

/** Router state understood by ChatView for an exact, access-checked message jump. */
export function postNavigationState(postID: string): { focusPostId: string } {
  return { focusPostId: postID };
}

// Time formatting is shared with the workspace so every surface renders the
// same strings for the same instant.
export { formatDateTime, formatRelativeTime, isToday } from "@/lib/time";

type SavedPostsWire = {
  order?: unknown;
  posts?: unknown;
};

function isPost(value: unknown): value is Post {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<Post>;
  return typeof candidate.id === "string"
    && typeof candidate.channel_id === "string"
    && typeof candidate.user_id === "string"
    && typeof candidate.message === "string";
}

// v0.1.1's handler returns `posts` as an ordered array while the historical
// TypeScript boundary declares a Mattermost-style id map. Keep the tolerance
// here, at the product-screen boundary, so neither shape can silently render
// an empty saved list.
export function normalizeSavedPosts(value: unknown): Post[] {
  if (!value || typeof value !== "object") return [];
  const wire = value as SavedPostsWire;
  const order = Array.isArray(wire.order)
    ? wire.order.filter((id): id is string => typeof id === "string")
    : [];
  if (Array.isArray(wire.posts)) {
    const posts = wire.posts.filter(isPost);
    if (order.length === 0) return posts;
    const byId = new Map(posts.map((post) => [post.id, post]));
    const ordered = order.map((id) => byId.get(id)).filter((post): post is Post => Boolean(post));
    const included = new Set(ordered.map((post) => post.id));
    return [...ordered, ...posts.filter((post) => !included.has(post.id))];
  }
  if (!wire.posts || typeof wire.posts !== "object") return [];
  const byId = wire.posts as Record<string, unknown>;
  if (order.length > 0) {
    const ordered = order.map((id) => byId[id]).filter(isPost);
    const included = new Set(ordered.map((post) => post.id));
    return [...ordered, ...Object.values(byId).filter(isPost).filter((post) => !included.has(post.id))];
  }
  return Object.values(byId).filter(isPost);
}
