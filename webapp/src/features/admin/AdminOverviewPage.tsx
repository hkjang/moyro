import HubRounded from "@mui/icons-material/HubRounded";
import KeyRounded from "@mui/icons-material/KeyRounded";
import LanguageRounded from "@mui/icons-material/LanguageRounded";
import PsychologyRounded from "@mui/icons-material/PsychologyRounded";
import RuleRounded from "@mui/icons-material/RuleRounded";
import SecurityRounded from "@mui/icons-material/SecurityRounded";
import { Card, CardActionArea, CardContent, Chip, Grid, Stack, Typography } from "@mui/material";
import { useNavigate } from "react-router-dom";
import { SettingsPage } from "@/components/settings/SettingsPrimitives";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";

const cards = [
  {
    to: "/admin/site",
    title: "사이트 설정",
    description: "서비스 이름, HTTPS 기준 URL과 outbound host allowlist를 관리합니다.",
    icon: <LanguageRounded color="primary" />,
    anyOf: ["manage_settings"],
  },
  {
    to: "/admin/auth/keycloak",
    title: "Keycloak SSO",
    description: "Issuer URL과 client 정보만으로 OIDC 로그인을 연결합니다.",
    icon: <SecurityRounded color="primary" />,
    anyOf: ["manage_oidc"],
  },
  {
    to: "/admin/ai/providers",
    title: "AI 공급자",
    description: "사내 AI endpoint, 모델, streaming과 최대 token을 관리합니다.",
    icon: <PsychologyRounded color="primary" />,
    anyOf: ["manage_ai"],
  },
  {
    to: "/admin/security/keys",
    title: "키 정책",
    description: "개인 키의 scope, 수명, 회전 및 유예 정책을 설정합니다.",
    icon: <KeyRounded color="primary" />,
    anyOf: ["manage_key_permissions", "manage_roles", "manage_api_keys"],
  },
  {
    to: "/admin/integrations/mcp",
    title: "MCP · API",
    description: "MCP transport와 노출할 tool/resource 권한을 관리합니다.",
    icon: <HubRounded color="primary" />,
    anyOf: ["manage_settings"],
  },
  {
    to: "/admin/workflows/review",
    title: "검토 · 승인",
    description: "필요한 경우에만 팀장 검토와 승인/반려 흐름을 활성화합니다.",
    icon: <RuleRounded color="primary" />,
    anyOf: ["manage_approval_policies"],
  },
];

export function AdminOverviewPage() {
  const navigate = useNavigate();
  const access = useAdminAccess();
  const visibleCards = cards.filter((card) => access.canAny(card.anyOf));
  return (
    <SettingsPage
      title="서비스 관리"
      description="moyro 인스턴스 전체에 적용되는 인증, AI, 키 및 업무 흐름 설정입니다."
      actions={<Chip label="오프라인 운영 준비" color="success" variant="outlined" />}
    >
      <Grid container spacing={2}>
        {visibleCards.map((card) => (
          <Grid key={card.to} size={{ xs: 12, md: 6 }}>
            <Card variant="outlined" sx={{ height: "100%" }}>
              <CardActionArea onClick={() => navigate(card.to)} sx={{ height: "100%", alignItems: "stretch" }}>
                <CardContent>
                  <Stack direction="row" sx={{ alignItems: "center", gap: 1.25 }}>
                    {card.icon}
                    <Typography component="h2" variant="h5">{card.title}</Typography>
                  </Stack>
                  <Typography color="text.secondary" sx={{ mt: 1.25 }}>{card.description}</Typography>
                </CardContent>
              </CardActionArea>
            </Card>
          </Grid>
        ))}
      </Grid>
    </SettingsPage>
  );
}
