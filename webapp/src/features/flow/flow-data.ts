import { useCallback, useEffect, useMemo, useState } from "react";
import {
  api,
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

export function formatDateTime(value: number): string {
  if (!value) return "—";
  return new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(value);
}

export function formatRelativeTime(value: number): string {
  if (!value) return "—";
  const delta = value - Date.now();
  const abs = Math.abs(delta);
  const formatter = new Intl.RelativeTimeFormat("ko-KR", { numeric: "auto" });
  if (abs < 60_000) return formatter.format(Math.round(delta / 1_000), "second");
  if (abs < 3_600_000) return formatter.format(Math.round(delta / 60_000), "minute");
  if (abs < 86_400_000) return formatter.format(Math.round(delta / 3_600_000), "hour");
  return formatter.format(Math.round(delta / 86_400_000), "day");
}

export function isToday(value: number): boolean {
  if (!value) return false;
  const target = new Date(value);
  const today = new Date();
  return target.getFullYear() === today.getFullYear()
    && target.getMonth() === today.getMonth()
    && target.getDate() === today.getDate();
}

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

export function useFlowWorkspaceIndex(token: string | null): FlowWorkspaceIndex {
  const [teams, setTeams] = useState<Team[]>([]);
  const [entries, setEntries] = useState<FlowChannelEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [warnings, setWarnings] = useState<string[]>([]);
  const [revision, setRevision] = useState(0);
  const refresh = useCallback(() => setRevision((current) => current + 1), []);

  useEffect(() => {
    let active = true;
    if (!token) {
      setTeams([]);
      setEntries([]);
      setWarnings([]);
      setError("로그인 세션이 없습니다.");
      setLoading(false);
      return () => { active = false; };
    }

    setLoading(true);
    setError("");
    void (async () => {
      try {
        const teamRows = await api.listTeams(token);
        const settled = await Promise.all(teamRows.map(async (team) => {
          const [channelsResult, membersResult] = await Promise.allSettled([
            api.listChannels(token, team.id),
            api.listMyChannelMembers(token, team.id),
          ]);
          return { team, channelsResult, membersResult };
        }));
        if (!active) return;

        const nextWarnings: string[] = [];
        const deduped = new Map<string, FlowChannelEntry>();
        for (const row of settled) {
          if (row.channelsResult.status === "rejected") {
            nextWarnings.push(`${row.team.display_name} 채널 목록을 불러오지 못했습니다.`);
            continue;
          }
          const members = row.membersResult.status === "fulfilled" ? row.membersResult.value : [];
          if (row.membersResult.status === "rejected") {
            nextWarnings.push(`${row.team.display_name} 읽지 않음 집계를 불러오지 못했습니다.`);
          }
          const memberByChannel = new Map(members.map((member) => [member.channel_id, member]));
          for (const channel of row.channelsResult.value) {
            const existing = deduped.get(channel.id);
            const membership = memberByChannel.get(channel.id) ?? existing?.membership;
            if (!existing) {
              deduped.set(channel.id, { channel, team: row.team, membership });
            } else if (!existing.membership && membership) {
              deduped.set(channel.id, { ...existing, membership });
            }
          }
        }
        setTeams(teamRows);
        setEntries([...deduped.values()]);
        setWarnings(nextWarnings);
      } catch (loadError) {
        if (!active) return;
        setTeams([]);
        setEntries([]);
        setWarnings([]);
        setError(errorMessage(loadError, "워크스페이스 정보를 불러오지 못했습니다."));
      } finally {
        if (active) setLoading(false);
      }
    })();
    return () => { active = false; };
  }, [revision, token]);

  const channelById = useMemo(
    () => Object.fromEntries(entries.map((entry) => [entry.channel.id, entry])),
    [entries],
  );

  return { teams, entries, channelById, loading, error, warnings, refresh };
}
