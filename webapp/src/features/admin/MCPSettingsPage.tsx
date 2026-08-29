import {
  Alert,
  FormControlLabel,
  Grid,
  Stack,
  Switch,
  TextField,
} from "@mui/material";
import { useEffect, useState } from "react";
import { useSelector } from "react-redux";
import { moyroAdminApi, type MCPSettings } from "@/api/client";
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
};

const parseList = (value: string) => value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean);

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
          setSettings({ ...DEFAULT_SETTINGS, ...value, transport: "streamable-http", endpoint_path: "/mcp" });
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

  async function save() {
    if (!token) return;
    setSaving(true);
    try {
      const result = await moyroAdminApi.patchSettings<MCPSettings>(token, "mcp", {
        ...settings,
        transport: "streamable-http",
        endpoint_path: "/mcp",
      });
      setSettings(result);
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
        <SaveBar saving={saving} saved={saved} onSave={save} />
      </LoadState>
    </SettingsPage>
  );
}
