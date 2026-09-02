import ArrowDownwardRounded from "@mui/icons-material/ArrowDownwardRounded";
import "@/features/workspace/messages/timeline.css";

/** A day boundary in the message list. */
export function DateDivider({ label, at }: { label: string; at: number }) {
  return (
    <div className="timeline-divider timeline-date" role="separator" aria-label={label}>
      <time dateTime={new Date(at).toISOString()}>{label}</time>
    </div>
  );
}

/** Marks where messages the reader has not seen begin. */
export function UnreadDivider() {
  return (
    <div className="timeline-divider timeline-unread" role="separator" aria-label="새 메시지">
      <span>새 메시지</span>
    </div>
  );
}

/**
 * Floating affordance shown while the reader is scrolled up. With a pending
 * count it reports how many messages arrived; without one it simply offers the
 * way back to the end of the conversation.
 */
export function JumpToLatestButton({ count, onClick }: { count: number; onClick: () => void }) {
  const label = count > 0 ? `새 메시지 ${count}개` : "최신 메시지로";
  return (
    <button type="button" className="timeline-jump" onClick={onClick} aria-live="polite">
      <ArrowDownwardRounded fontSize="inherit" aria-hidden />
      <span>{label}</span>
    </button>
  );
}
