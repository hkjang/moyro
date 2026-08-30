import CheckRounded from "@mui/icons-material/CheckRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import { Alert, Button, TextField, Typography } from "@mui/material";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useSelector } from "react-redux";
import { useNavigate, useParams } from "react-router-dom";
import {
  compatApi,
  moyroMeApi,
  moyroReviewApi,
  type ApprovalRequest,
  type User,
} from "@/api/client";
import { useSystemInfo } from "@/features/system/SystemInfoContext";
import type { RootState } from "@/store";
import {
  FlowCard,
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
import { errorMessage, formatDateTime, useFlowWorkspaceIndex } from "./flow-data";

export type ApprovalCenterTab = "mine" | "review";

function validTab(value: string | undefined): value is ApprovalCenterTab {
  return value === "mine" || value === "review";
}

function isActionableApproval(request: ApprovalRequest): boolean {
  return request.status === "pending" && (request.expires_at <= 0 || request.expires_at > Date.now());
}

function approvalStatus(request: ApprovalRequest): {
  label: string;
  tone: "default" | "warning" | "success" | "error" | "info";
} {
  if (request.executed_at > 0) return { label: "실행 완료", tone: "success" };
  if (request.status === "pending" && !isActionableApproval(request)) return { label: "기한 경과", tone: "error" };
  switch (request.status) {
    case "pending": return { label: "검토 대기", tone: "warning" };
    case "approved": return { label: "승인", tone: "success" };
    case "rejected": return { label: "반려", tone: "error" };
    case "expired": return { label: "만료", tone: "default" };
    case "executing": return { label: "실행 중", tone: "info" };
    case "failed": return { label: "실행 실패", tone: "error" };
    default: return { label: request.status || "상태 없음", tone: "default" };
  }
}

function actionLabel(action: string): string {
  if (action === "create_post") return "메시지 작성";
  if (action === "reply_to_thread") return "스레드 답글";
  return action;
}

const SENSITIVE_PAYLOAD_KEY = /secret|password|authorization|access[_-]?token|refresh[_-]?token|api[_-]?key|credential/i;

function redactPayload(value: unknown, depth = 0): unknown {
  if (depth > 8) return "[중첩 데이터 생략]";
  if (Array.isArray(value)) return value.map((item) => redactPayload(item, depth + 1));
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(
    Object.entries(value as Record<string, unknown>).map(([key, item]) => [
      key,
      SENSITIVE_PAYLOAD_KEY.test(key) ? "[보호된 값]" : redactPayload(item, depth + 1),
    ]),
  );
}

function safePayload(value: unknown): string {
  if (value === undefined || value === null) return "";
  try {
    const visible = redactPayload(value);
    if (visible && typeof visible === "object" && !Array.isArray(visible) && Object.keys(visible).length === 0) return "";
    return JSON.stringify(visible, null, 2);
  } catch {
    return "[요청 데이터를 안전하게 표시할 수 없습니다.]";
  }
}

function ApprovalCard({
  request,
  requester,
  teamName,
  resourceName,
  children,
}: {
  request: ApprovalRequest;
  requester?: User;
  teamName?: string;
  resourceName?: string;
  children?: ReactNode;
}) {
  const status = approvalStatus(request);
  const payload = safePayload(request.payload);
  return (
    <FlowCard>
      <div className="flow-toolbar">
        <div className="flow-badges">
          <Typography component="h3" className="flow-item-title">{actionLabel(request.action_type)}</Typography>
          <FlowStatusBadge label={status.label} tone={status.tone} />
        </div>
        <Typography className="flow-item-subtitle">요청 {formatDateTime(request.create_at)}</Typography>
      </div>
      <Typography className="flow-item-subtitle">
        요청자 {requester ? `@${requester.username}` : request.requester_id.slice(0, 12)}
        {teamName ? ` · ${teamName}` : request.team_id ? ` · 팀 ${request.team_id.slice(0, 12)}` : ""}
        {resourceName ? ` · ${resourceName}` : request.resource_type ? ` · ${request.resource_type} ${request.resource_id.slice(0, 12)}` : ""}
      </Typography>
      {request.expires_at > 0 && request.status === "pending" && <Typography className="flow-item-subtitle">검토 기한 {formatDateTime(request.expires_at)}</Typography>}
      {payload && <pre className="flow-preformatted" aria-label="승인 요청 데이터">{payload}</pre>}
      {children}
    </FlowCard>
  );
}

export function ApprovalCenterPage({ initialTab = "mine" }: { initialTab?: ApprovalCenterTab }) {
  const navigate = useNavigate();
  const params = useParams<{ tab?: string }>();
  const tab = validTab(params.tab) ? params.tab : initialTab;
  const token = useSelector((state: RootState) => state.auth.token);
  const systemInfo = useSystemInfo();
  const workspace = useFlowWorkspaceIndex(token);
  const [mine, setMine] = useState<ApprovalRequest[]>([]);
  const [review, setReview] = useState<ApprovalRequest[]>([]);
  const [users, setUsers] = useState<Record<string, User>>({});
  const [loading, setLoading] = useState(true);
  const [mineError, setMineError] = useState("");
  const [reviewError, setReviewError] = useState("");
  const [metadataWarning, setMetadataWarning] = useState("");
  const [feedback, setFeedback] = useState("");
  const [decisionError, setDecisionError] = useState("");
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [decidingId, setDecidingId] = useState("");
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (params.tab !== undefined && !validTab(params.tab)) {
      navigate("/approvals/mine", { replace: true });
    }
  }, [navigate, params.tab]);

  useEffect(() => {
    let active = true;
    if (!token) {
      setMine([]);
      setReview([]);
      setUsers({});
      setLoading(false);
      setMineError("로그인 세션이 없습니다.");
      setReviewError("로그인 세션이 없습니다.");
      return () => { active = false; };
    }
    setLoading(true);
    void (async () => {
      const [mineResult, reviewResult] = await Promise.allSettled([
        moyroMeApi.listApprovalRequests(token),
        // Do not infer reviewer access from global permissions: this endpoint
        // evaluates the caller's team-scoped review permission on the server.
        moyroReviewApi.listApprovalRequests(token, "pending"),
      ] as const);
      if (!active) return;
      const mineRows = mineResult.status === "fulfilled" ? mineResult.value : [];
      const reviewRows = reviewResult.status === "fulfilled" ? reviewResult.value : [];
      setMine(mineRows);
      setReview(reviewRows);
      setMineError(mineResult.status === "rejected" ? errorMessage(mineResult.reason, "내 승인 요청을 불러오지 못했습니다.") : "");
      setReviewError(reviewResult.status === "rejected" ? errorMessage(reviewResult.reason, "검토 범위를 불러오지 못했습니다.") : "");

      const requesterIds = [...new Set([...mineRows, ...reviewRows].map((request) => request.requester_id).filter(Boolean))];
      if (requesterIds.length > 0) {
        try {
          const rows = await compatApi.usersByIds(token, requesterIds);
          if (active) setUsers(Object.fromEntries(rows.map((user) => [user.id, user])));
        } catch (error) {
          if (active) setMetadataWarning(`요청자 이름을 불러오지 못했습니다: ${errorMessage(error, "알 수 없는 오류")}`);
        }
      } else {
        setUsers({});
      }
      if (active) setLoading(false);
    })();
    return () => { active = false; };
  }, [revision, token]);

  const teamById = useMemo(() => Object.fromEntries(workspace.teams.map((team) => [team.id, team])), [workspace.teams]);

  async function decide(request: ApprovalRequest, decision: "approve" | "reject") {
    if (!token) return;
    if (!isActionableApproval(request)) {
      setDecisionError("검토 기한이 지난 요청은 처리할 수 없습니다. 최신 상태를 다시 확인하세요.");
      setRevision((current) => current + 1);
      return;
    }
    const reason = reasons[request.id]?.trim() ?? "";
    if (decision === "reject" && !reason) {
      setDecisionError("반려할 때는 사유를 입력하세요.");
      return;
    }
    setDecidingId(request.id);
    setDecisionError("");
    setFeedback("");
    try {
      const result = await moyroReviewApi.decideApprovalRequest(token, request.id, { decision, reason });
      setReview((current) => current.filter((item) => item.id !== result.id));
      setMine((current) => current.map((item) => item.id === result.id ? result : item));
      setFeedback(
        decision === "approve"
          ? `승인 결정을 저장했습니다. 현재 상태: ${approvalStatus(result).label}`
          : "반려 결정을 저장했습니다.",
      );
    } catch (error) {
      setDecisionError(`${errorMessage(error, "결정 처리 결과를 확인하지 못했습니다.")} 결정과 실행 상태가 달라졌을 수 있어 최신 목록을 다시 조회합니다.`);
      setRevision((current) => current + 1);
    } finally {
      setDecidingId("");
    }
  }

  const refreshAll = () => {
    setFeedback("");
    setDecisionError("");
    setMetadataWarning("");
    workspace.refresh();
    setRevision((current) => current + 1);
  };

  const activeRows = tab === "mine" ? mine : review;
  const activeError = tab === "mine" ? mineError : reviewError;
  const actionableReviewCount = review.filter(isActionableApproval).length;

  return (
    <FlowPage
      eyebrow="안전한 자동화"
      title="승인 센터"
      description="승인이 필요한 작업의 요청 맥락을 확인하고, 서버가 허용한 검토 범위 안에서 결정합니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={refreshAll} disabled={loading}>새로고침</Button>}
    >
      <FlowTabs
        idPrefix="approval-center"
        label="승인 센터 분류"
        value={tab}
        onChange={(value) => navigate(`/approvals/${value}`)}
        options={[
          { value: "mine", label: "내 요청", count: mine.length },
          { value: "review", label: "검토 대기", count: actionableReviewCount },
        ]}
      />
      {systemInfo.loaded && systemInfo.approval_enabled === false && (
        <Alert severity="info">현재 승인 정책이 비활성 상태입니다. 기존 요청 이력은 계속 조회할 수 있지만 새 요청은 생성되지 않을 수 있습니다.</Alert>
      )}
      {activeError && <FlowError message={activeError} />}
      {decisionError && <FlowError message={decisionError} />}
      {feedback && <Alert severity="success" role="status" aria-live="polite">{feedback}</Alert>}
      {metadataWarning && <Alert severity="warning">{metadataWarning}</Alert>}
      {workspace.error && <Alert severity="warning">팀·채널 이름을 불러오지 못했습니다: {workspace.error}</Alert>}
      {workspace.warnings.map((warning) => <Alert severity="warning" key={warning}>{warning}</Alert>)}

      {(["mine", "review"] as const).map((panelTab) => (
        <FlowTabPanel key={panelTab} idPrefix="approval-center" value={panelTab} active={tab === panelTab}>
          {tab === panelTab && (
            <FlowSection
              title={tab === "mine" ? "내 승인 요청" : "내가 검토할 요청"}
              description={tab === "mine" ? "요청 상태와 실행 여부를 확인합니다." : "이 목록은 review API가 계산한 팀별 검토 권한 범위입니다."}
              id="approval-list"
            >
              {loading ? <FlowLoading label="승인 요청을 불러오는 중…" /> : activeRows.length === 0 ? (
                <FlowEmpty
                  title={tab === "mine" ? "승인 요청 이력이 없습니다" : "검토할 요청이 없습니다"}
                  description={tab === "mine" ? "승인이 필요한 MCP 작업을 요청하면 상태가 여기에 표시됩니다." : "현재 계정의 팀별 검토 범위에 대기 요청이 없습니다."}
                />
              ) : (
                <div className="flow-stack">
                  {activeRows.map((request) => {
                    const entry = request.resource_type === "channel" ? workspace.channelById[request.resource_id] : undefined;
                    return (
                      <ApprovalCard
                        key={request.id}
                        request={request}
                        requester={users[request.requester_id]}
                        teamName={teamById[request.team_id]?.display_name}
                        resourceName={entry?.channel.display_name || entry?.channel.name}
                      >
                        {tab === "review" && isActionableApproval(request) && (
                          <div className="flow-stack flow-approval-actions">
                            <TextField
                              label="검토 의견 / 반려 사유"
                              value={reasons[request.id] ?? ""}
                              onChange={(event) => setReasons((current) => ({ ...current, [request.id]: event.target.value }))}
                              multiline
                              minRows={2}
                              helperText="반려에는 사유가 필요합니다. 승인 의견은 선택 사항입니다."
                            />
                            <div className="flow-list-actions">
                              <Button
                                color="error"
                                variant="outlined"
                                startIcon={<CloseRounded />}
                                disabled={decidingId === request.id || !(reasons[request.id] ?? "").trim()}
                                onClick={() => void decide(request, "reject")}
                              >반려</Button>
                              <Button
                                color="success"
                                variant="contained"
                                startIcon={<CheckRounded />}
                                disabled={decidingId === request.id}
                                onClick={() => void decide(request, "approve")}
                              >승인</Button>
                            </div>
                          </div>
                        )}
                        {tab === "review" && request.status === "pending" && !isActionableApproval(request) && (
                          <Alert severity="warning">검토 기한이 지나 승인·반려할 수 없습니다. 서버의 만료 처리가 완료되면 목록에서 제거됩니다.</Alert>
                        )}
                      </ApprovalCard>
                    );
                  })}
                </div>
              )}
            </FlowSection>
          )}
        </FlowTabPanel>
      ))}

      <FlowSection title="정책 설명 범위" id="approval-prepared">
        <div className="flow-card-grid">
          <FlowPrepared title="위험도·변경 전후 Diff" description="현재 승인 응답에는 계산된 위험도나 외부 시스템 Preview 계약이 없어 추정값을 표시하지 않습니다." />
          <FlowPrepared title="검토자별 결정 타임라인" description="결정 상세 조회 API가 준비되면 검토자, 사유, 실행 결과를 시간순으로 연결합니다." />
        </div>
      </FlowSection>
    </FlowPage>
  );
}
