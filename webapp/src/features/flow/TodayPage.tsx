import ArrowForwardRounded from "@mui/icons-material/ArrowForwardRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import { Alert, Button, Chip, Typography } from "@mui/material";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { useNavigate } from "react-router-dom";
import {
  api,
  moyroMeApi,
  moyroReviewApi,
  type ApprovalRequest,
  type Post,
  type Reminder,
  type ScheduledPost,
} from "@/api/client";
import type { RootState } from "@/store";
import {
  FlowCard,
  FlowEmpty,
  FlowError,
  FlowLoading,
  FlowMetric,
  FlowPage,
  FlowPrepared,
  FlowSection,
  FlowStatusBadge,
} from "./FlowPage";
import {
  channelPath,
  errorMessage,
  formatRelativeTime,
  isToday,
  normalizeSavedPosts,
  useFlowWorkspaceIndex,
} from "./flow-data";
import { TodayBriefing } from "./TodayBriefing";

type TodayData = {
  saved: Post[];
  scheduled: ScheduledPost[];
  reminders: Reminder[];
  mine: ApprovalRequest[];
  review: ApprovalRequest[];
};

const EMPTY_DATA: TodayData = { saved: [], scheduled: [], reminders: [], mine: [], review: [] };

function isActionableApproval(request: ApprovalRequest): boolean {
  return request.status === "pending" && (request.expires_at <= 0 || request.expires_at > Date.now());
}

