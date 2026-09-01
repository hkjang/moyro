import CalendarMonthRounded from "@mui/icons-material/CalendarMonthRounded";
import DashboardRounded from "@mui/icons-material/DashboardRounded";
import FormatListBulletedRounded from "@mui/icons-material/FormatListBulletedRounded";
import TimelineRounded from "@mui/icons-material/TimelineRounded";
import { Box, ToggleButton, ToggleButtonGroup, Typography } from "@mui/material";
import { useMemo, useState, type ReactNode } from "react";
import type { WorkItem } from "@/api/work-items";

export type WorkManagementViewMode = "list" | "board" | "calendar" | "timeline";

const BOARD_COLUMNS = [
  { status: "open", label: "할 일" },
  { status: "in_progress", label: "진행 중" },
  { status: "done", label: "완료" },
  { status: "cancelled", label: "취소" },
] as const;

function dateKey(timestamp: number): string {
  if (!timestamp) return "마감 없음";
  return new Intl.DateTimeFormat("ko-KR", { year: "numeric", month: "long", day: "numeric" }).format(timestamp);
}

export function groupTasksByDueDate(items: WorkItem[]): Array<{ label: string; items: WorkItem[] }> {
  const sorted = [...items].sort((left, right) => {
    if (!left.due_at) return 1;
    if (!right.due_at) return -1;
    return left.due_at - right.due_at || left.id.localeCompare(right.id);
  });
  const groups = new Map<string, WorkItem[]>();
  for (const item of sorted) {
    const label = dateKey(item.due_at);
    groups.set(label, [...(groups.get(label) ?? []), item]);
  }
  return [...groups].map(([label, grouped]) => ({ label, items: grouped }));
}

export function WorkManagementView({ items, renderItem }: {
  items: WorkItem[];
  renderItem: (item: WorkItem) => ReactNode;
}) {
  const [mode, setMode] = useState<WorkManagementViewMode>("list");
  const dueGroups = useMemo(() => groupTasksByDueDate(items), [items]);
  const timeline = useMemo(() => [...items].sort((left, right) =>
    (left.due_at || Number.MAX_SAFE_INTEGER) - (right.due_at || Number.MAX_SAFE_INTEGER) || left.create_at - right.create_at), [items]);

  return (
    <Box sx={{ display: "grid", gap: 1.5 }}>
      <ToggleButtonGroup
        exclusive
        size="small"
        value={mode}
        onChange={(_, value: WorkManagementViewMode | null) => { if (value) setMode(value); }}
        aria-label="작업 보기 방식"
      >
        <ToggleButton value="list"><FormatListBulletedRounded fontSize="small" /> 목록</ToggleButton>
        <ToggleButton value="board"><DashboardRounded fontSize="small" /> 보드</ToggleButton>
        <ToggleButton value="calendar"><CalendarMonthRounded fontSize="small" /> 캘린더</ToggleButton>
        <ToggleButton value="timeline"><TimelineRounded fontSize="small" /> 타임라인</ToggleButton>
      </ToggleButtonGroup>
      {mode === "list" && <div className="flow-list">{items.map(renderItem)}</div>}
      {mode === "board" && (
        <Box sx={{ display: "grid", gridTemplateColumns: { xs: "1fr", lg: "repeat(4, minmax(0, 1fr))" }, gap: 1.5, alignItems: "start" }}>
          {BOARD_COLUMNS.map((column) => {
            const rows = items.filter((item) => item.status === column.status);
            return (
              <Box key={column.status} sx={{ p: 1.5, bgcolor: "action.hover", borderRadius: 2, minWidth: 0 }}>
                <Typography component="h3" variant="subtitle2" sx={{ mb: 1 }}>{column.label} · {rows.length}</Typography>
                <Box sx={{ display: "grid", gap: 1 }}>{rows.map(renderItem)}</Box>
              </Box>
            );
          })}
        </Box>
      )}
      {mode === "calendar" && (
        <Box sx={{ display: "grid", gap: 2 }}>
          {dueGroups.map((group) => <Box key={group.label}><Typography component="h3" variant="subtitle2" sx={{ mb: 1 }}>{group.label}</Typography><div className="flow-list">{group.items.map(renderItem)}</div></Box>)}
        </Box>
      )}
      {mode === "timeline" && (
        <Box sx={{ display: "grid", gap: 1, borderLeft: 2, borderColor: "divider", pl: 2 }}>
          {timeline.map((item) => <Box key={item.id} sx={{ position: "relative", "&::before": { content: '""', position: "absolute", left: -21, top: 18, width: 8, height: 8, borderRadius: "50%", bgcolor: "primary.main" } }}>{renderItem(item)}</Box>)}
        </Box>
      )}
    </Box>
  );
}
