import type { Invite } from "./client";
import { compatRequest } from "./transport";

export type InviteKind = "member" | "guest";

export type CollaborationInvite = Invite & {
  kind: InviteKind;
  channel_ids: string[];
  guest_expires_after_seconds: number;
  guest_file_download: boolean;
};

export type CreateCollaborationInvite = {
  max_uses: number;
  ttl_seconds: number;
  kind: InviteKind;
  channel_ids?: string[];
  guest_expires_after_seconds?: number;
  guest_file_download?: boolean;
};

export const collaborationInvitesApi = {
  create: (token: string, teamID: string, input: CreateCollaborationInvite) =>
    compatRequest<CollaborationInvite>(token, `/teams/${encodeURIComponent(teamID)}/invites`, {
      method: "POST",
      body: input,
    }),
  list: (token: string, teamID: string) =>
    compatRequest<CollaborationInvite[]>(token, `/teams/${encodeURIComponent(teamID)}/invites`),
  revoke: (token: string, teamID: string, inviteID: string) =>
    compatRequest<{ status: string }>(token, `/teams/${encodeURIComponent(teamID)}/invites/${encodeURIComponent(inviteID)}`, {
      method: "DELETE",
    }),
};
