import CheckRounded from "@mui/icons-material/CheckRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import ContentCopyRounded from "@mui/icons-material/ContentCopyRounded";
import DeleteSweepRounded from "@mui/icons-material/DeleteSweepRounded";
import EditRounded from "@mui/icons-material/EditRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import SendRounded from "@mui/icons-material/SendRounded";
import SettingsRounded from "@mui/icons-material/SettingsRounded";
import StopCircleRounded from "@mui/icons-material/StopCircleRounded";
import { Alert, Button, Chip, TextField, Typography } from "@mui/material";
import { useEffect, useRef, useState, type FormEvent } from "react";
import ReactMarkdown from "react-markdown";
import { useSelector } from "react-redux";
import { useNavigate } from "react-router-dom";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import { moyroMeApi, type PersonalAIPreferences } from "@/api/client";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";
import {
  FlowCard,
  FlowEmpty,
  FlowError,
  FlowLoading,
  FlowPage,
  FlowSection,
} from "./FlowPage";
import { errorMessage } from "./flow-data";
import "./ai-assistant.css";

export type ChatTurn = {
  id: string;
  role: "user" | "assistant";
  content: string;
};

export const AI_CONTEXT_LIMITS = {
  turns: 24,
  characters: 48_000,
} as const;

// Provider tokenizers are deliberately kept on the server. The browser still
// bounds the conversation before transmission so a long-lived page cannot
// grow requests without limit. Newest turns win; a single oversized newest
// turn is kept from its tail, where the user's current instruction lives.
export function boundedAIHistory(history: ChatTurn[]): ChatTurn[] {
  const selected: ChatTurn[] = [];
  let characters = 0;
  for (let index = history.length - 1; index >= 0 && selected.length < AI_CONTEXT_LIMITS.turns; index -= 1) {
    const turn = history[index];
    const content = turn.content.trim();
    if (!content) continue;
    const remaining = AI_CONTEXT_LIMITS.characters - characters;
    if (remaining <= 0) break;
    const codePoints = Array.from(content);
    const boundedContent = codePoints.length > remaining
      ? codePoints.slice(codePoints.length - remaining).join("")
      : content;
    selected.unshift({ ...turn, content: boundedContent });
    characters += Math.min(codePoints.length, remaining);
    if (codePoints.length > remaining) break;
  }
  // Starting a clipped request with an orphaned assistant response gives the
  // provider misleading context. Drop it when a later user turn is present.
  while (selected.length > 1 && selected[0].role === "assistant") selected.shift();
  return selected;
}

const PROMPT_TEMPLATES = [
  { label: "요약", prompt: "다음 내용을 핵심과 후속 조치 중심으로 간결하게 요약해 주세요.\n\n" },
  { label: "영어 번역", prompt: "다음 내용을 자연스러운 업무용 영어로 번역해 주세요. 원문의 의미와 어조를 유지해 주세요.\n\n" },
  { label: "검토", prompt: "다음 내용을 검토하고 모호한 점, 위험, 개선 제안을 구분해 알려 주세요.\n\n" },
  { label: "보고서 작성", prompt: "다음 내용을 제목, 핵심 요약, 상세 내용, 후속 조치가 있는 업무 보고서로 정리해 주세요.\n\n" },
] as const;

