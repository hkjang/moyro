import ArrowForwardRounded from "@mui/icons-material/ArrowForwardRounded";
import CheckCircleOutlineRounded from "@mui/icons-material/CheckCircleOutlineRounded";
import DoneAllRounded from "@mui/icons-material/DoneAllRounded";
import MarkEmailReadOutlined from "@mui/icons-material/MarkEmailReadOutlined";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import ScheduleRounded from "@mui/icons-material/ScheduleRounded";
import { Alert, Button, Chip, MenuItem, TextField, Typography } from "@mui/material";
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
import {
  activityApi,
  type ActivityEvent,
  type ActivityEventPage,
  type ActivityEventType,
  type ActivityStatePatch,
} from "@/api/activity";
import {
  DEFAULT_INBOX_PREFERENCES,
  arrangeActivities,
  inboxPreferencesApi,
  type InboxPreferences,
} from "@/api/inbox-preferences";
import type { RootState } from "@/store";
import {
  FlowEmpty,
  FlowError,
  FlowLoading,
  FlowPage,
  FlowSection,
  FlowStatusBadge,
  FlowTabPanel,
  FlowTabs,
} from "./FlowPage";
import { buildApprovalPreview } from "./approval-preview";
import { channelPath, errorMessage, formatDateTime, postNavigationState } from "./flow-data";
import { useFlowWorkspaceIndex } from "./FlowDataProvider";

type InboxTab = "updates" | "conversations" | "approvals" | "reminders";
type InboxData = {
  mine: ApprovalRequest[];
  review: ApprovalRequest[];
  reminders: Reminder[];
  reminderPosts: Record<string, Post>;
};

const EMPTY_DATA: InboxData = { mine: [], review: [], reminders: [], reminderPosts: {} };
const EMPTY_ACTIVITY_PAGE: ActivityEventPage = { events: [], next_cursor: "" };

const ACTIVITY_TYPE_LABELS: Record<ActivityEventType, string> = {
  mention: "멘션",
  thread_reply: "스레드 답글",
  direct_message: "다이렉트 메시지",
  approval_requested: "승인 요청",
  decided: "승인 결과",
  reminder_fired: "리마인더",
  task_assigned: "작업 할당",
  system_warning: "시스템 알림",
  plugin_event: "연결 앱",
};

function activityTone(type: ActivityEventType): "default" | "warning" | "success" | "error" | "info" {
  if (type === "approval_requested" || type === "reminder_fired") return "warning";
  if (type === "decided") return "success";
  if (type === "system_warning") return "error";
  if (type === "mention" || type === "direct_message" || type === "thread_reply") return "info";
  return "default";
}

export function activitySource(event: ActivityEvent, hasChannel: boolean): {
  label: string;
  path: string;
  state?: { focusPostId: string };
} | null {
  if (event.type === "approval_requested" && event.resource_type === "approval_review") {
    return { label: "검토하기", path: "/approvals/review" };
  }
  if (event.type === "approval_requested") return { label: "상태 보기", path: "/approvals/mine" };
  if (event.type === "decided") return { label: "결과 보기", path: "/approvals/mine" };
  if (event.type === "task_assigned") return { label: "작업 보기", path: "/my-work/tasks" };
  if (hasChannel && event.channel_id) {
    return {
      label: event.post_id ? "원문 메시지" : "채널 열기",
      path: event.channel_id,
      state: event.post_id ? postNavigationState(event.post_id) : undefined,
    };
  }
  if (event.type === "reminder_fired") return { label: "리마인더 보기", path: "/my-work/reminders" };
  return null;
}

function isActionableApproval(request: ApprovalRequest): boolean {
  return request.status === "pending" && (request.expires_at <= 0 || request.expires_at > Date.now());
}

function validTab(value: string | undefined): value is InboxTab {
  return value === "updates" || value === "conversations" || value === "approvals" || value === "reminders";
}

