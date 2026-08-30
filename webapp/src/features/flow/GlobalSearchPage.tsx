import ArrowBackRounded from "@mui/icons-material/ArrowBackRounded";
import ArrowForwardRounded from "@mui/icons-material/ArrowForwardRounded";
import SearchRounded from "@mui/icons-material/SearchRounded";
import { Alert, Button, Chip, MenuItem, TextField, Typography } from "@mui/material";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useSelector } from "react-redux";
import { useNavigate, useSearchParams } from "react-router-dom";
import { api, compatApi, type Post, type SearchResult, type User } from "@/api/client";
import type { RootState } from "@/store";
import {
  FlowEmpty,
  FlowError,
  FlowLoading,
  FlowPage,
  FlowSection,
} from "./FlowPage";
import { channelPath, errorMessage, formatDateTime, postNavigationState } from "./flow-data";
import { useFlowWorkspaceIndex } from "./FlowDataProvider";

const PAGE_SIZE = 20;

function searchPage(value: string | null): number {
  if (!value || !/^\d+$/.test(value)) return 0;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed <= 10_000 ? parsed : 0;
}

function orderedResults(result: SearchResult): Post[] {
  const ordered = result.order.map((id) => result.posts[id]).filter((post): post is Post => Boolean(post));
  const included = new Set(ordered.map((post) => post.id));
  return [...ordered, ...Object.values(result.posts).filter((post) => !included.has(post.id))];
}

