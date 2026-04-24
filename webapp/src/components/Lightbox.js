import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
// Lightbox overlays a full-resolution image when the user clicks a
// thumbnail in the chat stream. Intentionally minimal: a backdrop + a
// centered <img>, no zoom / pan / gallery — we treat this as "click the
// thumbnail to see it larger" rather than a full media viewer. Escape
// closes, clicks on the backdrop (but not the image) close.
import { useEffect } from "react";
export function Lightbox({ src, alt, onClose, }) {
    useEffect(() => {
        function onKey(e) {
            if (e.key === "Escape")
                onClose();
        }
        document.addEventListener("keydown", onKey);
        return () => document.removeEventListener("keydown", onKey);
    }, [onClose]);
    return (_jsxs("div", { className: "lightbox-backdrop", onClick: onClose, children: [_jsx("img", { className: "lightbox-image", src: src, alt: alt ?? "", onClick: (e) => e.stopPropagation() }), _jsx("button", { type: "button", className: "lightbox-close", "aria-label": "\uB2EB\uAE30", onClick: onClose, children: "\u2715" })] }));
}
