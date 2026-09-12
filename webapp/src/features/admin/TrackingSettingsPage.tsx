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
import { moyroAdminApi, type TrackingProvider, type TrackingSettings, type TrackingViolation } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

export const DEFAULT_TRACKING: TrackingSettings = {
  enabled: false,
  provider: "none",
  momento_url: "",
  momento_site_id: "",
  momento_proxy: true,
  measurement_id: "",
  matomo_url: "",
  matomo_site_id: "",
  custom_snippet: "",
  allowed_hosts: [],
  include_admin: false,
  placement: "head",
};

export const MAX_SNIPPET_BYTES = 8 * 1024;

// Momento comes first: it is the self-hosted collector, the only choice whose
// data never leaves the network.
const PROVIDERS: { value: TrackingProvider; label: string }[] = [
  { value: "none", label: "사용 안 함" },
  { value: "momento", label: "Momento (사내 수집기)" },
  { value: "ga4", label: "Google Analytics 4" },
  { value: "gtm", label: "Google Tag Manager" },
  { value: "matomo", label: "Matomo" },
  { value: "custom", label: "직접 붙여넣기" },
];

const parseHosts = (value: string) => Array.from(new Set(
  value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean),
));

const snippetBytes = (value: string) => new TextEncoder().encode(value).length;

function validateAbsoluteURL(value: string): string {
  if (!value.trim()) return "";
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return "http(s) 절대 URL을 입력하세요.";
    return "";
  } catch {
    return "http(s)로 시작하는 올바른 절대 URL을 입력하세요.";
  }
}

// What the server will refuse, said before the round trip. The server is the
// authority; this only keeps the obvious cases from becoming a save error.
export function validateTracking(settings: TrackingSettings): string {
  if (snippetBytes(settings.custom_snippet) > MAX_SNIPPET_BYTES) {
    return `추적 코드는 ${MAX_SNIPPET_BYTES.toLocaleString()}바이트를 넘을 수 없습니다.`;
  }
  const urlError = validateAbsoluteURL(settings.momento_url) || validateAbsoluteURL(settings.matomo_url);
  if (urlError) return urlError;
  if (!settings.enabled) return "";
  switch (settings.provider) {
    case "none": return "켜기 전에 공급자를 고르세요.";
    case "momento": return settings.momento_url.trim() && settings.momento_site_id.trim() ? "" : "Momento 주소와 사이트 ID가 필요합니다.";
    case "ga4":
    case "gtm": return settings.measurement_id.trim() ? "" : "측정 ID가 필요합니다.";
    case "matomo": return settings.matomo_url.trim() && settings.matomo_site_id.trim() ? "" : "Matomo 주소와 사이트 ID가 필요합니다.";
    case "custom": return settings.custom_snippet.trim() ? "" : "붙여넣을 추적 코드가 비어 있습니다.";
    default: return "";
  }
}

