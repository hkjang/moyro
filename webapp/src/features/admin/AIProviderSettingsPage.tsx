import CheckCircleRounded from "@mui/icons-material/CheckCircleRounded";
import {
  Alert,
  Button,
  FormControlLabel,
  Grid,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useState } from "react";
import { useSelector } from "react-redux";
import { moyroAdminApi, type AIProviderSettings } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

const TOKEN_LIMIT = 262_144;

const DEFAULT_PROVIDER: AIProviderSettings = {
  name: "사내 AI",
  enabled: false,
  api_type: "openai-compatible",
  base_url: "",
  model: "",
  streaming_default: true,
  context_window_tokens: TOKEN_LIMIT,
  max_output_tokens: 16_384,
  timeout_seconds: 120,
  status: "unknown",
};

function boundedInteger(raw: string, fallback: number): number {
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.max(1, Math.min(TOKEN_LIMIT, parsed));
}

export function AIProviderSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [provider, setProvider] = useState<AIProviderSettings>(DEFAULT_PROVIDER);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    moyroAdminApi.listAIProviders(token).then(
      (rows) => {
        if (cancelled) return;
        if (rows[0]) setProvider({ ...DEFAULT_PROVIDER, ...rows[0], api_key: "" });
        setError("");
      },
      (err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "AI 설정 API에 연결하지 못했습니다.");
      },
    ).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [token]);

  const update = <K extends keyof AIProviderSettings>(key: K, value: AIProviderSettings[K]) => {
    setProvider((prev) => ({ ...prev, [key]: value }));
    setSaved("");
  };

  async function save() {
    if (!token) return;
    setSaving(true);
    try {
      const normalized = {
        ...provider,
        context_window_tokens: Math.min(TOKEN_LIMIT, provider.context_window_tokens),
        max_output_tokens: Math.min(provider.context_window_tokens, TOKEN_LIMIT, provider.max_output_tokens),
        streaming_default: true,
      };
      if (!normalized.api_key?.trim()) delete normalized.api_key;
      const value = normalized.id
        ? await moyroAdminApi.patchAIProvider(token, normalized.id, normalized)
        : await moyroAdminApi.createAIProvider(token, normalized);
      setProvider({ ...DEFAULT_PROVIDER, ...value, api_key: "", streaming_default: true });
      setError("");
      setSaved("저장되었습니다. AI 요청은 streaming으로 시작됩니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  async function testConnection() {
    if (!token) return;
    setTesting(true);
    try {
      const payload: AIProviderSettings = { ...provider, streaming_default: true };
      if (!payload.api_key?.trim()) delete payload.api_key;
      const result = await moyroAdminApi.testAIProvider(token, payload);
      update("status", result.ok ? "ready" : "error");
      if (result.ok) {
        setError("");
        setSaved(`연결 확인 완료${result.model ? ` · ${result.model}` : ""}`);
      } else {
        setError(result.message || "AI endpoint 확인에 실패했습니다.");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "연결 확인에 실패했습니다.");
    } finally {
      setTesting(false);
    }
  }

  return (
    <SettingsPage title="AI 공급자" description="인터넷 연결 없이 접근 가능한 사내 AI 또는 OpenAI 호환 endpoint를 설정합니다.">
      <LoadState loading={loading} error={error}>
        {provider.status === "ready" && <Alert severity="success" icon={<CheckCircleRounded />}>AI endpoint와 모델 응답을 확인했습니다.</Alert>}
        <SettingsCard title="공급자 연결" description="API key는 암호화해 저장하며 조회 시 설정 여부만 반환합니다.">
          <Stack spacing={2.25}>
            <FormControlLabel control={<Switch checked={provider.enabled} onChange={(event) => update("enabled", event.target.checked)} />} label="서비스에서 AI 기능을 사용합니다" />
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth label="표시 이름" value={provider.name} onChange={(event) => update("name", event.target.value)} /></Grid>
              <Grid size={{ xs: 12, md: 6 }}>
                <TextField select fullWidth label="API 형식" value={provider.api_type} onChange={(event) => update("api_type", event.target.value as AIProviderSettings["api_type"])}>
                  <MenuItem value="openai-compatible">OpenAI 호환</MenuItem>
                  <MenuItem value="openai">OpenAI</MenuItem>
                </TextField>
              </Grid>
              <Grid size={{ xs: 12, md: 8 }}><TextField fullWidth required label="Base URL" placeholder="https://ai.internal/v1" value={provider.base_url} onChange={(event) => update("base_url", event.target.value)} /></Grid>
              <Grid size={{ xs: 12, md: 4 }}><TextField fullWidth required label="기본 모델" value={provider.model} onChange={(event) => update("model", event.target.value)} /></Grid>
              <Grid size={{ xs: 12 }}>
                <TextField fullWidth type="password" label="API Key" value={provider.api_key ?? ""} onChange={(event) => update("api_key", event.target.value)} placeholder={provider.api_key_state?.configured ? "저장된 API key 유지" : "API key가 없는 내부 endpoint면 비워 두세요"} />
              </Grid>
            </Grid>
          </Stack>
        </SettingsCard>

        <SettingsCard title="Streaming과 token" description="문맥 크기와 최대 출력 token을 분리하며 각각 최대 256K까지 설정할 수 있습니다.">
          <Stack spacing={2.25}>
            <FormControlLabel control={<Switch checked disabled />} label="Streaming 기본 사용 (필수)" />
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, md: 4 }}>
                <TextField fullWidth type="number" label="Context window tokens" value={provider.context_window_tokens} onChange={(event) => update("context_window_tokens", boundedInteger(event.target.value, provider.context_window_tokens))} slotProps={{ htmlInput: { min: 1, max: TOKEN_LIMIT } }} helperText="최대 262,144" />
              </Grid>
              <Grid size={{ xs: 12, md: 4 }}>
                <TextField fullWidth type="number" label="Max output tokens" value={provider.max_output_tokens} onChange={(event) => update("max_output_tokens", boundedInteger(event.target.value, provider.max_output_tokens))} slotProps={{ htmlInput: { min: 1, max: TOKEN_LIMIT } }} helperText={`실제 적용 최대 ${Math.min(provider.context_window_tokens, TOKEN_LIMIT).toLocaleString()}`} />
              </Grid>
              <Grid size={{ xs: 12, md: 4 }}>
                <TextField fullWidth type="number" label="Timeout (초)" value={provider.timeout_seconds} onChange={(event) => update("timeout_seconds", Math.max(5, Math.min(3600, Number(event.target.value) || 120)))} slotProps={{ htmlInput: { min: 5, max: 3600 } }} />
              </Grid>
            </Grid>
          </Stack>
        </SettingsCard>

        <Stack direction={{ xs: "column", sm: "row" }} sx={{ justifyContent: "flex-end", alignItems: "center", gap: 1.5 }}>
          {saved && <Typography color="success.main" variant="body2" role="status">{saved}</Typography>}
          <Button variant="outlined" onClick={testConnection} disabled={testing || !provider.base_url || !provider.model}>{testing ? "확인 중…" : "연결 확인"}</Button>
          <SaveBar saving={saving} saved="" onSave={save} />
        </Stack>
      </LoadState>
    </SettingsPage>
  );
}
