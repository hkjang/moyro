import { Alert, CircularProgress, Stack } from "@mui/material";
import { useEffect, useState } from "react";
import { useSelector } from "react-redux";
import { useParams } from "react-router-dom";

import { prefsApi } from "@/api/client";
import { FormSection, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";
import { PluginSurface } from "./PluginSurface";
import { usePluginRegistryState } from "./registry";
import { mattermostPluginStore } from "./runtime";

export function PluginUserSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const { pluginId = "" } = useParams<{ pluginId: string }>();
  const registry = usePluginRegistryState();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const registration = registry.userSettings.find((entry) => (
    entry.pluginId === pluginId || entry.settingsId === pluginId
  ));

  useEffect(() => {
    if (!token || !registration) {
      setLoading(false);
      return;
    }
    let active = true;
    setLoading(true);
    setError("");
    prefsApi.list(token).then((preferences) => {
      if (!active) return;
      mattermostPluginStore.updateContext({ preferences });
    }).catch((reason: unknown) => {
      if (active) setError(reason instanceof Error ? reason.message : "개인 설정을 불러오지 못했습니다.");
    }).finally(() => {
      if (active) setLoading(false);
    });
    return () => { active = false; };
  }, [registration, token]);

  if (!registration) {
    return (
      <SettingsPage title="플러그인 설정" description="활성 플러그인이 제공하는 개인 설정입니다.">
        <Alert severity="warning">플러그인이 비활성화되었거나 웹 번들을 불러오지 못했습니다.</Alert>
      </SettingsPage>
    );
  }

  return (
    <SettingsPage
      title={registration.uiName}
      description="이 플러그인이 제공하는 내 계정 전용 설정입니다."
    >
      {loading && (
        <Stack direction="row" spacing={1.5} sx={{ alignItems: "center" }} role="status">
          <CircularProgress size={22} /> 개인 설정을 불러오는 중…
        </Stack>
      )}
      {error && <Alert severity="warning">{error}</Alert>}
      {!loading && registration.sections.map((section, index) => (
        <FormSection key={`${registration.id}-${index}`} title={section.title}>
          <PluginSurface
            component={section.component}
            label={`${registration.pluginId} user settings`}
          />
        </FormSection>
      ))}
    </SettingsPage>
  );
}
