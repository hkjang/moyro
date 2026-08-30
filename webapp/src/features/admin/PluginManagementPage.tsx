import { Alert, CircularProgress, LinearProgress, Stack, Typography } from "@mui/material";
import { useState } from "react";
import { useSelector } from "react-redux";
import { useNavigate } from "react-router-dom";

import { SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import { PluginAdminPanel } from "@/plugins/PluginAdminPanel";
import type { RootState } from "@/store";
import { useAdminPlugins } from "./AdminPluginsContext";

export function PluginManagementPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const navigate = useNavigate();
  const {
    plugins,
    statuses,
    runtimeManagementEnabled,
    initialized,
    loading,
    error,
    refresh,
  } = useAdminPlugins();
  const [operationError, setOperationError] = useState<string | null>(null);

  return (
    <SettingsPage
      title="플러그인"
      description="Mattermost 호환 플러그인 번들을 설치하고 실행 상태를 관리합니다. 각 플러그인의 설정은 왼쪽 하위 메뉴에서 엽니다."
    >
      <SettingsCard
        title="설치된 플러그인"
        description="manifest 형식과 번들 구조는 검사하지만 코드 서명과 배포자 신원은 검증하지 않습니다. 같은 ID를 교체하려면 교체 허용을 선택하세요."
      >
        <Alert severity="warning" sx={{ mb: 2 }}>
          Trusted Native 보안 모델: 서명 미검증 플러그인의 서버 실행 파일은 Moyro 서버 프로세스 권한으로 실행될 수 있습니다. 배포자와 아카이브 내용을 직접 확인한 신뢰 코드만 업로드하세요.
        </Alert>
        {!initialized ? (
          <Stack direction="row" sx={{ alignItems: "center", gap: 1.5 }} role="status">
            <CircularProgress size={22} />
            <Typography>플러그인 목록을 불러오는 중입니다.</Typography>
          </Stack>
        ) : token ? (
          <Stack spacing={2}>
            {loading && <LinearProgress aria-label="플러그인 목록 새로고침 중" />}
            {error && <Alert severity="warning">{error}</Alert>}
            {operationError && <Alert severity="warning">{operationError}</Alert>}
            <PluginAdminPanel
              token={token}
              plugins={[...plugins]}
              statuses={[...statuses]}
              runtimeManagementEnabled={runtimeManagementEnabled}
              onRefresh={refresh}
              onError={setOperationError}
              onOpenSettings={(pluginID) => navigate(`/admin/integrations/plugins/${encodeURIComponent(pluginID)}`)}
            />
          </Stack>
        ) : (
          <Alert severity="error">관리자 세션이 만료되었습니다. 다시 로그인해 주세요.</Alert>
        )}
      </SettingsCard>
    </SettingsPage>
  );
}
