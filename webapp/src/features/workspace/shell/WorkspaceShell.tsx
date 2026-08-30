import { useEffect, useRef, type ReactNode } from "react";
import MenuRounded from "@mui/icons-material/MenuRounded";
import "@/features/workspace/workspace.css";

type WorkspaceShellProps = {
  children?: ReactNode;
  sidebar: ReactNode;
  main: ReactNode;
  context?: ReactNode;
  mobileSidebarOpen: boolean;
  onOpenMobileSidebar: () => void;
  onCloseMobileSidebar: () => void;
};

/**
 * Workspace-local slot shell. Product-level navigation is intentionally owned
 * by the parent ProductShell; this component only arranges workspace context.
 */
export function WorkspaceShell({
  children,
  sidebar,
  main,
  context,
  mobileSidebarOpen,
  onOpenMobileSidebar,
  onCloseMobileSidebar,
}: WorkspaceShellProps) {
  const sidebarSlotRef = useRef<HTMLDivElement | null>(null);
  const mainSlotRef = useRef<HTMLDivElement | null>(null);
  const contextSlotRef = useRef<HTMLDivElement | null>(null);
  const mobileTriggerRef = useRef<HTMLButtonElement | null>(null);
  const onCloseMobileSidebarRef = useRef(onCloseMobileSidebar);
  onCloseMobileSidebarRef.current = onCloseMobileSidebar;

  useEffect(() => {
    if (!mobileSidebarOpen) return;
    const dialog = sidebarSlotRef.current;
    if (!dialog) return;
    const previouslyFocused = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : mobileTriggerRef.current;
    const background = [mainSlotRef.current, contextSlotRef.current].filter(
      (element): element is HTMLDivElement => element !== null,
    );
    background.forEach((element) => element.setAttribute("inert", ""));
    const previousBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const focusableElements = () => Array.from(dialog.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
    )).filter((element) => element.offsetParent !== null);
    const focusFirst = () => {
      const closeButton = dialog.querySelector<HTMLElement>("[data-workspace-sidebar-close]");
      const visibleCloseButton = closeButton?.offsetParent !== null ? closeButton : null;
      (visibleCloseButton ?? focusableElements()[0] ?? dialog).focus();
    };
    const focusFrame = window.requestAnimationFrame(focusFirst);

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onCloseMobileSidebarRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = focusableElements();
      if (focusable.length === 0) {
        event.preventDefault();
        dialog.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && (document.activeElement === first || !dialog.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (document.activeElement === last || !dialog.contains(document.activeElement))) {
        event.preventDefault();
        first.focus();
      }
    };
    const onFocusIn = (event: FocusEvent) => {
      if (!dialog.contains(event.target as Node)) focusFirst();
    };

    document.addEventListener("keydown", onKeyDown, true);
    document.addEventListener("focusin", onFocusIn, true);
    return () => {
      window.cancelAnimationFrame(focusFrame);
      document.removeEventListener("keydown", onKeyDown, true);
      document.removeEventListener("focusin", onFocusIn, true);
      background.forEach((element) => element.removeAttribute("inert"));
      document.body.style.overflow = previousBodyOverflow;
      if (previouslyFocused?.isConnected) previouslyFocused.focus();
    };
  }, [mobileSidebarOpen]);

  return (
    <div
      className="chat-shell workspace-shell"
      data-mobile-sidebar-open={mobileSidebarOpen ? "true" : "false"}
      data-context-open={context ? "true" : "false"}
    >
      {mobileSidebarOpen && (
        <div
          className="workspace-sidebar-backdrop"
          aria-hidden="true"
          onClick={onCloseMobileSidebar}
        />
      )}
      <div
        id="workspace-sidebar-dialog"
        ref={sidebarSlotRef}
        className="workspace-sidebar-slot"
        role={mobileSidebarOpen ? "dialog" : undefined}
        aria-modal={mobileSidebarOpen ? "true" : undefined}
        aria-label={mobileSidebarOpen ? "채널 탐색" : undefined}
        tabIndex={mobileSidebarOpen ? -1 : undefined}
      >
        {sidebar}
      </div>
      <div ref={mainSlotRef} className="workspace-main-slot">
        <button
          ref={mobileTriggerRef}
          type="button"
          className="workspace-mobile-sidebar-trigger"
          aria-label="채널 탐색 열기"
          aria-expanded={mobileSidebarOpen}
          aria-controls="workspace-sidebar-dialog"
          onClick={onOpenMobileSidebar}
        >
          <MenuRounded fontSize="inherit" aria-hidden />
        </button>
        {main}
      </div>
      {context && <div ref={contextSlotRef} className="workspace-context-slot">{context}</div>}
      {children}
    </div>
  );
}
