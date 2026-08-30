import { Alert, CircularProgress, LinearProgress, Stack, Typography } from "@mui/material";
import { useCallback, useEffect, useState } from "react";
import { useSelector } from "react-redux";

import {
  adminApi,
  type AdminPlugin,
  type AdminPluginStatus,
} from "@/api/client";
import { SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import { PluginAdminPanel } from "@/plugins/PluginAdminPanel";
import type { RootState } from "@/store";

export function PluginManagementPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [plugins, setPlugins] = useState<AdminPlugin[]>([]);
  const [statuses, setStatuses] = useState<AdminPluginStatus[]>([]);
  const [runtimeManagementEnabled, setRuntimeManagementEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [initialized, setInitialized] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!token) {
      setLoading(false);
      setInitialized(true);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const [pluginRows, statusRows, capabilities] = await Promise.all([
        adminApi.listPlugins(token),
        adminApi.listPluginStatuses(token),
        adminApi.getPluginManagementCapabilities(token),
      ]);
      setPlugins(pluginRows);
      setStatuses(statusRows);
      setRuntimeManagementEnabled(
        capabilities.management_enabled && capabilities.uploads_enabled,
      );
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "플러그인 목록을 불러오지 못했습니다.");
    } finally {
      setLoading(false);
      setInitialized(true);
    }
  }, [token]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <SettingsPage
      title="플러그인"
      description="Mattermost 호환 플러그인 번들을 설치하고 런타임 상태와 플러그인별 설정을 관리합니다."
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
            <PluginAdminPanel
              token={token}
              plugins={plugins}
              statuses={statuses}
              runtimeManagementEnabled={runtimeManagementEnabled}
              onRefresh={load}
              onError={setError}
            />
          </Stack>
        ) : (
          <Alert severity="error">관리자 세션이 만료되었습니다. 다시 로그인해 주세요.</Alert>
        )}
      </SettingsCard>
    </SettingsPage>
  );
}
