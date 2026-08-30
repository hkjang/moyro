import type { Post } from "@/api/client";
import type { ChannelFileEntry } from "@/features/workspace/context/ChannelContextViews";
import type { FilesMap, UsersMap } from "@/features/workspace/model/types";

export function selectChannelFileEntries(
  posts: Post[],
  channelID: string | null,
  filesByID: FilesMap,
  users: UsersMap,
): ChannelFileEntry[] {
  const entries: ChannelFileEntry[] = [];
  const seen = new Set<string>();
  for (const post of [...posts].reverse()) {
    if (post.channel_id !== channelID || post.delete_at !== 0) continue;
    for (const fileID of post.file_ids ?? []) {
      if (seen.has(fileID)) continue;
      const file = filesByID[fileID];
      if (!file) continue;
      seen.add(fileID);
      entries.push({
        file,
        post,
        author: users[post.user_id]?.username ?? post.user_id.slice(0, 8),
      });
    }
  }
  return entries;
}
