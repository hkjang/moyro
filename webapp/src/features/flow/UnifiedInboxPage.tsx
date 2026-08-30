import ArrowForwardRounded from "@mui/icons-material/ArrowForwardRounded";
import DoneAllRounded from "@mui/icons-material/DoneAllRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import { Alert, Button, Chip, Typography } from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { useNavigate, useParams } from "react-router-dom";
import {
  api,
  compatApi,
  moyroMeApi,
  moyroReviewApi,
  type ApprovalRequest,
  type Post,
  type Reminder,
} from "@/api/client";
import type { RootState } from "@/store";
import {
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
import { channelPath, errorMessage, formatDateTime, postNavigationState, useFlowWorkspaceIndex } from "./flow-data";

type InboxTab = "conversations" | "approvals" | "reminders";
type InboxData = {
  mine: ApprovalRequest[];
  review: ApprovalRequest[];
  reminders: Reminder[];
  reminderPosts: Record<string, Post>;
};

const EMPTY_DATA: InboxData = { mine: [], review: [], reminders: [], reminderPosts: {} };

function isActionableApproval(request: ApprovalRequest): boolean {
  return request.status === "pending" && (request.expires_at <= 0 || request.expires_at > Date.now());
}

function validTab(value: string | undefined): value is InboxTab {
  return value === "conversations" || value === "approvals" || value === "reminders";
}

export function UnifiedInboxPage() {
  const navigate = useNavigate();
  const params = useParams<{ tab?: string }>();
  const token = useSelector((state: RootState) => state.auth.token);
  const workspace = useFlowWorkspaceIndex(token);
  const tab: InboxTab = validTab(params.tab) ? params.tab : "conversations";
  const [data, setData] = useState<InboxData>(EMPTY_DATA);
  const [loading, setLoading] = useState(true);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [clearedChannelIds, setClearedChannelIds] = useState<Set<string>>(new Set());
  const [markingId, setMarkingId] = useState("");
  const [feedback, setFeedback] = useState("");
  const [actionError, setActionError] = useState("");
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (params.tab !== undefined && !validTab(params.tab)) {
      navigate("/inbox/conversations", { replace: true });
    }
  }, [navigate, params.tab]);

  useEffect(() => {
    let active = true;
    if (!token) {
      setData(EMPTY_DATA);
      setClearedChannelIds(new Set());
      setLoading(false);
      setWarnings(["로그인 세션이 없습니다."]);
      return () => { active = false; };
    }
    setLoading(true);
    void (async () => {
      const [mineResult, reviewResult, reminderResult] = await Promise.allSettled([
        moyroMeApi.listApprovalRequests(token),
        moyroReviewApi.listApprovalRequests(token, "pending"),
        api.listMyReminders(token),
      ] as const);
      if (!active) return;
      const nextWarnings: string[] = [];
      if (mineResult.status === "rejected") nextWarnings.push(`내 승인 요청을 불러오지 못했습니다: ${errorMessage(mineResult.reason, "알 수 없는 오류")}`);
      if (reviewResult.status === "rejected") nextWarnings.push(`검토 대기를 불러오지 못했습니다: ${errorMessage(reviewResult.reason, "알 수 없는 오류")}`);
      if (reminderResult.status === "rejected") nextWarnings.push(`리마인더를 불러오지 못했습니다: ${errorMessage(reminderResult.reason, "알 수 없는 오류")}`);

      const reminders = reminderResult.status === "fulfilled" ? reminderResult.value : [];
      let reminderPosts: Record<string, Post> = {};
      if (reminders.length > 0) {
        try {
          const posts = await compatApi.postsByIds(token, [...new Set(reminders.map((item) => item.post_id))]);
          reminderPosts = Object.fromEntries(posts.map((post) => [post.id, post]));
        } catch (postError) {
          nextWarnings.push(`리마인더 원문을 불러오지 못했습니다: ${errorMessage(postError, "알 수 없는 오류")}`);
        }
      }
      if (!active) return;
      setData({
        mine: mineResult.status === "fulfilled" ? mineResult.value : [],
        review: reviewResult.status === "fulfilled" ? reviewResult.value : [],
        reminders,
        reminderPosts,
      });
      setWarnings(nextWarnings);
      setLoading(false);
    })();
    return () => { active = false; };
  }, [revision, token]);

  const conversations = useMemo(
    () => workspace.entries
      .filter((entry) => !clearedChannelIds.has(entry.channel.id))
      .filter((entry) => (entry.membership?.msg_count ?? 0) > 0 || (entry.membership?.mention_count ?? 0) > 0)
      .sort((left, right) => {
        const mentionDelta = (right.membership?.mention_count ?? 0) - (left.membership?.mention_count ?? 0);
        return mentionDelta || (right.membership?.msg_count ?? 0) - (left.membership?.msg_count ?? 0);
      }),
    [clearedChannelIds, workspace.entries],
  );
  const pendingMine = data.mine.filter(isActionableApproval);
  const pendingReview = data.review.filter(isActionableApproval);
  const approvalCount = pendingReview.length + pendingMine.length;

  async function markChannelRead(channelId: string) {
    if (!token) return;
    setMarkingId(channelId);
    setActionError("");
    setFeedback("");
    try {
      await api.viewChannel(token, channelId);
      setClearedChannelIds((current) => new Set(current).add(channelId));
      setFeedback("채널의 현재 메시지를 모두 읽음으로 표시했습니다.");
    } catch (error) {
      setActionError(errorMessage(error, "채널을 읽음으로 표시하지 못했습니다."));
    } finally {
      setMarkingId("");
    }
  }

  const refreshAll = () => {
    setClearedChannelIds(new Set());
    setFeedback("");
    setActionError("");
    workspace.refresh();
    setRevision((current) => current + 1);
  };

  return (
    <FlowPage
      eyebrow="개인 흐름"
      title="통합 알림함"
      description="채널의 읽지 않음·멘션, 실제 승인 요청과 리마인더를 한곳에서 확인합니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={refreshAll} disabled={loading || workspace.loading}>새로고침</Button>}
    >
      <FlowTabs
        idPrefix="inbox"
        label="알림함 분류"
        value={tab}
        onChange={(value) => navigate(`/inbox/${value}`)}
        options={[
          { value: "conversations", label: "대화", count: conversations.length },
          { value: "approvals", label: "승인", count: approvalCount },
          { value: "reminders", label: "리마인더", count: data.reminders.length },
        ]}
      />
      {workspace.error && <FlowError message={workspace.error} onRetry={workspace.refresh} />}
      {actionError && <FlowError message={actionError} />}
      {feedback && <Alert severity="success" role="status" aria-live="polite">{feedback}</Alert>}
      {[...workspace.warnings, ...warnings].map((warning) => <Alert severity="warning" key={warning}>{warning}</Alert>)}

      <FlowTabPanel idPrefix="inbox" value="conversations" active={tab === "conversations"}>
        {tab === "conversations" && (
          <FlowSection title="읽지 않은 대화" description="개별 알림이 아니라 채널 단위 서버 카운터입니다." id="inbox-conversations">
          {workspace.loading ? <FlowLoading /> : conversations.length === 0 ? (
            <FlowEmpty title="읽지 않은 채널이 없습니다" description="멘션이나 새 메시지가 있는 채널이 여기에 표시됩니다." />
          ) : (
            <div className="flow-list">
              {conversations.map((entry) => (
                <article className="flow-list-row" key={entry.channel.id}>
                  <div className="flow-list-main">
                    <div className="flow-badges">
                      <Typography component="h3" className="flow-item-title">{entry.channel.display_name || entry.channel.name}</Typography>
                      <Chip size="small" variant="outlined" label={`읽지 않음 ${entry.membership?.msg_count ?? 0}`} />
                      {(entry.membership?.mention_count ?? 0) > 0 && <FlowStatusBadge label={`멘션 ${entry.membership?.mention_count}`} tone="warning" />}
                    </div>
                    <Typography className="flow-item-subtitle">{entry.team.display_name} · 읽음 처리는 이 채널의 현재 메시지 전체에 적용됩니다.</Typography>
                  </div>
                  <div className="flow-list-actions">
                    <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate(channelPath(entry))}>채널 열기</Button>
                    <Button
                      variant="outlined"
                      startIcon={<DoneAllRounded />}
                      disabled={markingId === entry.channel.id}
                      onClick={() => void markChannelRead(entry.channel.id)}
                      aria-label={`${entry.channel.display_name || entry.channel.name} 채널 전체 읽음`}
                    >
                      {markingId === entry.channel.id ? "처리 중…" : "채널 전체 읽음"}
                    </Button>
                  </div>
                </article>
              ))}
            </div>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowTabPanel idPrefix="inbox" value="approvals" active={tab === "approvals"}>
        {tab === "approvals" && (
          <FlowSection title="승인 알림" description="검토 API가 반환한 내 검토 범위와 내 요청 상태입니다." id="inbox-approvals">
          {loading ? <FlowLoading /> : approvalCount === 0 ? (
            <FlowEmpty title="처리할 승인이 없습니다" description="새 승인 요청이나 내 요청의 대기 상태가 생기면 여기에 표시됩니다." />
          ) : (
            <div className="flow-list">
              {pendingReview.map((request) => (
                <article className="flow-list-row" key={`review-${request.id}`}>
                  <div className="flow-list-main">
                    <div className="flow-badges"><Typography component="h3" className="flow-item-title">검토 요청 · {request.action_type}</Typography><FlowStatusBadge label="검토 대기" tone="warning" /></div>
                    <Typography className="flow-item-subtitle">요청 {formatDateTime(request.create_at)} · 서버가 현재 사용자의 검토 범위를 적용했습니다.</Typography>
                  </div>
                  <div className="flow-list-actions"><Button onClick={() => navigate("/approvals/review")}>검토하기</Button></div>
                </article>
              ))}
              {pendingMine.map((request) => (
                <article className="flow-list-row" key={`mine-${request.id}`}>
                  <div className="flow-list-main">
                    <div className="flow-badges"><Typography component="h3" className="flow-item-title">내 요청 · {request.action_type}</Typography><FlowStatusBadge label="승인 대기" tone="info" /></div>
                    <Typography className="flow-item-subtitle">요청 {formatDateTime(request.create_at)}</Typography>
                  </div>
                  <div className="flow-list-actions"><Button onClick={() => navigate("/approvals/mine")}>상태 보기</Button></div>
                </article>
              ))}
            </div>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowTabPanel idPrefix="inbox" value="reminders" active={tab === "reminders"}>
        {tab === "reminders" && (
          <FlowSection title="메시지 리마인더" description="내 계정에 저장된 실제 리마인더입니다." id="inbox-reminders">
          {loading ? <FlowLoading /> : data.reminders.length === 0 ? (
            <FlowEmpty title="예정된 리마인더가 없습니다" description="메시지에서 리마인더를 설정하면 이 목록에 나타납니다." />
          ) : (
            <div className="flow-list">
              {data.reminders.map((reminder) => {
                const post = data.reminderPosts[reminder.post_id];
                const entry = post ? workspace.channelById[post.channel_id] : undefined;
                return (
                  <article className="flow-list-row" key={reminder.id}>
                    <div className="flow-list-main">
                      <Typography component="h3" className="flow-item-title">{post?.message || "원문을 불러올 수 없는 메시지"}</Typography>
                      <Typography className="flow-item-subtitle">알림 {formatDateTime(reminder.remind_at)}{reminder.delivered_at > 0 ? ` · 전달 ${formatDateTime(reminder.delivered_at)}` : ""}</Typography>
                    </div>
                    <div className="flow-list-actions">
                      {entry && post && <Button onClick={() => navigate(channelPath(entry), { state: postNavigationState(post.id) })}>원문 메시지</Button>}
                      <Button onClick={() => navigate("/my-work/reminders")}>관리</Button>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowSection title="알림 처리 범위" id="inbox-contract">
        <div className="flow-card-grid">
          <FlowPrepared title="개별 알림 읽음·나중에·완료" description="영속 알림 이벤트 API가 없어 개별 항목 상태를 임시로 꾸미지 않습니다. 현재는 채널 전체 읽음만 저장됩니다." />
          <FlowPrepared title="답글·시스템 이벤트 피드" description="사용자별 영속 피드 API가 준비되면 실제 이벤트 ID와 상태를 연결합니다." />
        </div>
      </FlowSection>
    </FlowPage>
  );
}
