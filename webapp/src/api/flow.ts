import type { Channel, ChannelMemberWithCounts, Team } from "./client";
import { moyroRequest } from "./transport";

export type FlowSummary = {
  updated_at: number;
  counts: {
    unread_channels: number;
    mentions: number;
  };
  teams: Team[];
  channels: Channel[];
  memberships: ChannelMemberWithCounts[];
  top_unread_channels: Array<{
    team_id: string;
    channel_id: string;
    msg_count: number;
    mention_count: number;
    last_viewed_at: number;
  }>;
};

export const flowApi = {
  getSummary: (token: string, signal?: AbortSignal) =>
    moyroRequest<FlowSummary>(token, "/me/flow-summary", { signal }),
};
