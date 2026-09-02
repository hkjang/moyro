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
    section: "조직",
    items: [
      { tab: "org", label: "조직" },
      { tab: "workspaces", label: "워크스페이스" },
    ],
  },
  {
    section: "디렉터리",
    items: [
      { tab: "users", label: "멤버" },
      { tab: "channels", label: "채널" },
    ],
  },
  {
    section: "앱",
    items: [
      { tab: "apps", label: "설치된 앱" },
      { tab: "plugins", label: "플러그인" },
      { tab: "bots", label: "봇 · 토큰" },
    ],
  },
  {
    section: "보안",
    items: [
      { tab: "auth", label: "2FA · SSO · 세션" },
      { tab: "roles", label: "권한 · 역할" },
      { tab: "policies", label: "접근 제어" },
    ],
  },
  {
    section: "운영",
    items: [
      { tab: "system", label: "시스템 · 저장소" },
      { tab: "jobs", label: "백그라운드 작업" },
      { tab: "audit", label: "로그 · 감사" },
      { tab: "invites", label: "팀 초대" },
      { tab: "emoji", label: "커스텀 이모지" },
      { tab: "incoming", label: "인커밍 웹훅" },
      { tab: "outgoing", label: "아웃고잉 웹훅" },
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
