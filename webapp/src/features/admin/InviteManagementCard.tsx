import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { api, integrationsApi, type Invite, type Team } from "@/api/client";
import { SettingsCard } from "@/components/settings/SettingsPrimitives";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";

const TTL_OPTIONS = [
  { value: 24 * 60 * 60, label: "1일" },
  { value: 7 * 24 * 60 * 60, label: "7일" },
  { value: 30 * 24 * 60 * 60, label: "30일" },
];

function absoluteInviteURL(value: string) {
  try {
    return new URL(value, window.location.origin).toString();
  } catch {
    return value;
  }
}

export function InviteManagementCard() {
  const token = useSelector((state: RootState) => state.auth.token);
  const access = useAdminAccess();
  const [teams, setTeams] = useState<Team[]>([]);
  const [teamID, setTeamID] = useState("");
  const [invites, setInvites] = useState<Invite[]>([]);
  const [maxUses, setMaxUses] = useState(1);
  const [ttlSeconds, setTTLSeconds] = useState(TTL_OPTIONS[1].value);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [revokeTarget, setRevokeTarget] = useState<Invite | null>(null);
  const selectedTeam = useMemo(() => teams.find((team) => team.id === teamID), [teamID, teams]);

  useEffect(() => {
    if (!token || !access.can("manage_system")) return;
    let cancelled = false;
    api.listTeams(token).then((rows) => {
      if (cancelled) return;
      setTeams(rows);
      setTeamID((current) => current || rows[0]?.id || "");
    }).catch((err: unknown) => {
      if (!cancelled) setError(err instanceof Error ? err.message : "워크스페이스를 불러오지 못했습니다.");
    });
    return () => { cancelled = true; };
  }, [access, token]);

  useEffect(() => {
    if (!token || !teamID || !access.can("manage_system")) return;
    let cancelled = false;
    setBusy(true);
	integrationsApi.listInvites(token, teamID).then((rows) => {
      if (!cancelled) {
        setInvites(rows);
        setError("");
      }
    }).catch((err: unknown) => {
      if (!cancelled) setError(err instanceof Error ? err.message : "초대 링크를 불러오지 못했습니다.");
    }).finally(() => { if (!cancelled) setBusy(false); });
    return () => { cancelled = true; };
  }, [access, teamID, token]);

  if (!access.can("manage_system")) return null;

  async function createInvite() {
    if (!token || !teamID || maxUses < 0 || maxUses > 10000) return;
    setBusy(true);
    setMessage("");
    try {
		const created = await integrationsApi.createInvite(token, teamID, maxUses, ttlSeconds);
      setInvites((current) => [created, ...current]);
      setError("");
      setMessage("초대 링크를 만들었습니다. 링크를 안전한 경로로 전달하세요.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "초대 링크를 만들지 못했습니다.");
    } finally {
      setBusy(false);
    }
  }

  async function copyInvite(invite: Invite) {
    try {
      await navigator.clipboard.writeText(absoluteInviteURL(invite.url));
      setMessage("초대 링크를 클립보드에 복사했습니다.");
      setError("");
    } catch {
      setError("브라우저가 클립보드 접근을 허용하지 않았습니다. 링크를 직접 선택해 복사하세요.");
    }
  }

  async function revokeInvite() {
    if (!token || !revokeTarget) return;
    setBusy(true);
    try {
		await integrationsApi.revokeInvite(token, revokeTarget.team_id, revokeTarget.id);
      setInvites((current) => current.filter((invite) => invite.id !== revokeTarget.id));
      setRevokeTarget(null);
      setError("");
      setMessage("초대 링크를 폐기했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "초대 링크를 폐기하지 못했습니다.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <SettingsCard
      title="사용자 초대"
      description="공개 가입을 열지 않고 워크스페이스별 만료·사용 횟수 제한 링크로 로컬 사용자를 온보딩합니다."
    >
      <Stack spacing={2}>
        {error && <Alert severity="error">{error}</Alert>}
        {message && <Alert severity="success">{message}</Alert>}
        <Stack direction={{ xs: "column", sm: "row" }} spacing={1.5}>
          <TextField
            select
            fullWidth
            label="워크스페이스"
            value={teamID}
            onChange={(event) => { setTeamID(event.target.value); setMessage(""); }}
          >
            {teams.map((team) => <MenuItem key={team.id} value={team.id}>{team.display_name}</MenuItem>)}
          </TextField>
          <TextField
            type="number"
            label="최대 사용 횟수"
            value={maxUses}
            onChange={(event) => setMaxUses(Math.max(0, Number(event.target.value) || 0))}
			slotProps={{ htmlInput: { min: 0, max: 10000 } }}
            helperText="0은 만료 전까지 무제한"
            sx={{ minWidth: { sm: 180 } }}
          />
          <TextField
            select
            label="유효 기간"
            value={ttlSeconds}
            onChange={(event) => setTTLSeconds(Number(event.target.value))}
            sx={{ minWidth: { sm: 140 } }}
          >
            {TTL_OPTIONS.map((option) => <MenuItem key={option.value} value={option.value}>{option.label}</MenuItem>)}
          </TextField>
          <Button variant="contained" disabled={busy || !teamID} onClick={createInvite} sx={{ minWidth: 112 }}>
            링크 생성
          </Button>
        </Stack>
        <Divider />
        {invites.length === 0 ? (
          <Alert severity="info">{busy ? "초대 링크를 확인하는 중입니다." : `${selectedTeam?.display_name ?? "선택한 워크스페이스"}에 활성 초대 링크가 없습니다.`}</Alert>
        ) : (
          <Stack spacing={1.25}>
            {invites.map((invite) => {
              const exhausted = invite.max_uses > 0 && invite.use_count >= invite.max_uses;
              const expired = invite.expires_at <= Date.now();
              return (
                <Box key={invite.id} sx={{ border: 1, borderColor: "divider", borderRadius: 1.5, p: 1.5 }}>
				<Stack direction={{ xs: "column", md: "row" }} spacing={1.25} sx={{ alignItems: { md: "center" } }}>
                    <Box sx={{ minWidth: 0, flex: 1 }}>
                      <Typography variant="body2" sx={{ overflowWrap: "anywhere" }}>{absoluteInviteURL(invite.url)}</Typography>
                      <Typography variant="caption" color="text.secondary">
                        {new Date(invite.expires_at).toLocaleString("ko-KR")} 만료 · {invite.use_count}/{invite.max_uses === 0 ? "∞" : invite.max_uses}회 사용
                      </Typography>
                    </Box>
                    {(expired || exhausted) && <Chip size="small" label={expired ? "만료" : "소진"} />}
                    <Button size="small" disabled={expired || exhausted} onClick={() => copyInvite(invite)}>복사</Button>
                    <Button size="small" color="error" onClick={() => setRevokeTarget(invite)}>폐기</Button>
                  </Stack>
                </Box>
              );
            })}
          </Stack>
        )}
      </Stack>
      <Dialog open={Boolean(revokeTarget)} onClose={() => !busy && setRevokeTarget(null)}>
        <DialogTitle>초대 링크 폐기</DialogTitle>
        <DialogContent><Typography>이 링크로는 더 이상 가입할 수 없습니다. 폐기하시겠습니까?</Typography></DialogContent>
        <DialogActions>
          <Button disabled={busy} onClick={() => setRevokeTarget(null)}>취소</Button>
          <Button disabled={busy} color="error" variant="contained" onClick={revokeInvite}>폐기</Button>
        </DialogActions>
      </Dialog>
    </SettingsCard>
  );
}
