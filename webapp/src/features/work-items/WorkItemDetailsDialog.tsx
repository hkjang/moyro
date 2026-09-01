import LinkRounded from "@mui/icons-material/LinkRounded";
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import { workItemsApi, type WorkItem, type WorkItemEvent } from "@/api/work-items";

const EVENT_LABELS: Record<string, string> = {
  created: "생성", updated: "내용 수정", status_changed: "상태 변경", assigned: "담당 변경",
  reviewer_changed: "검토자 변경", dependency_added: "선행 작업 연결", dependency_removed: "선행 작업 해제",
  impact_added: "영향 작업 연결", impact_removed: "영향 작업 해제", superseded: "새 결정으로 대체",
  recurrence_spawned: "다음 반복 작업 생성",
};

function eventSummary(event: WorkItemEvent): string {
  if (event.from_status || event.to_status) return `${event.from_status || "—"} → ${event.to_status || "—"}`;
  const values = Object.values(event.details).filter((value) => typeof value === "string");
  return values.length > 0 ? values.join(" · ") : "";
}

export function WorkItemDetailsDialog({ token, item, items, canManage, onClose, onChanged }: {
  token: string;
  item: WorkItem | null;
  items: WorkItem[];
  canManage: boolean;
  onClose: () => void;
  onChanged: (item: WorkItem) => void;
}) {
  const [current, setCurrent] = useState<WorkItem | null>(item);
  const [events, setEvents] = useState<WorkItemEvent[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [loading, setLoading] = useState(false);
  const [mutating, setMutating] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    setCurrent(item);
    setSelectedID("");
  }, [item]);

  useEffect(() => {
    if (!item || !token) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    void workItemsApi.events(token, item.id, controller.signal).then(
      setEvents,
      (cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "변경 이력을 불러오지 못했습니다."); },
    ).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [item, revision, token]);

  const linkedIDs = current?.kind === "task" ? current.dependency_ids ?? [] : current?.impact_task_ids ?? [];
  const candidates = useMemo(() => {
    if (!current) return [];
    return items.filter((candidate) => candidate.kind === "task" && candidate.channel_id === current.channel_id && candidate.id !== current.id && !linkedIDs.includes(candidate.id));
  }, [current, items, linkedIDs]);
  const itemByID = useMemo(() => Object.fromEntries(items.map((candidate) => [candidate.id, candidate])), [items]);

  async function mutate(targetID: string, remove: boolean) {
    if (!current || !token || mutating || !targetID) return;
    setMutating(true);
    setError("");
    try {
      const updated = current.kind === "task"
        ? remove
          ? await workItemsApi.removeDependency(token, current.id, targetID)
          : await workItemsApi.addDependency(token, current.id, targetID)
        : remove
          ? await workItemsApi.removeImpact(token, current.id, targetID)
          : await workItemsApi.addImpact(token, current.id, targetID);
      setCurrent(updated);
      setSelectedID("");
      setRevision((value) => value + 1);
      onChanged(updated);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "연결을 변경하지 못했습니다.");
    } finally {
      setMutating(false);
    }
  }

  return (
    <Dialog open={Boolean(item)} onClose={onClose} fullWidth maxWidth="md" aria-labelledby="work-item-details-title">
      <DialogTitle id="work-item-details-title">{current?.title ?? "업무 상세"}</DialogTitle>
      <DialogContent dividers>
        <Stack spacing={3}>
          {error && <Alert severity="error">{error}</Alert>}
          {current && (
            <Box component="section" aria-labelledby="work-item-links-title">
              <Typography id="work-item-links-title" component="h3" variant="subtitle1" sx={{ mb: 1 }}>
                {current.kind === "task" ? "선행 작업" : "영향 받는 작업"}
              </Typography>
              {linkedIDs.length === 0 ? <Typography color="text.secondary">연결된 작업이 없습니다.</Typography> : (
                <Stack spacing={1}>
                  {linkedIDs.map((id) => (
                    <Stack key={id} direction="row" spacing={1} sx={{ alignItems: "center" }}>
                      <LinkRounded fontSize="small" color="action" />
                      <Typography sx={{ flex: 1 }}>{itemByID[id]?.title ?? id}</Typography>
                      {canManage && <Button size="small" color="error" disabled={mutating} onClick={() => void mutate(id, true)}>연결 해제</Button>}
                    </Stack>
                  ))}
                </Stack>
              )}
              {canManage && (
                <Stack direction={{ xs: "column", sm: "row" }} spacing={1} sx={{ mt: 1.5 }}>
                  <TextField select fullWidth size="small" label={current.kind === "task" ? "선행 작업 선택" : "영향 작업 선택"} value={selectedID} onChange={(event) => setSelectedID(event.target.value)}>
                    {candidates.map((candidate) => <MenuItem key={candidate.id} value={candidate.id}>{candidate.title}</MenuItem>)}
                  </TextField>
                  <Button variant="outlined" disabled={!selectedID || mutating} onClick={() => void mutate(selectedID, false)}>연결 추가</Button>
                </Stack>
              )}
            </Box>
          )}
          <Box component="section" aria-labelledby="work-item-history-title">
            <Typography id="work-item-history-title" component="h3" variant="subtitle1" sx={{ mb: 1 }}>변경 이력</Typography>
            {loading ? <Typography role="status">이력을 불러오는 중…</Typography> : events.length === 0 ? <Typography color="text.secondary">기록된 변경이 없습니다.</Typography> : (
              <Stack spacing={1}>
                {events.map((event) => (
                  <Box key={event.id} sx={{ borderLeft: 2, borderColor: "divider", pl: 1.5 }}>
                    <Typography variant="body2">{EVENT_LABELS[event.event_type] ?? event.event_type}</Typography>
                    <Typography variant="caption" color="text.secondary">
                      {new Date(event.create_at).toLocaleString("ko-KR")}{eventSummary(event) ? ` · ${eventSummary(event)}` : ""}
                    </Typography>
                  </Box>
                ))}
              </Stack>
            )}
          </Box>
        </Stack>
      </DialogContent>
      <DialogActions><Button onClick={onClose}>닫기</Button></DialogActions>
    </Dialog>
  );
}