export function TrackingSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [settings, setSettings] = useState<TrackingSettings>(DEFAULT_TRACKING);
  const [hostsText, setHostsText] = useState("");
  const [violations, setViolations] = useState<TrackingViolation[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const validationError = useMemo(() => validateTracking(settings), [settings]);
  const bytes = useMemo(() => snippetBytes(settings.custom_snippet), [settings.custom_snippet]);
  const update = (patch: Partial<TrackingSettings>) => {
    setSettings((current) => ({ ...current, ...patch }));
    setSaved("");
  };

  const loadViolations = useCallback(async () => {
    if (!token) return;
    try {
      const result = await moyroAdminApi.listTrackingViolations(token);
      setViolations(result.items ?? []);
    } catch {
      // The list is a troubleshooting aid; a failed refresh keeps the last one.
    }
  }, [token]);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    moyroAdminApi.getSettings<TrackingSettings>(token, "tracking").then(
      (value) => {
        if (cancelled) return;
        const next = { ...DEFAULT_TRACKING, ...value, allowed_hosts: value.allowed_hosts ?? [] };
        setSettings(next);
        setHostsText(next.allowed_hosts.join("\n"));
        setError("");
      },
      (err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "방문 추적 설정 API에 연결하지 못했습니다.");
      },
    ).finally(() => { if (!cancelled) setLoading(false); });
    void loadViolations();
    return () => { cancelled = true; };
  }, [token, loadViolations]);

  async function persist(next: TrackingSettings, message: string) {
    if (!token) return;
    setSaving(true);
    setSaved("");
    try {
      const result = await moyroAdminApi.patchSettings<TrackingSettings>(token, "tracking", next);
      const stored = { ...DEFAULT_TRACKING, ...result, allowed_hosts: result.allowed_hosts ?? [] };
      setSettings(stored);
      setHostsText(stored.allowed_hosts.join("\n"));
      setError("");
      setSaved(message);
      await loadViolations();
    } catch (err) {
      setError(err instanceof Error ? err.message : "방문 추적 설정을 저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  async function save() {
    const problem = validateTracking({ ...settings, allowed_hosts: parseHosts(hostsText) });
    if (problem) {
      setError(problem);
      return;
    }
    await persist({ ...settings, allowed_hosts: parseHosts(hostsText) }, "방문 추적 설정을 저장했습니다.");
  }

  // One click turns a reported origin into an allow-list entry and saves.
  async function allow(origin: string) {
    const hosts = parseHosts(hostsText);
    if (!hosts.some((host) => host.toLowerCase() === origin.toLowerCase())) hosts.push(origin);
    await persist({ ...settings, allowed_hosts: hosts }, `${origin} 을(를) 허용 목록에 넣었습니다.`);
  }

  async function clearViolations() {
    if (!token) return;
    try {
      await moyroAdminApi.clearTrackingViolations(token);
      setViolations([]);
    } catch (err) {
      setError(err instanceof Error ? err.message : "차단 기록을 지우지 못했습니다.");
    }
  }

  const provider = settings.provider;
  const proxied = provider === "momento" && settings.momento_proxy;

  return (
    <SettingsPage title="방문 추적" description="관리자가 붙이는 방문 통계 스크립트입니다. 기본값은 꺼짐이고, 켜면 요청마다 nonce 를 단 스크립트가 화면에 들어가며 콘텐츠 보안 정책(CSP)은 그 스크립트가 필요로 하는 출처만큼만 넓어집니다.">
      <LoadState loading={loading} error={error}>
        <SettingsCard title="공급자" description="Momento 는 사내 자체 호스팅 수집기라 데이터가 밖으로 나가지 않는 유일한 선택지입니다. 같은 오리진 프록시를 쓰면 외부 출처가 정책에 아예 등장하지 않습니다.">
          <Stack spacing={2}>
            <FormControlLabel
              control={<Switch checked={settings.enabled} onChange={(event) => update({ enabled: event.target.checked })} />}
              label="방문 추적 켜기"
            />
            <TextField
              select
              label="공급자"
              value={provider}
              onChange={(event) => update({ provider: event.target.value as TrackingProvider })}
            >
              {PROVIDERS.map((item) => <MenuItem key={item.value} value={item.value}>{item.label}</MenuItem>)}
            </TextField>
            {provider === "momento" && (
              <>
                <TextField
                  required
                  label="Momento 주소"
                  value={settings.momento_url}
                  onChange={(event) => update({ momento_url: event.target.value })}
                  placeholder="https://momento.corp.example"
                  error={Boolean(validateAbsoluteURL(settings.momento_url))}
                  helperText={validateAbsoluteURL(settings.momento_url) || "수집기의 http(s) 절대 주소입니다. tracker.js 와 이벤트 수집 경로가 이 아래에 있습니다."}
                />
                <TextField
                  required
                  label="사이트 ID"
                  value={settings.momento_site_id}
                  onChange={(event) => update({ momento_site_id: event.target.value })}
                  placeholder="moyro-prd"
                />
                <FormControlLabel
                  control={<Switch checked={settings.momento_proxy} onChange={(event) => update({ momento_proxy: event.target.checked })} />}
                  label="같은 오리진 프록시(/momento/*)로 전달"
                />
                {proxied ? (
                  <Alert severity="success">브라우저는 이 서버의 <code>/momento/*</code> 만 호출하고 서버가 수집기로 넘깁니다. CSP 에 외부 출처가 들어가지 않습니다.</Alert>
                ) : (
                  <Alert severity="info">브라우저가 수집기를 직접 호출합니다. 수집기 주소가 <code>script-src</code>·<code>connect-src</code>·<code>img-src</code> 에 자동으로 더해집니다.</Alert>
                )}
              </>
            )}
            {(provider === "ga4" || provider === "gtm") && (
              <TextField
                required
                label={provider === "ga4" ? "측정 ID (G-…)" : "컨테이너 ID (GTM-…)"}
                value={settings.measurement_id}
                onChange={(event) => update({ measurement_id: event.target.value })}
                helperText="googletagmanager.com 과 google-analytics.com 출처가 정책에 더해집니다. 폐쇄망에서는 동작하지 않습니다."
              />
            )}
            {provider === "matomo" && (
              <>
                <TextField
                  required
                  label="Matomo 주소"
                  value={settings.matomo_url}
                  onChange={(event) => update({ matomo_url: event.target.value })}
                  placeholder="https://matomo.corp.example"
                  error={Boolean(validateAbsoluteURL(settings.matomo_url))}
                  helperText={validateAbsoluteURL(settings.matomo_url) || "matomo.js 와 matomo.php 가 있는 주소입니다."}
                />
                <TextField
                  required
                  label="사이트 ID"
                  value={settings.matomo_site_id}
                  onChange={(event) => update({ matomo_site_id: event.target.value })}
                />
              </>
            )}
            {provider === "custom" && (
              <TextField
                required
                multiline
                minRows={6}
                label="추적 코드"
                value={settings.custom_snippet}
                onChange={(event) => update({ custom_snippet: event.target.value })}
                placeholder={'<script async src="https://stats.corp.example/t.js" data-site="…"></script>'}
                error={bytes > MAX_SNIPPET_BYTES}
                helperText={`${bytes.toLocaleString()} / ${MAX_SNIPPET_BYTES.toLocaleString()} 바이트. 모든 <script> 태그에 요청마다 nonce 가 붙고, 코드 안의 http(s) 주소는 정책에 자동으로 더해집니다.`}
                slotProps={{ htmlInput: { spellCheck: false, style: { fontFamily: "monospace" } } }}
              />
            )}
          </Stack>
        </SettingsCard>

        <SettingsCard title="붙는 자리" description="관리 화면과 개인 설정 화면은 기본적으로 제외합니다. 로그인 화면에는 붙더라도 개인 식별 값을 보내지 않습니다.">
          <Stack spacing={1.5}>
            <TextField
              select
              label="삽입 위치"
              value={settings.placement}
              onChange={(event) => update({ placement: event.target.value as TrackingSettings["placement"] })}
            >
              <MenuItem value="head">&lt;head&gt; 끝</MenuItem>
              <MenuItem value="body">&lt;body&gt; 끝</MenuItem>
            </TextField>
            <FormControlLabel
              control={<Switch checked={settings.include_admin} onChange={(event) => update({ include_admin: event.target.checked })} />}
              label="관리 화면(/admin, /settings)에서도 추적"
            />
          </Stack>
        </SettingsCard>

        <SettingsCard title="추가로 허용할 출처" description="스니펫에서 자동으로 읽지 못한 주소를 더하는 자리입니다. 아래 차단 기록의 '허용' 을 누르면 여기에 들어갑니다.">
          <TextField
            multiline
            minRows={3}
            label="허용 출처"
            value={hostsText}
            onChange={(event) => { setHostsText(event.target.value); setSaved(""); }}
            placeholder={"https://pixel.corp.example\n*.stats.corp.example"}
            helperText="한 줄 또는 쉼표로 구분한 http(s) 출처입니다. scheme 을 생략하면 https 로 봅니다. 경로는 넣지 않습니다."
          />
        </SettingsCard>

        <SettingsCard title="정책이 막은 출처" description="추적이 켜져 있는 동안 브라우저가 신고한 차단 내역입니다. 횟수보다 어떤 출처가 막혔는지가 중요하므로 서로 다른 출처 100개까지만 기억합니다.">
          <Stack spacing={1.5}>
            {violations.length === 0 ? (
              <Alert severity="info">{settings.enabled ? "신고된 차단이 없습니다. 화면을 한 번 열어 본 뒤 새로고침하세요." : "추적을 켜면 정책 위반 신고가 여기에 쌓입니다."}</Alert>
            ) : (
              <Table size="small" aria-label="차단된 출처">
                <TableHead>
                  <TableRow>
                    <TableCell>출처</TableCell>
                    <TableCell>지시어</TableCell>
                    <TableCell>마지막 페이지</TableCell>
                    <TableCell align="right">횟수</TableCell>
                    <TableCell align="right">조치</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {violations.map((item) => (
                    <TableRow key={`${item.directive} ${item.origin}`} className="violation-row">
                      <TableCell><code>{item.origin}</code></TableCell>
                      <TableCell><code>{item.directive}</code></TableCell>
                      <TableCell sx={{ maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{item.page}</TableCell>
                      <TableCell align="right">{item.count}</TableCell>
                      <TableCell align="right">
                        {item.allowed ? (
                          <Chip size="small" color="success" label="허용됨" />
                        ) : (
                          <Button size="small" variant="outlined" disabled={saving} onClick={() => { void allow(item.origin); }}>허용</Button>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
            <Stack direction="row" spacing={1} sx={{ justifyContent: "flex-end" }}>
              <Button size="small" onClick={() => { void loadViolations(); }}>새로고침</Button>
              <Button size="small" color="warning" disabled={violations.length === 0} onClick={() => { void clearViolations(); }}>기록 지우기</Button>
            </Stack>
            <Typography variant="body2" color="text.secondary">
              정책은 <code>'unsafe-inline'</code> 으로 풀지 않습니다. 한 번 풀면 모든 인라인 스크립트가 함께 허용되고, 추적을 끈 뒤에도 느슨한 채 남기 때문입니다.
            </Typography>
          </Stack>
        </SettingsCard>

        {validationError && settings.enabled && <Alert severity="warning">{validationError}</Alert>}
        <SaveBar saving={saving} saved={saved} onSave={() => { void save(); }} />
      </LoadState>
    </SettingsPage>
  );
}
