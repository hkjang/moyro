import { useEffect } from "react";

export type WorkspaceShortcutHandlers = {
  /** Ctrl/⌘+K — always, even while typing. */
  onQuickSwitch: () => void;
  /** `?` outside a text field. */
  onHelp: () => void;
  /** Alt+↑ / Alt+↓ — step through channels in sidebar order. */
  onChannelStep: (direction: -1 | 1) => void;
};

function isTyping(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null;
  return Boolean(element && (
    element.tagName === "INPUT" || element.tagName === "TEXTAREA" || element.isContentEditable
  ));
}

/**
 * Global key handling for the workspace. Handlers are read through a ref so
 * the listener is registered once and never closes over stale state; the
 * list of shortcuts here is exactly what the `?` dialog documents.
 */
export function useWorkspaceShortcuts(handlers: WorkspaceShortcutHandlers): void {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && (e.key === "k" || e.key === "K")) {
        // The whole point of the switcher is to grab focus from anywhere,
        // so a text field does not suppress it.
        e.preventDefault();
        handlers.onQuickSwitch();
        return;
      }
      // "?" is a plain character in a text field; only outside one is it
      // the help shortcut.
      if (e.key === "?" && !isTyping(e.target) && !mod && !e.altKey) {
        e.preventDefault();
        handlers.onHelp();
        return;
      }
      if (e.altKey && !mod && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
        e.preventDefault();
        handlers.onChannelStep(e.key === "ArrowUp" ? -1 : 1);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // Handlers are looked up at call time through the closure over the
    // latest `handlers` object captured on each render; the caller keeps
    // them stable via refs where the underlying state changes.
  }, [handlers]);
}
