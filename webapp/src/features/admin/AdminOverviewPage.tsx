import CheckCircleRounded from "@mui/icons-material/CheckCircleRounded";
import CloudDoneRounded from "@mui/icons-material/CloudDoneRounded";
import EmailRounded from "@mui/icons-material/EmailRounded";
import ExtensionRounded from "@mui/icons-material/ExtensionRounded";
import FactCheckRounded from "@mui/icons-material/FactCheckRounded";
import HubRounded from "@mui/icons-material/HubRounded";
import KeyRounded from "@mui/icons-material/KeyRounded";
import LanguageRounded from "@mui/icons-material/LanguageRounded";
import PsychologyRounded from "@mui/icons-material/PsychologyRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import RuleRounded from "@mui/icons-material/RuleRounded";
import SecurityRounded from "@mui/icons-material/SecurityRounded";
import WarningAmberRounded from "@mui/icons-material/WarningAmberRounded";
import {
  Alert,
  Box,
  Button,
  Card,
  CardActionArea,
  CardContent,
  Chip,
  CircularProgress,
  Grid,
  LinearProgress,
  Stack,
  Typography,
} from "@mui/material";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useSelector } from "react-redux";
import { useNavigate } from "react-router-dom";
import {
  api,
  moyroAdminApi,
  moyroReviewApi,
  type AIProviderSettings,
  type ApprovalPolicy,
  type MCPSettings,
  type OIDCProviderSettings,
} from "@/api/client";
import { SettingsPage } from "@/components/settings/SettingsPrimitives";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import type { RootState } from "@/store";
import "@/features/admin/admin-overview.css";

type DashboardSnapshot = {
  ping: "ready" | "error" | "unknown";
  aiProviders: ProviderQueryState<AIProviderSettings>;
  oidcProviders: ProviderQueryState<OIDCProviderSettings>;
  mcp: MCPSettings | null;
  policies: ApprovalPolicy[] | null;
  pendingApprovals: number | null;
};

type ProviderQueryState<T> =
  | { state: "loading" }
  | { state: "not_authorized" }
  | { state: "loaded"; providers: T[] }
  | { state: "error"; message: string };

type ProviderOperationalState = "loading" | "not_authorized" | "ready" | "error" | "unknown";

type ProviderCardSummary = {
  state: ProviderOperationalState;
  value: string;
  detail: string;
  tone: StatusTone;
  lastTestedAt?: number;
};

const EMPTY_SNAPSHOT: DashboardSnapshot = {
  ping: "unknown",
  aiProviders: { state: "loading" },
  oidcProviders: { state: "loading" },
  mcp: null,
  policies: null,
  pendingApprovals: null,
};

type StatusTone = "success" | "warning" | "error" | "default" | "info";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback;
}

function testedAtValue(value: number | undefined): number | undefined {
  if (!value || !Number.isFinite(value) || value <= 0) return undefined;
  return Number.isNaN(new Date(value).getTime()) ? undefined : value;
}

function formatTestedAt(value: number): string {
  return new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "short",
    timeStyle: "short",
  }).format(value);
}

