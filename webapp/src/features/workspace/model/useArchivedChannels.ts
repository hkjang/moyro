import { useCallback, useEffect, useRef, useState } from "react";
import { api, type Channel } from "@/api/client";

export type ArchivedChannels = {
  /** Archived rows for the current team; empty while the toggle is off. */
  channels: Channel[];
  /** Re-fetches the archived slice, e.g. after restoring a channel. */
  reload: () => Promise<void>;
};

export type ArchivedChannelsOptions = {
  token: string | null;
  teamId: string | null;
  /** Off by default so the sidebar stays lean. */
  enabled: boolean;
  onError: (message: string) => void;
};

/**
 * Loads the archived-only slice of a team's channels while the sidebar toggle
 * is on.
 *
 * The server returns active and archived rows together for
 * `include_deleted=true`, so the archived rows are filtered here and kept
 * separate from the active list rather than merged into it.
 *
 * Every fetch carries a generation counter and re-checks the team it was
 * issued for. Switching teams or toggling the view mid-flight would otherwise
 * let a late response install another team's archived channels.
 */
export function useArchivedChannels({
  token,
  teamId,
  enabled,
  onError,
}: ArchivedChannelsOptions): ArchivedChannels {
  const [channels, setChannels] = useState<Channel[]>([]);
  const generationRef = useRef(0);
  const teamIdRef = useRef(teamId);
  teamIdRef.current = teamId;

  const reload = useCallback(async () => {
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    if (!token || !teamId) return;
    const requestedTeamId = teamId;
    try {
      const all = await api.listChannels(token, requestedTeamId, true);
      if (generationRef.current !== generation || teamIdRef.current !== requestedTeamId) return;
      setChannels((all ?? []).filter((c) => (c.delete_at ?? 0) > 0));
    } catch (e) {
      if (generationRef.current === generation && teamIdRef.current === requestedTeamId) {
        onError(e instanceof Error ? e.message : "보관 채널 로드 실패");
      }
    }
  }, [token, teamId, onError]);

  useEffect(() => {
    // Clear first, then refetch. `reload` changes identity when the team or
    // credential changes, so this also drops the previous team's archived rows
    // the moment the user switches rather than leaving them on screen until the
    // next response lands. Bumping the generation invalidates any in-flight
    // fetch so it cannot repopulate a hidden or stale list.
    generationRef.current += 1;
    setChannels([]);
    if (!enabled) return;
    void reload();
  }, [enabled, reload]);

  return { channels, reload };
}
