import {
  Alert,
  Button,
  Chip,
  FormControlLabel,
  MenuItem,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { moyroAdminApi, type MailDelivery, type MailSecurity, type MailSettings } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

// Off by default; an internal relay on port 25 with no credentials and no
// TLS is the common case, so those are the defaults too.
export const DEFAULT_MAIL: MailSettings = {
  enabled: false,
  smtp_host: "",
  smtp_port: 25,
  security: "auto",
  skip_tls_verify: false,
  username: "",
  from_address: "",
  from_name: "moyro",
  base_url: "",
  timeout_seconds: 10,
  notify_approval_requested: true,
  notify_approval_decided: true,
  notify_task_assigned: true,
  password_configured: false,
};

const SECURITY_OPTIONS: { value: MailSecurity; label: string }[] = [
  { value: "auto", label: "자동 (서버가 STARTTLS 를 알리면 사용)" },
  { value: "none", label: "없음 (평문)" },
  { value: "starttls", label: "STARTTLS 필수" },
  { value: "tls", label: "TLS (암묵적, 대개 465 포트)" },
];

const EVENT_LABELS: Record<string, string> = {
  approval_requested: "승인 요청",
  approval_decided: "승인 결과",
  task_assigned: "작업 할당",
  bundle: "묶음",
  test: "시험 발송",
};

const STATUS_LABELS: Record<MailDelivery["status"], { label: string; color: "default" | "success" | "error" }> = {
  queued: { label: "대기", color: "default" },
  sent: { label: "보냄", color: "success" },
  failed: { label: "실패", color: "error" },
};

// What the server will refuse, said before the round trip. The server is the
// authority; this only keeps the obvious cases from becoming a save error.
export function validateMail(settings: MailSettings): string {
  if (!Number.isInteger(settings.smtp_port) || settings.smtp_port < 1 || settings.smtp_port > 65535) return "포트는 1~65535 사이여야 합니다.";
  if (!Number.isInteger(settings.timeout_seconds) || settings.timeout_seconds < 1 || settings.timeout_seconds > 120) return "제한 시간은 1~120초 사이여야 합니다.";
  if (settings.from_address.trim() && !/^[^\s<>"@]+@[^\s<>"@]+$/.test(settings.from_address.trim())) return "보내는 주소는 이름 없이 주소만 적습니다.";
  if (settings.base_url.trim() && !/^https?:\/\//.test(settings.base_url.trim())) return "앱 주소는 http(s):// 로 시작해야 합니다.";
  if (!settings.enabled) return "";
  if (!settings.smtp_host.trim()) return "켜기 전에 릴레이 주소를 적으세요.";
  if (!settings.from_address.trim()) return "켜기 전에 보내는 주소를 적으세요.";
  return "";
}

const formatTime = (ms: number) => new Date(ms).toLocaleString("ko-KR", { hour12: false });

export function MailSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [settings, setSettings] = useState<MailSettings>(DEFAULT_MAIL);
  const [password, setPassword] = useState("");
  const [clearPassword, setClearPassword] = useState(false);
  const [deliveries, setDeliveries] = useState<MailDelivery[]>([]);
  const [summary, setSummary] = useState<Record<string, number>>({});
  const [testRecipient, setTestRecipient] = useState("");
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null);
  const [testing, setTesting] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const validationError = useMemo(() => validateMail(settings), [settings]);
  const update = (patch: Partial<MailSettings>) => {
    setSettings((current) => ({ ...current, ...patch }));
    setSaved("");
  };

  const loadDeliveries = useCallback(async () => {
    if (!token) return;
    try {
      const page = await moyroAdminApi.listMailDeliveries(token);
      setDeliveries(page.items ?? []);
      setSummary(page.summary?.status ?? {});
    } catch {
      // The log is a troubleshooting aid; a failed refresh keeps the last one.
    }
  }, [token]);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    moyroAdminApi.getSettings<MailSettings>(token, "mail").then(
      (value) => {
        if (cancelled) return;
        setSettings({ ...DEFAULT_MAIL, ...value });
        setError("");
      },
      (err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "메일 알림 설정 API에 연결하지 못했습니다.");
      },
    ).finally(() => { if (!cancelled) setLoading(false); });
    void loadDeliveries();
    return () => { cancelled = true; };
  }, [token, loadDeliveries]);

  async function save() {
    if (!token) return;
    const problem = validateMail(settings);
    if (problem) {
      setError(problem);
      return;
    }
    setSaving(true);
    setSaved("");
    try {
      const { password_configured: _configured, ...editable } = settings;
      const body: MailSettings = { ...editable, password_configured: false };
      if (clearPassword) body.clear_password = true;
      else if (password) body.password = password;
      const result = await moyroAdminApi.patchSettings<MailSettings>(token, "mail", body);
      setSettings({ ...DEFAULT_MAIL, ...result });
      setPassword("");
      setClearPassword(false);
      setError("");
      setSaved("메일 알림 설정을 저장했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "메일 알림 설정을 저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  // Sends one real message with the saved settings and shows the relay's
  // answer right here; relay settings are rarely right the first time.
  async function sendTest() {
    if (!token) return;
    setTesting(true);
    setTestResult(null);
    try {
      const result = await moyroAdminApi.sendTestMail(token, testRecipient.trim());
      setTestResult({ ok: result.sent, text: result.sent ? `${result.recipient} 으로 보냈습니다. 받은 편지함을 확인하세요.` : result.message ?? "보내지 못했습니다." });
    } catch (err) {
      setTestResult({ ok: false, text: err instanceof Error ? err.message : "시험 발송에 실패했습니다." });
    } finally {
      setTesting(false);
      await loadDeliveries();
    }
  }

  return (
    <SettingsPage title="메일 알림" description="사내 SMTP 릴레이로 사람이 기다리는 일을 메일로 알립니다. 기본값은 꺼짐이고, 메일은 배경에서 보내므로 릴레이가 죽어 있어도 화면의 작업은 평소대로 끝납니다. 보낸 시도는 아래 기록에 남습니다.">
      <LoadState loading={loading} error={error}>
        <SettingsCard title="릴레이" description="사내 릴레이는 25번 포트 · 인증 없음 · TLS 없음이 흔합니다. 그것이 기본값이고, 인증과 암호화는 있으면 쓰는 선택 사항입니다. 저장한 설정은 즉시 적용됩니다.">
          <Stack spacing={2}>
            <FormControlLabel
              control={<Switch checked={settings.enabled} onChange={(event) => update({ enabled: event.target.checked })} />}
              label="메일 알림 켜기"
            />
            <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
              <TextField
                required
                fullWidth
                label="릴레이 주소 (smtp_host)"
                value={settings.smtp_host}
                onChange={(event) => update({ smtp_host: event.target.value })}
                placeholder="postra.corp.example"
                helperText="호스트 이름이나 IP 만 적습니다. 폐쇄망에서는 사내 메일 서비스(postra)를 권합니다."
              />
              <TextField
                label="포트 (smtp_port)"
                type="number"
                value={settings.smtp_port}
                onChange={(event) => update({ smtp_port: Number(event.target.value) })}
                sx={{ minWidth: 140 }}
              />
            </Stack>
            <TextField
              select
              label="보안 (security)"
              value={settings.security}
              onChange={(event) => update({ security: event.target.value as MailSecurity })}
            >
              {SECURITY_OPTIONS.map((item) => <MenuItem key={item.value} value={item.value}>{item.label}</MenuItem>)}
            </TextField>
            <FormControlLabel
              control={<Switch checked={settings.skip_tls_verify} onChange={(event) => update({ skip_tls_verify: event.target.checked })} />}
              label="인증서 검증 건너뛰기 (skip_tls_verify) — 사내 사설 인증서일 때만"
            />
            <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
              <TextField
                fullWidth
                label="사용자 이름 (username)"
                value={settings.username}
                onChange={(event) => update({ username: event.target.value })}
                helperText="인증 없는 릴레이면 비워 둡니다."
                autoComplete="off"
              />
              <TextField
                fullWidth
                label="비밀번호 (password)"
                type="password"
                value={password}
                onChange={(event) => { setPassword(event.target.value); setClearPassword(false); setSaved(""); }}
                placeholder={settings.password_configured ? "설정됨 — 바꿀 때만 입력" : "비어 있음"}
                helperText={settings.password_configured ? "저장된 비밀번호는 되읽히지 않습니다. 비워 두면 그대로 둡니다." : "인증 없는 릴레이면 비워 둡니다."}
                autoComplete="new-password"
                slotProps={{ input: { endAdornment: settings.password_configured ? <Chip size="small" color="success" label="설정됨" /> : undefined } }}
              />
            </Stack>
            {settings.password_configured && (
              <FormControlLabel
                control={<Switch checked={clearPassword} onChange={(event) => { setClearPassword(event.target.checked); if (event.target.checked) setPassword(""); setSaved(""); }} />}
                label="저장된 비밀번호 지우기"
              />
            )}
          </Stack>
        </SettingsCard>

        <SettingsCard title="보내는 사람과 링크" description="받는 사람이 답장할 수 있는 주소가 좋습니다. 앱 주소는 메일 속 '바로 열기' 링크가 가리킬 곳이며, 비워 두면 사이트 설정의 공개 주소를 씁니다.">
          <Stack spacing={2}>
            <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
              <TextField
                required
                fullWidth
                label="보내는 주소 (from_address)"
                value={settings.from_address}
                onChange={(event) => update({ from_address: event.target.value })}
                placeholder="moyro@corp.example"
              />
              <TextField
                fullWidth
                label="보내는 이름 (from_name)"
                value={settings.from_name}
                onChange={(event) => update({ from_name: event.target.value })}
              />
            </Stack>
            <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
              <TextField
                fullWidth
                label="앱 주소 (base_url)"
                value={settings.base_url}
                onChange={(event) => update({ base_url: event.target.value })}
                placeholder="https://moyro.corp.example"
              />
              <TextField
                label="제한 시간 (timeout_seconds)"
                type="number"
                value={settings.timeout_seconds}
                onChange={(event) => update({ timeout_seconds: Number(event.target.value) })}
                sx={{ minWidth: 200 }}
              />
            </Stack>
          </Stack>
        </SettingsCard>

        <SettingsCard title="보낼 이벤트" description="이 메일이 오지 않으면 누군가 손해를 보거나 화면을 계속 새로고침하는 일만 보냅니다. 자기가 한 일은 자기에게 보내지 않고, 한 사람에게 잇달아 생긴 알림은 한 통으로 묶습니다. 받는 사람은 개인 설정 → 알림에서 끌 수 있습니다.">
          <Stack spacing={0.5}>
            <FormControlLabel
              control={<Switch checked={settings.notify_approval_requested} onChange={(event) => update({ notify_approval_requested: event.target.checked })} />}
              label="승인 요청 — 검토자에게. 검토가 끝날 때까지 요청자의 작업이 실행되지 않습니다"
            />
            <FormControlLabel
              control={<Switch checked={settings.notify_approval_decided} onChange={(event) => update({ notify_approval_decided: event.target.checked })} />}
              label="승인 결과 — 요청자에게. 승인·반려·만료를 기다리는 사람입니다"
            />
            <FormControlLabel
              control={<Switch checked={settings.notify_task_assigned} onChange={(event) => update({ notify_task_assigned: event.target.checked })} />}
              label="작업 할당 — 담당자에게. 내 차례가 됐다는 사실입니다"
            />
          </Stack>
        </SettingsCard>

        {validationError && settings.enabled && <Alert severity="warning">{validationError}</Alert>}
        <SaveBar saving={saving} saved={saved} onSave={() => { void save(); }} />

        <SettingsCard title="시험 발송" description="저장한 설정으로 실제 한 통을 보내고 릴레이의 답을 그 자리에서 보여 줍니다. 비워 두면 내 계정 주소로 보냅니다.">
          <Stack spacing={1.5}>
            <Stack direction={{ xs: "column", sm: "row" }} spacing={1.5}>
              <TextField
                fullWidth
                label="받는 주소"
                value={testRecipient}
                onChange={(event) => setTestRecipient(event.target.value)}
                placeholder="me@corp.example"
              />
              <Button variant="outlined" disabled={testing || !settings.enabled} onClick={() => { void sendTest(); }} sx={{ whiteSpace: "nowrap" }}>
                {testing ? "보내는 중…" : "시험 발송"}
              </Button>
            </Stack>
            {!settings.enabled && <Alert severity="info">메일 알림을 켜고 저장한 뒤 시험 발송할 수 있습니다.</Alert>}
            {testResult && <Alert severity={testResult.ok ? "success" : "error"}>{testResult.text}</Alert>}
          </Stack>
        </SettingsCard>

        <SettingsCard title="발송 기록" description="시도마다 남깁니다 — 언제, 어떤 이벤트로, 누구에게, 제목이 무엇이었고, 되었는지. 본문은 담지 않습니다. '안 왔다'는 문의에 이 표로 답합니다.">
          <Stack spacing={1.5}>
            <Stack direction="row" spacing={1} sx={{ flexWrap: "wrap" }}>
              <Chip size="small" color="success" label={`보냄 ${summary.sent ?? 0}`} />
              <Chip size="small" color="error" label={`실패 ${summary.failed ?? 0}`} />
              <Chip size="small" label={`대기 ${summary.queued ?? 0}`} />
            </Stack>
            {deliveries.length === 0 ? (
              <Alert severity="info">{settings.enabled ? "아직 보낸 메일이 없습니다." : "메일 알림을 켜면 보낸 기록이 여기에 쌓입니다."}</Alert>
            ) : (
              <Table size="small" aria-label="발송 기록">
                <TableHead>
                  <TableRow>
                    <TableCell>시각</TableCell>
                    <TableCell>이벤트</TableCell>
                    <TableCell>받는 사람</TableCell>
                    <TableCell>제목</TableCell>
                    <TableCell>상태</TableCell>
                    <TableCell align="right">시도</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {deliveries.map((item) => (
                    <TableRow key={item.id} className="delivery-row">
                      <TableCell sx={{ whiteSpace: "nowrap" }}>{formatTime(item.create_at)}</TableCell>
                      <TableCell>{EVENT_LABELS[item.event] ?? item.event}</TableCell>
                      <TableCell><code>{item.recipient}</code></TableCell>
                      <TableCell sx={{ maxWidth: 320, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{item.subject}</TableCell>
                      <TableCell>
                        <Chip size="small" color={STATUS_LABELS[item.status]?.color ?? "default"} label={STATUS_LABELS[item.status]?.label ?? item.status} />
                        {item.error_message && <Typography variant="caption" color="error" sx={{ display: "block", maxWidth: 280 }}>{item.error_message}</Typography>}
                      </TableCell>
                      <TableCell align="right">{item.attempts}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
            <Stack direction="row" spacing={1} sx={{ justifyContent: "flex-end" }}>
              <Button size="small" onClick={() => { void loadDeliveries(); }}>새로고침</Button>
            </Stack>
          </Stack>
        </SettingsCard>
      </LoadState>
    </SettingsPage>
  );
}
