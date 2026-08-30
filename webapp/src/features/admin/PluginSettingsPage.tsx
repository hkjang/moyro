import ArrowBackRounded from "@mui/icons-material/ArrowBackRounded";
import { Alert, Box, Button, Chip, CircularProgress, Stack, Typography } from "@mui/material";
import { useSelector } from "react-redux";
import { useNavigate, useParams } from "react-router-dom";

import { SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import { PluginAdminSettingsPanel } from "@/plugins/PluginAdminSettingsPanel";
import type { RootState } from "@/store";
import { useAdminPlugins } from "./AdminPluginsContext";
import {
  adminPluginDescription,
  adminPluginDisplayName,
  adminPluginID,
  adminPluginVersion,
} from "./adminPluginIdentity";

const pluginManagementPath = "/admin/integrations/plugins";

export function PluginSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const navigate = useNavigate();
  const { pluginId = "" } = useParams<{ pluginId: string }>();
  const { plugins, statuses, initialized, loading, error, refresh } = useAdminPlugins();
  const plugin = plugins.find((candidate) => adminPluginID(candidate) === pluginId);

  if (!initialized) {
    return (
      <SettingsPage title="플러그인 설정" description="설치된 플러그인을 확인하고 있습니다.">
        <Stack direction="row" sx={{ alignItems: "center", gap: 1.5 }} role="status">
          <CircularProgress size={22} />
          <Typography>플러그인 정보를 불러오는 중입니다.</Typography>
        </Stack>
      </SettingsPage>
    );
  }

  if (!plugin || !token) {
    return (
      <SettingsPage
        title="플러그인을 찾을 수 없습니다"
        description="삭제되었거나 현재 관리자 계정에서 조회할 수 없는 플러그인입니다."
        actions={(
          <Button startIcon={<ArrowBackRounded />} variant="outlined" onClick={() => navigate(pluginManagementPath, { replace: true })}>
            플러그인 관리로
          </Button>
        )}
      >
        {error ? (
          <Alert
            severity="warning"
            action={<Button color="inherit" size="small" onClick={() => void refresh()}>다시 시도</Button>}
          >
            {error}
          </Alert>
        ) : <Alert severity="info">설치 목록을 새로고침한 뒤 다시 선택해 주세요.</Alert>}
      </SettingsPage>
    );
  }

  const pluginID = adminPluginID(plugin);
  const state = statuses.find((status) => status.plugin_id === pluginID)?.state
    ?? String(plugin.state ?? "unknown");
  const enabled = typeof plugin.enabled === "boolean"
    ? plugin.enabled
    : state === "running" || state === "enabled";
  const failedButEnabled = enabled && state !== "running" && state !== "enabled";

  return (
    <SettingsPage
      title={adminPluginDisplayName(plugin)}
      description={adminPluginDescription(plugin)}
      actions={(
        <Button startIcon={<ArrowBackRounded />} variant="outlined" onClick={() => navigate(pluginManagementPath)}>
          전체 플러그인 관리
        </Button>
      )}
    >
      {loading && <Alert severity="info">플러그인 실행 상태를 새로고치는 중입니다.</Alert>}
      {error && (
        <Alert
          severity="warning"
          action={<Button color="inherit" size="small" onClick={() => void refresh()}>다시 시도</Button>}
        >
          {error}
        </Alert>
      )}
      <SettingsCard title="플러그인 정보" description="설치 상태와 전용 관리자 화면의 대상입니다.">
        <Stack direction={{ xs: "column", sm: "row" }} sx={{ alignItems: { sm: "center" }, gap: 1.25 }}>
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Typography variant="subtitle2" sx={{ overflowWrap: "anywhere" }}>{pluginID}</Typography>
            <Typography variant="body2" color="text.secondary">
              v{adminPluginVersion(plugin)}{plugin.runtime ? ` · ${String(plugin.runtime)}` : ""}
            </Typography>
          </Box>
          <Chip
            size="small"
            color={failedButEnabled ? "error" : enabled ? "success" : "default"}
            variant={enabled && !failedButEnabled ? "filled" : "outlined"}
            label={failedButEnabled ? `${state} · 활성화됨` : state}
          />
        </Stack>
        {plugin.error && <Alert severity="error" sx={{ mt: 2 }}>{String(plugin.error)}</Alert>}
      </SettingsCard>
      <SettingsCard
        title="플러그인 설정"
        description="manifest 설정 스키마와 플러그인 웹 번들이 제공하는 사용자 지정 필드를 한 화면에서 관리합니다."
      >
        <PluginAdminSettingsPanel key={pluginID} token={token} pluginID={pluginID} enabled={enabled} />
      </SettingsCard>
    </SettingsPage>
  );
}
