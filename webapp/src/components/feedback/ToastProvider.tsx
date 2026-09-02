import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { Alert, Snackbar } from "@mui/material";

export type ToastTone = "success" | "error" | "info";

export type Toast = {
  id: number;
  tone: ToastTone;
  message: string;
  /** Optional one-shot action, e.g. "실행 취소". */
  action?: { label: string; onClick: () => void };
};

export type ToastApi = {
  show: (tone: ToastTone, message: string, action?: Toast["action"]) => void;
  success: (message: string, action?: Toast["action"]) => void;
  error: (message: string, action?: Toast["action"]) => void;
  info: (message: string, action?: Toast["action"]) => void;
};

const DURATION_MS: Record<ToastTone, number> = {
  success: 4_000,
  info: 5_000,
  // Errors stay long enough to read and copy; they still dismiss so a stale
  // failure does not sit over the workspace indefinitely.
  error: 8_000,
};

// A no-op default lets components call useToast() in isolation — unit tests
// and storybook-style renders — without wrapping them in the provider.
const noop: ToastApi = {
  show: () => undefined,
  success: () => undefined,
  error: () => undefined,
  info: () => undefined,
};

const ToastContext = createContext<ToastApi>(noop);

export function useToast(): ToastApi {
  return useContext(ToastContext);
}

/**
 * One feedback surface for the whole app.
 *
 * Before this, chat errors reused the login form's inline error style and two
 * features carried private Snackbars, while most successful actions gave no
 * confirmation at all. Toasts queue so a burst of results is shown one after
 * another rather than overwriting each other.
 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [queue, setQueue] = useState<Toast[]>([]);
  const [open, setOpen] = useState(false);
  const nextId = useRef(1);

  const show = useCallback<ToastApi["show"]>((tone, message, action) => {
    const trimmed = message.trim();
    if (!trimmed) return;
    setQueue((current) => {
      // Collapse an identical message already waiting; repeating the same
      // failure five times helps nobody.
      if (current.some((toast) => toast.message === trimmed && toast.tone === tone)) return current;
      return [...current, { id: nextId.current++, tone, message: trimmed, action }];
    });
    setOpen(true);
  }, []);

  const api = useMemo<ToastApi>(() => ({
    show,
    success: (message, action) => show("success", message, action),
    error: (message, action) => show("error", message, action),
    info: (message, action) => show("info", message, action),
  }), [show]);

  const current = queue[0];

  const dismiss = (_event?: unknown, reason?: string) => {
    if (reason === "clickaway") return;
    setOpen(false);
  };

  // Advance the queue only after the exit transition so consecutive toasts
  // visibly replace each other instead of flashing.
  const advance = () => {
    setQueue((rest) => rest.slice(1));
    if (queue.length > 1) setOpen(true);
  };

  return (
    <ToastContext.Provider value={api}>
      {children}
      <Snackbar
        key={current?.id ?? "idle"}
        open={open && Boolean(current)}
        autoHideDuration={current ? DURATION_MS[current.tone] : null}
        onClose={dismiss}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
        slotProps={{ transition: { onExited: advance } }}
      >
        {current ? (
          <Alert
            severity={current.tone}
            variant="filled"
            onClose={() => dismiss()}
            role={current.tone === "error" ? "alert" : "status"}
            action={current.action ? (
              <button
                type="button"
                className="toast-action"
                onClick={() => { current.action?.onClick(); dismiss(); }}
              >
                {current.action.label}
              </button>
            ) : undefined}
            sx={{ minWidth: 280, maxWidth: 560, alignItems: "center" }}
          >
            {current.message}
          </Alert>
        ) : undefined}
      </Snackbar>
    </ToastContext.Provider>
  );
}
