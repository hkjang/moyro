import CheckRounded from "@mui/icons-material/CheckRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import {
  Alert,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { useCallback, useEffect, useState } from "react";
import { useSelector } from "react-redux";
import {
  moyroMeApi,
  moyroReviewApi,
  type ApprovalRequest,
} from "@/api/client";
import { SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

const dateTime = (value: number) => value
  ? new Intl.DateTimeFormat("ko-KR", { dateStyle: "medium", timeStyle: "short" }).format(value)
  : "—";

function requestStatus(request: ApprovalRequest): {
  label: string;
  color: "default" | "warning" | "success" | "error";
} {
  if (request.executed_at > 0) return { label: "실행 완료", color: "success" };
  switch (request.status) {
    case "pending": return { label: "검토 대기", color: "warning" };
    case "approved": return { label: "승인", color: "success" };
    case "rejected": return { label: "반려", color: "error" };
    case "expired": return { label: "만료", color: "default" };
    default: return { label: request.status, color: "default" };
  }
}

function RequestCard({
  request,
  actions,
}: {
  request: ApprovalRequest;
  actions?: React.ReactNode;
}) {
  const status = requestStatus(request);
  const payload = request.payload && Object.keys(request.payload as Record<string, unknown>).length > 0
    ? JSON.stringify(request.payload, null, 2)
    : "";
  return (
    <Card variant="outlined">
      <CardContent>
        <Stack spacing={1.5}>
          <Stack direction={{ xs: "column", sm: "row" }} sx={{ gap: 1, justifyContent: "space-between", alignItems: { sm: "center" } }}>
            <Stack direction="row" sx={{ gap: 1, alignItems: "center", flexWrap: "wrap" }}>
              <Typography component="h2" variant="h5">{request.action_type}</Typography>
              <Chip size="small" label={status.label} color={status.color} />
            </Stack>
            <Typography variant="caption" color="text.secondary">요청 {dateTime(request.create_at)}</Typography>
          </Stack>
          <Typography variant="body2" color="text.secondary">
            요청자 {request.requester_id.slice(0, 12)}
            {request.team_id ? ` · 팀 ${request.team_id.slice(0, 12)}` : ""}
            {request.resource_type ? ` · ${request.resource_type} ${request.resource_id.slice(0, 12)}` : ""}
          </Typography>
          {request.expires_at > 0 && request.status === "pending" && (
            <Typography variant="caption" color="warning.main">검토 기한 {dateTime(request.expires_at)}</Typography>
          )}
          {payload && (
            <Typography
              component="pre"
              variant="body2"
              sx={{ m: 0, p: 1.5, borderRadius: 1, bgcolor: "action.hover", overflow: "auto", whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
            >
              {payload}
            </Typography>
          )}
          {actions}
        </Stack>
      </CardContent>
    </Card>
  );
}

function LoadingState() {
  return (
    <Stack direction="row" sx={{ gap: 1.5, alignItems: "center" }} role="status">
      <CircularProgress size={22} />
      <Typography>승인 요청을 불러오는 중입니다.</Typography>
    </Stack>
  );
}

export function MyApprovalRequestsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [requests, setRequests] = useState<ApprovalRequest[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      setRequests(await moyroMeApi.listApprovalRequests(token));
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "내 승인 요청을 불러오지 못했습니다.");
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => { void load(); }, [load]);

  return (
    <SettingsPage
      title="내 승인 요청"
      description="승인이 필요한 MCP 작업의 검토 상태와 실행 결과를 확인합니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={() => void load()} disabled={loading}>새로고침</Button>}
    >
      {error && <Alert severity="error">{error}</Alert>}
      {loading ? <LoadingState /> : requests.length === 0 ? (
        <Alert severity="info">승인 요청 내역이 없습니다.</Alert>
      ) : (
        <Stack spacing={1.5}>{requests.map((request) => <RequestCard key={request.id} request={request} />)}</Stack>
      )}
    </SettingsPage>
  );
}

export function ReviewApprovalRequestsPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [requests, setRequests] = useState<ApprovalRequest[]>([]);
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [decidingId, setDecidingId] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  const load = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      setRequests(await moyroReviewApi.listApprovalRequests(token, "pending"));
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "검토 대기 요청을 불러오지 못했습니다.");
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => { void load(); }, [load]);

  async function decide(request: ApprovalRequest, decision: "approve" | "reject") {
    if (!token) return;
    const reason = reasons[request.id]?.trim() ?? "";
    if (decision === "reject" && !reason) {
      setError("반려할 때는 사유를 입력하세요.");
      return;
    }
    setDecidingId(request.id);
    setMessage("");
    try {
      const result = await moyroReviewApi.decideApprovalRequest(token, request.id, { decision, reason });
      setRequests((current) => current.filter((item) => item.id !== result.id));
      setError("");
      setMessage(decision === "approve" ? "요청을 승인했습니다." : "요청을 반려했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "결정을 저장하지 못했습니다.");
    } finally {
      setDecidingId("");
    }
  }

  return (
    <SettingsPage
      title="검토 대기"
      description="내게 검토 권한이 있는 승인 요청을 확인하고 승인 또는 반려합니다. 권한이 없으면 서버가 접근을 거부합니다."
      actions={<Button startIcon={<RefreshRounded />} onClick={() => void load()} disabled={loading}>새로고침</Button>}
    >
      {error && <Alert severity="error">{error}</Alert>}
      {message && <Alert severity="success" role="status">{message}</Alert>}
      {loading ? <LoadingState /> : requests.length === 0 ? (
        <Alert severity="info">검토할 승인 요청이 없습니다.</Alert>
      ) : (
        <Stack spacing={1.5}>
          {requests.map((request) => (
            <RequestCard
              key={request.id}
              request={request}
              actions={request.status === "pending" ? (
                <Stack spacing={1.25}>
                  <TextField
                    label="검토 의견 / 반려 사유"
                    value={reasons[request.id] ?? ""}
                    onChange={(event) => setReasons((current) => ({ ...current, [request.id]: event.target.value }))}
                    multiline
                    minRows={2}
                  />
                  <Stack direction="row" sx={{ gap: 1, justifyContent: "flex-end", flexWrap: "wrap" }}>
                    <Button
                      color="error"
                      variant="outlined"
                      startIcon={<CloseRounded />}
                      disabled={decidingId === request.id || !(reasons[request.id] ?? "").trim()}
                      onClick={() => void decide(request, "reject")}
                    >
                      반려
                    </Button>
                    <Button
                      color="success"
                      variant="contained"
                      startIcon={<CheckRounded />}
                      disabled={decidingId === request.id}
                      onClick={() => void decide(request, "approve")}
                    >
                      승인
                    </Button>
                  </Stack>
                </Stack>
              ) : undefined}
            />
          ))}
        </Stack>
      )}
    </SettingsPage>
  );
}
