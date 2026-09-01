import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import DescriptionRounded from "@mui/icons-material/DescriptionRounded";
import OpenInNewRounded from "@mui/icons-material/OpenInNewRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import SaveRounded from "@mui/icons-material/SaveRounded";
import SearchRounded from "@mui/icons-material/SearchRounded";
import SmartToyRounded from "@mui/icons-material/SmartToyRounded";
import { Alert, Box, Button, Chip, Divider, MenuItem, Stack, TextField, Typography } from "@mui/material";
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import ReactMarkdown from "react-markdown";
import { useSelector } from "react-redux";
import { useNavigate } from "react-router-dom";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import { moyroMeApi, type PersonalAIPreferences } from "@/api/client";
import { documentsApi, type DocumentRecord } from "@/api/documents";
import { knowledgeApi, type KnowledgeSource } from "@/api/knowledge";
import { APIError } from "@/api/transport";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";
import { FlowCard, FlowEmpty, FlowError, FlowLoading, FlowPage, FlowSection } from "@/features/flow/FlowPage";
import { useFlowWorkspaceIndex } from "@/features/flow/FlowDataProvider";
import { channelPath, errorMessage, formatDateTime, postNavigationState } from "@/features/flow/flow-data";
import { knowledgeAnswerMessages, resolveCitationSources } from "./citations";
import { deterministicDocumentDraft } from "./document-draft";

function DocumentMarkdown({ body }: { body: string }) {
  return (
    <Box sx={{ overflowWrap: "anywhere", "& img": { display: "none" }, "& pre": { overflow: "auto" } }}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeSanitize]}>{body}</ReactMarkdown>
    </Box>
  );
}

function upsertDocument(documents: DocumentRecord[], updated: DocumentRecord): DocumentRecord[] {
  return [updated, ...documents.filter((document) => document.id !== updated.id)]
    .sort((left, right) => right.update_at - left.update_at || right.id.localeCompare(left.id));
}

