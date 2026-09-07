import ErrorOutlineRounded from "@mui/icons-material/ErrorOutlineRounded";
import type { FailedSend } from "@/features/workspace/model/usePostActions";

/**
 * A message the server refused, held in the timeline where it was meant to
 * land. The reader can retry it or drop it; nothing is lost silently and the
 * composer is free for the next message.
 */
export function FailedMessage({ entry, onRetry, onDiscard }: {
  entry: FailedSend;
  onRetry: () => void;
  onDiscard: () => void;
}) {
  return (
    <div className="msg failed-message" role="group" aria-label="전송하지 못한 메시지">
      <div className="failed-message-body">{entry.message}</div>
      <div className="failed-message-footer">
        <span className="failed-message-reason" role="alert">
          <ErrorOutlineRounded fontSize="inherit" aria-hidden /> {entry.reason}
        </span>
        <button type="button" className="context-secondary-button" disabled={entry.retrying} onClick={onRetry}>
          {entry.retrying ? "재시도 중…" : "재시도"}
        </button>
        <button type="button" className="context-secondary-button failed-message-discard" onClick={onDiscard}>
          삭제
        </button>
      </div>
    </div>
  );
}
