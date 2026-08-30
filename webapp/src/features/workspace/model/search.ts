import type { Channel, SearchFilters } from "@/api/client";
import type { UsersMap } from "@/features/workspace/model/types";

/** Resolve Mattermost-style search tokens while preserving unknown tokens. */
export function parseWorkspaceSearchFilters(
  raw: string,
  users: UsersMap,
  channels: Channel[],
): { terms: string; filters: SearchFilters } {
  const filters: SearchFilters = {};
  const residual: string[] = [];

  for (const token of raw.split(/\s+/)) {
    if (!token) continue;

    const fromMatch = token.match(/^from:(\S+)$/i);
    if (fromMatch) {
      const user = Object.values(users).find((candidate) => candidate.username === fromMatch[1]);
      if (user) {
        filters.from_user_id = user.id;
        continue;
      }
      residual.push(token);
      continue;
    }

    const channelMatch = token.match(/^in:(\S+)$/i);
    if (channelMatch) {
      const channel = channels.find((candidate) => (
        candidate.name === channelMatch[1] || candidate.display_name === channelMatch[1]
      ));
      if (channel) {
        filters.in_channel_id = channel.id;
        continue;
      }
      residual.push(token);
      continue;
    }

    const afterMatch = token.match(/^after:(\d{4}-\d{2}-\d{2})$/i);
    if (afterMatch) {
      const timestamp = Date.parse(afterMatch[1]);
      if (!Number.isNaN(timestamp)) {
        filters.after = timestamp;
        continue;
      }
    }

    const beforeMatch = token.match(/^before:(\d{4}-\d{2}-\d{2})$/i);
    if (beforeMatch) {
      const timestamp = Date.parse(beforeMatch[1]);
      if (!Number.isNaN(timestamp)) {
        // Include the named date instead of applying a surprising midnight cut-off.
        filters.before = timestamp + 24 * 60 * 60 * 1000;
        continue;
      }
    }

    if (/^has:file$/i.test(token)) {
      filters.has_file = true;
      continue;
    }
    if (/^has:link$/i.test(token)) {
      filters.has_link = true;
      continue;
    }
    residual.push(token);
  }

  return { terms: residual.join(" "), filters };
}
