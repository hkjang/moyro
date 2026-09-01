import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import {
  Alert,
  Avatar,
  Button,
  Checkbox,
  Chip,
  Divider,
  FormControlLabel,
  MenuItem,
  Radio,
  RadioGroup,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import { api, type SessionRow, type User } from "@/api/client";
import type { ActivityEventType } from "@/api/activity";
import {
  DEFAULT_INBOX_PREFERENCES,
  inboxPreferencesApi,
  type InboxPreferences,
} from "@/api/inbox-preferences";
import { AuthenticatedImage, isExternalImageURL } from "@/components/AuthenticatedMedia";
import { SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import { useSystemInfo } from "@/features/system/SystemInfoContext";
import { useThemePreference } from "@/features/theme/ThemePreferenceProvider";
import type { RootState } from "@/store";
import { setAuth } from "@/store/authSlice";

export function PersonalProfilePage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const user = useSelector((state: RootState) => state.auth.user);
  const dispatch = useDispatch();
  const [username, setUsername] = useState(user?.username ?? "");
  const [email, setEmail] = useState(user?.email ?? "");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [avatarFailed, setAvatarFailed] = useState(false);
  const externalPicture = isExternalImageURL(user?.picture);
  const showPicture = !!user?.picture && !avatarFailed && (externalPicture || !!token);

  useEffect(() => setAvatarFailed(false), [user?.picture, user?.update_at, token]);

  async function save() {
    if (!token) return;
    setBusy(true);
    try {
      const updated = await api.updateProfile(token, username.trim(), email.trim());
      dispatch(setAuth({ token, user: updated }));
      setMessage("프로필을 저장했습니다.");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "프로필을 저장하지 못했습니다.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <SettingsPage title="프로필" description="내 계정과 사용자에게 표시되는 기본 정보입니다.">
      <SettingsCard title="기본 정보">
        <Stack spacing={2.25}>
          <Stack direction="row" sx={{ alignItems: "center", gap: 2 }}>
            <Avatar sx={{ width: 56, height: 56 }}>
              {showPicture ? (
                externalPicture ? (
                  <img
                    src={user?.picture}
                    alt=""
                    referrerPolicy="no-referrer"
                    onError={() => setAvatarFailed(true)}
                    style={{ width: "100%", height: "100%", objectFit: "cover" }}
                  />
                ) : (
                  <AuthenticatedImage
                    token={token ?? ""}
                    path={api.userImagePath(user?.id ?? "", user?.update_at)}
                    alt=""
                    onFetchError={() => setAvatarFailed(true)}
                    onError={() => setAvatarFailed(true)}
                    style={{ width: "100%", height: "100%", objectFit: "cover" }}
                  />
                )
              ) : (
                (user?.username || "M").slice(0, 1).toUpperCase()
              )}
            </Avatar>
            <Stack>
              <Typography variant="subtitle1">{user?.username}</Typography>
              <Typography variant="body2" color="text.secondary">{user?.roles || "system_user"}</Typography>
            </Stack>
          </Stack>
          <TextField label="사용자명" value={username} onChange={(event) => setUsername(event.target.value)} />
          <TextField type="email" label="이메일" value={email} onChange={(event) => setEmail(event.target.value)} />
          <Stack direction="row" sx={{ justifyContent: "flex-end", alignItems: "center", gap: 1.5 }}>
            {message && <Typography variant="body2" role="status">{message}</Typography>}
            <Button variant="contained" onClick={save} disabled={busy || !username.trim() || !email.trim()}>{busy ? "저장 중…" : "프로필 저장"}</Button>
          </Stack>
        </Stack>
      </SettingsCard>
    </SettingsPage>
  );
}

export function AppearanceSettingsPage() {
  const { theme, setTheme } = useThemePreference();
  const [saved, setSaved] = useState("");

  async function apply(next: "light" | "dark" | "system") {
    try {
      await setTheme(next);
      setSaved("모든 기기에 적용할 화면 설정을 저장했습니다.");
    } catch (err) {
      setSaved(err instanceof Error ? `${err.message} 로컬 화면에는 적용했습니다.` : "로컬 화면에만 적용했습니다.");
    }
  }

  return (
    <SettingsPage title="화면" description="긴 대화에서도 읽기 편한 글꼴과 색상 모드를 선택합니다.">
      <SettingsCard title="테마">
        <RadioGroup value={theme} onChange={(event) => void apply(event.target.value as "light" | "dark" | "system")}>
          <FormControlLabel value="system" control={<Radio />} label="시스템 설정 따르기" />
          <FormControlLabel value="light" control={<Radio />} label="밝게" />
          <FormControlLabel value="dark" control={<Radio />} label="어둡게" />
        </RadioGroup>
        {saved && <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }} role="status">{saved}</Typography>}
      </SettingsCard>
      <Alert severity="info">본문은 16px, 메뉴는 14px, 보조 정보는 최소 13px을 기준으로 표시합니다.</Alert>
    </SettingsPage>
  );
}

