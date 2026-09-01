import DescriptionRounded from "@mui/icons-material/DescriptionRounded";
import SmartToyRounded from "@mui/icons-material/SmartToyRounded";
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  Snackbar,
  TextField,
  Typography,
} from "@mui/material";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { moyroMeApi, type PersonalAIPreferences, type Post } from "@/api/client";
import { documentsApi, type DocumentSource } from "@/api/documents";
import { APIError } from "@/api/transport";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import {
  DOCUMENT_TEMPLATES,
  deterministicDocumentDraft,
  documentDraftMessages,
  suggestedDocumentTitle,
  type DocumentTemplate,
} from "./document-draft";

type DocumentCreationContextValue = {
  available: boolean;
  open: (post: Post) => void;
};

const DocumentCreationContext = createContext<DocumentCreationContextValue>({
  available: false,
  open: () => undefined,
});

export function useDocumentCreation() {
  return useContext(DocumentCreationContext);
}

function requestKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `document-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback;
}

function boundRunes(value: string, maximum: number): string {
  const runes = Array.from(value);
  return runes.length <= maximum ? value : runes.slice(0, maximum).join("");
}

export function DocumentCreationProvider({
  token,
  currentUserID,
  children,
}: {
  token: string;
  currentUserID: string;
  children: ReactNode;
}) {
  const access = useAdminAccess();
  const [target, setTarget] = useState<Post | null>(null);
  const [source, setSource] = useState<DocumentSource | null>(null);
  const [template, setTemplate] = useState<DocumentTemplate>("meeting");
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [error, setError] = useState("");
  const [warning, setWarning] = useState("");
  const [feedback, setFeedback] = useState("");
  const [preferences, setPreferences] = useState<PersonalAIPreferences | null>(null);
  const keyRef = useRef("");
  const aiControllerRef = useRef<AbortController | null>(null);

  const available = Boolean(token && currentUserID);
  const open = useCallback((post: Post) => {
    if (!available) return;
    aiControllerRef.current?.abort();
    keyRef.current = requestKey();
    setTarget(post);
    setSource(null);
    setTemplate("meeting");
    setTitle("");
    setBody("");
    setError("");
    setWarning("");
  }, [available]);

  useEffect(() => {
    let active = true;
    if (!available || !access.loaded || !access.can("use_ai")) {
      setPreferences(null);
      return () => { active = false; };
    }
    void moyroMeApi.getAIPreferences(token).then(
      (value) => { if (active) setPreferences(value); },
      () => { if (active) setPreferences(null); },
    );
    return () => { active = false; };
  }, [access.loaded, access.permissions, available, token]);

  useEffect(() => {
    if (!target || !token) return undefined;
    let active = true;
    const controller = new AbortController();
    setLoading(true);
    void documentsApi.source(token, target.id, controller.signal).then(
      (loadedSource) => {
        if (!active) return;
        setSource(loadedSource);
        setTitle(suggestedDocumentTitle(loadedSource, "meeting"));
        setBody(deterministicDocumentDraft(loadedSource, "meeting"));
      },
      (loadError: unknown) => {
        if (active) setError(errorMessage(loadError, "원본 대화를 불러오지 못했습니다."));
      },
    ).finally(() => { if (active) setLoading(false); });
    return () => {
      active = false;
      controller.abort();
    };
  }, [target, token]);

  useEffect(() => () => aiControllerRef.current?.abort(), []);

  const contextValue = useMemo<DocumentCreationContextValue>(() => ({ available, open }), [available, open]);

  function selectTemplate(next: DocumentTemplate) {
    setTemplate(next);
    if (!source) return;
    setTitle(suggestedDocumentTitle(source, next));
    setBody(deterministicDocumentDraft(source, next));
    setWarning("");
  }

  function close() {
    if (saving) return;
    aiControllerRef.current?.abort();
    aiControllerRef.current = null;
    setTarget(null);
  }

  async function generateWithAI() {
    if (!source || !preferences?.enabled || generating || !token) return;
    const controller = new AbortController();
    aiControllerRef.current = controller;
    const fallback = body || deterministicDocumentDraft(source, template);
    let generated = "";
    setGenerating(true);
    setError("");
    setWarning("");
    try {
      await moyroMeApi.streamAICompletion(
        token,
        {
          model: preferences.model || undefined,
          messages: documentDraftMessages(source, template),
          max_output_tokens: preferences.max_output_tokens,
          temperature: preferences.temperature,
          stream: true,
        },
        (delta) => {
          generated = boundRunes(generated + delta, 100_000);
          setBody(generated);
        },
        controller.signal,
      );
      if (!generated.trim()) throw new Error("AI가 문서 초안을 반환하지 않았습니다.");
    } catch (generationError) {
      if (!controller.signal.aborted) {
        setBody(fallback);
        setWarning(`${errorMessage(generationError, "AI 초안 생성에 실패했습니다.")} 기본 초안은 그대로 사용할 수 있습니다.`);
      }
    } finally {
      if (aiControllerRef.current === controller) aiControllerRef.current = null;
      setGenerating(false);
    }
  }

  async function reloadChangedSource(): Promise<void> {
    if (!target) return;
    const latest = await documentsApi.source(token, target.id);
    setSource(latest);
    setTitle(suggestedDocumentTitle(latest, template));
    setBody(deterministicDocumentDraft(latest, template));
    keyRef.current = requestKey();
    setWarning("원본 대화가 바뀌어 최신 내용으로 기본 초안을 다시 만들었습니다. 확인 후 저장하세요.");
  }

  async function submit() {
    if (!target || !source || !title.trim() || !body.trim() || saving) return;
    setSaving(true);
    setError("");
    try {
      const result = await documentsApi.create(token, {
        title: title.trim(),
        body: body.trim(),
        source_post_id: target.id,
        source_cursor_at: source.cursor_at,
        idempotency_key: keyRef.current,
      });
      setTarget(null);
      setFeedback(result.replayed ? "이미 만든 문서를 다시 확인했습니다." : "대화를 문서로 저장했습니다.");
      window.dispatchEvent(new CustomEvent("moyro:document-changed", { detail: result.document }));
    } catch (submitError) {
      if (submitError instanceof APIError && submitError.status === 409) {
        try {
          await reloadChangedSource();
        } catch (reloadError) {
          setError(errorMessage(reloadError, "최신 원본 대화를 다시 불러오지 못했습니다."));
        }
      } else {
        setError(errorMessage(submitError, "문서를 저장하지 못했습니다."));
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <DocumentCreationContext.Provider value={contextValue}>
      {children}
      <Dialog open={Boolean(target)} onClose={close} fullWidth maxWidth="md" aria-labelledby="document-create-title">
        <DialogTitle id="document-create-title" sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <DescriptionRounded color="primary" />
          대화에서 문서 만들기
        </DialogTitle>
        <DialogContent dividers>
          <Box sx={{ display: "grid", gap: 2, pt: 0.5 }}>
            {loading && <Typography role="status">권한을 확인하고 원본 스레드를 불러오는 중…</Typography>}
            {source && (
              <Box sx={{ p: 1.5, borderRadius: 1.5, bgcolor: "action.hover", maxHeight: 180, overflow: "auto" }}>
                <Typography variant="caption" color="text.secondary">
                  서버가 확인한 원본 대화 · {source.posts.length}개 메시지
                </Typography>
                {source.posts.map((post, index) => (
                  <Typography key={post.id} variant="body2" sx={{ mt: 0.75, whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
                    [{`M${index + 1}`}] {post.username || post.user_id}: {post.message || "(내용 없음)"}
                  </Typography>
                ))}
              </Box>
            )}
            <TextField
              select
              label="문서 형식"
              value={template}
              onChange={(event) => selectTemplate(event.target.value as DocumentTemplate)}
              disabled={!source || saving || generating}
            >
              {DOCUMENT_TEMPLATES.map((option) => (
                <MenuItem key={option.value} value={option.value}>{option.label}</MenuItem>
              ))}
            </TextField>
            <TextField
              required
              label="문서 제목"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              disabled={!source || saving}
              slotProps={{ htmlInput: { maxLength: 240 } }}
            />
            <TextField
              required
              label="Markdown 본문"
              value={body}
              onChange={(event) => setBody(event.target.value)}
              multiline
              minRows={12}
              disabled={!source || saving || generating}
              slotProps={{ htmlInput: { maxLength: 100_000 } }}
            />
            {preferences?.enabled && access.can("use_ai") && (
              <Button
                variant="outlined"
                startIcon={<SmartToyRounded />}
                onClick={() => void generateWithAI()}
                disabled={!source || saving || generating}
                sx={{ justifySelf: "start" }}
              >
                {generating ? "AI 초안 생성 중…" : "AI로 초안 생성"}
              </Button>
            )}
            {warning && <Alert severity="warning" role="status">{warning}</Alert>}
            {error && <Alert severity="error" role="alert">{error}</Alert>}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={close} disabled={saving}>취소</Button>
          <Button
            variant="contained"
            onClick={() => void submit()}
            disabled={!source || saving || generating || !title.trim() || !body.trim()}
          >
            {saving ? "저장 중…" : "문서 저장"}
          </Button>
        </DialogActions>
      </Dialog>
      <Snackbar open={Boolean(feedback)} autoHideDuration={5000} onClose={() => setFeedback("")} message={feedback} />
    </DocumentCreationContext.Provider>
  );
}
