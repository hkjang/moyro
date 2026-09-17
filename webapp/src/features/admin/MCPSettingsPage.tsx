import ContentCopyRounded from "@mui/icons-material/ContentCopyRounded";
import {
  Alert,
  Button,
  FormControlLabel,
  Grid,
  IconButton,
  InputAdornment,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useState } from "react";
import { useSelector } from "react-redux";
import { moyroAdminApi, type MCPOAuthStatus, type MCPSettings } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

const DEFAULT_SETTINGS: MCPSettings = {
  enabled: false,
  transport: "streamable-http",
  endpoint_path: "/mcp",
  allowed_tools: [
    "list_teams",
    "list_channels",
    "search_messages",
    "get_thread",
    "create_post",
    "reply_to_thread",
    "list_pending_approvals",
    "approve_request",
    "reject_request",
  ],
  allowed_resources: ["moyro://teams", "moyro://channels", "moyro://threads"],
  required_scopes: ["mcp_read"],
  oauth: { enabled: false, resource: "", audience: [], scopes: ["mcp_read"] },
};

/** Permissions an SSO subject may be granted — the MCP half of the key vocabulary. */
export const MCP_OAUTH_SCOPES = ["mcp_read", "mcp_write", "request_approval", "review_approval"];

const parseList = (value: string) => value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean);

