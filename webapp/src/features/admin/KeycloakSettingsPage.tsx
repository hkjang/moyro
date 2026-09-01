import CheckCircleRounded from "@mui/icons-material/CheckCircleRounded";
import ContentCopyRounded from "@mui/icons-material/ContentCopyRounded";
import AddRounded from "@mui/icons-material/AddRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  Grid,
  IconButton,
  InputAdornment,
  Stack,
  Switch,
  TextField,
  MenuItem,
  Typography,
} from "@mui/material";
import { useEffect, useRef, useState } from "react";
import { useSelector } from "react-redux";
import { moyroAdminApi, type OIDCProviderSettings } from "@/api/client";
import {
  oidcOnboardingApi,
  type ManagedOIDCProviderSettings,
  type OIDCGroupMapping,
  type OIDCOnboardingTeamTarget,
} from "@/api/oidc-onboarding";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

const DEFAULT_PROVIDER: ManagedOIDCProviderSettings = {
  kind: "keycloak",
  name: "Keycloak",
  enabled: false,
  issuer_url: "",
  client_id: "",
  scopes: ["openid", "profile", "email"],
  username_claim: "preferred_username",
  email_claim: "email",
  groups_claim: "groups",
  group_mappings: [],
  allow_signup: true,
  require_verified_email: true,
  allow_insecure_backchannel: false,
  discovery_status: "unknown",
};