export function GlobalSearchPage() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const token = useSelector((state: RootState) => state.auth.token);
  const workspace = useFlowWorkspaceIndex();
  const [teamId, setTeamId] = useState("");
  const [query, setQuery] = useState("");
  const [submittedQuery, setSubmittedQuery] = useState("");
  const [results, setResults] = useState<Post[]>([]);
  const [users, setUsers] = useState<Record<string, User>>({});
  const [totalHits, setTotalHits] = useState(0);
  const [page, setPage] = useState(0);
  const [searched, setSearched] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [metadataWarning, setMetadataWarning] = useState("");
  const searchGeneration = useRef(0);
  const loadedRouteRef = useRef("");

  useEffect(() => {
    if (workspace.loading || workspace.teams.length === 0) return;
    const requestedTeamID = searchParams.get("team")?.trim() ?? "";
    const targetTeamID = workspace.teams.some((team) => team.id === requestedTeamID)
      ? requestedTeamID
      : workspace.teams[0].id;
    const routeQuery = (searchParams.get("q") ?? "").trim().slice(0, 500);
    const routePage = searchPage(searchParams.get("page"));
    setTeamId(targetTeamID);
    setQuery(routeQuery);

    if (routeQuery && (requestedTeamID !== targetTeamID || searchParams.get("page") !== String(routePage))) {
      const canonical = new URLSearchParams();
      canonical.set("q", routeQuery);
      canonical.set("team", targetTeamID);
      canonical.set("page", String(routePage));
      setSearchParams(canonical, { replace: true });
      return;
    }
    if (!routeQuery) {
      loadedRouteRef.current = "";
      setResults([]);
      setUsers({});
      setTotalHits(0);
      setSearched(false);
      setSubmittedQuery("");
      setPage(0);
      return;
    }
    const signature = `${targetTeamID}\u0000${routeQuery}\u0000${routePage}`;
    if (loadedRouteRef.current === signature) return;
    loadedRouteRef.current = signature;
    void search(routePage, routeQuery, targetTeamID);
  // `search` deliberately stays event-local: the route signature is the
  // stable source of truth and prevents duplicate requests across refreshes.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams, setSearchParams, workspace.loading, workspace.teams]);

  useEffect(() => {
    if (token) return;
    searchGeneration.current += 1;
    setResults([]);
    setUsers({});
    setTotalHits(0);
    setSearched(false);
    setSubmittedQuery("");
    loadedRouteRef.current = "";
  }, [token]);

  async function search(nextPage: number, terms: string, targetTeamID = teamId) {
    if (!token || !targetTeamID || !terms.trim()) return;
    const generation = ++searchGeneration.current;
    setLoading(true);
    setError("");
    setMetadataWarning("");
    try {
      const response = await api.searchPosts(token, targetTeamID, terms.trim(), { page: nextPage, perPage: PAGE_SIZE });
      if (generation !== searchGeneration.current) return;
      const rows = orderedResults(response);
      setResults(rows);
      setTotalHits(response.total_hits ?? rows.length);
      setPage(response.page ?? nextPage);
      setSubmittedQuery(terms.trim());
      setSearched(true);
      const authorIds = [...new Set(rows.map((post) => post.user_id).filter(Boolean))];
      if (authorIds.length === 0) {
        setUsers({});
      } else {
        try {
          const authors = await compatApi.usersByIds(token, authorIds);
          if (generation !== searchGeneration.current) return;
          setUsers(Object.fromEntries(authors.map((user) => [user.id, user])));
        } catch (metadataError) {
          if (generation !== searchGeneration.current) return;
          setUsers({});
          setMetadataWarning(`검색 결과 작성자 이름을 불러오지 못했습니다: ${errorMessage(metadataError, "알 수 없는 오류")}`);
        }
      }
    } catch (searchError) {
      if (generation !== searchGeneration.current) return;
      setResults([]);
      setTotalHits(0);
      setSearched(true);
      setError(errorMessage(searchError, "메시지를 검색하지 못했습니다."));
    } finally {
      if (generation === searchGeneration.current) setLoading(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const terms = query.trim();
    if (!teamId || !terms) return;
    const next = new URLSearchParams();
    next.set("q", terms);
    next.set("team", teamId);
    next.set("page", "0");
    setSearchParams(next);
  }

  function openResultPage(nextPage: number) {
    if (!teamId || !submittedQuery) return;
    const next = new URLSearchParams();
    next.set("q", submittedQuery);
    next.set("team", teamId);
    next.set("page", String(nextPage));
    setSearchParams(next);
  }

  const totalPages = Math.max(1, Math.ceil(totalHits / PAGE_SIZE));
  const selectedTeam = useMemo(() => workspace.teams.find((team) => team.id === teamId), [teamId, workspace.teams]);

  return (
    <FlowPage
      eyebrow="발견"
      title="메시지 검색"
      description="접근 가능한 팀을 선택하고 필요한 대화를 찾습니다."
    >
      {workspace.error && <FlowError message={workspace.error} onRetry={workspace.refresh} />}
      {workspace.warnings.map((warning) => <Alert severity="warning" key={warning}>{warning}</Alert>)}
      {error && <FlowError message={error} />}
      {metadataWarning && <Alert severity="warning">{metadataWarning}</Alert>}

      <FlowSection title="검색 조건" description="현재 계정이 접근할 수 있는 선택한 팀의 메시지만 검색합니다." id="global-search-form">
        <form className="flow-search-form" onSubmit={submit}>
          <TextField
            select
            label="검색할 팀"
            value={teamId}
            onChange={(event) => {
              searchGeneration.current += 1;
              loadedRouteRef.current = "";
              setTeamId(event.target.value);
              setQuery("");
              setResults([]);
              setUsers({});
              setTotalHits(0);
              setSearched(false);
              setSubmittedQuery("");
              setError("");
              setLoading(false);
              const next = new URLSearchParams();
              next.set("team", event.target.value);
              setSearchParams(next);
            }}
            disabled={workspace.loading || workspace.teams.length === 0}
          >
            {workspace.teams.map((team) => <MenuItem value={team.id} key={team.id}>{team.display_name}</MenuItem>)}
          </TextField>
          <TextField
            label="메시지 검색어"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="찾을 단어나 문장을 입력하세요"
            slotProps={{ htmlInput: { maxLength: 500 } }}
          />
          <Button type="submit" variant="contained" startIcon={<SearchRounded />} disabled={!teamId || !query.trim() || loading}>검색</Button>
        </form>
      </FlowSection>

      <FlowSection
        title={searched ? `검색 결과 ${totalHits.toLocaleString()}개` : "검색 결과"}
        description={submittedQuery ? `${selectedTeam?.display_name ?? "선택한 팀"}에서 “${submittedQuery}” 검색` : "검색을 실행하면 접근 가능한 메시지가 표시됩니다."}
        id="global-search-results"
      >
        {workspace.loading ? <FlowLoading label="팀 정보를 불러오는 중…" /> : workspace.teams.length === 0 ? (
          <FlowEmpty title="검색 가능한 팀이 없습니다" description="가입한 팀이 생기면 팀 범위 메시지 검색을 사용할 수 있습니다." />
        ) : loading ? <FlowLoading label="메시지를 검색하는 중…" /> : !searched ? (
          <FlowEmpty title="검색어를 입력하세요" description="현재 계정이 접근할 수 있는 선택 팀의 메시지만 검색됩니다." />
        ) : results.length === 0 && !error ? (
          <FlowEmpty title="검색 결과가 없습니다" description="검색어를 바꾸거나 다른 팀을 선택해 보세요." />
        ) : (
          <div className="flow-list">
            {results.map((post) => {
              const entry = workspace.channelById[post.channel_id];
              const author = users[post.user_id];
              return (
                <article className="flow-list-row" key={post.id}>
                  <div className="flow-list-main">
                    <div className="flow-badges">
                      <Typography component="h3" className="flow-item-title">{author ? `@${author.username}` : "작성자 정보 없음"}</Typography>
                      {entry && <Chip size="small" variant="outlined" label={entry.channel.display_name || entry.channel.name} />}
                    </div>
                    <Typography className="flow-item-message">{post.message || "내용 없는 메시지"}</Typography>
                    <Typography className="flow-item-subtitle">{formatDateTime(post.create_at)}{post.root_id ? " · 스레드 답글" : ""}</Typography>
                  </div>
                  <div className="flow-list-actions">
                    {entry && <Button endIcon={<ArrowForwardRounded />} onClick={() => navigate(channelPath(entry), { state: postNavigationState(post.id) })}>메시지 열기</Button>}
                  </div>
                </article>
              );
            })}
            {totalHits > PAGE_SIZE && (
              <nav className="flow-toolbar" aria-label="검색 결과 페이지">
                <Button startIcon={<ArrowBackRounded />} disabled={loading || page <= 0} onClick={() => openResultPage(page - 1)}>이전</Button>
                <Typography className="flow-item-subtitle">{page + 1} / {totalPages} 페이지</Typography>
                <Button endIcon={<ArrowForwardRounded />} disabled={loading || page + 1 >= totalPages} onClick={() => openResultPage(page + 1)}>다음</Button>
              </nav>
            )}
          </div>
        )}
      </FlowSection>

    </FlowPage>
  );
}
