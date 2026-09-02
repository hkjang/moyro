import { useEffect, useRef } from "react";
import CloseRounded from "@mui/icons-material/CloseRounded";

const IS_MAC = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
const MOD = IS_MAC ? "⌘" : "Ctrl";

/** Every shortcut the workspace actually implements. Keep this in step with
 *  the handlers; a listed shortcut that does nothing is worse than none. */
export const WORKSPACE_SHORTCUTS: { group: string; items: { keys: string[]; label: string }[] }[] = [
  {
    group: "이동",
    items: [
      { keys: [`${MOD} + K`], label: "채널·사용자 빠른 이동" },
      { keys: ["Alt + ↑", "Alt + ↓"], label: "이전 / 다음 채널" },
      { keys: ["Esc"], label: "패널·대화상자 닫기" },
    ],
  },
  {
    group: "메시지",
    items: [
      { keys: ["Enter"], label: "전송" },
      { keys: ["Shift + Enter"], label: "줄바꿈" },
      { keys: ["↑"], label: "빈 입력창에서 내 마지막 메시지 편집" },
      { keys: ["@"], label: "멘션 자동완성" },
    ],
  },
  {
    group: "도움말",
    items: [{ keys: ["?"], label: "이 단축키 목록" }],
  },
];

export function ShortcutHelpModal({ onClose }: { onClose: () => void }) {
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.stopPropagation(); onClose(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="modal-backdrop" onClick={onClose} role="presentation">
      <div
        className="modal shortcut-help"
        role="dialog"
        aria-modal="true"
        aria-labelledby="shortcut-help-title"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="shortcut-help-header">
          <h2 id="shortcut-help-title">키보드 단축키</h2>
          <button ref={closeRef} type="button" className="action-btn" aria-label="닫기" onClick={onClose}>
            <CloseRounded fontSize="inherit" aria-hidden />
          </button>
        </div>
        {WORKSPACE_SHORTCUTS.map((section) => (
          <section key={section.group} className="shortcut-help-group" aria-label={section.group}>
            <h3>{section.group}</h3>
            <dl>
              {section.items.map((item) => (
                <div key={item.label} className="shortcut-help-row">
                  <dt>{item.keys.map((key) => <kbd key={key}>{key}</kbd>)}</dt>
                  <dd>{item.label}</dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </div>
  );
}
