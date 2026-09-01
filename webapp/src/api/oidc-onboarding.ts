import type { OIDCProviderSettings } from "./client";
import { moyroRequest } from "./transport";

export type OIDCAccountRole = "user" | "admin" | "guest";
export type OIDCMembershipRole = "member" | "admin";

export type OIDCGroupMapping = {
  group: string;
  account_role: OIDCAccountRole;
  team_id?: string;
  team_role?: OIDCMembershipRole;
  channel_ids?: string[];
  channel_role?: OIDCMembershipRole;
  guest_expires_after_seconds?: number;
  guest_file_download: boolean;
};

export type ManagedOIDCProviderSettings = OIDCProviderSettings & {
  groups_claim: string;
  group_mappings: OIDCGroupMapping[];
};

export type OIDCOnboardingChannelTarget = {
  id: string;
  team_id: string;
  name: string;
  display_name: string;
  type: "O" | "P";
};

export type OIDCOnboardingTeamTarget = {
  id: string;
  name: string;
  display_name: string;
  channels: OIDCOnboardingChannelTarget[];
};

export const oidcOnboardingApi = {
  targets: (token: string, signal?: AbortSignal) =>
    moyroRequest<{ teams: OIDCOnboardingTeamTarget[] }>(token, "/admin/oidc/onboarding-targets", { signal }),
};