export function NotificationSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const systemInfo = useSystemInfo();
  const emailDigestAvailable = systemInfo.capabilities?.email_digest?.enabled === true;
  const [digest, setDigest] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [message, setMessage] = useState("");
  const [rules, setRules] = useState<InboxPreferences>(DEFAULT_INBOX_PREFERENCES);
  const [rulesLoaded, setRulesLoaded] = useState(false);
  const [rulesSaving, setRulesSaving] = useState(false);
  const [vipCandidates, setVIPCandidates] = useState<User[]>([]);
  const [snoozeInput, setSnoozeInput] = useState(DEFAULT_INBOX_PREFERENCES.snooze_presets_minutes.join(", "));

  useEffect(() => {
    if (!token || !systemInfo.loaded) return;
    if (!emailDigestAvailable) {
      setDigest(false);
      setLoaded(true);
      return;
    }
    api.getEmailPrefs(token).then(
      (value) => { setDigest(value.digest_enabled); setLoaded(true); },
      (err: unknown) => { setLoaded(true); setMessage(err instanceof Error ? err.message : "알림 설정을 불러오지 못했습니다."); },
    );
  }, [emailDigestAvailable, systemInfo.loaded, token]);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    setRulesLoaded(false);
    void Promise.all([
      inboxPreferencesApi.get(token, controller.signal),
      api.listUsers(token, 0, 200),
    ]).then(([preferences, users]) => {
      setRules(preferences);
      setSnoozeInput(preferences.snooze_presets_minutes.join(", "));
      setVIPCandidates(users);
      setRulesLoaded(true);
    }).catch((err: unknown) => {
      if (controller.signal.aborted) return;
      setRulesLoaded(true);
      setMessage(err instanceof Error ? err.message : "알림함 규칙을 불러오지 못했습니다.");
    });
    return () => controller.abort();
  }, [token]);

  async function update(next: boolean) {
    if (!token) return;
    const previous = digest;
    setDigest(next);
    try {
      const value = await api.updateEmailPrefs(token, { digest_enabled: next });
      setDigest(value.digest_enabled);
      setMessage("알림 설정을 저장했습니다.");
    } catch (err) {
      setDigest(previous);
      setMessage(err instanceof Error ? err.message : "알림 설정을 저장하지 못했습니다.");
    }
  }

  async function saveRules() {
    if (!token) return;
    const presets = [...new Set(snoozeInput.split(/[,\s]+/).filter(Boolean).map(Number))]
      .filter((value) => Number.isInteger(value));
    if (presets.some((value) => value < 5 || value > 43_200) || presets.length < 1 || presets.length > 8) {
      setMessage("미루기 프리셋은 5~43,200분 값 1~8개로 입력하세요.");
      return;
    }
    setRulesSaving(true);
    try {
      const { update_at: _serverRevision, ...editableRules } = rules;
      const updated = await inboxPreferencesApi.patch(token, { ...editableRules, snooze_presets_minutes: presets });
      setRules(updated);
      setSnoozeInput(updated.snooze_presets_minutes.join(", "));
      setMessage("알림함 규칙을 저장했습니다.");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "알림함 규칙을 저장하지 못했습니다.");
    } finally {
      setRulesSaving(false);
    }
  }

  const setRule = <K extends keyof InboxPreferences>(key: K, value: InboxPreferences[K]) => {
    setRules((current) => ({ ...current, [key]: value }));
  };

  const priorityOptions: Array<{ type: ActivityEventType; label: string }> = [
    { type: "mention", label: "멘션" },
    { type: "direct_message", label: "다이렉트 메시지" },
    { type: "thread_reply", label: "스레드 답글" },
    { type: "approval_requested", label: "승인 요청" },
    { type: "task_assigned", label: "작업 할당" },
    { type: "system_warning", label: "시스템 경고" },
  ];
  const weekdayOptions = ["월", "화", "수", "목", "금", "토", "일"];

  return (
    <SettingsPage title="알림" description="내 계정의 이메일과 기본 알림 방식을 관리합니다.">
      <SettingsCard title="이메일 요약">
        <FormControlLabel control={<Switch checked={digest} disabled={!loaded || !emailDigestAvailable} onChange={(event) => void update(event.target.checked)} />} label="놓친 멘션을 하루 한 번 이메일로 받기" />
        {systemInfo.loaded && !emailDigestAvailable && <Alert severity="info" sx={{ mt: 2 }}>현재 릴리스는 SMTP 관리 설정과 이메일 요약 발송을 지원하지 않습니다.</Alert>}
        {message && <Typography variant="body2" sx={{ mt: 2 }} role="status">{message}</Typography>}
      </SettingsCard>
      <SettingsCard title="우선순위와 묶음" description="VIP와 중요한 이벤트를 먼저 표시하고 반복 알림을 한 묶음으로 정리합니다.">
        <Stack spacing={2}>
          <TextField
            select
            fullWidth
            label="VIP 사용자"
            value={rules.vip_user_ids}
            disabled={!rulesLoaded}
            onChange={(event) => {
              const value = event.target.value;
              setRule("vip_user_ids", typeof value === "string" ? value.split(",") : value as string[]);
            }}
            slotProps={{ select: { multiple: true } }}
            helperText="선택한 사용자의 활동은 우선순위로 처리합니다."
          >
            {vipCandidates.map((candidate) => (
              <MenuItem key={candidate.id} value={candidate.id}>
                <Checkbox checked={rules.vip_user_ids.includes(candidate.id)} />
                {candidate.username}
              </MenuItem>
            ))}
          </TextField>
          <Stack direction="row" sx={{ flexWrap: "wrap", gap: 0.5 }}>
            {priorityOptions.map((option) => (
              <FormControlLabel
                key={option.type}
                control={<Checkbox
                  checked={rules.priority_event_types.includes(option.type)}
                  onChange={(event) => setRule(
                    "priority_event_types",
                    event.target.checked
                      ? [...rules.priority_event_types, option.type]
                      : rules.priority_event_types.filter((type) => type !== option.type),
                  )}
                />}
                label={option.label}
              />
            ))}
          </Stack>
          <TextField select label="알림 묶음 기준" value={rules.bundle_by} onChange={(event) => setRule("bundle_by", event.target.value as InboxPreferences["bundle_by"])}>
            <MenuItem value="none">묶지 않음</MenuItem>
            <MenuItem value="channel">채널별</MenuItem>
            <MenuItem value="type">이벤트 유형별</MenuItem>
          </TextField>
          <TextField
            label="미루기 프리셋(분)"
            value={snoozeInput}
            onChange={(event) => setSnoozeInput(event.target.value)}
            helperText="쉼표로 구분합니다. 예: 60, 240, 1440"
          />
        </Stack>
      </SettingsCard>
      <SettingsCard title="근무 시간" description="설정한 시간 밖에는 브라우저 알림을 조용히 보관합니다.">
        <Stack spacing={2}>
          <FormControlLabel
            control={<Switch checked={rules.work_hours_enabled} disabled={!rulesLoaded} onChange={(event) => setRule("work_hours_enabled", event.target.checked)} />}
            label="근무 시간에만 일반 알림 표시"
          />
          <TextField label="IANA 시간대" value={rules.work_hours_timezone} onChange={(event) => setRule("work_hours_timezone", event.target.value)} helperText="예: Asia/Seoul, UTC" />
          <Stack direction="row" sx={{ flexWrap: "wrap", gap: 0.5 }}>
            {weekdayOptions.map((label, index) => {
              const weekday = index + 1;
              return (
                <FormControlLabel
                  key={label}
                  control={<Checkbox
                    checked={rules.work_hours_weekdays.includes(weekday)}
                    onChange={(event) => setRule(
                      "work_hours_weekdays",
                      event.target.checked
                        ? [...rules.work_hours_weekdays, weekday].sort()
                        : rules.work_hours_weekdays.filter((value) => value !== weekday),
                    )}
                  />}
                  label={label}
                />
              );
            })}
          </Stack>
          <Stack direction={{ xs: "column", sm: "row" }} spacing={1.5}>
            <TextField
              fullWidth
              type="time"
              label="시작"
              value={`${String(Math.floor(rules.work_hours_start_minute / 60)).padStart(2, "0")}:${String(rules.work_hours_start_minute % 60).padStart(2, "0")}`}
              onChange={(event) => {
                const [hours, minutes] = event.target.value.split(":").map(Number);
                setRule("work_hours_start_minute", hours * 60 + minutes);
              }}
              slotProps={{ inputLabel: { shrink: true } }}
            />
            <TextField
              fullWidth
              type="time"
              label="종료"
              value={`${String(Math.floor(rules.work_hours_end_minute / 60)).padStart(2, "0")}:${String(rules.work_hours_end_minute % 60).padStart(2, "0")}`}
              onChange={(event) => {
                const [hours, minutes] = event.target.value.split(":").map(Number);
                setRule("work_hours_end_minute", hours * 60 + minutes);
              }}
              slotProps={{ inputLabel: { shrink: true } }}
            />
          </Stack>
          <FormControlLabel
            control={<Switch checked={rules.priority_override} onChange={(event) => setRule("priority_override", event.target.checked)} />}
            label="VIP·우선순위 알림은 근무 시간 밖에도 표시"
          />
          <Divider />
          <Stack direction="row" sx={{ justifyContent: "flex-end", alignItems: "center", gap: 1.5 }}>
            {message && <Typography variant="body2" role="status">{message}</Typography>}
            <Button variant="contained" onClick={() => void saveRules()} disabled={!rulesLoaded || rulesSaving || rules.work_hours_weekdays.length === 0}>
              {rulesSaving ? "저장 중…" : "알림함 규칙 저장"}
            </Button>
          </Stack>
        </Stack>
      </SettingsCard>
    </SettingsPage>
  );
}

