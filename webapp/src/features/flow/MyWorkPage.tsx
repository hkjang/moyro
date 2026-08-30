import ArrowForwardRounded from "@mui/icons-material/ArrowForwardRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import { Alert, Button, Chip, Typography } from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { useNavigate, useParams } from "react-router-dom";
import {
  api,
  compatApi,
  type Post,
  type Reminder,
  type ScheduledPost,
  type User,
} from "@/api/client";
import { workItemsApi, type WorkItem, type WorkItemStatus } from "@/api/work-items";
import type { RootState } from "@/store";
import {
  FlowConfirmDialog,
  FlowEmpty,
  FlowError,
  FlowLoading,
  FlowPage,
  FlowSection,
  FlowStatusBadge,
  FlowTabPanel,
  FlowTabs,
} from "./FlowPage";
import {
  channelPath,
  errorMessage,
  formatDateTime,
  normalizeSavedPosts,
  postNavigationState,
} from "./flow-data";
import { useFlowWorkspaceIndex } from "./FlowDataProvider";

export type MyWorkTab = "tasks" | "decisions" | "saved" | "scheduled" | "reminders";
type WorkItemTab = Extract<MyWorkTab, "tasks" | "decisions">;

type WorkData = {
  saved: Post[];
  scheduled: ScheduledPost[];
  reminders: Reminder[];
  reminderPosts: Record<string, Post>;
  workItems: WorkItem[];
  workItemPosts: Record<string, Post>;
  users: Record<string, User>;
};

type RemovalTarget = {
  kind: MyWorkTab;
  id: string;
  title: string;
};

const EMPTY_DATA: WorkData = {
  saved: [], scheduled: [], reminders: [], reminderPosts: {}, workItems: [], workItemPosts: {}, users: {},
};

const EMPTY_WORK_CURSORS: Record<WorkItemTab, string> = { tasks: "", decisions: "" };

function batches<T>(values: T[], size = 200): T[][] {
  const result: T[][] = [];
  for (let index = 0; index < values.length; index += size) result.push(values.slice(index, index + size));
  return result;
}

async function postsByIDs(token: string, ids: string[]): Promise<Post[]> {
  const unique = [...new Set(ids.filter(Boolean))];
  if (unique.length === 0) return [];
  return (await Promise.all(batches(unique).map((batch) => compatApi.postsByIds(token, batch)))).flat();
}

async function usersByIDs(token: string, ids: string[]): Promise<User[]> {
  const unique = [...new Set(ids.filter(Boolean))];
  if (unique.length === 0) return [];
  return (await Promise.all(batches(unique).map((batch) => compatApi.usersByIds(token, batch)))).flat();
}

function validTab(value: string | undefined): value is MyWorkTab {
  return value === "tasks" || value === "decisions" || value === "saved" || value === "scheduled" || value === "reminders";
}

function scheduledStatus(post: ScheduledPost): { label: string; tone: "default" | "warning" | "error" | "info" } {
  switch (post.status) {
    case "pending": return { label: "예약", tone: "info" };
    case "processing": return { label: "처리 중", tone: "warning" };
    case "retry": return { label: "재시도 대기", tone: "warning" };
    case "dead": return { label: "처리 실패", tone: "error" };
    default: return { label: post.status || "대기", tone: "default" };
  }
}

function workItemStatus(item: WorkItem): { label: string; tone: "default" | "warning" | "error" | "info" | "success" } {
  switch (item.status) {
    case "open": return { label: "할 일", tone: "info" };
    case "in_progress": return { label: "진행 중", tone: "warning" };
    case "done": return { label: "완료", tone: "success" };
    case "recorded": return { label: "기록됨", tone: "success" };
    case "superseded": return { label: "변경됨", tone: "default" };
    case "cancelled": return { label: "취소됨", tone: "error" };
    default: return { label: item.status, tone: "default" };
  }
}

