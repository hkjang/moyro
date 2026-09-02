/**
 * Placeholder rows shown while a channel's first page loads. They occupy the
 * space real rows will take, so the list does not jump from a one-line
 * "불러오는 중…" to a full column when the page lands.
 */
export function TimelineSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div className="timeline-skeleton" role="status" aria-live="polite" aria-label="메시지를 불러오는 중">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="timeline-skeleton-row" aria-hidden>
          <span className="timeline-skeleton-avatar" />
          <span className="timeline-skeleton-lines">
            <span className="timeline-skeleton-line" style={{ width: `${28 + ((index * 17) % 30)}%` }} />
            <span className="timeline-skeleton-line" style={{ width: `${55 + ((index * 23) % 40)}%` }} />
          </span>
        </div>
      ))}
    </div>
  );
}

/** Sidebar counterpart: a handful of channel-shaped rows. */
export function SidebarSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="sidebar-skeleton" role="status" aria-label="채널을 불러오는 중">
      {Array.from({ length: rows }, (_, index) => (
        <span key={index} className="sidebar-skeleton-row" aria-hidden style={{ width: `${60 + ((index * 13) % 35)}%` }} />
      ))}
    </div>
  );
}
