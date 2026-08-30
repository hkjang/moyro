import { useEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import CloseRounded from "@mui/icons-material/CloseRounded";

export type WorkspaceContextTab = "thread" | "summary" | "files" | "info";

const CONTEXT_TABS: readonly { id: WorkspaceContextTab; label: string }[] = [
  { id: "thread", label: "스레드" },
  { id: "summary", label: "요약" },
  { id: "files", label: "파일" },
  { id: "info", label: "정보" },
];

type ContextPanelProps = {
  activeTab: WorkspaceContextTab;
  panels: Record<WorkspaceContextTab, ReactNode>;
  onTabChange: (tab: WorkspaceContextTab) => void;
  onClose: () => void;
};

/**
 * Extensible right-hand workspace context. Only surfaces backed by current
 * channel data are exposed; tab state and data ownership remain in ChatView
 * so a workspace navigation can clear the whole context atomically.
 */
export function ContextPanel({ activeTab, panels, onTabChange, onClose }: ContextPanelProps) {
  const tabRefs = useRef<Partial<Record<WorkspaceContextTab, HTMLButtonElement | null>>>({});
  const panelRef = useRef<HTMLElement | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    const previouslyFocused = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    const focusFrame = window.requestAnimationFrame(() => tabRefs.current[activeTab]?.focus());

    function onDocumentKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key === "Escape") {
        const nestedOverlay = document.querySelector(
          ".modal-backdrop, .lightbox-backdrop, .emoji-picker, .mention-menu, .notify-menu, .user-menu-layer",
        );
        if (nestedOverlay) return;
        event.preventDefault();
        onCloseRef.current();
        return;
      }
      if (event.key !== "Tab" || !window.matchMedia("(max-width: 960px)").matches) return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusable = Array.from(panel.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
      )).filter((element) => element.offsetParent !== null);
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", onDocumentKeyDown);
    return () => {
      window.cancelAnimationFrame(focusFrame);
      document.removeEventListener("keydown", onDocumentKeyDown);
      if (previouslyFocused?.isConnected) previouslyFocused.focus();
    };
    // Focus is intentionally captured once per panel opening. Tab changes
    // retain the user's current focus and are handled by moveFocus below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function moveFocus(event: KeyboardEvent<HTMLButtonElement>, current: WorkspaceContextTab) {
    const currentIndex = CONTEXT_TABS.findIndex((tab) => tab.id === current);
    let nextIndex = currentIndex;
    if (event.key === "ArrowRight") nextIndex = (currentIndex + 1) % CONTEXT_TABS.length;
    else if (event.key === "ArrowLeft") nextIndex = (currentIndex - 1 + CONTEXT_TABS.length) % CONTEXT_TABS.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = CONTEXT_TABS.length - 1;
    else return;

    event.preventDefault();
    const next = CONTEXT_TABS[nextIndex].id;
    onTabChange(next);
    tabRefs.current[next]?.focus();
  }

  return (
    <aside ref={panelRef} className="thread-panel context-panel" aria-label="컨텍스트 패널">
      <header className="thread-header context-panel-header">
        <div className="context-panel-tabs" role="tablist" aria-label="채널 컨텍스트">
          {CONTEXT_TABS.map((tab) => (
            <button
              key={tab.id}
              ref={(node) => { tabRefs.current[tab.id] = node; }}
              id={`workspace-context-${tab.id}-tab`}
              type="button"
              className={`context-panel-tab ${activeTab === tab.id ? "is-active" : ""}`}
              role="tab"
              aria-selected={activeTab === tab.id}
              aria-controls={`workspace-context-${tab.id}-panel`}
              tabIndex={activeTab === tab.id ? 0 : -1}
              onClick={() => onTabChange(tab.id)}
              onKeyDown={(event) => moveFocus(event, tab.id)}
            >
              {tab.label}
            </button>
          ))}
        </div>
        <button
          type="button"
          className="action-btn context-panel-close"
          onClick={onClose}
          aria-label="컨텍스트 패널 닫기"
          title="닫기"
        >
          <CloseRounded fontSize="inherit" aria-hidden />
        </button>
      </header>
      <div className="context-panel-content">
        {CONTEXT_TABS.map((tab) => (
          <div
            key={tab.id}
            id={`workspace-context-${tab.id}-panel`}
            className="context-panel-pane"
            role="tabpanel"
            aria-labelledby={`workspace-context-${tab.id}-tab`}
            hidden={activeTab !== tab.id}
          >
            {panels[tab.id]}
          </div>
        ))}
      </div>
    </aside>
  );
}
