import CheckCircleRounded from "@mui/icons-material/CheckCircleRounded";
import ContentCopyRounded from "@mui/icons-material/ContentCopyRounded";
import {
  Alert,
  Box,
  Button,
  FormControlLabel,
  Grid,
  InputAdornment,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useRef, useState } from "react";
import { useSelector } from "react-redux";
import { moyroAdminApi, type OIDCProviderSettings } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

const DEFAULT_PROVIDER: OIDCProviderSettings = {
  kind: "keycloak",
  name: "Keycloak",
  enabled: false,
  issuer_url: "",
  client_id: "",
  scopes: ["openid", "profile", "email"],
  username_claim: "preferred_username",
  email_claim: "email",
  allow_signup: true,
  require_verified_email: true,
  allow_insecure_backchannel: false,
  discovery_status: "unknown",
};

export function KeycloakSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [provider, setProvider] = useState<OIDCProviderSettings>(DEFAULT_PROVIDER);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const editRevision = useRef(0);
  const callbackURL = provider.redirect_url?.trim()
    || `${window.location.origin}/api/moyro/v1/auth/oidc/callback`;

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    moyroAdminApi.listOIDCProviders(token).then(
      (rows) => {
        if (cancelled) return;
        const keycloak = rows.find((row) => row.kind === "keycloak") ?? rows[0];
        if (keycloak) setProvider({ ...DEFAULT_PROVIDER, ...keycloak, client_secret: "" });
        setError("");
      },
      (err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Keycloak 설정 API에 연결하지 못했습니다.");
      },
    ).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [token]);

  const update = <K extends keyof OIDCProviderSettings>(key: K, value: OIDCProviderSettings[K]) => {
    editRevision.current += 1;
    setProvider((prev) => ({ ...prev, [key]: value, discovery_status: "unknown" }));
    setSaved("");
  };

  async function save() {
    if (!token || testing) return;
    setSaving(true);
    setSaved("");
    try {
      const payload: OIDCProviderSettings = { ...provider };
      if (!payload.client_secret?.trim()) delete payload.client_secret;
      const value = payload.id
        ? await moyroAdminApi.patchOIDCProvider(token, payload.id, payload)
        : await moyroAdminApi.createOIDCProvider(token, payload);
      setProvider({ ...DEFAULT_PROVIDER, ...value, client_secret: "" });
      setError("");
      setSaved("저장되었습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  async function testConnection() {
    if (!token || saving) return;
    const testedRevision = editRevision.current;
    setTesting(true);
    setError("");
    setSaved("");
    setProvider((prev) => ({ ...prev, discovery_status: "unknown" }));
    try {
      const payload: OIDCProviderSettings = { ...provider };
      if (!payload.client_secret?.trim()) delete payload.client_secret;
      const result = await moyroAdminApi.testOIDCProvider(token, payload);
      if (editRevision.current !== testedRevision) return;
      setProvider((prev) => ({
        ...prev,
        issuer_url: result.issuer || prev.issuer_url,
        discovery_status: result.ok ? "ready" : "error",
      }));
      setError(result.ok ? "" : result.message || "OIDC discovery 확인에 실패했습니다.");
      if (result.ok) setSaved(`연동 확인 완료${result.issuer ? ` · ${result.issuer}` : ""}`);
    } catch (err) {
      if (editRevision.current !== testedRevision) return;
      setProvider((prev) => ({ ...prev, discovery_status: "error" }));
      setError(err instanceof Error ? err.message : "연동 확인에 실패했습니다.");
    } finally {
      setTesting(false);
    }
  }

  return (
    <SettingsPage title="Keycloak SSO" description="Issuer URL의 OIDC discovery 문서를 이용해 로그인 endpoint와 키를 자동 구성합니다.">
      <LoadState loading={loading} error={error}>
        {provider.discovery_status === "ready" && (
          <Alert severity="success" icon={<CheckCircleRounded />}>OIDC discovery와 JWKS 서명 키를 확인했습니다.</Alert>
        )}
        <SettingsCard title="연결 정보" description="Client Secret은 저장 후 다시 표시되지 않습니다.">
          <Stack spacing={2.25}>
            <FormControlLabel
              control={<Switch checked={provider.enabled} onChange={(event) => update("enabled", event.target.checked)} />}
              label="Keycloak 로그인을 사용합니다"
            />
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, md: 8 }}>
                <TextField
                  fullWidth
                  required
                  label="Issuer URL"
                  value={provider.issuer_url}
                  placeholder="https://keycloak.internal/realms/moyro"
                  onChange={(event) => update("issuer_url", event.target.value)}
                  helperText="realm까지 포함한 Issuer 또는 discovery 문서 주소를 입력하세요."
                />
              </Grid>
              <Grid size={{ xs: 12, md: 4 }}>
                <TextField fullWidth label="표시 이름" value={provider.name} onChange={(event) => update("name", event.target.value)} />
              </Grid>
              <Grid size={{ xs: 12, md: 6 }}>
                <TextField fullWidth required label="Client ID" value={provider.client_id} onChange={(event) => update("client_id", event.target.value)} />
              </Grid>
              <Grid size={{ xs: 12, md: 6 }}>
                <TextField
                  fullWidth
                  type="password"
                  label="Client Secret"
                  value={provider.client_secret ?? ""}
                  onChange={(event) => update("client_secret", event.target.value)}
                  placeholder={provider.client_secret_state?.configured ? "저장된 secret 유지" : "Keycloak client secret"}
                />
              </Grid>
              <Grid size={{ xs: 12 }}>
                <TextField
                  fullWidth
                  label="Callback URL"
                  value={callbackURL}
                  slotProps={{
                    input: {
                      readOnly: true,
                      endAdornment: (
                        <InputAdornment position="end">
                          <Button
                            size="small"
                            startIcon={<ContentCopyRounded />}
                            onClick={() => void navigator.clipboard.writeText(callbackURL)}
                          >복사</Button>
                        </InputAdornment>
                      ),
                    },
                  }}
                />
              </Grid>
            </Grid>
          </Stack>
        </SettingsCard>

        <SettingsCard title="Claim 매핑" description="기본값으로 대부분의 Keycloak realm과 바로 연동됩니다.">
          <Grid container spacing={2}>
            <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth label="Scopes" value={provider.scopes.join(" ")} onChange={(event) => update("scopes", event.target.value.split(/\s+/).filter(Boolean))} /></Grid>
            <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth label="Username claim" value={provider.username_claim} onChange={(event) => update("username_claim", event.target.value)} /></Grid>
            <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth label="Email claim" value={provider.email_claim} onChange={(event) => update("email_claim", event.target.value)} /></Grid>
            <Grid size={{ xs: 12, md: 6 }}>
              <Stack spacing={0.5}>
                <FormControlLabel control={<Switch checked={provider.allow_signup} onChange={(event) => update("allow_signup", event.target.checked)} />} label="처음 로그인한 사용자를 자동 생성" />
                <FormControlLabel control={<Switch checked={provider.require_verified_email} onChange={(event) => update("require_verified_email", event.target.checked)} />} label="검증된 이메일 claim 필수" />
              </Stack>
            </Grid>
            <Grid size={{ xs: 12 }}>
              <TextField fullWidth multiline minRows={3} label="내부 CA 인증서 (선택)" value={provider.ca_certificate_pem ?? ""} onChange={(event) => update("ca_certificate_pem", event.target.value)} />
            </Grid>
            <Grid size={{ xs: 12 }}>
              <FormControlLabel
                control={<Switch checked={provider.allow_insecure_backchannel} onChange={(event) => update("allow_insecure_backchannel", event.target.checked)} />}
                label="신뢰할 수 있는 내부망의 HTTP back-channel 허용"
              />
              {provider.allow_insecure_backchannel && (
                <Alert severity="warning" sx={{ mt: 1 }}>
                  Token, JWKS, UserInfo endpoint에만 적용됩니다. Client secret과 authorization code가 평문 HTTP로 전송될 수 있으므로 격리된 내부망에서만 사용하세요. HTTP 통신과 JWKS는 가로채기·변조될 수 있습니다. 브라우저 authorization endpoint는 계속 HTTPS여야 합니다.
                </Alert>
              )}
            </Grid>
          </Grid>
          {!provider.require_verified_email && (
            <Alert severity="warning" sx={{ mt: 2 }}>검증된 이메일 요구를 끄면 신규 계정 신뢰 수준이 낮아집니다. Keycloak realm 정책을 별도로 확인하세요.</Alert>
          )}
        </SettingsCard>

        <Box>
          <Stack direction={{ xs: "column", sm: "row" }} sx={{ gap: 1.5, justifyContent: "flex-end", alignItems: "center" }}>
            {saved && <Typography color="success.main" variant="body2" role="status">{saved}</Typography>}
            <Button variant="outlined" onClick={testConnection} disabled={testing || saving || !provider.issuer_url.trim() || !provider.client_id.trim()}>
              {testing ? "확인 중…" : "연동 확인"}
            </Button>
            <SaveBar saving={saving} saved="" onSave={save} disabled={testing} />
          </Stack>
        </Box>
      </LoadState>
    </SettingsPage>
  );
}
