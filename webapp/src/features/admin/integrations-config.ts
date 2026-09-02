// Static configuration and view-model types for the admin integrations panel.
//
// Split out of IntegrationsPanel.tsx so the component file holds behaviour
// rather than tables of labels. Nothing here is stateful; the panel imports
// these verbatim.

export type Tab =
  | "org"
  | "workspaces"
  | "bots"
  | "incoming"
  | "outgoing"
  | "emoji"
  | "invites"
  | "users"
  | "channels"
  | "apps"
  | "auth"
  | "system"
  | "plugins"
  | "roles"
  | "jobs"
  | "policies"
  | "audit";

export const TAB_LABELS: Record<Tab, string> = {
  org: "조직",
  workspaces: "워크스페이스",
  bots: "봇",
  incoming: "인커밍 웹훅",
  outgoing: "아웃고잉 웹훅",
  emoji: "이모지",
  invites: "초대 링크",
  users: "멤버",
  channels: "채널",
  apps: "앱",
  auth: "보안",
  system: "시스템",
  plugins: "플러그인",
  roles: "권한",
  jobs: "작업",
  policies: "정책",
  audit: "감사 로그",
};

export const ADMIN_NAV: { section: string; items: { tab: Tab; label: string }[] }[] = [
  {
    section: "Organization",
    items: [
      { tab: "org", label: "Organization" },
      { tab: "workspaces", label: "Workspaces" },
    ],
  },
  {
    section: "Directory",
    items: [
      { tab: "users", label: "Members" },
      { tab: "channels", label: "Channels" },
    ],
  },
  {
    section: "Apps",
    items: [
      { tab: "apps", label: "Installed Apps" },
      { tab: "plugins", label: "Plugins" },
      { tab: "bots", label: "Bots / Tokens" },
    ],
  },
  {
    section: "Security",
    items: [
      { tab: "auth", label: "2FA / SSO / Sessions" },
      { tab: "roles", label: "Permissions / Roles" },
      { tab: "policies", label: "Access Control" },
    ],
  },
  {
    section: "Operations",
    items: [
      { tab: "system", label: "System / Storage" },
      { tab: "jobs", label: "Background Jobs" },
      { tab: "audit", label: "Logging / Audit" },
      { tab: "invites", label: "Team Invites" },
      { tab: "emoji", label: "Custom Emoji" },
      { tab: "incoming", label: "Incoming Webhooks" },
      { tab: "outgoing", label: "Outgoing Webhooks" },
    ],
  },
];

// Human-readable labels for the TTL dropdown when issuing invites. Values
// are seconds; server converts to unix-millis `expires_at`.
export const INVITE_TTL_CHOICES: { label: string; seconds: number }[] = [
  { label: "1일", seconds: 24 * 60 * 60 },
  { label: "7일", seconds: 7 * 24 * 60 * 60 },
  { label: "30일", seconds: 30 * 24 * 60 * 60 },
];

// Common audit action prefix filters. Empty string = no filter.
export const AUDIT_PREFIXES: { label: string; value: string }[] = [
  { label: "전체", value: "" },
  { label: "사용자", value: "user." },
  { label: "초대", value: "invite." },
  { label: "세션", value: "session." },
  { label: "채널", value: "channel." },
  { label: "웹훅", value: "webhook." },
  { label: "봇", value: "bot." },
];

export type AdminPolicyProbe = {
  key: string;
  label: string;
  status: string;
  detail: string;
  count?: number;
  tone?: "ok" | "danger";
};

export type AdminAuthProbe = AdminPolicyProbe;

export type AdminDetailPanel = {
  title: string;
  subtitle: string;
  rows: { label: string; value: string }[];
};