export function SessionSettingsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [sessions, setSessions] = useState<SessionRow[]>([]);
  const [message, setMessage] = useState("");

  async function load() {
    if (!token) return;
    try {
      setSessions(await api.listMySessions(token));
      setMessage("");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "세션을 불러오지 못했습니다.");
    }
  }

  useEffect(() => { void load(); }, [token]); // eslint-disable-line react-hooks/exhaustive-deps

  async function revoke(id: string) {
    if (!token) return;
    try {
      await api.revokeSession(token, id);
      await load();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "세션을 종료하지 못했습니다.");
    }
  }

  return (
    <SettingsPage title="보안 · 세션" description="현재 계정으로 로그인된 기기를 확인하고 필요 없는 세션을 종료합니다.">
      {message && <Alert severity="warning">{message}</Alert>}
      <SettingsCard title={`활성 세션 ${sessions.length}개`}>
        <Stack divider={<Stack component="span" sx={{ borderTop: 1, borderColor: "divider" }} />}>
          {sessions.length === 0 && <Typography color="text.secondary">표시할 세션이 없습니다.</Typography>}
          {sessions.map((session) => (
            <Stack key={session.id} direction={{ xs: "column", sm: "row" }} sx={{ py: 1.5, alignItems: { sm: "center" }, gap: 1.5 }}>
              <Stack sx={{ flex: 1 }}>
                <Stack direction="row" sx={{ gap: 1, alignItems: "center" }}>
                  <Typography variant="subtitle2">{session.device_id || "알 수 없는 기기"}</Typography>
                  {session.is_current && <Chip size="small" color="success" label="현재 세션" />}
                </Stack>
                <Typography variant="caption" color="text.secondary">로그인 {new Date(session.create_at).toLocaleString()} · 만료 {new Date(session.expires_at).toLocaleString()}</Typography>
              </Stack>
              {!session.is_current && <Button color="error" startIcon={<DeleteOutlineRounded />} onClick={() => void revoke(session.id)}>종료</Button>}
            </Stack>
          ))}
        </Stack>
      </SettingsCard>
    </SettingsPage>
  );
}