export function TodayPage() {
  const navigate = useNavigate();
  const token = useSelector((state: RootState) => state.auth.token);
  const user = useSelector((state: RootState) => state.auth.user);
  const workspace = useFlowWorkspaceIndex(token);
  const [data, setData] = useState<TodayData>(EMPTY_DATA);
  const [loading, setLoading] = useState(true);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [revision, setRevision] = useState(0);

  const load = useCallback(() => setRevision((current) => current + 1), []);

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
      const results = await Promise.allSettled([
        api.listSavedPosts(token, 100, 0),
        api.listMyScheduledPosts(token),
        api.listMyReminders(token),
        moyroMeApi.listApprovalRequests(token),
        // The review endpoint applies team-scoped reviewer authorization itself.
        moyroReviewApi.listApprovalRequests(token, "pending"),
      ] as const);
      if (!active) return;
      const nextWarnings: string[] = [];
      const labels = ["저장한 메시지", "예약 메시지", "리마인더", "내 승인 요청", "검토 대기"];
      results.forEach((result, index) => {
        if (result.status === "rejected") {
          nextWarnings.push(`${labels[index]}을(를) 불러오지 못했습니다: ${errorMessage(result.reason, "알 수 없는 오류")}`);
        }
      });
      setData({
        saved: results[0].status === "fulfilled" ? normalizeSavedPosts(results[0].value) : [],
        scheduled: results[1].status === "fulfilled" ? results[1].value : [],
        reminders: results[2].status === "fulfilled" ? results[2].value : [],
        mine: results[3].status === "fulfilled" ? results[3].value : [],
        review: results[4].status === "fulfilled" ? results[4].value : [],
      });
      setWarnings(nextWarnings);
      setLoading(false);
    })();
    return () => { active = false; };
  }, [revision, token]);

  const unreadEntries = useMemo(
    () => workspace.entries
      .filter((entry) => (entry.membership?.msg_count ?? 0) > 0 || (entry.membership?.mention_count ?? 0) > 0)
      .sort((left, right) => {
        const mentionDelta = (right.membership?.mention_count ?? 0) - (left.membership?.mention_count ?? 0);
        return mentionDelta || (right.membership?.msg_count ?? 0) - (left.membership?.msg_count ?? 0);
      }),
    [workspace.entries],
  );
  const unreadCount = unreadEntries.reduce((sum, entry) => sum + (entry.membership?.msg_count ?? 0), 0);
  const mentionCount = unreadEntries.reduce((sum, entry) => sum + (entry.membership?.mention_count ?? 0), 0);
  const dueReminders = data.reminders.filter((reminder) => reminder.delivered_at <= 0 && isToday(reminder.remind_at));
  const pendingScheduled = data.scheduled.filter((post) => post.status === "pending" || post.status === "retry" || post.status === "processing");
  const pendingMine = data.mine.filter(isActionableApproval);
  const pendingReview = data.review.filter(isActionableApproval);

  const refreshAll = () => {
    workspace.refresh();
    load();
  };

  return (
    <FlowPage
      eyebrow="Moyro Flow"
      title={`${user?.username ?? "사용자"}님, 오늘의 흐름입니다`}
      description={new Intl.DateTimeFormat("ko-KR", { dateStyle: "full" }).format(new Date()) + " · 실제 대화와 개인 업무 상태를 한곳에서 확인합니다."}
      actions={<Button startIcon={<RefreshRounded />} onClick={refreshAll} disabled={loading || workspace.loading}>새로고침</Button>}
    >
      {workspace.error && <FlowError message={workspace.error} onRetry={workspace.refresh} />}
      {[...workspace.warnings, ...warnings].map((warning) => <Alert severity="warning" key={warning}>{warning}</Alert>)}

      <section aria-label="오늘 요약" className="flow-metric-grid">
        <FlowMetric label="읽지 않은 메시지" value={unreadCount} detail={`${unreadEntries.length}개 대화`} tone="brand" onClick={() => navigate("/inbox")} />
        <FlowMetric label="멘션" value={mentionCount} detail="채널 단위 실제 카운터" tone="warning" onClick={() => navigate("/inbox")} />
        <FlowMetric label="오늘 리마인더" value={dueReminders.length} detail={`${data.reminders.length}개 예정`} tone="success" onClick={() => navigate("/my-work/reminders")} />
        <FlowMetric label="검토할 승인" value={pendingReview.length} detail={`내 요청 대기 ${pendingMine.length}개`} tone="ai" onClick={() => navigate("/approvals/review")} />
      </section>

      <FlowSection title="AI 브리핑" description="명시적으로 요청할 때만 읽지 않은 실제 메시지를 제한적으로 사용합니다." id="today-briefing">
        <TodayBriefing
          unreadEntries={unreadEntries}
          workspaceLoading={workspace.loading}
          username={user?.username ?? "사용자"}
        />
      </FlowSection>

      <FlowSection title="먼저 볼 대화" description="서버에 저장된 채널별 읽지 않음·멘션 카운터입니다." id="today-unreads">
        {workspace.loading ? <FlowLoading label="읽지 않은 대화를 불러오는 중…" /> : unreadEntries.length === 0 ? (
          <FlowEmpty title="읽지 않은 대화가 없습니다" description="새 메시지가 도착하면 채널별 실제 카운터가 여기에 표시됩니다." />
        ) : (
          <div className="flow-list">
            {unreadEntries.slice(0, 6).map((entry) => (
              <article className="flow-list-row" key={entry.channel.id}>
                <div className="flow-list-main">
                  <div className="flow-badges">
                    <Typography component="h3" className="flow-item-title">{entry.channel.display_name || entry.channel.name}</Typography>
                    {(entry.membership?.mention_count ?? 0) > 0 && <FlowStatusBadge label={`멘션 ${entry.membership?.mention_count}`} tone="warning" />}
                    <Chip size="small" variant="outlined" label={`읽지 않음 ${entry.membership?.msg_count ?? 0}`} />
                  </div>
                  <Typography className="flow-item-subtitle">{entry.team.display_name} · 마지막 확인 {formatRelativeTime(entry.membership?.last_viewed_at ?? 0)}</Typography>
                </div>
                <div className="flow-list-actions">
                  <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate(channelPath(entry))}>대화 열기</Button>
                </div>
              </article>
            ))}
            {unreadEntries.length > 6 && <Button onClick={() => navigate("/inbox")}>나머지 {unreadEntries.length - 6}개 대화 보기</Button>}
          </div>
        )}
      </FlowSection>

      <FlowSection title="내 업무" description="저장, 예약, 리마인더, 승인의 실제 서버 상태입니다." id="today-work">
        {loading ? <FlowLoading label="개인 업무를 불러오는 중…" /> : (
          <div className="flow-card-grid">
            <FlowCard>
              <Typography component="h3" className="flow-item-title">저장한 메시지</Typography>
              <Typography className="flow-metric-value">{data.saved.length}{data.saved.length >= 100 ? "+" : ""}</Typography>
              <Typography className="flow-item-subtitle">최근 최대 100개를 집계했습니다.</Typography>
              <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate("/my-work/saved")}>확인하기</Button>
            </FlowCard>
            <FlowCard>
              <Typography component="h3" className="flow-item-title">예약 메시지</Typography>
              <Typography className="flow-metric-value">{pendingScheduled.length}</Typography>
              <Typography className="flow-item-subtitle">대기·처리·재시도 상태를 포함합니다.</Typography>
              <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate("/my-work/scheduled")}>확인하기</Button>
            </FlowCard>
            <FlowCard>
              <Typography component="h3" className="flow-item-title">리마인더</Typography>
              <Typography className="flow-metric-value">{data.reminders.length}</Typography>
              <Typography className="flow-item-subtitle">오늘 예정 {dueReminders.length}개</Typography>
              <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate("/my-work/reminders")}>확인하기</Button>
            </FlowCard>
            <FlowCard>
              <Typography component="h3" className="flow-item-title">승인</Typography>
              <Typography className="flow-metric-value">{pendingReview.length + pendingMine.length}</Typography>
              <Typography className="flow-item-subtitle">검토 {pendingReview.length}개 · 내 요청 {pendingMine.length}개</Typography>
              <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate("/approvals/review")}>승인 센터</Button>
            </FlowCard>
          </div>
        )}
      </FlowSection>

      <FlowSection title="구조화된 업무" description="서버 계약이 준비된 뒤 실제 메시지와 연결됩니다." id="today-prepared">
        <div className="flow-card-grid">
          <FlowPrepared title="Action Item" description="작업 저장 API가 아직 없어 임시 항목을 만들거나 완료로 표시하지 않습니다." />
          <FlowPrepared title="Decision Record" description="결정 기록 API가 아직 없어 예시 결정이나 가짜 통계를 표시하지 않습니다." />
        </div>
      </FlowSection>
    </FlowPage>
  );
}
