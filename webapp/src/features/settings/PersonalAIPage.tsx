import StopCircleRounded from "@mui/icons-material/StopCircleRounded";
import {
  Alert,
  Button,
  FormControlLabel,
  Grid,
  Slider,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useRef, useState } from "react";
import { useSelector } from "react-redux";
import { moyroMeApi, type PersonalAIPreferences } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

const MAX_TOKENS = 262_144;
const DEFAULT_PREFERENCES: PersonalAIPreferences = {
  enabled: true,
  provider_id: "",
  model: "",
  streaming: true,
  max_output_tokens: 8_192,
  temperature: 0.7,
};

export function PersonalAIPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [preferences, setPreferences] = useState(DEFAULT_PREFERENCES);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const [prompt, setPrompt] = useState("");
  const [output, setOutput] = useState("");
  const [streaming, setStreaming] = useState(false);
  const [streamStatus, setStreamStatus] = useState("");
  const controllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    moyroMeApi.getAIPreferences(token).then(
      (value) => { if (!cancelled) { setPreferences({ ...DEFAULT_PREFERENCES, ...value, streaming: true }); setError(""); } },
      (err: unknown) => { if (!cancelled) setError(err instanceof Error ? err.message : "AI 개인화 API에 연결하지 못했습니다."); },
    ).finally(() => { if (!cancelled) setLoading(false); });
    return () => {
      cancelled = true;
      controllerRef.current?.abort();
    };
  }, [token]);

  const update = <K extends keyof PersonalAIPreferences>(key: K, value: PersonalAIPreferences[K]) => {
    setPreferences((prev) => ({ ...prev, [key]: value, streaming: true }));
    setSaved("");
  };

  async function save() {
    if (!token) return;
    setSaving(true);
    try {
      const result = await moyroMeApi.patchAIPreferences(token, {
        ...preferences,
        streaming: true,
        max_output_tokens: Math.max(1, Math.min(MAX_TOKENS, preferences.max_output_tokens)),
      });
      setPreferences({ ...DEFAULT_PREFERENCES, ...result, streaming: true });
      setError("");
      setSaved("AI 개인 설정을 저장했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "AI 설정을 저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  async function runStreamingTest() {
    if (!token || !prompt.trim()) return;
    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    setOutput("");
    setStreaming(true);
    setStreamStatus("응답을 streaming으로 받고 있습니다.");
    try {
      await moyroMeApi.streamAICompletion(
        token,
        {
          model: preferences.model || undefined,
          messages: [{ role: "user", content: prompt.trim() }],
          max_output_tokens: preferences.max_output_tokens,
          temperature: preferences.temperature,
          stream: true,
        },
        (delta) => setOutput((prev) => prev + delta),
        controller.signal,
      );
      setStreamStatus("응답이 완료되었습니다.");
    } catch (err) {
      if (controller.signal.aborted) setStreamStatus("요청을 중지했습니다. 받은 내용은 유지됩니다.");
      else setStreamStatus(err instanceof Error ? err.message : "AI 요청에 실패했습니다.");
    } finally {
      if (controllerRef.current === controller) controllerRef.current = null;
      setStreaming(false);
    }
  }

  return (
    <SettingsPage title="AI 개인화" description="관리자가 허용한 공급자 안에서 내 기본 모델과 응답 특성을 선택합니다.">
      <LoadState loading={loading} error={error}>
        <SettingsCard title="내 기본값">
          <Stack spacing={2.25}>
            <FormControlLabel control={<Switch checked={preferences.enabled} onChange={(event) => update("enabled", event.target.checked)} />} label="내 계정에서 AI 기능 사용" />
            <FormControlLabel control={<Switch checked disabled />} label="Streaming 기본 사용 (필수)" />
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth label="공급자 ID (선택)" value={preferences.provider_id ?? ""} onChange={(event) => update("provider_id", event.target.value)} /></Grid>
              <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth label="기본 모델 (선택)" value={preferences.model ?? ""} onChange={(event) => update("model", event.target.value)} /></Grid>
              <Grid size={{ xs: 12, md: 6 }}><TextField fullWidth type="number" label="Max output tokens" value={preferences.max_output_tokens} onChange={(event) => update("max_output_tokens", Math.max(1, Math.min(MAX_TOKENS, Number(event.target.value) || 1)))} helperText="최대 262,144" slotProps={{ htmlInput: { min: 1, max: MAX_TOKENS } }} /></Grid>
              <Grid size={{ xs: 12, md: 6 }}>
                <Typography gutterBottom>창의성 · {preferences.temperature.toFixed(1)}</Typography>
                <Slider min={0} max={2} step={0.1} value={preferences.temperature} onChange={(_, value) => update("temperature", value as number)} valueLabelDisplay="auto" aria-label="AI 응답 창의성" />
              </Grid>
            </Grid>
          </Stack>
        </SettingsCard>
        <SaveBar saving={saving} saved={saved} onSave={save} />

        <SettingsCard title="Streaming 확인" description="부분 응답을 즉시 표시하고 중지해도 이미 받은 내용은 남깁니다.">
          <Stack spacing={2}>
            <TextField multiline minRows={3} label="테스트 메시지" value={prompt} onChange={(event) => setPrompt(event.target.value)} />
            <Stack direction="row" sx={{ gap: 1 }}>
              <Button variant="contained" onClick={() => void runStreamingTest()} disabled={streaming || !prompt.trim()}>Streaming 요청</Button>
              {streaming && <Button color="error" startIcon={<StopCircleRounded />} onClick={() => controllerRef.current?.abort()}>중지</Button>}
            </Stack>
            {streamStatus && <Alert severity={streamStatus.includes("실패") ? "error" : "info"} role="status">{streamStatus}</Alert>}
            {output && <Typography component="pre" className="moyro-ai-output">{output}</Typography>}
          </Stack>
        </SettingsCard>
      </LoadState>
    </SettingsPage>
  );
}
