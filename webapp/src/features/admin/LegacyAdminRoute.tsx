import ArrowBackRounded from "@mui/icons-material/ArrowBackRounded";
import { Alert, Box, Button, Chip, Paper, Stack, Typography } from "@mui/material";
import { useNavigate } from "react-router-dom";

export function LegacyAdminRoute() {
  const navigate = useNavigate();
  return (
    <Box sx={{ minHeight: "100dvh", bgcolor: "background.default", p: { xs: 2, md: 5 } }}>
      <Paper variant="outlined" sx={{ maxWidth: 840, mx: "auto", p: { xs: 2.5, md: 4 } }}>
        <Stack spacing={3}>
          <Stack direction="row" sx={{ alignItems: "center", justifyContent: "space-between", gap: 2, flexWrap: "wrap" }}>
            <Box>
              <Typography component="h1" variant="h4">호환 운영 API</Typography>
              <Typography color="text.secondary" sx={{ mt: 0.75 }}>
                레거시 Mattermost 운영 경계의 지원 상태를 안내합니다.
              </Typography>
            </Box>
            <Chip label="v0.2.3 호환 경계" color="warning" variant="outlined" />
          </Stack>
          <Alert severity="warning">
            로그·클러스터·Busy 상태·백그라운드 작업의 레거시 Mattermost 운영 API는 moyro v0.2.3에서
            실제 운영 기능으로 제공하지 않습니다. 관련 호환 API는 거짓 성공 대신 501 Not Implemented를 반환합니다.
          </Alert>
          <Typography>
            사이트, Keycloak OIDC, AI, 키·권한, MCP, 승인 정책과 Trusted Native 플러그인은
            서비스 관리자 메뉴에서 PostgreSQL 기반 상태로 관리합니다.
          </Typography>
          <Stack direction={{ xs: "column", sm: "row" }} spacing={1.5}>
            <Button variant="contained" startIcon={<ArrowBackRounded />} onClick={() => navigate("/admin/overview")}>
              서비스 관리로 돌아가기
            </Button>
            <Button variant="outlined" onClick={() => navigate("/workspace")}>워크스페이스</Button>
          </Stack>
        </Stack>
      </Paper>
    </Box>
  );
}