/** Why the server would refuse the SSO card as typed, before a round trip; "" when it would save. */
export function validateMCPOAuth(settings: MCPSettings, status: MCPOAuthStatus | undefined): string {
  const oauth = settings.oauth;
  if (oauth.resource.trim() && !/^https?:\/\/[^\s?#]+$/.test(oauth.resource.trim())) return "리소스 식별자는 query·fragment 없는 절대 http(s) URL이어야 합니다.";
  const unknown = oauth.scopes.filter((scope) => !MCP_OAUTH_SCOPES.includes(scope));
  if (unknown.length) return `SSO 범위에 쓸 수 없는 값입니다: ${unknown.join(", ")}`;
  if (!oauth.enabled) return "";
  if (!oauth.scopes.length) return "SSO 주체에게 줄 범위를 하나 이상 고르세요.";
  if (status && !status.oidc_configured) return "먼저 Keycloak SSO를 켜고 저장하세요. SSO 토큰은 그 발급자로 검증합니다.";
  if (!oauth.resource.trim() && status && !status.resource) return "리소스 식별자를 적거나 사이트 설정의 공개 주소를 먼저 저장하세요.";
  return "";
}

function CopyField({ label, value, helperText }: { label: string; value: string; helperText?: string }) {
  return (
    <TextField
      fullWidth
      label={label}
      value={value}
      helperText={helperText}
      slotProps={{
        input: {
          readOnly: true,
          endAdornment: value ? (
            <InputAdornment position="end">
              <IconButton aria-label={`${label} 복사`} size="small" onClick={() => void navigator.clipboard.writeText(value)}>
                <ContentCopyRounded fontSize="small" />
              </IconButton>
            </InputAdornment>
          ) : undefined,
        },
      }}
    />
  );
}

export function MCPSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [settings, setSettings] = useState<MCPSettings>(DEFAULT_SETTINGS);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    moyroAdminApi.getSettings<MCPSettings>(token, "mcp").then(
      (value) => {
        if (!cancelled) {
          setSettings({ ...DEFAULT_SETTINGS, ...value, oauth: { ...DEFAULT_SETTINGS.oauth, ...value.oauth }, transport: "streamable-http", endpoint_path: "/mcp" });
          setError("");
        }
      },
      (err: unknown) => { if (!cancelled) setError(err instanceof Error ? err.message : "MCP 설정 API에 연결하지 못했습니다."); },
    ).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [token]);

  const update = <K extends keyof MCPSettings>(key: K, value: MCPSettings[K]) => {
    setSettings((prev) => ({ ...prev, [key]: value }));
    setSaved("");
  };
  const updateOAuth = <K extends keyof MCPSettings["oauth"]>(key: K, value: MCPSettings["oauth"][K]) => {
    setSettings((prev) => ({ ...prev, oauth: { ...prev.oauth, [key]: value } }));
    setSaved("");
  };
  const status = settings.oauth_status;
  const oauthProblem = validateMCPOAuth(settings, status);

  async function save() {
    if (!token) return;
    if (oauthProblem) {
      setError(oauthProblem);
      return;
    }
    setSaving(true);
    try {
      const { oauth_status: _status, ...editable } = settings;
      const result = await moyroAdminApi.patchSettings<MCPSettings>(token, "mcp", {
        ...editable,
        transport: "streamable-http",
        endpoint_path: "/mcp",
      });
      setSettings({ ...DEFAULT_SETTINGS, ...result, oauth: { ...DEFAULT_SETTINGS.oauth, ...result.oauth } });
      setError("");
      setSaved("MCP·API 설정을 저장했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "MCP 설정을 저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsPage title="MCP · API" description="내부 자동화가 사용할 MCP endpoint와 노출 권한을 제한합니다.">
      <LoadState loading={loading} error={error}>
        <SettingsCard title="MCP endpoint" description="이번 릴리스는 현재 MCP 표준인 Streamable HTTP만 지원합니다.">
          <Stack spacing={2}>
            <Alert severity="info">Legacy HTTP+SSE transport는 제공하지 않습니다. MCP client를 Streamable HTTP endpoint로 연결하세요.</Alert>
            <FormControlLabel control={<Switch checked={settings.enabled} onChange={(event) => update("enabled", event.target.checked)} />} label="MCP endpoint를 활성화합니다" />
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, md: 5 }}>
                <TextField fullWidth label="Transport" value="Streamable HTTP" slotProps={{ input: { readOnly: true } }} helperText="고정값" />
              </Grid>
              <Grid size={{ xs: 12, md: 7 }}><TextField fullWidth label="Endpoint path" value="/mcp" slotProps={{ input: { readOnly: true } }} helperText="이번 릴리스의 고정 endpoint" /></Grid>
            </Grid>
          </Stack>
        </SettingsCard>
        <SettingsCard title="노출 범위" description="목록에 없는 tool과 resource는 서버에서 거부해야 합니다.">
          <Stack spacing={2}>
            <TextField multiline minRows={3} label="허용 tools" value={settings.allowed_tools.join("\n")} onChange={(event) => update("allowed_tools", parseList(event.target.value))} helperText="한 줄에 하나씩 입력합니다." />
            <TextField multiline minRows={3} label="허용 resources" value={settings.allowed_resources.join("\n")} onChange={(event) => update("allowed_resources", parseList(event.target.value))} />
            <TextField label="필수 key scopes" value={settings.required_scopes.join(", ")} onChange={(event) => update("required_scopes", parseList(event.target.value))} />
          </Stack>
        </SettingsCard>
        <SettingsCard title="SSO(OAuth)로 연결" description="개인 키 없이 Keycloak 액세스 토큰으로 /mcp에 들어오게 합니다. 키 체계는 그대로 남습니다.">
          <Stack spacing={2}>
            {status?.active && (
              <Alert severity="success">SSO 토큰을 받고 있습니다. MCP 클라이언트에는 아래 MCP 주소 하나만 주면 스스로 로그인해 토큰을 받아 옵니다.</Alert>
            )}
            {settings.oauth.enabled && status && !status.active && status.reason !== "disabled" && (
              <Alert severity="warning">켜져 있지만 지금은 토큰을 받지 않습니다: {status.reason}</Alert>
            )}
            {!settings.oauth.enabled && (
              <Alert severity="info">
                이 서버는 리소스 서버입니다. 로그인과 토큰 발급은 Keycloak이 하고, 여기서는 토큰의 서명·발급자·만료·대상을 검사해 웹으로 이미 로그인한 계정에만 연결합니다. 계정을 새로 만들지 않습니다.
              </Alert>
            )}
            <FormControlLabel
              control={<Switch checked={settings.oauth.enabled} onChange={(event) => updateOAuth("enabled", event.target.checked)} />}
              label="Keycloak 액세스 토큰으로 MCP 접속을 허용합니다"
            />
            <TextField
              label="리소스 식별자 (mcp.oauth.resource)"
              value={settings.oauth.resource}
              onChange={(event) => updateOAuth("resource", event.target.value)}
              placeholder={status?.resource || "https://chat.corp.example/mcp"}
              helperText="클라이언트가 실제로 접속하는 공개 HTTPS 주소 + /mcp. 비우면 사이트 설정의 공개 주소로 만듭니다. Keycloak Audience 매퍼에 넣을 값입니다."
            />
            <TextField
              label="허용 대상 (mcp.oauth.audience)"
              value={settings.oauth.audience.join(", ")}
              onChange={(event) => updateOAuth("audience", parseList(event.target.value))}
              placeholder="claude-mcp"
              helperText="Audience 매퍼 없이 쓸 때 토큰의 aud 또는 azp에 실려 오는 MCP 클라이언트 ID. 쉼표로 구분합니다. 웹 로그인 클라이언트와는 다른 클라이언트입니다."
            />
            <TextField
              label="SSO 주체에게 주는 범위 (mcp.oauth.scopes)"
              value={settings.oauth.scopes.join(", ")}
              onChange={(event) => updateOAuth("scopes", parseList(event.target.value))}
              helperText={`쓸 수 있는 값: ${MCP_OAUTH_SCOPES.join(", ")}. 토큰의 role로 권한이 올라가지 않으며 사용자의 현재 역할과 교집합만 적용됩니다.`}
            />
            {oauthProblem && <Alert severity="warning">{oauthProblem}</Alert>}
            <Typography variant="subtitle2">클라이언트에 줄 값</Typography>
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, md: 6 }}>
                <CopyField label="MCP 주소" value={status?.resource ?? ""} helperText="MCP 클라이언트에 넣는 URL" />
              </Grid>
              <Grid size={{ xs: 12, md: 6 }}>
                <CopyField label="메타데이터 주소" value={status?.metadata_url ?? ""} helperText="401 응답의 resource_metadata가 가리키는 문서(RFC 9728)" />
              </Grid>
              <Grid size={{ xs: 12, md: 6 }}>
                <CopyField label="인증 서버 (Keycloak issuer)" value={status?.issuer_url ?? ""} helperText="Keycloak SSO 설정의 발급자 주소를 그대로 씁니다" />
              </Grid>
            </Grid>
            {status?.metadata_url && (
              <Button size="small" variant="outlined" component="a" href={status.metadata_url} target="_blank" rel="noreferrer" sx={{ alignSelf: "flex-start" }}>
                메타데이터 문서 열기
              </Button>
            )}
          </Stack>
        </SettingsCard>
        <SaveBar saving={saving} saved={saved} onSave={save} />
      </LoadState>
    </SettingsPage>
  );
}
