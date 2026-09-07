export function workspaceSlug(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40) || `x-${Date.now()}`;
}

// Mattermost-compatible `posted` events may encode mention IDs as a JSON
// string to keep the WebSocket envelope flat. Accept both supported shapes.
export function parseMentionIDs(raw: unknown): string[] {
  if (!raw) return [];
  if (Array.isArray(raw)) return raw.map(String);
  if (typeof raw !== "string") return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.map(String) : [];
  } catch {
    return [];
  }
}

import type { Channel, User } from "@/api/client";

/** The other participant's id in a direct-message channel name ("a__b"). */
export function directMessagePeer(channelName: string, currentUserId: string): string {
  const [first, second] = channelName.split("__");
  if (!second) return first ?? "";
  return first === currentUserId ? second : first;
}

/**
 * Human label for a channel: "#general" for rooms, the other person's name
 * for a direct message. A DM whose peer is not loaded yet says "다이렉트
 * 메시지" rather than leaking the raw user id.
 */
export function channelDisplayLabel(
  channel: Pick<Channel, "type" | "name" | "display_name"> | null | undefined,
  users: Record<string, User>,
  currentUserId: string,
): string {
  if (!channel) return "채널";
  if (channel.type === "D") {
    const peer = users[directMessagePeer(channel.name, currentUserId)];
    return peer?.username ? `@${peer.username}` : "다이렉트 메시지";
  }
  return `#${channel.display_name || channel.name}`;
}
