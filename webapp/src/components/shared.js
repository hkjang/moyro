import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
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
export function useEscClose(enabled, onClose) {
    useEffect(() => {
        if (!enabled)
            return;
        function onKeyDown(e) {
            if (e.key === "Escape") {
                e.stopPropagation();
                onClose();
            }
        }
        document.addEventListener("keydown", onKeyDown);
        return () => document.removeEventListener("keydown", onKeyDown);
    }, [enabled, onClose]);
}
export function useConfirm() {
    const [pending, setPending] = useState(null);
    // Ref-mirrored pending so the callbacks we hand back are stable — React
    // state closures would otherwise force callers to re-create handlers on
    // every render just to see the latest pending value.
    const pendingRef = useRef(null);
    useEffect(() => { pendingRef.current = pending; }, [pending]);
    const confirm = useCallback((opts) => {
        // Coalesce: if a prior confirm is already open, resolve it as false
        // before queuing the new one. Prevents dangling promises when code
        // accidentally triggers two confirm flows back-to-back.
        if (pendingRef.current) {
            pendingRef.current.resolve(false);
        }
        return new Promise((resolve) => {
            setPending({ ...opts, resolve });
        });
    }, []);
    const close = useCallback((ok) => {
        const p = pendingRef.current;
        if (!p)
            return;
        p.resolve(ok);
        setPending(null);
    }, []);
    const render = useCallback(() => {
        if (!pending)
            return null;
        return (_jsx(ConfirmDialog, { title: pending.title, message: pending.message, confirmLabel: pending.confirmLabel ?? (pending.destructive ? "삭제" : "확인"), cancelLabel: pending.cancelLabel ?? "취소", destructive: !!pending.destructive, onCancel: () => close(false), onConfirm: () => close(true) }));
    }, [pending, close]);
    return { confirm, render };
}
export function ConfirmDialog({ title, message, confirmLabel, cancelLabel, destructive, onCancel, onConfirm, }) {
    useEscClose(true, onCancel);
    const confirmRef = useRef(null);
    // Autofocus the confirm button so Enter immediately proceeds — matches
    // the native confirm() default and keeps destructive actions at 1 key.
    useEffect(() => { confirmRef.current?.focus(); }, []);
    return (_jsx("div", { className: "modal-backdrop", onClick: onCancel, children: _jsxs("div", { className: "modal-card confirm-dialog", onClick: (e) => e.stopPropagation(), role: "alertdialog", "aria-modal": "true", "aria-labelledby": "confirm-title", children: [_jsx("h3", { id: "confirm-title", style: { margin: "0 0 10px" }, children: title }), _jsx("p", { className: "confirm-message", children: message }), _jsxs("div", { className: "confirm-actions", children: [_jsx("button", { type: "button", className: "btn-ghost", style: { width: "auto", padding: "0 14px", height: 36 }, onClick: onCancel, children: cancelLabel }), _jsx("button", { ref: confirmRef, type: "button", className: destructive ? "btn-danger" : "btn-primary", style: { width: "auto", padding: "0 14px", height: 36 }, onClick: onConfirm, children: confirmLabel })] })] }) }));
}