function providerSummary<T extends { enabled: boolean; last_tested_at?: number }>(
  query: ProviderQueryState<T>,
  statusOf: (provider: T) => "ready" | "error" | "unknown" | undefined,
  label: string,
  unauthorizedDetail: string,
): ProviderCardSummary {
  if (query.state === "loading") {
    return {
      state: "loading",
      value: "확인 중",
      detail: `${label} 설정과 연결 상태를 확인하고 있습니다.`,
      tone: "default",
    };
  }
  if (query.state === "not_authorized") {
    return {
      state: "not_authorized",
      value: "상세 권한 없음",
      detail: unauthorizedDetail,
      tone: "default",
    };
  }
  if (query.state === "error") {
    return {
      state: "error",
      value: "조회 실패",
      detail: query.message,
      tone: "error",
    };
  }

  const enabled = query.providers.filter((provider) => provider.enabled);
  if (enabled.length === 0) {
    return {
      state: "unknown",
      value: query.providers.length === 0 ? "미설정" : "비활성",
      detail: query.providers.length === 0
        ? `${label} 설정이 없습니다.`
        : `${label} 설정 ${query.providers.length}개 · 활성 설정 없음`,
      tone: query.providers.length === 0 ? "warning" : "info",
    };
  }

  // A ready flag without a persisted test time is not sufficient evidence of
  // a successful connection. Treat it as unknown until both fields agree.
  const readyCount = enabled.filter((provider) => (
    statusOf(provider) === "ready" && testedAtValue(provider.last_tested_at) !== undefined
  )).length;
  const errorCount = enabled.filter((provider) => statusOf(provider) === "error").length;
  const unknownCount = enabled.length - readyCount - errorCount;
  const lastTestedAt = enabled.reduce<number | undefined>((latest, provider) => {
    const testedAt = testedAtValue(provider.last_tested_at);
    if (testedAt === undefined) return latest;
    return latest === undefined || testedAt > latest ? testedAt : latest;
  }, undefined);
  const detail = `활성 ${enabled.length}개 · 연결 확인 ${readyCount}개 · 오류 ${errorCount}개 · 미확인 ${unknownCount}개`;

  if (errorCount > 0) {
    return {
      state: "error",
      value: errorCount === enabled.length ? "연결 오류" : "일부 연결 오류",
      detail,
      tone: "error",
      lastTestedAt,
    };
  }
  if (unknownCount > 0) {
    return {
      state: "unknown",
      value: readyCount > 0 ? "일부 미확인" : "연결 미확인",
      detail,
      tone: "warning",
      lastTestedAt,
    };
  }
  return {
    state: "ready",
    value: "연결 확인",
    detail,
    tone: "success",
    lastTestedAt,
  };
}

function StatusCard({ title, value, detail, icon, tone = "default", state, lastTestedAt }: {
  title: string;
  value: string;
  detail: string;
  icon: ReactNode;
  tone?: StatusTone;
  state?: ProviderOperationalState;
  lastTestedAt?: number;
}) {
  return (
    <Card
      variant="outlined"
      className="admin-status-card"
      data-operational-state={state}
      aria-busy={state === "loading" || undefined}
    >
      <CardContent>
        <Stack direction="row" sx={{ alignItems: "flex-start", justifyContent: "space-between", gap: 1.5 }}>
          <Box className={`admin-status-icon tone-${tone}`} aria-hidden>{icon}</Box>
          <Chip size="small" label={value} color={tone} variant={tone === "default" ? "outlined" : "filled"} />
        </Stack>
        <Typography component="h2" variant="subtitle1" sx={{ mt: 1.75 }}>{title}</Typography>
        <Typography className="admin-status-detail" variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>{detail}</Typography>
        {lastTestedAt !== undefined && (
          <Typography
            component="time"
            dateTime={new Date(lastTestedAt).toISOString()}
            className="admin-status-tested-at"
            variant="caption"
            color="text.secondary"
          >
            마지막 연결 검사 {formatTestedAt(lastTestedAt)}
          </Typography>
        )}
      </CardContent>
    </Card>
  );
}

const destinations = [
  { to: "/admin/site", title: "사이트와 공개 URL", description: "서비스 이름, 공개 URL과 outbound host 정책", icon: <LanguageRounded />, anyOf: ["manage_settings"] },
  { to: "/admin/auth/keycloak", title: "인증과 Keycloak", description: "OIDC 연결, 가입 정책과 내부 CA", icon: <SecurityRounded />, anyOf: ["manage_oidc"] },
  { to: "/admin/ai/providers", title: "AI 공급자", description: "허용 모델, streaming과 연결 확인", icon: <PsychologyRounded />, anyOf: ["manage_ai"] },
  { to: "/admin/security/keys", title: "키와 권한", description: "API·MCP 키 scope, 수명과 역할", icon: <KeyRounded />, anyOf: ["manage_key_permissions", "manage_roles", "manage_api_keys"] },
  { to: "/admin/integrations/mcp", title: "MCP와 API", description: "활성 도구, resource와 요구 scope", icon: <HubRounded />, anyOf: ["manage_settings"] },
  { to: "/admin/integrations/plugins", title: "플러그인", description: "Mattermost 호환 번들, 런타임과 플러그인별 설정", icon: <ExtensionRounded />, anyOf: ["manage_plugins"] },
  { to: "/admin/workflows/review", title: "승인 정책", description: "보호 Action, 검토자와 만료 정책", icon: <RuleRounded />, anyOf: ["manage_approval_policies"] },
] as const;

