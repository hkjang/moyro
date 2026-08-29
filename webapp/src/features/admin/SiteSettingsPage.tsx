import {
  Alert,
  FormControlLabel,
  Stack,
  Switch,
  TextField,
} from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { moyroAdminApi, type SiteSettings } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import { InviteManagementCard } from "@/features/admin/InviteManagementCard";
import type { RootState } from "@/store";

const DEFAULT_SETTINGS: SiteSettings = {
  site_name: "moyro",
  public_base_url: "",
  allowed_outgoing_hosts: [],
  local_signup_enabled: false,
};

const parseHosts = (value: string) => Array.from(new Set(
  value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean),
));

function validatePublicURL(value: string): string {
  if (!value.trim()) return "";
  try {
    const parsed = new URL(value);
    if ((parsed.protocol !== "https:" && parsed.protocol !== "http:") || !parsed.hostname) {
      return "HTTP(S) scheme과 hostname을 포함한 절대 URL을 입력하세요.";
    }
    if (parsed.username || parsed.password) return "URL에 계정 정보를 포함할 수 없습니다.";
    if ((parsed.pathname && parsed.pathname !== "/") || parsed.search || parsed.hash) {
      return "경로, query, fragment 없이 origin만 입력하세요.";
    }
    return "";
  } catch {
    return "https://로 시작하는 올바른 절대 URL을 입력하세요.";
  }
}

export function SiteSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [settings, setSettings] = useState<SiteSettings>(DEFAULT_SETTINGS);
  const [hostsText, setHostsText] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const urlError = useMemo(
    () => settings.public_base_url ? validatePublicURL(settings.public_base_url) : "",
    [settings.public_base_url],
  );
  const invalidHost = useMemo(
    () => parseHosts(hostsText).find((host) => host.includes("://") || /[/\\@?#\s]/.test(host)) ?? "",
    [hostsText],
  );

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    moyroAdminApi.getSettings<SiteSettings>(token, "site").then(
      (value) => {
        if (cancelled) return;
        const next = { ...DEFAULT_SETTINGS, ...value };
        setSettings(next);
        setHostsText(next.allowed_outgoing_hosts.join("\n"));
        setError("");
      },
      (err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "사이트 설정 API에 연결하지 못했습니다.");
      },
    ).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [token]);

  async function save() {
    if (!token) return;
    const validationError = validatePublicURL(settings.public_base_url);
    if (!settings.site_name.trim() || validationError || invalidHost) {
      setError(validationError || (invalidHost
        ? `hostname/IP만 입력하세요: ${invalidHost}`
        : "사이트 이름을 입력하세요."));
      return;
    }
    setSaving(true);
    setSaved("");
    try {
      const payload: SiteSettings = {
        site_name: settings.site_name.trim(),
        public_base_url: settings.public_base_url.trim().replace(/\/+$/, ""),
        allowed_outgoing_hosts: parseHosts(hostsText),
        local_signup_enabled: settings.local_signup_enabled,
      };
      const result = await moyroAdminApi.patchSettings<SiteSettings>(token, "site", payload);
      setSettings(result);
      setHostsText(result.allowed_outgoing_hosts.join("\n"));
      setError("");
      setSaved("사이트 설정을 저장했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "사이트 설정을 저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsPage title="사이트 설정" description="사용자에게 표시할 이름과 외부에서 접근 가능한 기준 URL, 내부망 outbound 허용 대상을 관리합니다.">
      <LoadState loading={loading} error={error}>
        <SettingsCard title="서비스 주소" description="로그인 redirect와 링크 생성에 사용할 절대 origin입니다. 운영 환경에서는 인증서가 유효한 HTTPS URL을 권장합니다.">
          <Stack spacing={2.25}>
            <TextField
              required
              label="사이트 이름"
              value={settings.site_name}
              onChange={(event) => { setSettings((current) => ({ ...current, site_name: event.target.value })); setSaved(""); }}
              helperText="로그인 화면과 알림에 표시됩니다."
            />
            <TextField
              label="Public Base URL"
              value={settings.public_base_url}
              onChange={(event) => { setSettings((current) => ({ ...current, public_base_url: event.target.value })); setSaved(""); }}
              placeholder="https://moyro.internal.example"
              error={Boolean(urlError)}
              helperText={urlError || "경로 없는 HTTP(S) 절대 origin입니다. 운영 환경은 HTTPS를 권장합니다."}
            />
            {settings.public_base_url.trim().startsWith("http://") && (
              <Alert severity="warning">HTTP URL은 전송 중 인증 정보가 노출될 수 있습니다. 운영 환경에서는 HTTPS를 사용하세요.</Alert>
            )}
          </Stack>
        </SettingsCard>

        <SettingsCard title="로컬 계정 가입" description="기본값은 닫힘입니다. 초대 링크와 Keycloak SSO는 이 설정과 별도로 사용할 수 있습니다.">
          <Stack spacing={1.5}>
            <FormControlLabel
              control={(
                <Switch
                  checked={settings.local_signup_enabled}
                  onChange={(event) => {
                    setSettings((current) => ({ ...current, local_signup_enabled: event.target.checked }));
                    setSaved("");
                  }}
                />
              )}
              label="로그인 화면에서 누구나 로컬 계정을 만들 수 있도록 허용"
            />
            {settings.local_signup_enabled ? (
              <Alert severity="warning">내부망에 접근할 수 있는 모든 사용자가 계정을 만들 수 있습니다. 필요한 기간에만 켜세요.</Alert>
            ) : (
              <Alert severity="info">공개 회원가입이 닫혀 있습니다. 관리자가 발급한 초대 링크 또는 Keycloak SSO를 사용하세요.</Alert>
            )}
          </Stack>
        </SettingsCard>

        <SettingsCard title="Outbound host allowlist" description="Webhook 등 서버 outbound 요청이 도달할 수 있는 hostname 또는 IP를 명시적으로 제한합니다.">
          <Stack spacing={2}>
            <TextField
              multiline
              minRows={5}
              label="허용 hostname / IP"
              value={hostsText}
              onChange={(event) => { setHostsText(event.target.value); setSaved(""); }}
              placeholder={"hooks.internal.example\n10.20.30.40"}
              error={Boolean(invalidHost)}
              helperText={invalidHost
                ? `scheme이나 경로 없이 hostname/IP만 입력하세요: ${invalidHost}`
                : "한 줄 또는 쉼표로 구분합니다. URL scheme과 경로는 입력하지 않습니다."}
            />
            {parseHosts(hostsText).length === 0 && (
              <Alert severity="info">허용 목록이 비어 있어 외부 목적지로의 전송이 모두 차단됩니다.</Alert>
            )}
          </Stack>
        </SettingsCard>
        <InviteManagementCard />
        <SaveBar saving={saving} saved={saved} onSave={save} />
      </LoadState>
    </SettingsPage>
  );
}
