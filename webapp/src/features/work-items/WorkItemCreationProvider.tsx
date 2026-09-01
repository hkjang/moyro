import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import AssignmentTurnedInRounded from "@mui/icons-material/AssignmentTurnedInRounded";
import GavelRounded from "@mui/icons-material/GavelRounded";
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  Snackbar,
  TextField,
  Typography,
} from "@mui/material";
import { api, compatApi, type Post, type User } from "@/api/client";
import {
  workItemsApi,
  type WorkItem,
  type WorkItemKind,
  type WorkItemPriority,
  type WorkItemRecurrence,
} from "@/api/work-items";

type CreationTarget = { post: Post; kind: WorkItemKind; supersedesID?: string };
type CreationOptions = { supersedesID?: string };

type WorkItemCreationContextValue = {
  available: boolean;
  open: (post: Post, kind: WorkItemKind, options?: CreationOptions) => void;
};

const WorkItemCreationContext = createContext<WorkItemCreationContextValue>({
  available: false,
  open: () => undefined,
});

export function useWorkItemCreation() {
  return useContext(WorkItemCreationContext);
}

function requestKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `work-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function suggestedTitle(message: string): string {
  const compact = message.replace(/\s+/g, " ").trim();
  return [...compact].slice(0, 120).join("") || "메시지에서 만든 업무";
}

function localDateTimeValue(timestamp: number): string {
  if (!timestamp) return "";
  const date = new Date(timestamp);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

async function channelUsersByIDs(token: string, ids: string[]): Promise<User[]> {
  const unique = [...new Set(ids.filter(Boolean))];
  const pages: string[][] = [];
  for (let index = 0; index < unique.length; index += 200) pages.push(unique.slice(index, index + 200));
  if (pages.length === 0) return [];
  return (await Promise.all(pages.map((page) => compatApi.usersByIds(token, page)))).flat();
}

export function WorkItemCreationProvider({
  token,
  currentUserID,
  children,
}: {
  token: string;
  currentUserID: string;
  children: ReactNode;
}) {
  const [target, setTarget] = useState<CreationTarget | null>(null);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [assigneeID, setAssigneeID] = useState("");
  const [reviewerID, setReviewerID] = useState("");
  const [dueAt, setDueAt] = useState("");
  const [priority, setPriority] = useState<WorkItemPriority>("normal");
  const [recurrenceUnit, setRecurrenceUnit] = useState<WorkItemRecurrence>("none");
  const [recurrenceInterval, setRecurrenceInterval] = useState(1);
  const [decisionStatus, setDecisionStatus] = useState<"proposed" | "recorded">("recorded");
  const [members, setMembers] = useState<User[]>([]);
  const [membersLoading, setMembersLoading] = useState(false);
  const [memberWarning, setMemberWarning] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState("");
  const keyRef = useRef("");

  const available = Boolean(token && currentUserID);
  const open = useCallback((post: Post, kind: WorkItemKind, options: CreationOptions = {}) => {
    if (!available) return;
    keyRef.current = requestKey();
    setTarget({ post, kind, supersedesID: options.supersedesID });
    setTitle(suggestedTitle(post.message));
    setDescription("");
    setAssigneeID(kind === "task" ? currentUserID : "");
    setReviewerID("");
    setDueAt("");
    setPriority("normal");
    setRecurrenceUnit("none");
    setRecurrenceInterval(1);
    setDecisionStatus("recorded");
    setMembers([]);
    setMembersLoading(true);
    setMemberWarning("");
    setError("");
  }, [available, currentUserID]);

  useEffect(() => {
    if (!target) return undefined;
    const controller = new AbortController();
    setMembersLoading(true);
    void api.listChannelMembers(token, target.post.channel_id)
      .then((rows) => channelUsersByIDs(token, rows.map((row) => row.user_id)))
      .then((users) => {
        if (!controller.signal.aborted) setMembers(users);
      })
      .catch(() => {
        if (!controller.signal.aborted) setMemberWarning("채널 멤버 목록을 불러오지 못했습니다.");
      })
      .finally(() => {
        if (!controller.signal.aborted) setMembersLoading(false);
      });
    return () => controller.abort();
  }, [target, token]);

  const contextValue = useMemo<WorkItemCreationContextValue>(() => ({ available, open }), [available, open]);

  async function submit() {
    if (!target || !title.trim() || saving) return;
    setSaving(true);
    setError("");
    try {
      const parsedDueAt = dueAt ? new Date(dueAt).getTime() : 0;
      if (dueAt && (!Number.isFinite(parsedDueAt) || parsedDueAt <= Date.now())) {
        setError("마감 시각은 현재보다 뒤여야 합니다.");
        return;
      }
      if (target.kind === "task" && recurrenceUnit !== "none" && !parsedDueAt) {
        setError("반복 작업에는 첫 마감 시각이 필요합니다.");
        return;
      }
      const result = await workItemsApi.create(token, {
        kind: target.kind,
        title: title.trim(),
        description: description.trim(),
        assignee_id: target.kind === "task" ? (assigneeID || currentUserID) : undefined,
        reviewer_id: target.kind === "decision" ? reviewerID || undefined : undefined,
        source_post_id: target.post.id,
        due_at: target.kind === "task" ? parsedDueAt : undefined,
        priority: target.kind === "task" ? priority : undefined,
        recurrence_unit: target.kind === "task" ? recurrenceUnit : undefined,
        recurrence_interval: target.kind === "task" && recurrenceUnit !== "none" ? recurrenceInterval : 0,
        initial_status: target.kind === "decision" ? (target.supersedesID ? "recorded" : decisionStatus) : undefined,
        supersedes_id: target.kind === "decision" ? target.supersedesID : undefined,
        idempotency_key: keyRef.current,
      });
      setTarget(null);
      setFeedback(result.replayed
        ? "이미 만든 업무를 다시 열었습니다."
        : target.kind === "task" ? "메시지를 작업으로 만들었습니다." : "메시지를 결정으로 기록했습니다.");
      window.dispatchEvent(new CustomEvent<WorkItem>("moyro:work-item-changed", { detail: result.item }));
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "업무를 만들지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  const isTask = target?.kind === "task";
  return (
    <WorkItemCreationContext.Provider value={contextValue}>
      {children}
      <Dialog
        open={Boolean(target)}
        onClose={() => { if (!saving) setTarget(null); }}
        fullWidth
        maxWidth="sm"
        aria-labelledby="work-item-create-title"
      >
        <DialogTitle id="work-item-create-title" sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          {isTask ? <AssignmentTurnedInRounded color="primary" /> : <GavelRounded color="warning" />}
          {isTask ? "작업으로 만들기" : "결정으로 기록"}
        </DialogTitle>
        <DialogContent dividers>
          <Box sx={{ display: "grid", gap: 2, pt: 0.5 }}>
            <Box sx={{ p: 1.5, borderRadius: 1.5, bgcolor: "action.hover" }}>
              <Typography variant="caption" color="text.secondary">원본 메시지</Typography>
              <Typography variant="body2" sx={{ mt: 0.5, whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
                {target?.post.message || "첨부파일 메시지"}
              </Typography>
            </Box>
            <TextField
              autoFocus
              required
              label={isTask ? "작업 제목" : "결정 내용"}
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              slotProps={{ htmlInput: { maxLength: 240 } }}
            />
            <TextField
              label={isTask ? "설명" : "근거와 메모"}
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              multiline
              minRows={3}
              slotProps={{ htmlInput: { maxLength: 10_000 } }}
            />
            {isTask && (
              <>
                <TextField
                  select
                  label="담당자"
                  value={assigneeID || currentUserID}
                  onChange={(event) => setAssigneeID(event.target.value)}
                  disabled={membersLoading}
                  helperText={membersLoading ? "채널 멤버를 불러오는 중…" : undefined}
                >
                  {members.length === 0 && <MenuItem value={currentUserID}>나에게 할당</MenuItem>}
                  {members.map((member) => (
                    <MenuItem key={member.id} value={member.id}>
                      {member.id === currentUserID ? `나 · ${member.username}` : member.username}
                    </MenuItem>
                  ))}
                </TextField>
                <TextField
                  select
                  label="우선순위"
                  value={priority}
                  onChange={(event) => setPriority(event.target.value as WorkItemPriority)}
                >
                  <MenuItem value="low">낮음</MenuItem>
                  <MenuItem value="normal">보통</MenuItem>
                  <MenuItem value="high">높음</MenuItem>
                  <MenuItem value="urgent">긴급</MenuItem>
                </TextField>
                <TextField
                  type="datetime-local"
                  label="마감 시각 (선택)"
                  value={dueAt}
                  onChange={(event) => setDueAt(event.target.value)}
                  slotProps={{ inputLabel: { shrink: true }, htmlInput: { min: localDateTimeValue(Date.now() + 60_000) } }}
                />
                <TextField
                  select
                  label="반복"
                  value={recurrenceUnit}
                  onChange={(event) => setRecurrenceUnit(event.target.value as WorkItemRecurrence)}
                >
                  <MenuItem value="none">반복 안 함</MenuItem>
                  <MenuItem value="daily">일 단위</MenuItem>
                  <MenuItem value="weekly">주 단위</MenuItem>
                  <MenuItem value="monthly">월 단위</MenuItem>
                </TextField>
                {recurrenceUnit !== "none" && (
                  <TextField
                    type="number"
                    label="반복 간격"
                    value={recurrenceInterval}
                    onChange={(event) => setRecurrenceInterval(Math.min(365, Math.max(1, Number(event.target.value) || 1)))}
                    helperText={recurrenceUnit === "daily" ? "며칠마다" : recurrenceUnit === "weekly" ? "몇 주마다" : "몇 달마다"}
                    slotProps={{ htmlInput: { min: 1, max: 365 } }}
                  />
                )}
              </>
            )}
            {!isTask && (
              <>
                <TextField
                  select
                  label="검토자 (선택)"
                  value={reviewerID}
                  onChange={(event) => setReviewerID(event.target.value)}
                  disabled={membersLoading}
                  helperText={membersLoading ? "채널 멤버를 불러오는 중…" : undefined}
                >
                  <MenuItem value="">검토자 없음</MenuItem>
                  {members.map((member) => (
                    <MenuItem key={member.id} value={member.id}>
                      {member.id === currentUserID ? `나 · ${member.username}` : member.username}
                    </MenuItem>
                  ))}
                </TextField>
                <TextField
                  select
                  label="초기 상태"
                  value={target?.supersedesID ? "recorded" : decisionStatus}
                  onChange={(event) => setDecisionStatus(event.target.value as "proposed" | "recorded")}
                  disabled={Boolean(target?.supersedesID)}
                  helperText={target?.supersedesID ? "대체 결정은 즉시 확정되어 기존 결정을 대체합니다." : undefined}
                >
                  <MenuItem value="proposed">제안 · 검토 전</MenuItem>
                  <MenuItem value="recorded">확정 기록</MenuItem>
                </TextField>
              </>
            )}
            {memberWarning && <Alert severity="warning">{memberWarning}</Alert>}
            {error && <Alert severity="error" role="alert">{error}</Alert>}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setTarget(null)} disabled={saving}>취소</Button>
          <Button variant="contained" onClick={() => void submit()} disabled={saving || !title.trim()}>
            {saving ? "저장 중…" : isTask ? "작업 만들기" : "결정 기록"}
          </Button>
        </DialogActions>
      </Dialog>
      <Snackbar
        open={Boolean(feedback)}
        autoHideDuration={5000}
        onClose={() => setFeedback("")}
        message={feedback}
      />
    </WorkItemCreationContext.Provider>
  );
}