export function AdminOverviewPage() {
  const navigate = useNavigate();
  const token = useSelector((state: RootState) => state.auth.token);
  const access = useAdminAccess();
  const systemInfo = useSystemInfo();
  const [snapshot, setSnapshot] = useState<DashboardSnapshot>(EMPTY_SNAPSHOT);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [checkedAt, setCheckedAt] = useState(0);

  const load = useCallback(async () => {
    if (!token || !access.loaded) return;
    setLoading(true);
    const canManageAI = access.can("manage_ai");
    const canManageOIDC = access.can("manage_oidc");
    setSnapshot((current) => ({
      ...current,
      aiProviders: canManageAI ? { state: "loading" } : { state: "not_authorized" },
      oidcProviders: canManageOIDC ? { state: "loading" } : { state: "not_authorized" },
    }));
    const pingPromise = api.ping();
    const aiPromise = canManageAI
      ? moyroAdminApi.listAIProviders(token)
      : Promise.resolve<AIProviderSettings[]>([]);
    const oidcPromise = canManageOIDC
      ? moyroAdminApi.listOIDCProviders(token)
      : Promise.resolve<OIDCProviderSettings[]>([]);
    const mcpPromise = access.can("manage_settings")
      ? moyroAdminApi.getSettings<MCPSettings>(token, "mcp")
      : Promise.resolve<MCPSettings | null>(null);
    const policiesPromise = access.can("manage_approval_policies")
      ? moyroAdminApi.listApprovalPolicies(token)
      : Promise.resolve<ApprovalPolicy[] | null>(null);
    const pendingPromise = moyroReviewApi.listApprovalRequests(token, "pending")
      .then((items) => items.filter((item) => (
        item.status === "pending" && (item.expires_at <= 0 || item.expires_at > Date.now())
      )).length);

    const [ping, ai, oidc, mcp, policies, pending] = await Promise.allSettled([
      pingPromise,
      aiPromise,
      oidcPromise,
      mcpPromise,
      policiesPromise,
      pendingPromise,
    ]);
    setSnapshot({
      ping: ping.status === "fulfilled" && ping.value.status === "OK" ? "ready" : "error",
      aiProviders: !canManageAI
        ? { state: "not_authorized" }
        : ai.status === "fulfilled"
          ? { state: "loaded", providers: ai.value }
          : { state: "error", message: errorMessage(ai.reason, "AI 공급자 설정을 불러오지 못했습니다.") },
      oidcProviders: !canManageOIDC
        ? { state: "not_authorized" }
        : oidc.status === "fulfilled"
          ? { state: "loaded", providers: oidc.value }
          : { state: "error", message: errorMessage(oidc.reason, "OIDC 공급자 설정을 불러오지 못했습니다.") },
      mcp: mcp.status === "fulfilled" ? mcp.value : null,
      policies: policies.status === "fulfilled" ? policies.value : null,
      pendingApprovals: pending.status === "fulfilled" ? pending.value : null,
    });
    const failedLabels = [
      ping.status === "rejected" ? "애플리케이션" : "",
      canManageAI && ai.status === "rejected" ? "AI 공급자" : "",
      canManageOIDC && oidc.status === "rejected" ? "OIDC 공급자" : "",
      access.can("manage_settings") && mcp.status === "rejected" ? "MCP" : "",
      access.can("manage_approval_policies") && policies.status === "rejected" ? "승인 정책" : "",
      pending.status === "rejected" ? "승인 대기 목록" : "",
    ].filter(Boolean);
    setError(failedLabels.length > 0 ? `다음 상태를 불러오지 못했습니다: ${failedLabels.join(", ")}` : "");
    setCheckedAt(Date.now());
    setLoading(false);
  }, [access, token]);

  useEffect(() => { void load(); }, [load]);

  const aiSummary = providerSummary(
    snapshot.aiProviders,
    (provider) => provider.status,
    "AI 공급자",
    "manage_ai 권한이 없어 공급자 설정과 연결 상태를 조회하지 않았습니다.",
  );
  const oidcSummary = providerSummary(
    snapshot.oidcProviders,
    (provider) => provider.discovery_status,
    "OIDC 공급자",
    systemInfo.loaded
      ? `manage_oidc 권한 없음 · 공개 capability: OIDC 로그인 ${systemInfo.oidc_enabled ? "활성" : "비활성"} · 연결 상태 미확인`
      : "manage_oidc 권한이 없어 공급자 설정과 연결 상태를 조회하지 않았습니다.",
  );
  const activePolicies = snapshot.policies?.filter((policy) => policy.enabled).length ?? null;
  const visibleDestinations = destinations.filter((item) => access.canAny(item.anyOf));
  const notices = useMemo(() => {
    const result: string[] = [];
    if (snapshot.ping === "error") result.push("애플리케이션 상태 확인에 실패했습니다.");
    if (snapshot.aiProviders.state === "loaded") {
      const enabledAI = snapshot.aiProviders.providers.filter((provider) => provider.enabled);
      if (enabledAI.length === 0) result.push("활성 AI 공급자가 없습니다. AI 기능 요청은 실패할 수 있습니다.");
      else if (aiSummary.state === "error") result.push("활성 AI 공급자 중 연결 오류 상태가 있습니다.");
      else if (aiSummary.state === "unknown") result.push("활성 AI 공급자의 연결 확인 결과가 없거나 일부만 확인되었습니다.");
    }
    if (snapshot.oidcProviders.state === "loaded") {
      const enabledOIDC = snapshot.oidcProviders.providers.filter((provider) => provider.enabled);
      if (enabledOIDC.length > 0 && oidcSummary.state === "error") result.push("활성 OIDC 공급자 중 연결 오류 상태가 있습니다.");
      else if (enabledOIDC.length > 0 && oidcSummary.state === "unknown") result.push("활성 OIDC 공급자의 연결 확인 결과가 없거나 일부만 확인되었습니다.");
    }
    if (snapshot.mcp && !snapshot.mcp.enabled) result.push("MCP transport가 비활성화되어 있습니다.");
    if (activePolicies === 0) result.push("활성 승인 정책이 없습니다. 과거 승인 이력은 계속 조회할 수 있습니다.");
    if (!systemInfo.capabilities?.email_digest?.enabled) result.push("이메일 Digest worker가 비활성 상태입니다.");
    return result;
  }, [activePolicies, aiSummary.state, oidcSummary.state, snapshot.aiProviders, snapshot.mcp, snapshot.oidcProviders, snapshot.ping, systemInfo.capabilities?.email_digest?.enabled]);

  return (
    <SettingsPage
      title="운영 현황"
      description="현재 확인 가능한 실제 상태와 설정 경고를 먼저 보여줍니다. 표시되지 않는 지표를 정상으로 추정하지 않습니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={() => void load()} disabled={loading}>상태 새로고침</Button>}
    >
      {loading && <LinearProgress aria-label="운영 상태 확인 중" />}
      {error && <Alert severity="warning">{error}</Alert>}

      <Grid container spacing={2} aria-label="운영 상태 카드">
        <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
          <StatusCard
            title="애플리케이션"
            value={snapshot.ping === "ready" ? "정상" : snapshot.ping === "error" ? "확인 필요" : "확인 중"}
            detail={`${displayVersion(systemInfo.version)} · build ${systemInfo.build_hash?.slice(0, 8) || "미상"}`}
            icon={snapshot.ping === "ready" ? <CloudDoneRounded /> : <WarningAmberRounded />}
            tone={snapshot.ping === "ready" ? "success" : snapshot.ping === "error" ? "error" : "default"}
          />
        </Grid>
        <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
          <StatusCard
            title="인증"
            value={oidcSummary.value}
            detail={oidcSummary.detail}
            icon={<SecurityRounded />}
            tone={oidcSummary.tone}
            state={oidcSummary.state}
            lastTestedAt={oidcSummary.lastTestedAt}
          />
        </Grid>
        <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
          <StatusCard
            title="AI"
            value={aiSummary.value}
            detail={aiSummary.detail}
            icon={<PsychologyRounded />}
            tone={aiSummary.tone}
            state={aiSummary.state}
            lastTestedAt={aiSummary.lastTestedAt}
          />
        </Grid>
        <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
          <StatusCard title="MCP" value={snapshot.mcp === null ? "권한 범위 외" : snapshot.mcp.enabled ? "활성" : "비활성"} detail={snapshot.mcp === null ? "manage_settings 권한 필요" : `허용 Tool ${snapshot.mcp.allowed_tools.length}개`} icon={<HubRounded />} tone={snapshot.mcp?.enabled ? "success" : snapshot.mcp === null ? "default" : "warning"} />
        </Grid>
        <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
          <StatusCard title="승인" value={snapshot.pendingApprovals === null ? "범위별 조회" : `대기 ${snapshot.pendingApprovals}건`} detail={activePolicies === null ? "정책 관리 권한 범위 외" : `활성 정책 ${activePolicies}개`} icon={<FactCheckRounded />} tone={snapshot.pendingApprovals === null ? "default" : snapshot.pendingApprovals > 0 ? "warning" : "success"} />
        </Grid>
        <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
          <StatusCard title="이메일 알림" value={systemInfo.capabilities?.email_digest?.enabled ? "활성" : "비활성"} detail={systemInfo.capabilities?.email_digest?.configured ? "SMTP 설정 감지" : "SMTP 설정 없음"} icon={<EmailRounded />} tone={systemInfo.capabilities?.email_digest?.enabled ? "success" : "warning"} />
        </Grid>
      </Grid>

      <Stack spacing={1.25} component="section" aria-labelledby="admin-notices-title">
        <Stack direction="row" sx={{ alignItems: "center", justifyContent: "space-between", gap: 2 }}>
          <Typography id="admin-notices-title" component="h2" variant="h5">설정 경고</Typography>
          {checkedAt > 0 && <Typography variant="caption" color="text.secondary">마지막 확인 {new Intl.DateTimeFormat("ko-KR", { timeStyle: "short" }).format(checkedAt)}</Typography>}
        </Stack>
        {loading ? (
          <Stack direction="row" sx={{ alignItems: "center", gap: 1 }} role="status">
            <CircularProgress size={18} /><Typography variant="body2">상태를 확인하는 중입니다.</Typography>
          </Stack>
        ) : notices.length === 0 ? (
          <Alert severity="success" icon={<CheckCircleRounded />}>현재 조회 범위에서 설정 경고가 없습니다.</Alert>
        ) : notices.map((notice) => <Alert key={notice} severity="warning">{notice}</Alert>)}
      </Stack>

      <Box component="section" aria-labelledby="admin-quick-title">
        <Typography id="admin-quick-title" component="h2" variant="h5" sx={{ mb: 1.5 }}>빠른 설정</Typography>
        <Grid container spacing={2}>
          {visibleDestinations.map((item) => (
            <Grid key={item.to} size={{ xs: 12, md: 6 }}>
              <Card variant="outlined" sx={{ height: "100%" }}>
                <CardActionArea onClick={() => navigate(item.to)} sx={{ height: "100%", alignItems: "stretch" }}>
                  <CardContent>
                    <Stack direction="row" sx={{ alignItems: "center", gap: 1.25, color: "primary.main" }}>
                      {item.icon}
                      <Typography component="h3" variant="subtitle1" color="text.primary">{item.title}</Typography>
                    </Stack>
                    <Typography color="text.secondary" variant="body2" sx={{ mt: 0.75 }}>{item.description}</Typography>
                  </CardContent>
                </CardActionArea>
              </Card>
            </Grid>
          ))}
        </Grid>
      </Box>
    </SettingsPage>
  );
}
