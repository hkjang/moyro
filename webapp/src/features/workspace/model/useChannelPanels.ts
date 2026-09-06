import { useCallback, useEffect, useState } from "react";
import { api, type ChannelMember, type Post } from "@/api/client";

/**
 * Loads a channel's member list once the members tab is opened, and again
 * whenever the channel changes while it is open. Membership events from the
 * socket can call `reload` to refresh without a full refetch cycle elsewhere.
 */
export function useChannelMembers(token: string | null, channelId: string | null, enabled: boolean) {
  const [members, setMembers] = useState<ChannelMember[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const reload = useCallback(() => {
    if (!token || !channelId || !enabled) return;
    const requested = channelId;
    setLoading(true);
    setError("");
    api.listChannelMembers(token, requested)
      .then((list) => { if (requested === channelId) setMembers(list ?? []); })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : "멤버를 불러오지 못했습니다."))
      .finally(() => setLoading(false));
  }, [token, channelId, enabled]);

  useEffect(() => {
    setMembers([]);
    reload();
  }, [reload]);

  return { members, loading, error, reload };
}

/** Loads a channel's pinned posts when the pinned tab is open. */
export function useChannelPinned(token: string | null, channelId: string | null, enabled: boolean) {
  const [posts, setPosts] = useState<Post[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const reload = useCallback(() => {
    if (!token || !channelId || !enabled) return;
    const requested = channelId;
    setLoading(true);
    setError("");
    api.listPinned(token, requested)
      .then((list) => {
        if (requested !== channelId) return;
        const ordered = (list.order ?? []).map((id) => list.posts[id]).filter(Boolean);
        setPosts(ordered);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : "고정 메시지를 불러오지 못했습니다."))
      .finally(() => setLoading(false));
  }, [token, channelId, enabled]);

  useEffect(() => {
    setPosts([]);
    reload();
  }, [reload]);

  return { posts, loading, error, reload };
}
