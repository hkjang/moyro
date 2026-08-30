import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import {
  Alert,
  Avatar,
  Button,
  Chip,
  FormControlLabel,
  Radio,
  RadioGroup,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import { api, type SessionRow } from "@/api/client";
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

  return (
    <SettingsPage title="알림" description="내 계정의 이메일과 기본 알림 방식을 관리합니다.">
      <SettingsCard title="이메일 요약">
        <FormControlLabel control={<Switch checked={digest} disabled={!loaded || !emailDigestAvailable} onChange={(event) => void update(event.target.checked)} />} label="놓친 멘션을 하루 한 번 이메일로 받기" />
        {systemInfo.loaded && !emailDigestAvailable && <Alert severity="info" sx={{ mt: 2 }}>현재 릴리스는 SMTP 관리 설정과 이메일 요약 발송을 지원하지 않습니다.</Alert>}
        {message && <Typography variant="body2" sx={{ mt: 2 }} role="status">{message}</Typography>}
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
