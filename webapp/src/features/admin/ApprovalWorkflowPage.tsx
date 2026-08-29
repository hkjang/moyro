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
import { moyroAdminApi, type ApprovalPolicy } from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import { useSystemInfo } from "@/features/system/SystemInfoContext";
import type { RootState } from "@/store";

const DEFAULT_POLICY: ApprovalPolicy = {
  name: "팀장 검토",
  enabled: false,
  protected_actions: ["mcp.create_post", "mcp.reply_to_thread"],
  reviewer_roles: ["team_lead", "system_admin"],
  require_rejection_reason: true,
  allow_self_approval: false,
  expires_after_hours: 72,
};

const parseList = (value: string) => value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);

export function ApprovalWorkflowPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const systemInfo = useSystemInfo();
  const [policy, setPolicy] = useState<ApprovalPolicy>(DEFAULT_POLICY);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    moyroAdminApi.listApprovalPolicies(token).then(
      (rows) => { if (!cancelled) { if (rows[0]) setPolicy({ ...DEFAULT_POLICY, ...rows[0] }); setError(""); } },
      (err: unknown) => { if (!cancelled) setError(err instanceof Error ? err.message : "승인 정책 API에 연결하지 못했습니다."); },
    ).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [token]);

  const update = <K extends keyof ApprovalPolicy>(key: K, value: ApprovalPolicy[K]) => {
    setPolicy((prev) => ({ ...prev, [key]: value }));
    setSaved("");
  };

  async function save() {
    if (!token) return;
    setSaving(true);
    try {
      const result = policy.id
        ? await moyroAdminApi.patchApprovalPolicy(token, policy.id, policy)
        : await moyroAdminApi.createApprovalPolicy(token, policy);
      setPolicy({ ...DEFAULT_POLICY, ...result });
      await systemInfo.refresh();
      setError("");
      setSaved(policy.enabled ? "검토·승인 절차를 활성화했습니다." : "검토·승인 절차를 제외했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "승인 정책을 저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsPage title="검토 · 승인" description="관리자가 명시적으로 켠 경우에만 승인 대기, 승인, 반려 상태가 서비스에 나타납니다.">
      <LoadState loading={loading} error={error}>
        <Alert severity={policy.enabled ? "warning" : "info"}>
          {policy.enabled
            ? "선택한 작업은 즉시 실행되지 않고 검토자의 결정을 기다립니다."
            : "현재는 검토 절차가 없습니다. 관련 메뉴와 상태를 사용자 화면에서 제외하고 작업을 즉시 처리합니다."}
        </Alert>
        <SettingsCard title="정책 상태" description="기본값은 비활성이며 설정 저장 전에는 어떤 업무도 차단하지 않습니다.">
          <Stack spacing={2}>
            <FormControlLabel control={<Switch checked={policy.enabled} onChange={(event) => update("enabled", event.target.checked)} />} label="팀장 검토 및 승인 절차 사용" />
            <TextField fullWidth label="정책 이름" value={policy.name} onChange={(event) => update("name", event.target.value)} />
          </Stack>
        </SettingsCard>
        <SettingsCard title="검토 규칙">
          <Grid container spacing={2}>
            <Grid size={{ xs: 12 }}><TextField fullWidth label="검토가 필요한 작업" value={policy.protected_actions.join(", ")} onChange={(event) => update("protected_actions", parseList(event.target.value))} helperText="예: mcp.create_post, mcp.reply_to_thread" disabled={!policy.enabled} /></Grid>
            <Grid size={{ xs: 12, md: 7 }}><TextField fullWidth label="검토자 roles" value={policy.reviewer_roles.join(", ")} onChange={(event) => update("reviewer_roles", parseList(event.target.value))} helperText="해당 role과 review_approval 권한을 모두 가진 사용자만 결정할 수 있습니다." disabled={!policy.enabled} /></Grid>
            <Grid size={{ xs: 12, md: 5 }}><TextField fullWidth type="number" label="승인 대기 만료 (시간)" value={policy.expires_after_hours} onChange={(event) => update("expires_after_hours", Math.max(1, Number(event.target.value) || 1))} disabled={!policy.enabled} /></Grid>
            <Grid size={{ xs: 12, md: 6 }}><FormControlLabel control={<Switch checked={policy.require_rejection_reason} onChange={(event) => update("require_rejection_reason", event.target.checked)} />} label="반려 사유 필수" disabled={!policy.enabled} /></Grid>
            <Grid size={{ xs: 12, md: 6 }}><FormControlLabel control={<Switch checked={policy.allow_self_approval} onChange={(event) => update("allow_self_approval", event.target.checked)} />} label="요청자 자기 승인 허용" disabled={!policy.enabled} /></Grid>
          </Grid>
        </SettingsCard>
        <SaveBar saving={saving} saved={saved} onSave={save} label={policy.enabled ? "정책 저장" : "비활성 상태 저장"} />
      </LoadState>
    </SettingsPage>
  );
}