function turnID(role: ChatTurn["role"]): string {
  return `${role}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

async function copyToClipboard(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  try {
    if (!document.execCommand("copy")) throw new Error("copy command was rejected");
  } finally {
    textarea.remove();
  }
}

function AssistantMarkdown({ source }: { source: string }) {
  return (
    <div className="flow-chat-body flow-ai-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeSanitize]}
        components={{
          a: ({ href, children, ...rest }) => (
            <a {...rest} href={href} target="_blank" rel="noopener noreferrer">{children}</a>
          ),
          img: ({ alt }) => <span>{alt ?? ""}</span>,
          h1: ({ children }) => <h3>{children}</h3>,
          h2: ({ children }) => <h4>{children}</h4>,
          h3: ({ children }) => <h5>{children}</h5>,
          h4: ({ children }) => <h6>{children}</h6>,
          pre: ({ children }) => <pre className="flow-ai-code-block">{children}</pre>,
          code: ({ className, children, ...rest }) => (
            <code className={className ? `flow-ai-code ${className}` : "flow-ai-code-inline"} {...rest}>{children}</code>
          ),
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  );
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
  const [editingTurnID, setEditingTurnID] = useState("");
  const [editingContent, setEditingContent] = useState("");
  const controllerRef = useRef<AbortController | null>(null);
  const chatLogRef = useRef<HTMLDivElement | null>(null);
  const promptInputRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    let active = true;
    if (!token) {
      setPreferences(null);
      setPreferencesLoading(false);
      setTurns([]);
      setPrompt("");
      setStatus("");
      setRequestError("");
      setEditingTurnID("");
      setEditingContent("");
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

  useEffect(() => {
    const log = chatLogRef.current;
    if (!log || turns.length === 0) return;
    const top = log.scrollHeight;
    if (typeof log.scrollTo === "function") {
      log.scrollTo({ top, behavior: streaming ? "auto" : "smooth" });
    } else {
      log.scrollTop = top;
    }
  }, [streaming, turns]);

  async function requestCompletion(history: ChatTurn[]) {
    if (!token || history.length === 0 || !preferences?.enabled || streaming || controllerRef.current) return;
    const controller = new AbortController();
    const requestHistory = boundedAIHistory(history);
    controllerRef.current = controller;
    const assistantID = turnID("assistant");
    setTurns([...history, { id: assistantID, role: "assistant", content: "" }]);
    setStreaming(true);
    setRequestError("");
    setStatus(requestHistory.length < history.length
      ? `최근 대화 ${requestHistory.length}개만 안전한 범위로 전송하고 AI 응답을 받고 있습니다.`
      : "AI 응답을 스트리밍으로 받고 있습니다.");
    let received = false;
    try {
      await moyroMeApi.streamAICompletion(
        token,
        {
          model: preferences.model || undefined,
          messages: requestHistory.map((turn) => ({ role: turn.role, content: turn.content })),
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

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const content = prompt.trim();
    if (!content) return;
    const history = [...turns, { id: turnID("user"), role: "user" as const, content }];
    setPrompt("");
    await requestCompletion(history);
  }

  async function regenerateLastResponse() {
    if (streaming || turns.length < 2 || turns[turns.length - 1]?.role !== "assistant") return;
    await requestCompletion(turns.slice(0, -1));
  }

  async function resubmitEditedTurn(event: FormEvent<HTMLFormElement>, turnIndex: number) {
    event.preventDefault();
    const content = editingContent.trim();
    const current = turns[turnIndex];
    if (!content || !current || current.role !== "user") return;
    setEditingTurnID("");
    setEditingContent("");
    await requestCompletion([
      ...turns.slice(0, turnIndex),
      { ...current, content },
    ]);
  }

  async function copyResponse(content: string) {
    try {
      await copyToClipboard(content);
      setRequestError("");
      setStatus("AI 응답을 클립보드에 복사했습니다.");
    } catch {
      setRequestError("응답을 복사하지 못했습니다. 브라우저의 클립보드 권한을 확인하세요.");
    }
  }

  function applyTemplate(template: string) {
    setPrompt((current) => `${template}${current.trimStart()}`);
    window.setTimeout(() => promptInputRef.current?.focus(), 0);
  }

  const allowed = access.loaded && access.can("use_ai");
  const ready = allowed && Boolean(preferences?.enabled) && !preferencesError;

  return (
    <FlowPage
      eyebrow="AI"
      title="AI 대화"
      description="관리자가 허용한 AI 모델과 내 개인 설정을 사용해 대화합니다."
      actions={
        <>
          <Button startIcon={<SettingsRounded />} onClick={() => navigate("/settings/ai")}>AI 설정</Button>
          <Button startIcon={<DeleteSweepRounded />} onClick={() => { setTurns([]); setStatus(""); setRequestError(""); }} disabled={streaming || turns.length === 0}>대화 지우기</Button>
        </>
      }
    >
      {!access.loaded || preferencesLoading ? <FlowLoading label="AI 사용 조건을 확인하는 중…" /> : !allowed ? (
        <Alert severity="error">
          현재 계정에는 AI 사용 권한이 없습니다. 권한을 확인하지 못한 경우에도 안전하게 차단되므로, 계속 문제가 있으면 관리자에게 문의하세요.
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
            </div>
            {preferences && (
              <details className="flow-ai-details">
                <summary>AI 사용 세부 정보</summary>
                <dl>
                  <div><dt>모델</dt><dd>{preferences.model || "관리자 기본값"}</dd></div>
                  <div><dt>공급자</dt><dd>{preferences.provider_id || "관리자 기본값"}</dd></div>
                  <div><dt>최대 출력</dt><dd>{preferences.max_output_tokens.toLocaleString()} tokens</dd></div>
                  <div><dt>대화 범위</dt><dd>최근 {AI_CONTEXT_LIMITS.turns}개 · {AI_CONTEXT_LIMITS.characters.toLocaleString()}자 이하</dd></div>
                  <div><dt>창의성</dt><dd>{preferences.temperature.toFixed(1)}</dd></div>
                </dl>
              </details>
            )}
          </div>
          <div
            className="flow-chat-log"
            ref={chatLogRef}
            role="log"
            aria-live={streaming ? "off" : "polite"}
            aria-busy={streaming}
            aria-label="AI 대화 내용"
          >
            {turns.length === 0 ? (
              <FlowEmpty title="새 대화를 시작하세요" description="채널이나 파일은 자동으로 참고하지 않습니다. 입력한 내용만 AI에 전달됩니다." />
            ) : turns.map((turn, turnIndex) => (
              <article
                className={`flow-chat-turn flow-chat-${turn.role}`}
                key={turn.id}
                aria-label={turn.role === "user" ? "내 메시지" : "AI 도우미 응답"}
              >
                <Typography className="flow-chat-role">{turn.role === "user" ? "나" : "AI 도우미"}</Typography>
                {turn.role === "assistant" ? (
                  turn.content ? <AssistantMarkdown source={turn.content} /> : <Typography className="flow-chat-body">응답 수신 중…</Typography>
                ) : editingTurnID === turn.id ? (
                  <form className="flow-chat-edit-form" onSubmit={(event) => void resubmitEditedTurn(event, turnIndex)}>
                    <TextField
                      label="사용자 메시지 수정"
                      value={editingContent}
                      onChange={(event) => setEditingContent(event.target.value)}
                      multiline
                      minRows={2}
                      autoFocus
                      slotProps={{ htmlInput: { maxLength: 20_000, "aria-label": "사용자 메시지 수정" } }}
                    />
                    <div className="flow-chat-turn-actions">
                      <Button type="button" size="small" startIcon={<CloseRounded />} onClick={() => { setEditingTurnID(""); setEditingContent(""); }}>취소</Button>
                      <Button type="submit" size="small" variant="contained" startIcon={<CheckRounded />} disabled={!editingContent.trim()}>수정하여 전송</Button>
                    </div>
                  </form>
                ) : <Typography className="flow-chat-body">{turn.content}</Typography>}
                {turn.content && editingTurnID !== turn.id && (
                  <div className="flow-chat-turn-actions">
                    {turn.role === "assistant" ? (
                      <>
                        <Button size="small" startIcon={<ContentCopyRounded />} aria-label={`${turnIndex + 1}번째 AI 응답 복사`} onClick={() => void copyResponse(turn.content)}>응답 복사</Button>
                        {turnIndex === turns.length - 1 && (
                          <Button size="small" startIcon={<RefreshRounded />} onClick={() => void regenerateLastResponse()} disabled={streaming}>다시 생성</Button>
                        )}
                      </>
                    ) : (
                      <Button
                        size="small"
                        startIcon={<EditRounded />}
                        aria-label={`${turnIndex + 1}번째 내 메시지 수정 후 재전송`}
                        onClick={() => { setEditingTurnID(turn.id); setEditingContent(turn.content); }}
                        disabled={streaming}
                      >
                        수정 후 재전송
                      </Button>
                    )}
                  </div>
                )}
              </article>
            ))}
          </div>
          <form className="flow-chat-form" onSubmit={(event) => void submit(event)}>
            <div className="flow-ai-templates" role="group" aria-label="프롬프트 템플릿">
              <Typography component="span" className="flow-item-subtitle">빠른 시작</Typography>
              {PROMPT_TEMPLATES.map((template) => (
                <Button key={template.label} type="button" size="small" variant="outlined" onClick={() => applyTemplate(template.prompt)} disabled={!ready || streaming}>
                  {template.label}
                </Button>
              ))}
            </div>
            <TextField
              label="AI에게 보낼 메시지"
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              onKeyDown={(event) => {
                if ((event.ctrlKey || event.metaKey) && event.key === "Enter" && !event.nativeEvent.isComposing) {
                  event.preventDefault();
                  event.currentTarget.closest("form")?.requestSubmit();
                }
              }}
              inputRef={promptInputRef}
              multiline
              minRows={3}
              disabled={!ready || streaming}
              helperText="현재 대화의 텍스트만 전송됩니다. 메시지·파일 검색은 자동으로 수행되지 않습니다. Ctrl 또는 Cmd + Enter로도 보낼 수 있습니다."
              slotProps={{ htmlInput: { maxLength: 20_000, "aria-label": "AI에게 보낼 메시지" } }}
            />
            <div className="flow-list-actions">
              {streaming && <Button color="error" variant="outlined" startIcon={<StopCircleRounded />} onClick={() => controllerRef.current?.abort()}>중지</Button>}
              <Button type="submit" variant="contained" startIcon={<SendRounded />} disabled={!ready || streaming || !prompt.trim()}>보내기</Button>
            </div>
          </form>
        </FlowCard>
      </FlowSection>

    </FlowPage>
  );
}
