import { useCallback, useEffect } from "react";
import type { Post } from "@/api/client";
import type { WorkspaceContextTab } from "@/features/workspace/context/ContextPanel";
import { useChannelMembers, useChannelPinned } from "@/features/workspace/model/useChannelPanels";

/**
 * Data behind the context panel's member and pinned tabs, plus the permalink
 * builder shared by message rows. Rosters and pinned lists load only while
 * their tab is open; authors are hydrated as the lists arrive.
 */
export function useChannelContextData({ token, teamId, channelId, activeContext, hydrateUsers }: {
  token: string | null;
  teamId: string | null;
  channelId: string | null;
  activeContext: WorkspaceContextTab | null;
  hydrateUsers: (ids: string[]) => void;
}) {
  const members = useChannelMembers(token, channelId, activeContext === "members");
  const pinned = useChannelPinned(token, channelId, activeContext === "pinned");

  useEffect(() => {
    if (members.members.length) hydrateUsers(members.members.map((m) => m.user_id));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [members.members]);
  useEffect(() => {
    if (pinned.posts.length) hydrateUsers(Array.from(new Set(pinned.posts.map((p) => p.user_id))));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pinned.posts]);

  // A copied link carries the post in the query string so it survives being
  // opened in a fresh tab, where router state does not.
  const permalinkFor = useCallback((post: Post) => {
    const team = teamId ?? "";
    return `${window.location.origin}/workspace/${encodeURIComponent(team)}/channel/${encodeURIComponent(post.channel_id)}?post=${encodeURIComponent(post.id)}`;
  }, [teamId]);

  return { members, pinned, permalinkFor };
}