export function KeycloakSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [provider, setProvider] = useState<ManagedOIDCProviderSettings>(DEFAULT_PROVIDER);
  const [onboardingTeams, setOnboardingTeams] = useState<OIDCOnboardingTeamTarget[]>([]);
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
        if (keycloak) setProvider({
          ...DEFAULT_PROVIDER,
          ...keycloak,
          group_mappings: (keycloak as ManagedOIDCProviderSettings).group_mappings ?? [],
          client_secret: "",
        } as ManagedOIDCProviderSettings);
        setError("");
      },
      (err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Keycloak 설정 API에 연결하지 못했습니다.");
      },
    ).finally(() => { if (!cancelled) setLoading(false); });
    void oidcOnboardingApi.targets(token).then(
      (result) => { if (!cancelled) setOnboardingTeams(result.teams); },
      (err: unknown) => { if (!cancelled) setError(err instanceof Error ? err.message : "온보딩 대상을 불러오지 못했습니다."); },
    );
    return () => { cancelled = true; };
  }, [token]);

  const update = <K extends keyof ManagedOIDCProviderSettings>(key: K, value: ManagedOIDCProviderSettings[K]) => {
    editRevision.current += 1;
    setProvider((prev) => ({ ...prev, [key]: value, discovery_status: "unknown" }));
    setSaved("");
  };

  async function save() {
    if (!token || testing) return;
    setSaving(true);
    setSaved("");
    try {
      const payload: ManagedOIDCProviderSettings = { ...provider };
      if (!payload.client_secret?.trim()) delete payload.client_secret;
      const value = payload.id
        ? await moyroAdminApi.patchOIDCProvider(token, payload.id, payload)
        : await moyroAdminApi.createOIDCProvider(token, payload);
      setProvider({
        ...DEFAULT_PROVIDER,
        ...value,
        group_mappings: (value as ManagedOIDCProviderSettings).group_mappings ?? [],
        client_secret: "",
      } as ManagedOIDCProviderSettings);
      setError("");
      setSaved("저장되었습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  const updateMapping = (index: number, patch: Partial<OIDCGroupMapping>) => {
    const mappings = provider.group_mappings.map((mapping, mappingIndex) => mappingIndex === index ? { ...mapping, ...patch } : mapping);
    update("group_mappings", mappings);
  };

  const addMapping = () => update("group_mappings", [...provider.group_mappings, {
    group: "",
    account_role: "user",
    team_role: "member",
    channel_ids: [],
    channel_role: "member",
    guest_expires_after_seconds: 30 * 24 * 60 * 60,
    guest_file_download: false,
  }]);

  const removeMapping = (index: number) => update("group_mappings", provider.group_mappings.filter((_, mappingIndex) => mappingIndex !== index));

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
            <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth label="Groups claim" value={provider.groups_claim} onChange={(event) => update("groups_claim", event.target.value)} helperText="Keycloak ID token의 그룹 배열 claim" /></Grid>
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

        <SettingsCard title="그룹 자동 온보딩" description="Keycloak 그룹이 확인될 때 팀·채널·계정 역할을 멱등으로 추가합니다. 기존 수동 권한은 제거하지 않습니다.">
          <Stack spacing={2}>
            {provider.group_mappings.length === 0 && <Alert severity="info">매핑이 없으면 신규 SSO 사용자는 기존 기본 공간에 가입합니다.</Alert>}
            {provider.group_mappings.map((mapping, index) => {
              const team = onboardingTeams.find((candidate) => candidate.id === mapping.team_id);
              return (
                <Box key={`${index}-${mapping.group}`} sx={{ border: 1, borderColor: "divider", borderRadius: 1.5, p: 2 }}>
                  <Stack spacing={1.5}>
                    <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
                      <Typography variant="subtitle2" sx={{ flex: 1 }}>그룹 매핑 {index + 1}</Typography>
                      <IconButton aria-label={`그룹 매핑 ${index + 1} 삭제`} onClick={() => removeMapping(index)}><DeleteOutlineRounded /></IconButton>
                    </Stack>
                    <Grid container spacing={1.5}>
                      <Grid size={{ xs: 12, md: 6 }}>
                        <TextField fullWidth required label="Keycloak 그룹" value={mapping.group} placeholder="/engineering" onChange={(event) => updateMapping(index, { group: event.target.value })} />
                      </Grid>
                      <Grid size={{ xs: 12, md: 6 }}>
                        <TextField select fullWidth label="계정 역할" value={mapping.account_role} onChange={(event) => {
                          const accountRole = event.target.value as OIDCGroupMapping["account_role"];
                          updateMapping(index, accountRole === "guest"
                            ? { account_role: accountRole, team_role: "member", channel_role: "member" }
                            : { account_role: accountRole });
                        }}>
                          <MenuItem value="user">사용자</MenuItem>
                          <MenuItem value="admin">시스템 관리자</MenuItem>
                          <MenuItem value="guest">외부 게스트</MenuItem>
                        </TextField>
                      </Grid>
                      <Grid size={{ xs: 12, md: 8 }}>
                        <TextField select fullWidth label="팀" value={mapping.team_id ?? ""} onChange={(event) => updateMapping(index, { team_id: event.target.value, channel_ids: [] })}>
                          <MenuItem value="">계정 역할만 적용</MenuItem>
                          {onboardingTeams.map((candidate) => <MenuItem key={candidate.id} value={candidate.id}>{candidate.display_name}</MenuItem>)}
                        </TextField>
                      </Grid>
                      <Grid size={{ xs: 12, md: 4 }}>
                        <TextField select fullWidth label="팀 역할" value={mapping.team_role ?? "member"} disabled={!mapping.team_id || mapping.account_role === "guest"} onChange={(event) => updateMapping(index, { team_role: event.target.value as OIDCGroupMapping["team_role"] })}>
                          <MenuItem value="member">멤버</MenuItem>
                          <MenuItem value="admin">팀 관리자</MenuItem>
                        </TextField>
                      </Grid>
                      <Grid size={{ xs: 12, md: 8 }}>
                        <TextField
                          select
                          fullWidth
                          label="자동 가입 채널"
                          value={mapping.channel_ids ?? []}
                          disabled={!team}
                          onChange={(event) => {
                            const value = event.target.value;
                            updateMapping(index, { channel_ids: typeof value === "string" ? value.split(",") : value as string[] });
                          }}
                          slotProps={{ select: { multiple: true } }}
                        >
                          {(team?.channels ?? []).map((channel) => (
                            <MenuItem key={channel.id} value={channel.id}>
                              <Checkbox checked={(mapping.channel_ids ?? []).includes(channel.id)} />
                              {channel.display_name}
                            </MenuItem>
                          ))}
                        </TextField>
                      </Grid>
                      <Grid size={{ xs: 12, md: 4 }}>
                        <TextField select fullWidth label="채널 역할" value={mapping.channel_role ?? "member"} disabled={!mapping.channel_ids?.length || mapping.account_role === "guest"} onChange={(event) => updateMapping(index, { channel_role: event.target.value as OIDCGroupMapping["channel_role"] })}>
                          <MenuItem value="member">멤버</MenuItem>
                          <MenuItem value="admin">채널 관리자</MenuItem>
                        </TextField>
                      </Grid>
                      {mapping.account_role === "guest" && (
                        <>
                          <Grid size={{ xs: 12, md: 6 }}>
                            <TextField select fullWidth label="게스트 접근 기간" value={mapping.guest_expires_after_seconds ?? 30 * 24 * 60 * 60} onChange={(event) => updateMapping(index, { guest_expires_after_seconds: Number(event.target.value) })}>
                              <MenuItem value={24 * 60 * 60}>1일</MenuItem>
                              <MenuItem value={7 * 24 * 60 * 60}>7일</MenuItem>
                              <MenuItem value={30 * 24 * 60 * 60}>30일</MenuItem>
                              <MenuItem value={90 * 24 * 60 * 60}>90일</MenuItem>
                            </TextField>
                          </Grid>
                          <Grid size={{ xs: 12, md: 6 }}>
                            <FormControlLabel control={<Switch checked={mapping.guest_file_download} onChange={(event) => updateMapping(index, { guest_file_download: event.target.checked })} />} label="원본 파일 다운로드 허용" />
                          </Grid>
                        </>
                      )}
                    </Grid>
                  </Stack>
                </Box>
              );
            })}
            <Button startIcon={<AddRounded />} variant="outlined" onClick={addMapping} disabled={provider.group_mappings.length >= 100}>그룹 매핑 추가</Button>
          </Stack>
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
