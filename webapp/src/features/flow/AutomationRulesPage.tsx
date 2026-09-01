import AddRounded from "@mui/icons-material/AddRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import HistoryRounded from "@mui/icons-material/HistoryRounded";
import {
  Alert,
  Box,
  Button,
  Chip,
  FormControlLabel,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import {
  automationsApi,
  type AutomationAction,
  type AutomationActionType,
  type AutomationRule,
  type AutomationRun,
  type SaveAutomationRule,
} from "@/api/automations";
import type { RootState } from "@/store";
import { FlowCard, FlowEmpty, FlowError, FlowLoading, FlowPage, FlowSection, FlowStatusBadge } from "./FlowPage";
import { errorMessage, formatDateTime } from "./flow-data";
import { useFlowWorkspaceIndex } from "./FlowDataProvider";

function blankAction(type: AutomationActionType): AutomationAction {
  if (type === "reminder") return { type, config: { remind_offset_minutes: 60 } };
  if (type === "decision") return { type, config: { initial_status: "proposed" } };
  return { type, config: { priority: "normal", recurrence_unit: "none", recurrence_interval: 0 } };
}

function blankRule(channelID = ""): SaveAutomationRule {
  return {
    name: "", channel_id: channelID, enabled: true,
    match_type: "contains", match_value: "",
    actions: [blankAction("task")],
  };
}

function runTone(status: AutomationRun["status"]): "default" | "warning" | "error" | "info" | "success" {
  if (status === "succeeded") return "success";
  if (status === "dead") return "error";
  if (status === "retry" || status === "processing") return "warning";
  if (status === "pending") return "info";
  return "default";
}

export function AutomationRulesPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const workspace = useFlowWorkspaceIndex();
  const [rules, setRules] = useState<AutomationRule[]>([]);
  const [draft, setDraft] = useState<SaveAutomationRule>(() => blankRule());
  const [editingID, setEditingID] = useState("");
  const [runs, setRuns] = useState<Record<string, AutomationRun[]>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");

  const defaultChannelID = workspace.entries[0]?.channel.id ?? "";
  const channels = useMemo(
    () => [...workspace.entries].sort((left, right) =>
      (left.channel.display_name || left.channel.name).localeCompare(right.channel.display_name || right.channel.name, "ko")),
    [workspace.entries],
  );

  useEffect(() => {
    if (!draft.channel_id && defaultChannelID) setDraft((current) => ({ ...current, channel_id: defaultChannelID }));
  }, [defaultChannelID, draft.channel_id]);

  useEffect(() => {
    if (!token) {
      setLoading(false);
      setError("로그인 세션이 없습니다.");
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    void automationsApi.list(token, controller.signal).then(
      (rows) => { setRules(rows); setError(""); },
      (cause: unknown) => { if (!controller.signal.aborted) setError(errorMessage(cause, "자동화 규칙을 불러오지 못했습니다.")); },
    ).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [token]);

  function edit(rule: AutomationRule) {
    setEditingID(rule.id);
    setDraft({
      name: rule.name, channel_id: rule.channel_id, enabled: rule.enabled,
      match_type: rule.match_type, match_value: rule.match_value,
      revision: rule.revision,
      actions: rule.actions.map((action) => ({ ...action, config: { ...action.config } })),
    });
    setFeedback("");
    setError("");
  }

  function reset() {
    setEditingID("");
    setDraft(blankRule(defaultChannelID));
  }

  function updateAction(index: number, next: AutomationAction) {
    setDraft((current) => ({
      ...current,
      actions: current.actions.map((action, actionIndex) => actionIndex === index ? next : action),
    }));
  }

  async function save() {
    if (!token || saving || !draft.name.trim() || !draft.match_value.trim() || !draft.channel_id) return;
    setSaving(true);
    setError("");
    setFeedback("");
    try {
      const saved = editingID
        ? await automationsApi.update(token, editingID, draft)
        : await automationsApi.create(token, draft);
      setRules((current) => [saved, ...current.filter((rule) => rule.id !== saved.id)]);
      setFeedback(editingID ? "자동화 규칙을 수정했습니다." : "자동화 규칙을 만들었습니다.");
      reset();
    } catch (cause) {
      setError(errorMessage(cause, "자동화 규칙을 저장하지 못했습니다."));
    } finally {
      setSaving(false);
    }
  }

  async function remove(rule: AutomationRule) {
    if (!token || !window.confirm(`“${rule.name}” 규칙을 삭제할까요?`)) return;
    setError("");
    try {
      await automationsApi.remove(token, rule.id);
      setRules((current) => current.filter((candidate) => candidate.id !== rule.id));
      if (editingID === rule.id) reset();
      setFeedback("규칙과 아직 시작하지 않은 실행을 취소했습니다.");
    } catch (cause) {
      setError(errorMessage(cause, "자동화 규칙을 삭제하지 못했습니다."));
    }
  }

  async function showRuns(rule: AutomationRule) {
    if (!token) return;
    setError("");
    try {
      const rows = await automationsApi.runs(token, rule.id);
      setRuns((current) => ({ ...current, [rule.id]: rows }));
    } catch (cause) {
      setError(errorMessage(cause, "실행 이력을 불러오지 못했습니다."));
    }
  }

  return (
    <FlowPage
      eyebrow="대화 자동화"
      title="자동화 규칙"
      description="채널 메시지의 시작 문구나 포함 문구를 기준으로 작업·결정·리마인더를 유실 없이 만듭니다."
      actions={<Button startIcon={<AddRounded />} onClick={reset}>새 규칙</Button>}
    >
      {error && <FlowError message={error} />}
      {feedback && <Alert severity="success">{feedback}</Alert>}
      <FlowSection title={editingID ? "규칙 수정" : "규칙 만들기"} description="정규식 없이 단순한 두 조건만 제공해 예측 가능하게 동작합니다.">
        <FlowCard component="div">
          <Stack spacing={2}>
            <Stack direction={{ xs: "column", md: "row" }} spacing={2}>
              <TextField fullWidth required label="규칙 이름" value={draft.name} onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))} slotProps={{ htmlInput: { maxLength: 120 } }} />
              <TextField select fullWidth required label="대상 채널" value={draft.channel_id} onChange={(event) => setDraft((current) => ({ ...current, channel_id: event.target.value }))}>
                {channels.map((entry) => <MenuItem key={entry.channel.id} value={entry.channel.id}>{entry.channel.display_name || entry.channel.name}</MenuItem>)}
              </TextField>
            </Stack>
            <Stack direction={{ xs: "column", md: "row" }} spacing={2}>
              <TextField select label="조건" value={draft.match_type} onChange={(event) => setDraft((current) => ({ ...current, match_type: event.target.value as SaveAutomationRule["match_type"] }))} sx={{ minWidth: 180 }}>
                <MenuItem value="contains">메시지에 포함</MenuItem>
                <MenuItem value="starts_with">메시지로 시작</MenuItem>
              </TextField>
              <TextField fullWidth required label="찾을 문구" value={draft.match_value} onChange={(event) => setDraft((current) => ({ ...current, match_value: event.target.value }))} slotProps={{ htmlInput: { maxLength: 200 } }} />
              <FormControlLabel control={<Switch checked={draft.enabled} onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))} />} label="활성" />
            </Stack>
            {draft.actions.map((action, index) => (
              <Box key={action.id ?? `new-${index}`} sx={{ p: 2, border: 1, borderColor: "divider", borderRadius: 2 }}>
                <Stack direction={{ xs: "column", md: "row" }} spacing={2} sx={{ alignItems: { md: "center" } }}>
                  <TextField select label={`동작 ${index + 1}`} value={action.type} onChange={(event) => updateAction(index, blankAction(event.target.value as AutomationActionType))} sx={{ minWidth: 170 }}>
                    <MenuItem value="task">작업 만들기</MenuItem>
                    <MenuItem value="decision">결정 만들기</MenuItem>
                    <MenuItem value="reminder">리마인더 만들기</MenuItem>
                  </TextField>
                  {action.type !== "reminder" && <TextField fullWidth label="제목 템플릿" helperText="{message}를 쓰면 원문으로 치환됩니다." value={action.config.title ?? ""} onChange={(event) => updateAction(index, { ...action, config: { ...action.config, title: event.target.value } })} />}
                  {action.type === "task" && <TextField select label="우선순위" value={action.config.priority ?? "normal"} onChange={(event) => updateAction(index, { ...action, config: { ...action.config, priority: event.target.value as "low" | "normal" | "high" | "urgent" } })} sx={{ minWidth: 130 }}>
                    <MenuItem value="low">낮음</MenuItem><MenuItem value="normal">보통</MenuItem><MenuItem value="high">높음</MenuItem><MenuItem value="urgent">긴급</MenuItem>
                  </TextField>}
                  {action.type === "task" && <TextField type="number" label="마감까지 분" value={action.config.due_offset_minutes ?? 0} onChange={(event) => updateAction(index, { ...action, config: { ...action.config, due_offset_minutes: Math.max(0, Number(event.target.value) || 0) } })} slotProps={{ htmlInput: { min: 0 } }} sx={{ width: 150 }} />}
                  {action.type === "decision" && <TextField select label="초기 상태" value={action.config.initial_status ?? "proposed"} onChange={(event) => updateAction(index, { ...action, config: { ...action.config, initial_status: event.target.value as "proposed" | "recorded" } })} sx={{ minWidth: 140 }}><MenuItem value="proposed">제안</MenuItem><MenuItem value="recorded">확정</MenuItem></TextField>}
                  {action.type === "reminder" && <TextField type="number" label="알림까지 분" value={action.config.remind_offset_minutes ?? 60} onChange={(event) => updateAction(index, { ...action, config: { remind_offset_minutes: Math.max(1, Number(event.target.value) || 1) } })} slotProps={{ htmlInput: { min: 1 } }} sx={{ width: 170 }} />}
                  <Button color="error" disabled={draft.actions.length === 1} onClick={() => setDraft((current) => ({ ...current, actions: current.actions.filter((_, actionIndex) => actionIndex !== index) }))}>제거</Button>
                </Stack>
              </Box>
            ))}
            <Stack direction="row" spacing={1}>
              <Button disabled={draft.actions.length >= 5} onClick={() => setDraft((current) => ({ ...current, actions: [...current.actions, blankAction("task")] }))}>동작 추가</Button>
              <Box sx={{ flex: 1 }} />
              {editingID && <Button onClick={reset} disabled={saving}>취소</Button>}
              <Button variant="contained" onClick={() => void save()} disabled={saving || !draft.name.trim() || !draft.match_value.trim() || !draft.channel_id}>{saving ? "저장 중…" : "저장"}</Button>
            </Stack>
          </Stack>
        </FlowCard>
      </FlowSection>

      <FlowSection title="내 규칙" description="규칙 수정 후에도 이전 실행 이력은 보존됩니다.">
        {loading ? <FlowLoading /> : rules.length === 0 ? <FlowEmpty title="자동화 규칙이 없습니다" description="반복되는 대화 후속 조치를 위 폼에서 자동화해 보세요." /> : (
          <Stack spacing={1.5}>
            {rules.map((rule) => (
              <FlowCard key={rule.id}>
                <Stack spacing={1.5}>
                  <Stack direction={{ xs: "column", md: "row" }} spacing={1} sx={{ alignItems: { md: "center" } }}>
                    <Typography component="h3" className="flow-item-title">{rule.name}</Typography>
                    <Chip size="small" label={rule.enabled ? "활성" : "중지"} color={rule.enabled ? "success" : "default"} />
                    <Typography className="flow-item-subtitle">{rule.match_type === "starts_with" ? "시작" : "포함"}: “{rule.match_value}” · 동작 {rule.actions.length}개</Typography>
                    <Box sx={{ flex: 1 }} />
                    <Button onClick={() => edit(rule)}>수정</Button>
                    <Button startIcon={<HistoryRounded />} onClick={() => void showRuns(rule)}>실행 이력</Button>
                    <Button color="error" startIcon={<DeleteOutlineRounded />} onClick={() => void remove(rule)}>삭제</Button>
                  </Stack>
                  {runs[rule.id] && (runs[rule.id].length === 0 ? <Typography className="flow-item-subtitle">아직 실행되지 않았습니다.</Typography> : runs[rule.id].map((run) => (
                    <Stack key={run.id} direction={{ xs: "column", md: "row" }} spacing={1} sx={{ pl: 1, alignItems: { md: "center" } }}>
                      <FlowStatusBadge label={run.status} tone={runTone(run.status)} />
                      <Typography className="flow-item-subtitle">{run.action_type} · {formatDateTime(run.create_at)} · 시도 {run.attempt_count}회</Typography>
                      {run.last_error_text && <Typography color="error" className="flow-item-subtitle">{run.last_error_text}</Typography>}
                    </Stack>
                  )))}
                </Stack>
              </FlowCard>
            ))}
          </Stack>
        )}
      </FlowSection>
    </FlowPage>
  );
}
