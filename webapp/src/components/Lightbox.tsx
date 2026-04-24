// Lightbox overlays a full-resolution image when the user clicks a
// thumbnail in the chat stream. Intentionally minimal: a backdrop + a
// centered <img>, no zoom / pan / gallery — we treat this as "click the
// thumbnail to see it larger" rather than a full media viewer. Escape
// closes, clicks on the backdrop (but not the image) close.
import { useEffect } from "react";

export function Lightbox({
  src, alt, onClose,
}: {
  src: string;
  alt?: string;
  onClose: () => void;
}) {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="lightbox-backdrop" onClick={onClose}>
      <img
        className="lightbox-image"
        src={src}
        alt={alt ?? ""}
        onClick={(e) => e.stopPropagation()}
      />
      <button
        type="button"
        className="lightbox-close"
        aria-label="닫기"
        onClick={onClose}
      >✕</button>
    </div>
  );
}
