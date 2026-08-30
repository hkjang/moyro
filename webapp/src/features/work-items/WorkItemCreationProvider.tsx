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
import { workItemsApi, type WorkItem, type WorkItemKind } from "@/api/work-items";

type CreationTarget = { post: Post; kind: WorkItemKind };

type WorkItemCreationContextValue = {
  available: boolean;
  open: (post: Post, kind: WorkItemKind) => void;
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
  const [dueAt, setDueAt] = useState("");
  const [members, setMembers] = useState<User[]>([]);
  const [membersLoading, setMembersLoading] = useState(false);
  const [memberWarning, setMemberWarning] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState("");
  const keyRef = useRef("");

  const available = Boolean(token && currentUserID);
  const open = useCallback((post: Post, kind: WorkItemKind) => {
    if (!available) return;
    keyRef.current = requestKey();
    setTarget({ post, kind });
    setTitle(suggestedTitle(post.message));
    setDescription("");
    setAssigneeID(kind === "task" ? currentUserID : "");
    setDueAt("");
    setMembers([]);
    setMembersLoading(kind === "task");
    setMemberWarning("");
    setError("");
  }, [available, currentUserID]);

  useEffect(() => {
    if (!target || target.kind !== "task") return undefined;
    const controller = new AbortController();
    setMembersLoading(true);
    void api.listChannelMembers(token, target.post.channel_id)
      .then((rows) => channelUsersByIDs(token, rows.map((row) => row.user_id)))
      .then((users) => {
        if (!controller.signal.aborted) setMembers(users);
      })
      .catch(() => {
        if (!controller.signal.aborted) setMemberWarning("담당자 목록을 불러오지 못해 나에게 할당합니다.");
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
      const result = await workItemsApi.create(token, {
        kind: target.kind,
        title: title.trim(),
        description: description.trim(),
        assignee_id: target.kind === "task" ? (assigneeID || currentUserID) : undefined,
        source_post_id: target.post.id,
        due_at: target.kind === "task" ? parsedDueAt : undefined,
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
                  type="datetime-local"
                  label="마감 시각 (선택)"
                  value={dueAt}
                  onChange={(event) => setDueAt(event.target.value)}
                  slotProps={{ inputLabel: { shrink: true }, htmlInput: { min: localDateTimeValue(Date.now() + 60_000) } }}
                />
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