export function UnifiedInboxPage() {
  const navigate = useNavigate();
  const params = useParams<{ tab?: string }>();
  const token = useSelector((state: RootState) => state.auth.token);
  const workspace = useFlowWorkspaceIndex();
  const tab: InboxTab = validTab(params.tab) ? params.tab : "updates";
  const [data, setData] = useState<InboxData>(EMPTY_DATA);
  const [activityPage, setActivityPage] = useState<ActivityEventPage>(EMPTY_ACTIVITY_PAGE);
  const [activityLoading, setActivityLoading] = useState(true);
  const [activityLoadingMore, setActivityLoadingMore] = useState(false);
  const [activityWarning, setActivityWarning] = useState("");
  const [activityActionIds, setActivityActionIds] = useState<Set<string>>(new Set());
  const [activityNow, setActivityNow] = useState(() => Date.now());
  const [showCompleted, setShowCompleted] = useState(false);
  const [showSnoozed, setShowSnoozed] = useState(false);
  const [loading, setLoading] = useState(true);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [clearedChannelIds, setClearedChannelIds] = useState<Set<string>>(new Set());
  const [markingId, setMarkingId] = useState("");
  const [feedback, setFeedback] = useState("");
  const [actionError, setActionError] = useState("");
  const [revision, setRevision] = useState(0);
  const [inboxPreferences, setInboxPreferences] = useState<InboxPreferences>(DEFAULT_INBOX_PREFERENCES);

  useEffect(() => {
    if (params.tab !== undefined && !validTab(params.tab)) {
      navigate("/inbox/updates", { replace: true });
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

  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    if (!token) {
      setActivityPage(EMPTY_ACTIVITY_PAGE);
      setActivityLoading(false);
      setActivityWarning("로그인 세션이 없습니다.");
      return () => { active = false; };
    }
    setActivityLoading(true);
    setActivityWarning("");
    void activityApi.list(token, { limit: 100 }, controller.signal)
      .then((page) => {
        if (active) setActivityPage(page);
      })
      .catch((loadError: unknown) => {
        if (!active || controller.signal.aborted) return;
        setActivityPage(EMPTY_ACTIVITY_PAGE);
        setActivityWarning(errorMessage(loadError, "업데이트를 불러오지 못했습니다."));
      })
      .finally(() => {
        if (active) setActivityLoading(false);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [revision, token, workspace.activityRevision]);

  useEffect(() => {
    if (!token) {
      setInboxPreferences(DEFAULT_INBOX_PREFERENCES);
      return undefined;
    }
    const controller = new AbortController();
    void inboxPreferencesApi.get(token, controller.signal).then(setInboxPreferences).catch(() => undefined);
    return () => controller.abort();
  }, [token]);

  useEffect(() => {
    const current = Date.now();
    setActivityNow(current);
    const nextWakeAt = activityPage.events
      .filter((event) => event.completed_at === 0 && event.snoozed_until > current)
      .reduce((earliest, event) => Math.min(earliest, event.snoozed_until), Number.POSITIVE_INFINITY);
    if (!Number.isFinite(nextWakeAt)) return undefined;
    const timer = window.setTimeout(
      () => setActivityNow(Date.now()),
      Math.max(0, nextWakeAt - current + 50),
    );
    return () => window.clearTimeout(timer);
  }, [activityPage.events]);

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
  const completedCount = activityPage.events.filter((event) => event.completed_at > 0).length;
  const snoozedCount = activityPage.events.filter((event) => event.completed_at === 0 && event.snoozed_until > activityNow).length;
  const visibleActivityEvents = activityPage.events.filter((event) => {
    if (!showCompleted && event.completed_at > 0) return false;
    if (!showSnoozed && event.completed_at === 0 && event.snoozed_until > activityNow) return false;
    return true;
  });
  const arrangedActivityEvents = arrangeActivities(visibleActivityEvents, inboxPreferences);
  const unreadActivityIDs = visibleActivityEvents
    .filter((event) => event.read_at === 0 && event.completed_at === 0)
    .map((event) => event.id);

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

  async function patchActivity(event: ActivityEvent, patch: ActivityStatePatch, message: string) {
    if (!token || activityActionIds.has(event.id)) return;
    setActivityActionIds((current) => new Set(current).add(event.id));
    setActionError("");
    setFeedback("");
    try {
      const updated = await activityApi.patch(token, event.id, patch);
      setActivityPage((current) => ({
        ...current,
        events: current.events.map((item) => item.id === updated.id ? updated : item),
      }));
      setFeedback(message);
    } catch (error) {
      setActionError(errorMessage(error, "업데이트 상태를 변경하지 못했습니다."));
    } finally {
      setActivityActionIds((current) => {
        const next = new Set(current);
        next.delete(event.id);
        return next;
      });
    }
  }

  async function markVisibleActivityRead() {
    if (!token || unreadActivityIDs.length === 0) return;
    setActivityActionIds((current) => new Set([...current, ...unreadActivityIDs]));
    setActionError("");
    setFeedback("");
    try {
      const result = await activityApi.markRead(token, unreadActivityIDs);
      const readAt = Date.now();
      const selected = new Set(unreadActivityIDs);
      setActivityPage((current) => ({
        ...current,
        events: current.events.map((event) => selected.has(event.id) ? { ...event, read_at: readAt, update_at: readAt } : event),
      }));
      setFeedback(`${result.updated}개 업데이트를 읽음으로 표시했습니다.`);
    } catch (error) {
      setActionError(errorMessage(error, "업데이트를 읽음으로 표시하지 못했습니다."));
    } finally {
      setActivityActionIds((current) => {
        const next = new Set(current);
        for (const id of unreadActivityIDs) next.delete(id);
        return next;
      });
    }
  }

  async function loadMoreActivity() {
    if (!token || !activityPage.next_cursor || activityLoadingMore) return;
    setActivityLoadingMore(true);
    setActionError("");
    try {
      const nextPage = await activityApi.list(token, { cursor: activityPage.next_cursor, limit: 100 });
      setActivityPage((current) => {
        const seen = new Set(current.events.map((event) => event.id));
        return {
          events: [...current.events, ...nextPage.events.filter((event) => !seen.has(event.id))],
          next_cursor: nextPage.next_cursor,
        };
      });
    } catch (error) {
      setActionError(errorMessage(error, "이전 업데이트를 불러오지 못했습니다."));
    } finally {
      setActivityLoadingMore(false);
    }
  }

  function openActivitySource(event: ActivityEvent) {
    const entry = event.channel_id ? workspace.channelById[event.channel_id] : undefined;
    const source = activitySource(event, Boolean(entry));
    if (!source) return;
    if (entry && source.path === event.channel_id) {
      navigate(channelPath(entry), { state: source.state });
      return;
    }
    navigate(source.path, { state: source.state });
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
      description="멘션, 답글, 승인, 작업과 리마인더를 놓치지 않고 한곳에서 처리합니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={refreshAll} disabled={loading || workspace.loading}>새로고침</Button>}
    >
      <FlowTabs
        idPrefix="inbox"
        label="알림함 분류"
        value={tab}
        onChange={(value) => navigate(`/inbox/${value}`)}
        options={[
          { value: "updates", label: "업데이트", count: unreadActivityIDs.length },
          { value: "conversations", label: "대화", count: conversations.length },
          { value: "approvals", label: "승인", count: approvalCount },
          { value: "reminders", label: "리마인더", count: data.reminders.length },
        ]}
      />
      {workspace.error && <FlowError message={workspace.error} onRetry={workspace.refresh} />}
      {actionError && <FlowError message={actionError} />}
      {feedback && <Alert severity="success" role="status" aria-live="polite">{feedback}</Alert>}
      {activityWarning && <Alert severity="warning">{activityWarning}</Alert>}
      {[...workspace.warnings, ...warnings].map((warning) => <Alert severity="warning" key={warning}>{warning}</Alert>)}

      <FlowTabPanel idPrefix="inbox" value="updates" active={tab === "updates"}>
        {tab === "updates" && (
          <FlowSection
            title="내 업데이트"
            description="나에게 온 활동을 읽고, 미루고, 완료한 상태가 모든 로그인 환경에 저장됩니다."
            id="inbox-updates"
            action={unreadActivityIDs.length > 0 ? (
              <Button
                variant="outlined"
                startIcon={<MarkEmailReadOutlined />}
                onClick={() => void markVisibleActivityRead()}
                disabled={unreadActivityIDs.some((id) => activityActionIds.has(id))}
              >
                모두 읽음
              </Button>
            ) : undefined}
          >
            <div className="flow-toolbar flow-activity-filters" aria-label="업데이트 표시 옵션">
              <div className="flow-badges">
                <Button
                  size="small"
                  variant={showCompleted ? "contained" : "outlined"}
                  onClick={() => setShowCompleted((current) => !current)}
                  aria-pressed={showCompleted}
                >
                  완료 {completedCount}
                </Button>
                <Button
                  size="small"
                  variant={showSnoozed ? "contained" : "outlined"}
                  onClick={() => setShowSnoozed((current) => !current)}
                  aria-pressed={showSnoozed}
                >
                  미룬 항목 {snoozedCount}
                </Button>
              </div>
              <Typography className="flow-item-subtitle">새 업데이트 {unreadActivityIDs.length}개</Typography>
            </div>
            {activityLoading ? <FlowLoading label="업데이트를 불러오는 중…" /> : visibleActivityEvents.length === 0 ? (
              <FlowEmpty
                title="확인할 업데이트가 없습니다"
                description={completedCount > 0 || snoozedCount > 0
                  ? "완료하거나 미룬 항목은 위 표시 옵션에서 다시 확인할 수 있습니다."
                  : "멘션, 답글, 승인 요청, 작업 할당과 리마인더가 여기에 표시됩니다."}
              />
            ) : (
              <div className="flow-list" aria-live="polite">
                {arrangedActivityEvents.map(({ event, priority, startsBundle, bundleKey }) => {
                  const entry = event.channel_id ? workspace.channelById[event.channel_id] : undefined;
                  const source = activitySource(event, Boolean(entry));
                  const busy = activityActionIds.has(event.id);
                  const completed = event.completed_at > 0;
                  const snoozed = !completed && event.snoozed_until > activityNow;
                  const bundleLabel = bundleKey.startsWith("channel:")
                    ? (event.channel_id ? workspace.channelById[event.channel_id]?.channel.display_name ?? "채널" : "채널 없는 업데이트")
                    : ACTIVITY_TYPE_LABELS[event.type];
                  return (
                    <div key={event.id}>
                      {startsBundle && inboxPreferences.bundle_by !== "none" && (
                        <Typography variant="overline" color="text.secondary">{bundleLabel} 묶음</Typography>
                      )}
                    <article
                      className={`flow-list-row flow-activity-row ${event.read_at === 0 ? "flow-activity-unread" : ""}`.trim()}
                    >
                      <div className="flow-list-main">
                        <div className="flow-badges">
                          <FlowStatusBadge label={ACTIVITY_TYPE_LABELS[event.type]} tone={activityTone(event.type)} />
                          {event.read_at === 0 && <Chip size="small" label="새 항목" color="primary" />}
                          {priority && <Chip size="small" label="우선순위" color="warning" />}
                          {completed && <FlowStatusBadge label="완료" tone="success" />}
                          {snoozed && <Chip size="small" variant="outlined" label={`${formatDateTime(event.snoozed_until)}까지 미룸`} />}
                        </div>
                        <Typography component="h3" className="flow-item-title flow-activity-title">{event.title}</Typography>
                        {event.summary && <Typography className="flow-item-message">{event.summary}</Typography>}
                        <Typography className="flow-item-subtitle">{formatDateTime(event.create_at)}</Typography>
                      </div>
                      <div className="flow-list-actions">
                        {source && <Button endIcon={<ArrowForwardRounded />} onClick={() => openActivitySource(event)}>{source.label}</Button>}
                        <Button
                          variant="outlined"
                          disabled={busy}
                          onClick={() => void patchActivity(
                            event,
                            { read: event.read_at === 0 },
                            event.read_at === 0 ? "읽음으로 표시했습니다." : "읽지 않음으로 표시했습니다.",
                          )}
                        >
                          {event.read_at === 0 ? "읽음" : "읽지 않음"}
                        </Button>
                        {!completed && snoozed && (
                          <Button
                            variant="outlined"
                            startIcon={<ScheduleRounded />}
                            disabled={busy}
                            onClick={() => void patchActivity(
                              event,
                              { snoozed_until: 0 },
                              "업데이트를 다시 표시했습니다.",
                            )}
                          >
                            지금 보기
                          </Button>
                        )}
                        {!completed && !snoozed && (
                          <>
                            <Button
                              variant="outlined"
                              startIcon={<ScheduleRounded />}
                              disabled={busy}
                              onClick={() => {
                                const minutes = inboxPreferences.snooze_presets_minutes[0] ?? 60;
                                void patchActivity(event, { snoozed_until: Date.now() + minutes * 60_000 }, `${minutes}분 뒤에 다시 표시합니다.`);
                              }}
                            >
                              {(() => {
                                const minutes = inboxPreferences.snooze_presets_minutes[0] ?? 60;
                                return minutes >= 1440 && minutes % 1440 === 0 ? `${minutes / 1440}일 미루기` : minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60}시간 미루기` : `${minutes}분 미루기`;
                              })()}
                            </Button>
                            {inboxPreferences.snooze_presets_minutes.length > 1 && (
                              <TextField
                                select
                                size="small"
                                label="다른 시간"
                                value=""
                                disabled={busy}
                                onChange={(selectEvent) => {
                                  const minutes = Number(selectEvent.target.value);
                                  void patchActivity(event, { snoozed_until: Date.now() + minutes * 60_000 }, `${minutes}분 뒤에 다시 표시합니다.`);
                                }}
                                sx={{ minWidth: 120 }}
                              >
                                {inboxPreferences.snooze_presets_minutes.slice(1).map((minutes) => (
                                  <MenuItem key={minutes} value={minutes}>{minutes >= 1440 && minutes % 1440 === 0 ? `${minutes / 1440}일` : minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60}시간` : `${minutes}분`}</MenuItem>
                                ))}
                              </TextField>
                            )}
                          </>
                        )}
                        <Button
                          variant={completed ? "outlined" : "contained"}
                          startIcon={<CheckCircleOutlineRounded />}
                          disabled={busy}
                          onClick={() => void patchActivity(
                            event,
                            completed
                              ? { completed: false }
                              : { completed: true, read: true, snoozed_until: 0 },
                            completed ? "완료 상태를 되돌렸습니다." : "업데이트를 완료했습니다.",
                          )}
                        >
                          {completed ? "완료 취소" : "완료"}
                        </Button>
                      </div>
                    </article>
                    </div>
                  );
                })}
                {activityPage.next_cursor && (
                  <Button variant="outlined" onClick={() => void loadMoreActivity()} disabled={activityLoadingMore}>
                    {activityLoadingMore ? "불러오는 중…" : "이전 업데이트 더 보기"}
                  </Button>
                )}
              </div>
            )}
          </FlowSection>
        )}
      </FlowTabPanel>

      <FlowTabPanel idPrefix="inbox" value="conversations" active={tab === "conversations"}>
        {tab === "conversations" && (
          <FlowSection title="읽지 않은 대화" description="채널별 읽지 않은 메시지와 멘션을 확인합니다." id="inbox-conversations">
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
          <FlowSection title="승인 알림" description="내가 검토할 수 있는 요청과 내 요청 상태를 확인합니다." id="inbox-approvals">
          {loading ? <FlowLoading /> : approvalCount === 0 ? (
            <FlowEmpty title="처리할 승인이 없습니다" description="새 승인 요청이나 내 요청의 대기 상태가 생기면 여기에 표시됩니다." />
          ) : (
            <div className="flow-list">
              {pendingReview.map((request) => (
                <article className="flow-list-row" key={`review-${request.id}`}>
                  <div className="flow-list-main">
                    <div className="flow-badges"><Typography component="h3" className="flow-item-title">검토 요청 · {buildApprovalPreview(request).title}</Typography><FlowStatusBadge label="검토 대기" tone="warning" /></div>
                    <Typography className="flow-item-subtitle">요청 {formatDateTime(request.create_at)} · 내가 검토할 수 있는 요청입니다.</Typography>
                  </div>
                  <div className="flow-list-actions"><Button onClick={() => navigate("/approvals/review")}>검토하기</Button></div>
                </article>
              ))}
              {pendingMine.map((request) => (
                <article className="flow-list-row" key={`mine-${request.id}`}>
                  <div className="flow-list-main">
                    <div className="flow-badges"><Typography component="h3" className="flow-item-title">내 요청 · {buildApprovalPreview(request).title}</Typography><FlowStatusBadge label="승인 대기" tone="info" /></div>
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

    </FlowPage>
  );
}