export function MyWorkPage({ initialTab = "tasks" }: { initialTab?: MyWorkTab }) {
  const navigate = useNavigate();
  const params = useParams<{ tab?: string }>();
  const token = useSelector((state: RootState) => state.auth.token);
  const currentUserID = useSelector((state: RootState) => state.auth.user?.id) ?? "";
  const workspace = useFlowWorkspaceIndex();
  const tab = validTab(params.tab) ? params.tab : initialTab;
  const [data, setData] = useState<WorkData>(EMPTY_DATA);
  const [loading, setLoading] = useState(true);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [actionError, setActionError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [removing, setRemoving] = useState(false);
  const [target, setTarget] = useState<RemovalTarget | null>(null);
  const [revision, setRevision] = useState(0);
  const [workCursors, setWorkCursors] = useState<Record<WorkItemTab, string>>(EMPTY_WORK_CURSORS);
  const [loadingMore, setLoadingMore] = useState<WorkItemTab | null>(null);
  const [updatingID, setUpdatingID] = useState("");

  useEffect(() => {
    if (params.tab !== undefined && !validTab(params.tab)) {
      navigate("/my-work/tasks", { replace: true });
    }
  }, [navigate, params.tab]);

  useEffect(() => {
    let active = true;
    if (!token) {
      setData(EMPTY_DATA);
      setWorkCursors(EMPTY_WORK_CURSORS);
      setLoading(false);
      setWarnings(["로그인 세션이 없습니다."]);
      return () => { active = false; };
    }
    setLoading(true);
    void (async () => {
      const [savedResult, scheduledResult, reminderResult, taskResult, decisionResult] = await Promise.allSettled([
        api.listSavedPosts(token, 100, 0),
        api.listMyScheduledPosts(token),
        api.listMyReminders(token),
        workItemsApi.list(token, { kind: "task", perPage: 100 }),
        workItemsApi.list(token, { kind: "decision", perPage: 100 }),
      ] as const);
      if (!active) return;
      const nextWarnings: string[] = [];
      if (savedResult.status === "rejected") nextWarnings.push(`저장한 메시지를 불러오지 못했습니다: ${errorMessage(savedResult.reason, "알 수 없는 오류")}`);
      if (scheduledResult.status === "rejected") nextWarnings.push(`예약 메시지를 불러오지 못했습니다: ${errorMessage(scheduledResult.reason, "알 수 없는 오류")}`);
      if (reminderResult.status === "rejected") nextWarnings.push(`리마인더를 불러오지 못했습니다: ${errorMessage(reminderResult.reason, "알 수 없는 오류")}`);
      if (taskResult.status === "rejected") nextWarnings.push(`작업을 불러오지 못했습니다: ${errorMessage(taskResult.reason, "알 수 없는 오류")}`);
      if (decisionResult.status === "rejected") nextWarnings.push(`결정을 불러오지 못했습니다: ${errorMessage(decisionResult.reason, "알 수 없는 오류")}`);

      const saved = savedResult.status === "fulfilled" ? normalizeSavedPosts(savedResult.value) : [];
      const scheduled = scheduledResult.status === "fulfilled" ? scheduledResult.value : [];
      const reminders = reminderResult.status === "fulfilled" ? reminderResult.value : [];
      const tasks = taskResult.status === "fulfilled" ? taskResult.value.items : [];
      const decisions = decisionResult.status === "fulfilled" ? decisionResult.value.items : [];
      const workItems = [...tasks, ...decisions];
      let reminderPosts: Record<string, Post> = {};
      let workItemPosts: Record<string, Post> = {};
      const reminderPostIDs = reminders.map((item) => item.post_id);
      const workPostIDs = workItems.map((item) => item.source_post_id).filter((id): id is string => Boolean(id));
      const sourcePostIDs = [...new Set([...reminderPostIDs, ...workPostIDs])];
      if (sourcePostIDs.length > 0) {
        try {
          const posts = await postsByIDs(token, sourcePostIDs);
          const byID = Object.fromEntries(posts.map((post) => [post.id, post]));
          reminderPosts = Object.fromEntries(reminderPostIDs.flatMap((id) => byID[id] ? [[id, byID[id]]] : []));
          workItemPosts = Object.fromEntries(workPostIDs.flatMap((id) => byID[id] ? [[id, byID[id]]] : []));
        } catch (error) {
          nextWarnings.push(`업무 원문을 불러오지 못했습니다: ${errorMessage(error, "알 수 없는 오류")}`);
        }
      }
      let users: Record<string, User> = {};
      const authorIds = [...new Set([
        ...[...saved, ...Object.values(reminderPosts), ...Object.values(workItemPosts)].map((post) => post.user_id),
        ...workItems.flatMap((item) => [item.created_by, item.assignee_id ?? ""]),
      ].filter(Boolean))];
      if (authorIds.length > 0) {
        try {
          const rows = await usersByIDs(token, authorIds);
          users = Object.fromEntries(rows.map((user) => [user.id, user]));
        } catch (error) {
          nextWarnings.push(`메시지 작성자 정보를 불러오지 못했습니다: ${errorMessage(error, "알 수 없는 오류")}`);
        }
      }
      if (!active) return;
      setData({ saved, scheduled, reminders, reminderPosts, workItems, workItemPosts, users });
      setWorkCursors({
        tasks: taskResult.status === "fulfilled" ? taskResult.value.next_cursor ?? "" : "",
        decisions: decisionResult.status === "fulfilled" ? decisionResult.value.next_cursor ?? "" : "",
      });
      setWarnings(nextWarnings);
      setLoading(false);
    })();
    return () => { active = false; };
  }, [revision, token, workspace.workItemRevision]);

  const tabOptions = useMemo(() => [
    { value: "tasks" as const, label: "내 작업", count: data.workItems.filter((item) => item.kind === "task").length },
    { value: "decisions" as const, label: "내 결정", count: data.workItems.filter((item) => item.kind === "decision").length },
    { value: "saved" as const, label: "저장한 메시지", count: data.saved.length },
    { value: "scheduled" as const, label: "예약 메시지", count: data.scheduled.length },
    { value: "reminders" as const, label: "리마인더", count: data.reminders.length },
  ], [data.reminders.length, data.saved.length, data.scheduled.length, data.workItems]);

  async function removeTarget() {
    if (!token || !target) return;
    setRemoving(true);
    setActionError("");
    setFeedback("");
    try {
      if (target.kind === "saved") {
        await api.unsavePost(token, target.id);
        setData((current) => ({ ...current, saved: current.saved.filter((post) => post.id !== target.id) }));
        setFeedback("메시지 저장을 해제했습니다.");
      } else if (target.kind === "scheduled") {
        await api.deleteScheduledPost(token, target.id);
        setData((current) => ({ ...current, scheduled: current.scheduled.filter((post) => post.id !== target.id) }));
        setFeedback("예약 메시지를 취소했습니다.");
      } else if (target.kind === "reminders") {
        await api.deleteReminder(token, target.id);
        setData((current) => ({ ...current, reminders: current.reminders.filter((item) => item.id !== target.id) }));
        setFeedback("리마인더를 취소했습니다.");
      } else {
        await workItemsApi.remove(token, target.id);
        setData((current) => ({ ...current, workItems: current.workItems.filter((item) => item.id !== target.id) }));
        setFeedback(target.kind === "tasks" ? "작업을 삭제했습니다." : "결정 기록을 삭제했습니다.");
      }
      setTarget(null);
    } catch (error) {
      setActionError(errorMessage(error, "요청을 처리하지 못했습니다."));
    } finally {
      setRemoving(false);
    }
  }

  const refreshAll = () => {
    setFeedback("");
    setActionError("");
    workspace.refresh();
    setRevision((current) => current + 1);
  };

  async function loadMoreWorkItems(kind: WorkItem["kind"]) {
    if (!token) return;
    const targetTab: WorkItemTab = kind === "task" ? "tasks" : "decisions";
    const cursor = workCursors[targetTab];
    if (!cursor || loadingMore) return;
    setLoadingMore(targetTab);
    setActionError("");
    try {
      const page = await workItemsApi.list(token, { kind, cursor, perPage: 100 });
      const sourceIDs = page.items.map((item) => item.source_post_id).filter((id): id is string => Boolean(id));
      const posts = await postsByIDs(token, sourceIDs);
      const users = await usersByIDs(token, [
        ...page.items.flatMap((item) => [item.created_by, item.assignee_id ?? ""]),
        ...posts.map((post) => post.user_id),
      ]);
      setData((current) => {
        const existing = new Set(current.workItems.map((item) => item.id));
        return {
          ...current,
          workItems: [...current.workItems, ...page.items.filter((item) => !existing.has(item.id))],
          workItemPosts: { ...current.workItemPosts, ...Object.fromEntries(posts.map((post) => [post.id, post])) },
          users: { ...current.users, ...Object.fromEntries(users.map((user) => [user.id, user])) },
        };
      });
      setWorkCursors((current) => ({ ...current, [targetTab]: page.next_cursor ?? "" }));
    } catch (error) {
      setActionError(errorMessage(error, "업무를 더 불러오지 못했습니다."));
    } finally {
      setLoadingMore(null);
    }
  }

  async function updateWorkItemStatus(item: WorkItem, status: WorkItemStatus) {
    if (!token || updatingID) return;
    setUpdatingID(item.id);
    setActionError("");
    setFeedback("");
    try {
      const updated = await workItemsApi.patch(token, item.id, { status });
      setData((current) => ({
        ...current,
        workItems: current.workItems.map((candidate) => candidate.id === updated.id ? updated : candidate),
      }));
      setFeedback(item.kind === "task" ? "작업 상태를 변경했습니다." : "결정 상태를 변경했습니다.");
    } catch (error) {
      setActionError(errorMessage(error, "업무 상태를 변경하지 못했습니다."));
    } finally {
      setUpdatingID("");
    }
  }

  return (
    <FlowPage
      eyebrow="개인 흐름"
      title="내 업무"
      description="대화에서 만든 작업과 결정, 저장한 메시지, 예약 메시지와 리마인더를 한곳에서 관리합니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={refreshAll} disabled={loading}>새로고침</Button>}
    >
      <FlowTabs idPrefix="my-work" label="내 업무 분류" value={tab} options={tabOptions} onChange={(value) => navigate(`/my-work/${value}`)} />
      {workspace.error && <Alert severity="warning">채널 정보를 불러오지 못했습니다: {workspace.error}</Alert>}
      {actionError && <FlowError message={actionError} />}
      {feedback && <Alert severity="success" role="status" aria-live="polite">{feedback}</Alert>}
      {[...workspace.warnings, ...warnings].map((warning) => <Alert severity="warning" key={warning}>{warning}</Alert>)}

      <FlowTabPanel idPrefix="my-work" value="tasks" active={tab === "tasks"}>
        {tab === "tasks" && (
          <FlowSection title="내 작업" description="메시지에서 만든 담당자·마감·상태가 있는 작업입니다." id="work-tasks">
          {loading ? <FlowLoading /> : data.workItems.filter((item) => item.kind === "task").length === 0 ? (
            <FlowEmpty title="내 작업이 없습니다" description="메시지의 더보기 메뉴에서 작업으로 만들 수 있습니다." />
          ) : (
            <div className="flow-list">
              {data.workItems.filter((item) => item.kind === "task").map((item) => {
                const source = item.source_post_id ? data.workItemPosts[item.source_post_id] : undefined;
                const entry = workspace.channelById[item.channel_id];
                const assignee = item.assignee_id ? data.users[item.assignee_id] : undefined;
                const status = workItemStatus(item);
                const canUpdate = item.created_by === currentUserID || item.assignee_id === currentUserID;
                return (
                  <article className="flow-list-row" key={item.id}>
                    <div className="flow-list-main">
                      <div className="flow-badges">
                        <Typography component="h3" className="flow-item-title">{item.title}</Typography>
                        <FlowStatusBadge label={status.label} tone={status.tone} />
                        {entry && <Chip size="small" variant="outlined" label={entry.channel.display_name || entry.channel.name} />}
                      </div>
                      {item.description && <Typography className="flow-item-message">{item.description}</Typography>}
                      <Typography className="flow-item-subtitle">
                        {assignee ? `담당 @${assignee.username}` : "담당자 없음"}
                        {item.due_at > 0 ? ` · 마감 ${formatDateTime(item.due_at)}` : ""}
                        {source ? ` · 원문 ${formatDateTime(source.create_at)}` : ""}
                      </Typography>
                    </div>
                    <div className="flow-list-actions">
                      {entry && source && <Button onClick={() => navigate(channelPath(entry), { state: postNavigationState(source.id) })}>원문 메시지</Button>}
                      {canUpdate && item.status === "open" && <Button variant="outlined" disabled={Boolean(updatingID)} onClick={() => void updateWorkItemStatus(item, "in_progress")}>시작</Button>}
                      {canUpdate && item.status !== "done" && item.status !== "cancelled" && <Button variant="outlined" disabled={Boolean(updatingID)} onClick={() => void updateWorkItemStatus(item, "done")}>완료</Button>}
                      {canUpdate && item.status === "done" && <Button variant="outlined" disabled={Boolean(updatingID)} onClick={() => void updateWorkItemStatus(item, "open")}>다시 열기</Button>}
                      {item.created_by === currentUserID && <Button color="error" variant="outlined" startIcon={<DeleteOutlineRounded />} disabled={Boolean(updatingID)} onClick={() => setTarget({ kind: "tasks", id: item.id, title: "작업을 삭제할까요?" })}>삭제</Button>}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          {!loading && workCursors.tasks && (
            <Button disabled={Boolean(loadingMore)} onClick={() => void loadMoreWorkItems("task")}>
              {loadingMore === "tasks" ? "작업을 불러오는 중…" : "작업 더 불러오기"}
            </Button>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowTabPanel idPrefix="my-work" value="decisions" active={tab === "decisions"}>
        {tab === "decisions" && (
          <FlowSection title="내 결정" description="대화의 공식 결정과 변경 이력을 원본 메시지에 연결합니다." id="work-decisions">
          {loading ? <FlowLoading /> : data.workItems.filter((item) => item.kind === "decision").length === 0 ? (
            <FlowEmpty title="기록된 결정이 없습니다" description="메시지의 더보기 메뉴에서 결정으로 기록할 수 있습니다." />
          ) : (
            <div className="flow-list">
              {data.workItems.filter((item) => item.kind === "decision").map((item) => {
                const source = item.source_post_id ? data.workItemPosts[item.source_post_id] : undefined;
                const entry = workspace.channelById[item.channel_id];
                const author = data.users[item.created_by];
                const status = workItemStatus(item);
                return (
                  <article className="flow-list-row" key={item.id}>
                    <div className="flow-list-main">
                      <div className="flow-badges">
                        <Typography component="h3" className="flow-item-title">{item.title}</Typography>
                        <FlowStatusBadge label={status.label} tone={status.tone} />
                        {entry && <Chip size="small" variant="outlined" label={entry.channel.display_name || entry.channel.name} />}
                      </div>
                      {item.description && <Typography className="flow-item-message">{item.description}</Typography>}
                      <Typography className="flow-item-subtitle">
                        {author ? `기록 @${author.username}` : "기록자 정보 없음"} · {formatDateTime(item.decided_at || item.create_at)}
                      </Typography>
                    </div>
                    <div className="flow-list-actions">
                      {entry && source && <Button onClick={() => navigate(channelPath(entry), { state: postNavigationState(source.id) })}>근거 메시지</Button>}
                      {item.created_by === currentUserID && item.status === "recorded" && <Button variant="outlined" disabled={Boolean(updatingID)} onClick={() => void updateWorkItemStatus(item, "superseded")}>변경됨으로 표시</Button>}
                      {item.created_by === currentUserID && item.status === "superseded" && <Button variant="outlined" disabled={Boolean(updatingID)} onClick={() => void updateWorkItemStatus(item, "recorded")}>다시 유효하게</Button>}
                      {item.created_by === currentUserID && <Button color="error" variant="outlined" startIcon={<DeleteOutlineRounded />} disabled={Boolean(updatingID)} onClick={() => setTarget({ kind: "decisions", id: item.id, title: "결정 기록을 삭제할까요?" })}>삭제</Button>}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          {!loading && workCursors.decisions && (
            <Button disabled={Boolean(loadingMore)} onClick={() => void loadMoreWorkItems("decision")}>
              {loadingMore === "decisions" ? "결정을 불러오는 중…" : "결정 더 불러오기"}
            </Button>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowTabPanel idPrefix="my-work" value="saved" active={tab === "saved"}>
        {tab === "saved" && (
          <FlowSection title="저장한 메시지" description="최근 최대 100개입니다. 해제한 항목은 내 저장 목록에서 제거됩니다." id="work-saved">
          {loading ? <FlowLoading /> : data.saved.length === 0 ? (
            <FlowEmpty title="저장한 메시지가 없습니다" description="대화의 저장 액션으로 나중에 확인할 메시지를 모을 수 있습니다." />
          ) : (
            <div className="flow-list">
              {data.saved.map((post) => {
                const entry = workspace.channelById[post.channel_id];
                const author = data.users[post.user_id];
                return (
                  <article className="flow-list-row" key={post.id}>
                    <div className="flow-list-main">
                      <div className="flow-badges">
                        <Typography component="h3" className="flow-item-title">{author ? `@${author.username}` : "작성자 정보 없음"}</Typography>
                        {entry && <Chip size="small" variant="outlined" label={entry.channel.display_name || entry.channel.name} />}
                      </div>
                      <Typography className="flow-item-message">{post.message || "내용 없는 메시지"}</Typography>
                      <Typography className="flow-item-subtitle">{formatDateTime(post.create_at)}</Typography>
                    </div>
                    <div className="flow-list-actions">
                      {entry && <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate(channelPath(entry), { state: postNavigationState(post.id) })}>메시지 열기</Button>}
                      <Button color="error" variant="outlined" startIcon={<DeleteOutlineRounded />} onClick={() => setTarget({ kind: "saved", id: post.id, title: "메시지 저장을 해제할까요?" })}>저장 해제</Button>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowTabPanel idPrefix="my-work" value="scheduled" active={tab === "scheduled"}>
        {tab === "scheduled" && (
          <FlowSection title="예약 메시지" description="대기 또는 재시도 중인 예약 메시지를 확인하고 취소합니다." id="work-scheduled">
          {loading ? <FlowLoading /> : data.scheduled.length === 0 ? (
            <FlowEmpty title="예약 메시지가 없습니다" description="작성기에서 예약 전송을 설정하면 이 목록에서 확인하고 취소할 수 있습니다." />
          ) : (
            <div className="flow-list">
              {data.scheduled.map((post) => {
                const entry = workspace.channelById[post.channel_id];
                const status = scheduledStatus(post);
                const canCancel = post.status === "pending" || post.status === "retry";
                return (
                  <article className="flow-list-row" key={post.id}>
                    <div className="flow-list-main">
                      <div className="flow-badges">
                        <FlowStatusBadge label={status.label} tone={status.tone} />
                        {entry && <Chip size="small" variant="outlined" label={entry.channel.display_name || entry.channel.name} />}
                      </div>
                      <Typography className="flow-item-message">{post.message || "내용 없는 예약 메시지"}</Typography>
                      <Typography className="flow-item-subtitle">전송 예정 {formatDateTime(post.send_at)}{post.attempt_count > 0 ? ` · 시도 ${post.attempt_count}회` : ""}</Typography>
                      {(post.last_error_text || post.error_text) && <Typography className="flow-item-subtitle" color="error">최근 오류: {post.last_error_text || post.error_text}</Typography>}
                    </div>
                    <div className="flow-list-actions">
                      {entry && <Button onClick={() => navigate(channelPath(entry))}>채널 열기</Button>}
                      <Button
                        color="error"
                        variant="outlined"
                        startIcon={<DeleteOutlineRounded />}
                        disabled={!canCancel}
                        onClick={() => setTarget({ kind: "scheduled", id: post.id, title: "예약 메시지를 취소할까요?" })}
                      >예약 취소</Button>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowTabPanel idPrefix="my-work" value="reminders" active={tab === "reminders"}>
        {tab === "reminders" && (
          <FlowSection title="리마인더" description="전달 전인 메시지 리마인더를 확인하고 취소합니다." id="work-reminders">
          {loading ? <FlowLoading /> : data.reminders.length === 0 ? (
            <FlowEmpty title="리마인더가 없습니다" description="메시지의 리마인더 액션으로 다시 확인할 시간을 정할 수 있습니다." />
          ) : (
            <div className="flow-list">
              {data.reminders.map((reminder) => {
                const post = data.reminderPosts[reminder.post_id];
                const entry = post ? workspace.channelById[post.channel_id] : undefined;
                const author = post ? data.users[post.user_id] : undefined;
                return (
                  <article className="flow-list-row" key={reminder.id}>
                    <div className="flow-list-main">
                      <div className="flow-badges">
                        <FlowStatusBadge label="예정" tone="info" />
                        {author && <Chip size="small" variant="outlined" label={`@${author.username}`} />}
                      </div>
                      <Typography className="flow-item-message">{post?.message || "원문을 불러올 수 없는 메시지"}</Typography>
                      <Typography className="flow-item-subtitle">알림 {formatDateTime(reminder.remind_at)}</Typography>
                    </div>
                    <div className="flow-list-actions">
                      {entry && post && <Button onClick={() => navigate(channelPath(entry), { state: postNavigationState(post.id) })}>원문 메시지</Button>}
                      <Button color="error" variant="outlined" startIcon={<DeleteOutlineRounded />} onClick={() => setTarget({ kind: "reminders", id: reminder.id, title: "리마인더를 취소할까요?" })}>리마인더 취소</Button>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowConfirmDialog
        open={Boolean(target)}
        title={target?.title ?? "항목을 변경할까요?"}
        description="변경 내용은 내 업무 목록에 즉시 반영됩니다."
        confirmLabel={target?.kind === "saved" ? "저장 해제" : target?.kind === "scheduled" || target?.kind === "reminders" ? "취소" : "삭제"}
        destructive
        busy={removing}
        onCancel={() => setTarget(null)}
        onConfirm={() => void removeTarget()}
      />
    </FlowPage>
  );
}
