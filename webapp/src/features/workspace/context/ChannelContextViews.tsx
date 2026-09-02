import { useState } from "react";
import DownloadRounded from "@mui/icons-material/DownloadRounded";
import type { Channel, ChannelStats, FileInfo, Post, Team } from "@/api/client";
import { api } from "@/api/client";
import { downloadAuthenticatedMedia } from "@/components/AuthenticatedMedia";
import { formatDateTime } from "@/lib/time";

export type ChannelSummarySource = {
  ref: string;
  postId: string;
  author: string;
  message: string;
  createAt: number;
};

export type ChannelFileEntry = {
  file: FileInfo;
  post: Post;
  author: string;
};

type JumpHandler = (postId: string) => void;

function formatBytes(bytes: number): string {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / (1024 ** unit);
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function SourceLink({ postId, children, onJumpToPost }: {
  postId: string;
  children: React.ReactNode;
  onJumpToPost: JumpHandler;
}) {
  return (
    <a
      href={`#channel-post-${encodeURIComponent(postId)}`}
      className="context-source-link"
      onClick={(event) => {
        event.preventDefault();
        onJumpToPost(postId);
      }}
    >
      {children}
    </a>
  );
}

function SummarySourceList({ sources, onJumpToPost }: {
  sources: ChannelSummarySource[];
  onJumpToPost: JumpHandler;
}) {
  return (
    <ol>
      {sources.map((source) => (
        <li key={source.postId}>
          <SourceLink postId={source.postId} onJumpToPost={onJumpToPost}>
            <strong>[{source.ref}] {source.author}</strong>
            <time dateTime={new Date(source.createAt).toISOString()}>{formatDateTime(source.createAt)}</time>
            <span>{source.message}</span>
          </SourceLink>
        </li>
      ))}
    </ol>
  );
}

export function ChannelSummaryView({
  permissionLoaded,
  canUseAI,
  unavailableReason,
  availableMessageCount,
  output,
  sources,
  generatedAt,
  streaming,
  error,
  onRun,
  onStop,
  onJumpToPost,
}: {
  permissionLoaded: boolean;
  canUseAI: boolean;
  unavailableReason?: string;
  availableMessageCount: number;
  output: string;
  sources: ChannelSummarySource[];
  generatedAt: number | null;
  streaming: boolean;
  error: string;
  onRun: () => void;
  onStop: () => void;
  onJumpToPost: JumpHandler;
}) {
  const citedRefs = new Set(
    Array.from(output.matchAll(/\[(M\d+)\]/gi), (match) => match[1].toUpperCase()),
  );
  const validRefs = new Set(sources.map((source) => source.ref.toUpperCase()));
  const citedSources = sources.filter((source) => citedRefs.has(source.ref.toUpperCase()));
  const invalidRefs = [...citedRefs].filter((ref) => !validRefs.has(ref));
  const hasUncitedClaims = output.split(/\r?\n/).some((line) => {
    const normalized = line.trim().replace(/^[-*•#\d.)\s]+/, "").trim();
    if (!normalized || /^.{1,18}:?$/.test(normalized) && !/[.!?。]$/.test(normalized)) return false;
    const lineRefs = Array.from(normalized.matchAll(/\[(M\d+)\]/gi), (match) => match[1].toUpperCase());
    return lineRefs.length === 0 || !lineRefs.some((ref) => validRefs.has(ref));
  });

  return (
    <section className="context-view context-summary-view" aria-label="채널 AI 요약">
      <div className="context-view-heading">
        <div>
          <h3>최근 대화 요약</h3>
          <p>버튼을 눌렀을 때만 현재 로드된 최근 메시지를 AI에 전송합니다.</p>
        </div>
      </div>

      {!permissionLoaded ? (
        <div className="context-state" role="status">AI 사용 권한을 확인하고 있습니다.</div>
      ) : !canUseAI ? (
        <div className="context-state">{unavailableReason || "이 계정에서는 AI를 사용할 수 없습니다."}</div>
      ) : (
        <div className="context-primary-actions">
          <button
            type="button"
            className="context-primary-button"
            disabled={streaming || availableMessageCount === 0}
            onClick={onRun}
          >
            {output ? "다시 요약" : "요약 실행"}
          </button>
          {streaming && (
            <button type="button" className="context-secondary-button" onClick={onStop}>
              생성 중지
            </button>
          )}
          <span className="context-action-note">
            {availableMessageCount > 0 ? `최근 ${availableMessageCount}개 메시지 사용` : "요약할 메시지가 없습니다."}
          </span>
        </div>
      )}

      {error && <div className="context-error" role="alert">{error}</div>}
      {streaming && <div className="context-stream-status" role="status">요약을 생성하고 있습니다.</div>}

      {output && (
        <article className="context-ai-result" aria-label="AI 요약 결과">
          <div className="context-result-meta">
            <strong>AI 생성 요약</strong>
            {generatedAt && <time dateTime={new Date(generatedAt).toISOString()}>{formatDateTime(generatedAt)}</time>}
          </div>
          <div className="context-ai-copy" aria-live="polite">{output}</div>
        </article>
      )}

      {output && !streaming && citedSources.length === 0 && (
        <div className="context-warning" role="status">
          요약에서 유효한 근거 참조 형식을 확인할 수 없습니다. 결과를 원문과 대조해 주세요.
        </div>
      )}

      {output && !streaming && invalidRefs.length > 0 && (
        <div className="context-warning" role="status">
          실제 입력과 일치하지 않는 참조 {invalidRefs.map((ref) => `[${ref}]`).join(", ")}가 포함되어 있습니다.
        </div>
      )}

      {output && !streaming && citedSources.length > 0 && hasUncitedClaims && (
        <div className="context-warning" role="status">
          유효한 메시지 참조가 없는 문장이 포함되어 있습니다. 해당 문장은 검증되지 않았습니다.
        </div>
      )}

      {citedSources.length > 0 && (
        <section className="context-sources" aria-label="AI 요약에 인용된 메시지">
          <h4>인용된 메시지</h4>
          <SummarySourceList sources={citedSources} onJumpToPost={onJumpToPost} />
        </section>
      )}

      {sources.length > 0 && (
        <details className="context-input-sources">
          <summary>AI에 제공한 메시지 {sources.length}개</summary>
          <p>아래 목록은 입력 범위이며, 모두가 요약의 근거로 인용된 것은 아닙니다.</p>
          <SummarySourceList sources={sources} onJumpToPost={onJumpToPost} />
        </details>
      )}
    </section>
  );
}

export function ChannelFilesView({ token, entries, onJumpToPost }: {
  token: string;
  entries: ChannelFileEntry[];
  onJumpToPost: JumpHandler;
}) {
  const [downloadingID, setDownloadingID] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState("");

  async function download(file: FileInfo) {
    if (downloadingID) return;
    setDownloadingID(file.id);
    setDownloadError("");
    try {
      await downloadAuthenticatedMedia(token, api.fileDownloadPath(file.id), file.name);
    } catch {
      setDownloadError(`${file.name} 파일을 다운로드하지 못했습니다.`);
    } finally {
      setDownloadingID(null);
    }
  }

  return (
    <section className="context-view" aria-label="최근 불러온 파일">
      <div className="context-view-heading">
        <div>
          <h3>최근 파일</h3>
          <p>현재 로드된 채널 메시지의 첨부파일 {entries.length}개</p>
        </div>
      </div>
      {downloadError && <div className="context-error" role="alert">{downloadError}</div>}
      {entries.length === 0 ? (
        <div className="context-state">현재 불러온 메시지에는 첨부파일이 없습니다.</div>
      ) : (
        <ul className="context-file-list">
          {entries.map(({ file, post, author }) => (
            <li key={file.id} className="context-file-item">
              <div className="context-file-icon" aria-hidden>{file.extension?.toUpperCase().slice(0, 4) || "FILE"}</div>
              <div className="context-file-detail">
                <strong title={file.name}>{file.name}</strong>
                <span>{formatBytes(file.size)} · {author} · {formatDateTime(post.create_at)}</span>
                <SourceLink postId={post.id} onJumpToPost={onJumpToPost}>메시지로 이동</SourceLink>
              </div>
              <button
                type="button"
                className="context-icon-button"
                aria-label={`${file.name} 다운로드`}
                disabled={downloadingID === file.id}
                onClick={() => void download(file)}
              >
                {downloadingID === file.id ? "…" : <DownloadRounded fontSize="inherit" aria-hidden />}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function channelTypeLabel(channel: Channel): string {
  if (channel.type === "D") return "다이렉트 메시지";
  if (channel.type === "G") return "그룹 메시지";
  if (channel.type === "P") return "비공개 채널";
  return "공개 채널";
}

export function ChannelInfoView({ channel, team, stats }: {
  channel: Channel;
  team: Team | null;
  stats?: ChannelStats;
}) {
  return (
    <section className="context-view" aria-label="채널 정보">
      <div className="context-view-heading">
        <div>
          <h3>{channel.display_name}</h3>
          <p>{channelTypeLabel(channel)}</p>
        </div>
      </div>

      <dl className="context-info-list">
        {team && <><dt>팀</dt><dd>{team.display_name}</dd></>}
        <dt>이름</dt><dd>{channel.name}</dd>
        <dt>헤더</dt><dd>{channel.header?.trim() || "설정된 헤더가 없습니다."}</dd>
        <dt>목적</dt><dd>{channel.purpose?.trim() || "설정된 목적이 없습니다."}</dd>
        <dt>생성</dt><dd><time dateTime={new Date(channel.create_at).toISOString()}>{formatDateTime(channel.create_at)}</time></dd>
        {stats && (
          <>
            <dt>멤버</dt><dd>{stats.member_count}명{stats.guest_count > 0 ? ` · 게스트 ${stats.guest_count}명` : ""}</dd>
            <dt>파일</dt><dd>{stats.files_count}개</dd>
            <dt>고정 메시지</dt><dd>{stats.pinnedpost_count}개</dd>
          </>
        )}
      </dl>
    </section>
  );
}

export function EmptyThreadView() {
  return (
    <section className="context-view context-state" aria-label="스레드 없음">
      메시지의 스레드 열기 버튼을 선택하면 여기에 대화가 표시됩니다.
    </section>
  );
}
