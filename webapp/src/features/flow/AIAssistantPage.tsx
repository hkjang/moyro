import DeleteSweepRounded from "@mui/icons-material/DeleteSweepRounded";
import SendRounded from "@mui/icons-material/SendRounded";
import SettingsRounded from "@mui/icons-material/SettingsRounded";
import StopCircleRounded from "@mui/icons-material/StopCircleRounded";
import { Alert, Button, Chip, TextField, Typography } from "@mui/material";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useSelector } from "react-redux";
import { useNavigate } from "react-router-dom";
import { moyroMeApi, type PersonalAIPreferences } from "@/api/client";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";
import {
  FlowCard,
  FlowEmpty,
  FlowError,
  FlowLoading,
  FlowPage,
  FlowPrepared,
  FlowSection,
} from "./FlowPage";
import { errorMessage } from "./flow-data";

type ChatTurn = {
  id: string;
  role: "user" | "assistant";
  content: string;
};

function turnID(role: ChatTurn["role"]): string {
  return `${role}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function AIAssistantPage() {
  const navigate = useNavigate();
  const token = useSelector((state: RootState) => state.auth.token);
  const access = useAdminAccess();
  const [preferences, setPreferences] = useState<PersonalAIPreferences | null>(null);
  const [preferencesLoading, setPreferencesLoading] = useState(true);
  const [preferencesError, setPreferencesError] = useState("");
  const [turns, setTurns] = useState<ChatTurn[]>([]);
  const [prompt, setPrompt] = useState("");
  const [streaming, setStreaming] = useState(false);
  const [status, setStatus] = useState("");
  const [requestError, setRequestError] = useState("");
  const controllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    let active = true;
    if (!token) {
      setPreferences(null);
      setPreferencesLoading(false);
      setTurns([]);
      setPrompt("");
      setStatus("");
      setRequestError("");
      return () => { active = false; };
    }
    if (!access.loaded) return () => { active = false; };
    if (!access.can("use_ai")) {
      setPreferences(null);
      setPreferencesLoading(false);
      return () => { active = false; };
    }
    setPreferencesLoading(true);
    setPreferencesError("");
    void moyroMeApi.getAIPreferences(token).then(
      (value) => {
        if (active) setPreferences(value);
      },
      (error: unknown) => {
        if (active) setPreferencesError(errorMessage(error, "AI 개인 설정을 불러오지 못했습니다."));
      },
    ).finally(() => {
      if (active) setPreferencesLoading(false);
    });
    return () => {
      active = false;
      controllerRef.current?.abort();
    };
  }, [access.loaded, access.permissions, token]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const content = prompt.trim();
    if (!token || !content || !preferences?.enabled || streaming) return;

    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    const userTurn: ChatTurn = { id: turnID("user"), role: "user", content };
    const assistantID = turnID("assistant");
    const history = [...turns, userTurn];
    setTurns([...history, { id: assistantID, role: "assistant", content: "" }]);
    setPrompt("");
    setStreaming(true);
    setRequestError("");
    setStatus("AI 응답을 스트리밍으로 받고 있습니다.");
    let received = false;
    try {
      await moyroMeApi.streamAICompletion(
        token,
        {
          model: preferences.model || undefined,
          messages: history.map((turn) => ({ role: turn.role, content: turn.content })),
          max_output_tokens: preferences.max_output_tokens,
          temperature: preferences.temperature,
          stream: true,
        },
        (delta) => {
          received = true;
          setTurns((current) => current.map((turn) => turn.id === assistantID ? { ...turn, content: turn.content + delta } : turn));
        },
        controller.signal,
      );
      if (!received) {
        setTurns((current) => current.filter((turn) => turn.id !== assistantID));
        setRequestError("AI 서버가 텍스트 응답을 반환하지 않았습니다.");
        setStatus("");
      } else {
        setStatus("AI 응답이 완료되었습니다.");
      }
    } catch (error) {
      if (controller.signal.aborted) {
        setTurns((current) => current.filter((turn) => turn.id !== assistantID || Boolean(turn.content)));
        setStatus("요청을 중지했습니다. 이미 받은 내용은 현재 세션에 유지됩니다.");
      } else {
        setTurns((current) => current.filter((turn) => turn.id !== assistantID || Boolean(turn.content)));
        setRequestError(errorMessage(error, "AI 요청에 실패했습니다."));
        setStatus("");
      }
    } finally {
      if (controllerRef.current === controller) controllerRef.current = null;
      setStreaming(false);
    }
  }

  const allowed = access.loaded && access.can("use_ai");
  const ready = allowed && Boolean(preferences?.enabled) && !preferencesError;

  return (
    <FlowPage
      eyebrow="AI"
      title="AI 도우미"
      description="내 AI 권한과 개인 설정을 적용해 서버의 OpenAI 호환 공급자로 실제 스트리밍 요청을 보냅니다."
      actions={
        <>
          <Button startIcon={<SettingsRounded />} onClick={() => navigate("/settings/ai")}>AI 설정</Button>
          <Button startIcon={<DeleteSweepRounded />} onClick={() => { setTurns([]); setStatus(""); setRequestError(""); }} disabled={streaming || turns.length === 0}>대화 지우기</Button>
        </>
      }
    >
      {!access.loaded || preferencesLoading ? <FlowLoading label="AI 사용 조건을 확인하는 중…" /> : !allowed ? (
        <Alert severity="error">
          현재 권한 집합에 <code>use_ai</code>가 없습니다. 권한 조회가 실패한 경우에도 안전하게 차단되므로, 계속 문제가 있으면 관리자에게 권한 상태를 확인하세요.
        </Alert>
      ) : preferencesError ? (
        <FlowError message={preferencesError} />
      ) : preferences?.enabled === false ? (
        <Alert severity="info" action={<Button color="inherit" onClick={() => navigate("/settings/ai")}>설정 열기</Button>}>
          개인 AI 설정이 꺼져 있어 요청을 보내지 않습니다.
        </Alert>
      ) : null}
      {requestError && <FlowError message={requestError} />}
      {status && <Alert severity="info" role="status" aria-live="polite">{status}</Alert>}

      <FlowSection title="대화" description="이 화면을 벗어나면 대화는 저장되지 않습니다." id="ai-conversation">
        <FlowCard className="flow-chat">
          <div className="flow-toolbar">
            <div className="flow-badges">
              <Chip size="small" color={ready ? "success" : "default"} label={ready ? "AI 사용 가능" : "AI 사용 불가"} />
              {preferences?.model && <Chip size="small" variant="outlined" label={`모델 ${preferences.model}`} />}
              {preferences?.provider_id && <Chip size="small" variant="outlined" label={`공급자 ${preferences.provider_id}`} />}
            </div>
            {preferences && <Typography className="flow-item-subtitle">최대 출력 {preferences.max_output_tokens.toLocaleString()} tokens · 창의성 {preferences.temperature.toFixed(1)}</Typography>}
          </div>
          <div className="flow-chat-log" role="log" aria-live="polite" aria-busy={streaming} aria-label="AI 대화 내용">
            {turns.length === 0 ? (
              <FlowEmpty title="새 대화를 시작하세요" description="현재 화면에는 조직 검색이나 채널 지식 주입이 연결되어 있지 않습니다. 입력한 메시지만 모델에 전달됩니다." />
            ) : turns.map((turn) => (
              <article className={`flow-chat-turn flow-chat-${turn.role}`} key={turn.id}>
                <Typography className="flow-chat-role">{turn.role === "user" ? "나" : "AI 도우미"}</Typography>
                <Typography className="flow-chat-body">{turn.content || "응답 수신 중…"}</Typography>
              </article>
            ))}
          </div>
          <form className="flow-chat-form" onSubmit={(event) => void submit(event)}>
            <TextField
              label="AI에게 보낼 메시지"
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              multiline
              minRows={3}
              disabled={!ready || streaming}
              helperText="현재 대화의 텍스트만 전송됩니다. 메시지·파일 검색은 자동으로 수행되지 않습니다."
              slotProps={{ htmlInput: { maxLength: 20_000 } }}
            />
            <div className="flow-list-actions">
              {streaming && <Button color="error" variant="outlined" startIcon={<StopCircleRounded />} onClick={() => controllerRef.current?.abort()}>중지</Button>}
              <Button type="submit" variant="contained" startIcon={<SendRounded />} disabled={!ready || streaming || !prompt.trim()}>보내기</Button>
            </div>
          </form>
        </FlowCard>
      </FlowSection>

      <FlowSection title="신뢰 가능한 결과 범위" id="ai-prepared">
        <div className="flow-card-grid">
          <FlowPrepared title="채널·파일 근거와 Citation" description="RAG 검색·출처 응답 계약이 없어 모델 답변에 임의 링크나 근거 배지를 붙이지 않습니다." />
          <FlowPrepared title="후속 작업과 승인" description="AI 작업 실행 API가 준비될 때 Preview와 승인 요청을 거쳐 실제 동작으로 연결합니다." />
        </div>
      </FlowSection>
    </FlowPage>
  );
}
