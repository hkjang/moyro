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
import type { RootState } from "@/store";
import {
  FlowConfirmDialog,
  FlowEmpty,
  FlowError,
  FlowLoading,
  FlowPage,
  FlowPrepared,
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
  useFlowWorkspaceIndex,
} from "./flow-data";

export type MyWorkTab = "saved" | "scheduled" | "reminders";

type WorkData = {
  saved: Post[];
  scheduled: ScheduledPost[];
  reminders: Reminder[];
  reminderPosts: Record<string, Post>;
  users: Record<string, User>;
};

type RemovalTarget = {
  kind: MyWorkTab;
  id: string;
  title: string;
};

const EMPTY_DATA: WorkData = { saved: [], scheduled: [], reminders: [], reminderPosts: {}, users: {} };

function validTab(value: string | undefined): value is MyWorkTab {
  return value === "saved" || value === "scheduled" || value === "reminders";
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

export function MyWorkPage({ initialTab = "saved" }: { initialTab?: MyWorkTab }) {
  const navigate = useNavigate();
  const params = useParams<{ tab?: string }>();
  const token = useSelector((state: RootState) => state.auth.token);
  const workspace = useFlowWorkspaceIndex(token);
  const tab = validTab(params.tab) ? params.tab : initialTab;
  const [data, setData] = useState<WorkData>(EMPTY_DATA);
  const [loading, setLoading] = useState(true);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [actionError, setActionError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [removing, setRemoving] = useState(false);
  const [target, setTarget] = useState<RemovalTarget | null>(null);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (params.tab !== undefined && !validTab(params.tab)) {
      navigate("/my-work/saved", { replace: true });
    }
  }, [navigate, params.tab]);

  useEffect(() => {
    let active = true;
    if (!token) {
      setData(EMPTY_DATA);
      setLoading(false);
      setWarnings(["로그인 세션이 없습니다."]);
      return () => { active = false; };
    }
    setLoading(true);
    void (async () => {
      const [savedResult, scheduledResult, reminderResult] = await Promise.allSettled([
        api.listSavedPosts(token, 100, 0),
        api.listMyScheduledPosts(token),
        api.listMyReminders(token),
      ] as const);
      if (!active) return;
      const nextWarnings: string[] = [];
      if (savedResult.status === "rejected") nextWarnings.push(`저장한 메시지를 불러오지 못했습니다: ${errorMessage(savedResult.reason, "알 수 없는 오류")}`);
      if (scheduledResult.status === "rejected") nextWarnings.push(`예약 메시지를 불러오지 못했습니다: ${errorMessage(scheduledResult.reason, "알 수 없는 오류")}`);
      if (reminderResult.status === "rejected") nextWarnings.push(`리마인더를 불러오지 못했습니다: ${errorMessage(reminderResult.reason, "알 수 없는 오류")}`);

      const saved = savedResult.status === "fulfilled" ? normalizeSavedPosts(savedResult.value) : [];
      const scheduled = scheduledResult.status === "fulfilled" ? scheduledResult.value : [];
      const reminders = reminderResult.status === "fulfilled" ? reminderResult.value : [];
      let reminderPosts: Record<string, Post> = {};
      if (reminders.length > 0) {
        try {
          const posts = await compatApi.postsByIds(token, [...new Set(reminders.map((item) => item.post_id))]);
          reminderPosts = Object.fromEntries(posts.map((post) => [post.id, post]));
        } catch (error) {
          nextWarnings.push(`리마인더 원문을 불러오지 못했습니다: ${errorMessage(error, "알 수 없는 오류")}`);
        }
      }
      let users: Record<string, User> = {};
      const authorIds = [...new Set([...saved, ...Object.values(reminderPosts)].map((post) => post.user_id).filter(Boolean))];
      if (authorIds.length > 0) {
        try {
          const rows = await compatApi.usersByIds(token, authorIds);
          users = Object.fromEntries(rows.map((user) => [user.id, user]));
        } catch (error) {
          nextWarnings.push(`메시지 작성자 정보를 불러오지 못했습니다: ${errorMessage(error, "알 수 없는 오류")}`);
        }
      }
      if (!active) return;
      setData({ saved, scheduled, reminders, reminderPosts, users });
      setWarnings(nextWarnings);
      setLoading(false);
    })();
    return () => { active = false; };
  }, [revision, token]);

  const tabOptions = useMemo(() => [
    { value: "saved" as const, label: "저장한 메시지", count: data.saved.length },
    { value: "scheduled" as const, label: "예약 메시지", count: data.scheduled.length },
    { value: "reminders" as const, label: "리마인더", count: data.reminders.length },
  ], [data.reminders.length, data.saved.length, data.scheduled.length]);

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
      } else {
        await api.deleteReminder(token, target.id);
        setData((current) => ({ ...current, reminders: current.reminders.filter((item) => item.id !== target.id) }));
        setFeedback("리마인더를 취소했습니다.");
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

  return (
    <FlowPage
      eyebrow="개인 흐름"
      title="내 업무"
      description="저장한 메시지, 예약 메시지, 리마인더를 실제 계정 데이터로 관리합니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={refreshAll} disabled={loading}>새로고침</Button>}
    >
      <FlowTabs idPrefix="my-work" label="내 업무 분류" value={tab} options={tabOptions} onChange={(value) => navigate(`/my-work/${value}`)} />
      {workspace.error && <Alert severity="warning">채널 정보를 불러오지 못했습니다: {workspace.error}</Alert>}
      {actionError && <FlowError message={actionError} />}
      {feedback && <Alert severity="success" role="status" aria-live="polite">{feedback}</Alert>}
      {[...workspace.warnings, ...warnings].map((warning) => <Alert severity="warning" key={warning}>{warning}</Alert>)}

      <FlowTabPanel idPrefix="my-work" value="saved" active={tab === "saved"}>
        {tab === "saved" && (
          <FlowSection title="저장한 메시지" description="최근 최대 100개입니다. 해제하면 서버의 개인 저장 상태가 변경됩니다." id="work-saved">
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
          <FlowSection title="예약 메시지" description="대기 또는 재시도 중인 예약만 서버에서 반환됩니다." id="work-scheduled">
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

      <FlowSection title="확장 예정" id="work-prepared">
        <div className="flow-card-grid">
          <FlowPrepared title="폴더·태그·개인 메모" description="개인 메타데이터 저장 API가 준비되기 전에는 브라우저에 임시 상태를 만들지 않습니다." />
          <FlowPrepared title="내 작업·내 결정" description="작업과 결정의 영속 모델이 준비되면 메시지 출처와 상태를 연결합니다." />
        </div>
      </FlowSection>

      <FlowConfirmDialog
        open={Boolean(target)}
        title={target?.title ?? "항목을 변경할까요?"}
        description="이 작업은 서버의 개인 업무 상태에 즉시 반영됩니다."
        confirmLabel={target?.kind === "saved" ? "저장 해제" : "취소"}
        destructive
        busy={removing}
        onCancel={() => setTarget(null)}
        onConfirm={() => void removeTarget()}
      />
    </FlowPage>
  );
}
