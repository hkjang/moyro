import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useSelector } from "react-redux";
import { Outlet } from "react-router-dom";
import { flowApi, type FlowSummary } from "@/api/flow";
import { useWebsocket } from "@/hooks/useWebsocket";
import type { RootState } from "@/store";
import { useUnreadTitle } from "@/hooks/useUnreadTitle";
import {
  errorMessage,
  type FlowChannelEntry,
  type FlowWorkspaceIndex,
} from "./flow-data";

const FlowWorkspaceContext = createContext<FlowWorkspaceIndex | null>(null);

const FLOW_SUMMARY_INVALIDATION_EVENTS = new Set([
  "posted",
  "post_deleted",
  "unread_updated",
  "channel_unread_updated",
  "channel_viewed",
  "channel_updated",
  "channel_deleted",
  "channel_restored",
  "channel_converted",
  "channel_member_updated",
  "team_updated",
  "team_restored",
  "team_member_updated",
  "user_added",
  "user_removed",
]);

const ACTIVITY_INVALIDATION_EVENTS = new Set([
  "activity_event",
  "activity_state_changed",
]);

const WORK_ITEM_INVALIDATION_EVENTS = new Set([
  "work_item_changed",
  "work_item_deleted",
]);

export function shouldRefreshFlowSummary(event: unknown): boolean {
  return typeof event === "string" && FLOW_SUMMARY_INVALIDATION_EVENTS.has(event);
}

export function shouldRefreshActivityEvents(event: unknown): boolean {
  return typeof event === "string" && ACTIVITY_INVALIDATION_EVENTS.has(event);
}

export function shouldRefreshWorkItems(event: unknown): boolean {
  return typeof event === "string" && WORK_ITEM_INVALIDATION_EVENTS.has(event);
}

export function entriesFromFlowSummary(summary: FlowSummary): FlowChannelEntry[] {
  const teamByID = new Map(summary.teams.map((team) => [team.id, team]));
  const membershipByChannel = new Map(
    summary.memberships.map((membership) => [membership.channel_id, membership]),
  );
  return summary.channels.flatMap((channel) => {
    const team = teamByID.get(channel.team_id);
    if (!team) return [];
    return [{ channel, team, membership: membershipByChannel.get(channel.id) }];
  });
}

export function FlowWorkspaceProvider({ token, children }: {
  token: string | null;
  children: ReactNode;
}) {
  const [teams, setTeams] = useState<FlowSummary["teams"]>([]);
  const [entries, setEntries] = useState<FlowChannelEntry[]>([]);
  useUnreadTitle(
    entries.reduce((sum, entry) => sum + (entry.membership?.mention_count ?? 0), 0),
    entries.filter((entry) => (entry.membership?.msg_count ?? 0) > 0).length,
  );
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [activityRevision, setActivityRevision] = useState(0);
  const [workItemRevision, setWorkItemRevision] = useState(0);
  const refreshTimerRef = useRef<number | null>(null);
  const refresh = useCallback(() => setRevision((current) => current + 1), []);
  const scheduleRefresh = useCallback(() => {
    if (refreshTimerRef.current != null) return;
    // A post fan-out commonly emits both `posted` and `unread_updated`.
    // Coalesce that burst while keeping Flow badges comfortably inside the
    // two-second freshness target.
    refreshTimerRef.current = window.setTimeout(() => {
      refreshTimerRef.current = null;
      refresh();
    }, 250);
  }, [refresh]);

  const handleWSMessage = useCallback((message: MessageEvent) => {
    try {
      const payload = JSON.parse(String(message.data)) as { event?: unknown };
      if (shouldRefreshFlowSummary(payload.event)) scheduleRefresh();
      if (shouldRefreshActivityEvents(payload.event)) {
        setActivityRevision((current) => current + 1);
      }
      if (shouldRefreshWorkItems(payload.event)) {
        setWorkItemRevision((current) => current + 1);
      }
    } catch {
      // Authentication replies and malformed third-party frames are not Flow
      // invalidations; the socket hook handles its own authentication state.
    }
  }, [scheduleRefresh]);
  const websocket = useWebsocket(token, handleWSMessage);

  useEffect(() => {
    if (websocket.reconnectSeq > 0) {
      scheduleRefresh();
      // Events may have arrived while the socket was disconnected.
      setActivityRevision((current) => current + 1);
      setWorkItemRevision((current) => current + 1);
    }
  }, [scheduleRefresh, websocket.reconnectSeq]);

  useEffect(() => () => {
    if (refreshTimerRef.current != null) window.clearTimeout(refreshTimerRef.current);
  }, []);

  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    if (!token) {
      setTeams([]);
      setEntries([]);
      setError("로그인 세션이 없습니다.");
      setLoading(false);
      return () => { active = false; };
    }

    setLoading(true);
    setError("");
    void flowApi.getSummary(token, controller.signal)
      .then((summary) => {
        if (!active) return;
        setTeams(summary.teams);
        setEntries(entriesFromFlowSummary(summary));
      })
      .catch((loadError: unknown) => {
        if (!active) return;
        setTeams([]);
        setEntries([]);
        setError(errorMessage(loadError, "워크스페이스 정보를 불러오지 못했습니다."));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [revision, token]);

  const value = useMemo<FlowWorkspaceIndex>(() => {
    const channelById = Object.fromEntries(entries.map((entry) => [entry.channel.id, entry]));
    return {
      teams,
      entries,
      channelById,
      loading,
      error,
      warnings: [],
      activityRevision,
      workItemRevision,
      refresh,
    };
  }, [activityRevision, entries, error, loading, refresh, teams, workItemRevision]);

  return <FlowWorkspaceContext.Provider value={value}>{children}</FlowWorkspaceContext.Provider>;
}

export function useFlowWorkspaceIndex(): FlowWorkspaceIndex {
  const value = useContext(FlowWorkspaceContext);
  if (!value) throw new Error("useFlowWorkspaceIndex must be used inside FlowWorkspaceProvider");
  return value;
}

export function FlowDataLayout() {
  const token = useSelector((state: RootState) => state.auth.token);
  return (
    <FlowWorkspaceProvider token={token}>
      <Outlet />
    </FlowWorkspaceProvider>
  );
}