export function KnowledgePage() {
  const navigate = useNavigate();
  const token = useSelector((state: RootState) => state.auth.token);
  const currentUserID = useSelector((state: RootState) => state.auth.user?.id ?? "");
  const access = useAdminAccess();
  const workspace = useFlowWorkspaceIndex();
  const [teamID, setTeamID] = useState("");
  const [channelID, setChannelID] = useState("");
  const [query, setQuery] = useState("");
  const [submittedQuery, setSubmittedQuery] = useState("");
  const [sources, setSources] = useState<KnowledgeSource[]>([]);
  const [totalHits, setTotalHits] = useState(0);
  const [answer, setAnswer] = useState("");
  const [searching, setSearching] = useState(false);
  const [answering, setAnswering] = useState(false);
  const [searchError, setSearchError] = useState("");
  const [aiError, setAIError] = useState("");
  const [preferences, setPreferences] = useState<PersonalAIPreferences | null>(null);
  const [preferenceWarning, setPreferenceWarning] = useState("");
  const searchControllerRef = useRef<AbortController | null>(null);
  const answerControllerRef = useRef<AbortController | null>(null);

  const [documents, setDocuments] = useState<DocumentRecord[]>([]);
  const [documentsLoading, setDocumentsLoading] = useState(true);
  const [documentsError, setDocumentsError] = useState("");
  const [selected, setSelected] = useState<DocumentRecord | null>(null);
  const [editTitle, setEditTitle] = useState("");
  const [editBody, setEditBody] = useState("");
  const [pendingSourceCursor, setPendingSourceCursor] = useState<number | null>(null);
  const [documentStatus, setDocumentStatus] = useState("");
  const [documentError, setDocumentError] = useState("");
  const [documentSaving, setDocumentSaving] = useState(false);

  const channelEntries = useMemo(
    () => workspace.entries.filter((entry) => entry.team.id === teamID),
    [teamID, workspace.entries],
  );

  useEffect(() => {
    if (!teamID && workspace.teams[0]) setTeamID(workspace.teams[0].id);
    if (teamID && !workspace.teams.some((team) => team.id === teamID)) {
      setTeamID(workspace.teams[0]?.id ?? "");
      setChannelID("");
    }
  }, [teamID, workspace.teams]);

  useEffect(() => {
    let active = true;
    if (!token || !access.loaded || !access.can("use_ai")) {
      setPreferences(null);
      return () => { active = false; };
    }
    setPreferenceWarning("");
    void moyroMeApi.getAIPreferences(token).then(
      (value) => { if (active) setPreferences(value); },
      () => {
        if (!active) return;
        setPreferences(null);
        setPreferenceWarning("AI 설정을 불러오지 못했습니다. 원문 지식 검색은 계속 사용할 수 있습니다.");
      },
    );
    return () => { active = false; };
  }, [access.loaded, access.permissions, token]);

  const loadDocuments = useCallback(async (signal?: AbortSignal) => {
    if (!token) {
      setDocuments([]);
      setDocumentsLoading(false);
      return;
    }
    setDocumentsLoading(true);
    setDocumentsError("");
    try {
      setDocuments(await documentsApi.list(token, 100, signal));
    } catch (loadError) {
      if (!signal?.aborted) setDocumentsError(errorMessage(loadError, "문서 목록을 불러오지 못했습니다."));
    } finally {
      if (!signal?.aborted) setDocumentsLoading(false);
    }
  }, [token]);

  useEffect(() => {
    const controller = new AbortController();
    void loadDocuments(controller.signal);
    const refresh = () => { void loadDocuments(); };
    window.addEventListener("moyro:document-changed", refresh);
    return () => {
      controller.abort();
      window.removeEventListener("moyro:document-changed", refresh);
    };
  }, [loadDocuments]);

  useEffect(() => () => {
    searchControllerRef.current?.abort();
    answerControllerRef.current?.abort();
  }, []);

  function applySelected(document: DocumentRecord) {
    setSelected(document);
    setEditTitle(document.title);
    setEditBody(document.body);
    setPendingSourceCursor(null);
    setDocumentError("");
    setDocumentStatus("");
    setDocuments((current) => upsertDocument(current, document));
  }

  async function openDocument(documentID: string) {
    if (!token) return;
    setDocumentError("");
    try {
      applySelected(await documentsApi.get(token, documentID));
    } catch (openError) {
      setDocumentError(errorMessage(openError, "문서를 열지 못했습니다."));
    }
  }

  async function generateAnswer(searchQuery: string, filteredSources: KnowledgeSource[]) {
    if (!token || !preferences?.enabled || !access.can("use_ai") || filteredSources.length === 0) return;
    const controller = new AbortController();
    answerControllerRef.current?.abort();
    answerControllerRef.current = controller;
    let generated = "";
    setAnswer("");
    setAnswering(true);
    setAIError("");
    try {
      await moyroMeApi.streamAICompletion(
        token,
        {
          model: preferences.model || undefined,
          messages: knowledgeAnswerMessages(searchQuery, filteredSources),
          max_output_tokens: preferences.max_output_tokens,
          temperature: preferences.temperature,
          stream: true,
        },
        (delta) => {
          generated += delta;
          setAnswer(generated);
        },
        controller.signal,
      );
      if (!generated.trim()) throw new Error("AI가 답변을 반환하지 않았습니다.");
    } catch (generationError) {
      if (!controller.signal.aborted) {
        setAnswer("");
        setAIError(`${errorMessage(generationError, "AI 답변 생성에 실패했습니다.")} 권한이 확인된 원문 결과는 아래에서 계속 확인할 수 있습니다.`);
      }
    } finally {
      if (answerControllerRef.current === controller) {
        answerControllerRef.current = null;
        setAnswering(false);
      }
    }
  }

  async function submitSearch(event: FormEvent) {
    event.preventDefault();
    const normalizedQuery = query.trim();
    if (!token || !teamID || !normalizedQuery || searching) return;
    searchControllerRef.current?.abort();
    answerControllerRef.current?.abort();
    const controller = new AbortController();
    searchControllerRef.current = controller;
    setSearching(true);
    setSearchError("");
    setAIError("");
    setAnswer("");
    setSources([]);
    setTotalHits(0);
    setSubmittedQuery(normalizedQuery);
    try {
      const result = await knowledgeApi.search(token, {
        query: normalizedQuery,
        team_id: teamID,
        channel_id: channelID || undefined,
        limit: 20,
      }, controller.signal);
      setSources(result.sources);
      setTotalHits(result.total_hits);
      void generateAnswer(normalizedQuery, result.sources);
    } catch (loadError) {
      if (!controller.signal.aborted) setSearchError(errorMessage(loadError, "지식 검색에 실패했습니다."));
    } finally {
      if (searchControllerRef.current === controller) searchControllerRef.current = null;
      setSearching(false);
    }
  }

  const citationResolution = useMemo(() => resolveCitationSources(answer, sources), [answer, sources]);
  const citedRefs = useMemo(() => new Set(citationResolution.cited.map((source) => source.ref)), [citationResolution.cited]);

  function openSource(source: KnowledgeSource) {
    if (source.kind === "document" && source.document_id) {
      void openDocument(source.document_id);
      return;
    }
    const entry = workspace.channelById[source.channel_id];
    if (entry && source.post_id) {
      navigate(channelPath(entry), { state: postNavigationState(source.post_id) });
    }
  }

  async function prepareStaleRefresh() {
    if (!token || !selected || documentSaving) return;
    setDocumentSaving(true);
    setDocumentError("");
    try {
      const source = await documentsApi.source(token, selected.source_thread_id);
      setEditBody(deterministicDocumentDraft(source, "project"));
      setPendingSourceCursor(source.cursor_at);
      setDocumentStatus("최신 원본으로 새 초안을 만들었습니다. 내용을 검토한 뒤 저장하세요.");
    } catch (refreshError) {
      setDocumentError(errorMessage(refreshError, "최신 원본을 불러오지 못했습니다."));
    } finally {
      setDocumentSaving(false);
    }
  }

  async function saveDocument() {
    if (!token || !selected || selected.created_by !== currentUserID || !editTitle.trim() || !editBody.trim()) return;
    setDocumentSaving(true);
    setDocumentError("");
    setDocumentStatus("");
    try {
      const updated = await documentsApi.patch(token, selected.id, {
        title: editTitle.trim(),
        body: editBody.trim(),
        expected_revision: selected.revision,
        ...(pendingSourceCursor == null ? {} : { source_cursor_at: pendingSourceCursor }),
      });
      applySelected(updated);
      setDocumentStatus("문서를 저장했습니다.");
    } catch (saveError) {
      if (saveError instanceof APIError && saveError.status === 409) {
        try {
          applySelected(await documentsApi.get(token, selected.id));
          setDocumentError("원본 대화 또는 문서 revision이 변경되었습니다. 최신 문서를 다시 불러왔습니다.");
        } catch {
          void loadDocuments();
        }
      } else {
        setDocumentError(errorMessage(saveError, "문서를 저장하지 못했습니다."));
      }
    } finally {
      setDocumentSaving(false);
    }
  }

  async function deleteDocument() {
    if (!token || !selected || selected.created_by !== currentUserID || documentSaving) return;
    if (!window.confirm(`“${selected.title}” 문서를 삭제할까요?`)) return;
    setDocumentSaving(true);
    setDocumentError("");
    try {
      await documentsApi.remove(token, selected.id, selected.revision);
      setDocuments((current) => current.filter((document) => document.id !== selected.id));
      setSelected(null);
      setDocumentStatus("문서를 삭제했습니다.");
    } catch (removeError) {
      setDocumentError(errorMessage(removeError, "문서를 삭제하지 못했습니다."));
    } finally {
      setDocumentSaving(false);
    }
  }

  return (
    <FlowPage
      eyebrow="OFFLINE KNOWLEDGE"
      title="지식 검색"
      description="내가 현재 참여 중인 채널의 메시지와 문서만 검색하고, 선택적으로 출처가 붙은 AI 답변을 만듭니다."
    >
      <FlowSection title="질문하기" description="검색 결과는 AI 설정과 관계없이 PostgreSQL에서 바로 제공됩니다.">
        <FlowCard>
          <Box component="form" onSubmit={(event) => void submitSearch(event)} sx={{ display: "grid", gap: 2 }}>
            <Stack direction={{ xs: "column", md: "row" }} spacing={1.5}>
              <TextField
                select
                label="팀"
                value={teamID}
                onChange={(event) => { setTeamID(event.target.value); setChannelID(""); }}
                sx={{ minWidth: 180 }}
                disabled={workspace.loading || workspace.teams.length === 0}
              >
                {workspace.teams.map((team) => <MenuItem key={team.id} value={team.id}>{team.display_name}</MenuItem>)}
              </TextField>
              <TextField
                select
                label="채널 범위"
                value={channelID}
                onChange={(event) => setChannelID(event.target.value)}
                sx={{ minWidth: 200 }}
                disabled={!teamID}
              >
                <MenuItem value="">참여 중인 모든 채널</MenuItem>
                {channelEntries.map((entry) => (
                  <MenuItem key={entry.channel.id} value={entry.channel.id}>#{entry.channel.display_name}</MenuItem>
                ))}
              </TextField>
              <TextField
                fullWidth
                label="질문 또는 검색어"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                slotProps={{ htmlInput: { maxLength: 500 } }}
              />
              <Button type="submit" variant="contained" startIcon={<SearchRounded />} disabled={!teamID || !query.trim() || searching}>
                {searching ? "검색 중…" : "검색"}
              </Button>
            </Stack>
            {workspace.error && <FlowError message={workspace.error} onRetry={workspace.refresh} />}
            {searchError && <FlowError message={searchError} />}
          </Box>
        </FlowCard>
      </FlowSection>

      {(submittedQuery || searching) && (
        <FlowSection title="답변" description="답변의 [M#]/[D#]는 아래 서버 검증 출처 카드와 일치합니다.">
          {answering && <Alert severity="info" icon={<SmartToyRounded />}>출처를 바탕으로 AI 답변을 생성하는 중…</Alert>}
          {preferenceWarning && <Alert severity="warning">{preferenceWarning}</Alert>}
          {aiError && <Alert severity="warning" role="alert">{aiError}</Alert>}
          {!preferences?.enabled && sources.length > 0 && !aiError && (
            <Alert severity="info">AI가 꺼져 있어 원문 검색 결과만 표시합니다.</Alert>
          )}
          {answer && (
            <FlowCard>
              <DocumentMarkdown body={answer} />
              {citationResolution.unknown.length > 0 && (
                <Alert severity="warning">검증되지 않은 출처 표기는 카드로 연결하지 않았습니다: {citationResolution.unknown.join(", ")}</Alert>
              )}
            </FlowCard>
          )}
        </FlowSection>
      )}

      {(submittedQuery || searching) && (
        <FlowSection title={`검증된 출처 ${sources.length}개`} description={totalHits > sources.length ? `전체 ${totalHits}건 중 상위 ${sources.length}건` : undefined}>
          {searching && <FlowLoading label="권한이 있는 메시지와 문서를 검색하는 중…" />}
          {!searching && !searchError && sources.length === 0 && (
            <FlowEmpty title="검색 결과가 없습니다" description="다른 검색어나 채널 범위를 사용해 보세요." />
          )}
          <Box sx={{ display: "grid", gridTemplateColumns: { xs: "1fr", lg: "repeat(2, minmax(0, 1fr))" }, gap: 1.5 }}>
            {sources.map((source) => {
              const entry = workspace.channelById[source.channel_id];
              const canOpen = source.kind === "document" ? Boolean(source.document_id) : Boolean(entry && source.post_id);
              return (
                <FlowCard key={`${source.kind}:${source.id}`}>
                  <Stack direction="row" spacing={1} sx={{ alignItems: "center", flexWrap: "wrap" }}>
                    <Chip color="primary" label={`[${source.ref}]`} data-source-ref={source.ref} />
                    <Chip size="small" variant="outlined" label={source.kind === "document" ? "문서" : "메시지"} />
                    {citedRefs.has(source.ref) && <Chip size="small" color="success" label="답변 인용" />}
                    <Typography variant="caption" color="text.secondary">{entry ? `#${entry.channel.display_name}` : source.channel_id}</Typography>
                  </Stack>
                  {source.title && <Typography component="h3" variant="subtitle1" sx={{ mt: 1 }}>{source.title}</Typography>}
                  <Typography sx={{ mt: 1, whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{source.excerpt}</Typography>
                  <Stack direction="row" sx={{ justifyContent: "space-between", alignItems: "center", mt: 1.5 }}>
                    <Typography variant="caption" color="text.secondary">
                      {source.author_name || source.author_id} · {formatDateTime(source.update_at || source.create_at)}
                    </Typography>
                    <Button size="small" endIcon={<OpenInNewRounded />} disabled={!canOpen} onClick={() => openSource(source)}>
                      원문 열기
                    </Button>
                  </Stack>
                </FlowCard>
              );
            })}
          </Box>
        </FlowSection>
      )}

      <FlowSection title="대화에서 만든 문서" description="원본 대화가 바뀐 문서는 stale 상태로 표시되며, 저장할 때 revision을 다시 검증합니다." action={<Button onClick={() => void loadDocuments()} startIcon={<RefreshRounded />}>새로고침</Button>}>
        {documentsLoading && <FlowLoading label="문서를 불러오는 중…" />}
        {documentsError && <FlowError message={documentsError} onRetry={() => void loadDocuments()} />}
        {!documentsLoading && !documentsError && documents.length === 0 && (
          <FlowEmpty title="아직 만든 문서가 없습니다" description="메시지의 더보기 메뉴에서 ‘대화에서 문서 만들기’를 선택하세요." />
        )}
        <Stack spacing={1}>
          {documents.map((document) => (
            <Button
              key={document.id}
              variant={selected?.id === document.id ? "contained" : "outlined"}
              color={document.stale ? "warning" : "primary"}
              onClick={() => void openDocument(document.id)}
              sx={{ justifyContent: "space-between", textAlign: "left" }}
              startIcon={<DescriptionRounded />}
            >
              <span>{document.title}</span>
              <span>{document.stale ? "원본 변경됨" : `rev ${document.revision}`}</span>
            </Button>
          ))}
        </Stack>
        {documentError && <FlowError message={documentError} />}
        {documentStatus && <Alert severity="info" role="status">{documentStatus}</Alert>}
        {selected && (
          <FlowCard>
            <Stack direction="row" spacing={1} sx={{ justifyContent: "space-between", alignItems: "center" }}>
              <Box>
                <Typography component="h3" variant="h6">{selected.title}</Typography>
                <Typography variant="caption" color="text.secondary">
                  revision {selected.revision} · {formatDateTime(selected.update_at)}
                </Typography>
              </Box>
              {selected.stale && <Chip color="warning" label="원본 대화 변경됨" />}
            </Stack>
            <Divider sx={{ my: 2 }} />
            {selected.created_by === currentUserID ? (
              <Stack spacing={2}>
                <TextField label="제목" value={editTitle} onChange={(event) => setEditTitle(event.target.value)} slotProps={{ htmlInput: { maxLength: 240 } }} />
                <TextField label="Markdown 본문" value={editBody} onChange={(event) => setEditBody(event.target.value)} multiline minRows={12} slotProps={{ htmlInput: { maxLength: 100_000 } }} />
                {pendingSourceCursor != null && <Alert severity="warning">최신 원본 revision을 반영할 준비가 됐습니다. 저장 전 내용을 검토하세요.</Alert>}
                <Stack direction={{ xs: "column", sm: "row" }} spacing={1}>
                  {selected.stale && (
                    <Button variant="outlined" color="warning" startIcon={<RefreshRounded />} onClick={() => void prepareStaleRefresh()} disabled={documentSaving}>
                      최신 원본으로 초안 만들기
                    </Button>
                  )}
                  <Button variant="contained" startIcon={<SaveRounded />} onClick={() => void saveDocument()} disabled={documentSaving || !editTitle.trim() || !editBody.trim()}>
                    revision 확인 후 저장
                  </Button>
                  <Button color="error" startIcon={<DeleteOutlineRounded />} onClick={() => void deleteDocument()} disabled={documentSaving}>
                    삭제
                  </Button>
                </Stack>
                <Divider />
                <Typography variant="subtitle2">미리보기</Typography>
                <DocumentMarkdown body={editBody} />
              </Stack>
            ) : (
              <DocumentMarkdown body={selected.body} />
            )}
          </FlowCard>
        )}
      </FlowSection>
    </FlowPage>
  );
}
