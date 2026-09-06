import { useEffect, useRef, useState } from "react";
import type { Emoji } from "@/api/client";
import { api } from "@/api/client";
import { AuthenticatedImage } from "@/components/AuthenticatedMedia";
import { loadEmojis } from "@/components/EmojiPicker";
import { BuiltinSticker } from "./Sticker";
import { BUILTIN_PREFIX, CUSTOM_PREFIX, STICKER_PACKS, stickersInPack, type StickerPackId } from "./stickers";

type Tab = StickerPackId | "custom";

/**
 * Emoticon picker in the KakaoTalk shape: pack tabs across the top, a grid
 * below, one tap sends. Custom emoji uploaded by an administrator appear as
 * a final pack so a workspace's own images can be sent large too.
 */
export function StickerPicker({ token, onPick, onClose }: {
  token: string;
  onPick: (stickerId: string, caption: string) => void;
  onClose: () => void;
}) {
  const [tab, setTab] = useState<Tab>("feeling");
  const [custom, setCustom] = useState<Emoji[]>([]);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    loadEmojis(token).then((list) => { if (!cancelled) setCustom(list); }, () => undefined);
    return () => { cancelled = true; };
  }, [token]);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") { event.stopPropagation(); onClose(); }
    }
    function onDown(event: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) onClose();
    }
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
    };
  }, [onClose]);

  const tabs: { id: Tab; label: string }[] = [
    ...STICKER_PACKS,
    ...(custom.length > 0 ? [{ id: "custom" as const, label: "커스텀" }] : []),
  ];

  return (
    <div ref={rootRef} className="sticker-picker" role="dialog" aria-label="이모티콘 선택">
      <div className="sticker-picker-tabs" role="tablist" aria-label="이모티콘 팩">
        {tabs.map((entry) => (
          <button
            key={entry.id}
            type="button"
            role="tab"
            aria-selected={tab === entry.id}
            className={`sticker-picker-tab ${tab === entry.id ? "is-active" : ""}`}
            onClick={() => setTab(entry.id)}
          >
            {entry.label}
          </button>
        ))}
      </div>
      <div className="sticker-picker-grid" role="listbox" aria-label="이모티콘">
        {tab === "custom"
          ? custom.map((emoji) => (
            <button
              key={emoji.id}
              type="button"
              role="option"
              aria-selected={false}
              className="sticker-picker-item"
              title={emoji.name}
              onClick={() => onPick(`${CUSTOM_PREFIX}${emoji.name}`, `:${emoji.name}:`)}
            >
              <AuthenticatedImage token={token} path={api.emojiImagePath(emoji.id)} className="sticker-picker-custom" alt={emoji.name} />
            </button>
          ))
          : stickersInPack(tab).map((spec) => (
            <button
              key={spec.id}
              type="button"
              role="option"
              aria-selected={false}
              className="sticker-picker-item"
              title={spec.caption}
              onClick={() => onPick(`${BUILTIN_PREFIX}${spec.id}`, spec.caption)}
            >
              <BuiltinSticker spec={spec} size={72} />
            </button>
          ))}
      </div>
      <div className="sticker-picker-hint">누르면 바로 전송됩니다</div>
    </div>
  );
}
