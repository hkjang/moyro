// Shared UI primitives used across multiple feature panels.
//
// Keeping these one-off hooks + a lightweight confirm dialog in a single
// file is cheap and beats re-implementing the same Escape-to-close +
// destructive-confirm patterns inline in every modal.

import { useCallback, useEffect, useRef, useState } from "react";

// ---- useEscClose ----
//
// Binds a document-level `keydown` listener while `enabled=true` and fires
// `onClose` on Escape. Modals that have both a backdrop click handler and a
// close button still benefit from this — keyboard users expect Escape to
// dismiss overlays. Listener is only mounted while enabled so the global
// cost is zero when no modal is open.
//
// Important: consumers should memoize `onClose` (or pass a stable ref)
// because a re-created function on every render would thrash the listener.
// In practice React setter functions (`setX`) are stable, so most call
// sites pass e.g. `() => setOpen(false)` which is recreated but listener
// churn is invisible — the effect re-subscribes cleanly each render.
export function useEscClose(enabled: boolean, onClose: () => void) {
  useEffect(() => {
    if (!enabled) return;
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [enabled, onClose]);
}

// ---- ConfirmDialog + useConfirm ----
//
// Shared replacement for `window.confirm()`. Renders in the same
// `modal-backdrop` / `modal-card` language as the rest of the app, honours
// Escape-to-close (via useEscClose), and carries a structured warning +
// a destructive-styled confirm label when appropriate.
//
// Usage pattern:
//   const confirmer = useConfirm();
//   async function onDelete() {
//     const ok = await confirmer.confirm({
//       title: "메시지 삭제",
//       message: "이 메시지를 삭제할까요? 되돌릴 수 없습니다.",
//       destructiveLabel: "삭제",
//     });
//     if (!ok) return;
//     ...
//   }
//   return <>{other ui}{confirmer.render()}</>;
export type ConfirmOptions = {
  title: string;
  message: string;
  // Label shown on the primary (confirm) button. Defaults to "확인". When
  // `destructive` is true the button is rendered in the danger color.
  confirmLabel?: string;
  cancelLabel?: string;
  destructive?: boolean;
};

type PendingConfirm = ConfirmOptions & {
  resolve: (ok: boolean) => void;
};

export function useConfirm() {
  const [pending, setPending] = useState<PendingConfirm | null>(null);
  // Ref-mirrored pending so the callbacks we hand back are stable — React
  // state closures would otherwise force callers to re-create handlers on
  // every render just to see the latest pending value.
  const pendingRef = useRef<PendingConfirm | null>(null);
  useEffect(() => { pendingRef.current = pending; }, [pending]);

  const confirm = useCallback((opts: ConfirmOptions): Promise<boolean> => {
    // Coalesce: if a prior confirm is already open, resolve it as false
    // before queuing the new one. Prevents dangling promises when code
    // accidentally triggers two confirm flows back-to-back.
    if (pendingRef.current) {
      pendingRef.current.resolve(false);
    }
    return new Promise<boolean>((resolve) => {
      setPending({ ...opts, resolve });
    });
  }, []);

  const close = useCallback((ok: boolean) => {
    const p = pendingRef.current;
    if (!p) return;
    p.resolve(ok);
    setPending(null);
  }, []);

  const render = useCallback((): JSX.Element | null => {
    if (!pending) return null;
    return (
      <ConfirmDialog
        title={pending.title}
        message={pending.message}
        confirmLabel={pending.confirmLabel ?? (pending.destructive ? "삭제" : "확인")}
        cancelLabel={pending.cancelLabel ?? "취소"}
        destructive={!!pending.destructive}
        onCancel={() => close(false)}
        onConfirm={() => close(true)}
      />
    );
  }, [pending, close]);

  return { confirm, render };
}

type ConfirmDialogProps = {
  title: string;
  message: string;
  confirmLabel: string;
  cancelLabel: string;
  destructive: boolean;
  onCancel: () => void;
  onConfirm: () => void;
};

export function ConfirmDialog({
  title, message, confirmLabel, cancelLabel, destructive, onCancel, onConfirm,
}: ConfirmDialogProps) {
  useEscClose(true, onCancel);
  const confirmRef = useRef<HTMLButtonElement>(null);
  // Autofocus the confirm button so Enter immediately proceeds — matches
  // the native confirm() default and keeps destructive actions at 1 key.
  useEffect(() => { confirmRef.current?.focus(); }, []);
  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div
        className="modal-card confirm-dialog"
        onClick={(e) => e.stopPropagation()}
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
      >
        <h3 id="confirm-title" style={{ margin: "0 0 10px" }}>{title}</h3>
        <p className="confirm-message">{message}</p>
        <div className="confirm-actions">
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 14px", height: 36 }}
            onClick={onCancel}
          >{cancelLabel}</button>
          <button
            ref={confirmRef}
            type="button"
            className={destructive ? "btn-danger" : "btn-primary"}
            style={{ width: "auto", padding: "0 14px", height: 36 }}
            onClick={onConfirm}
          >{confirmLabel}</button>
        </div>
      </div>
    </div>
  );
}
