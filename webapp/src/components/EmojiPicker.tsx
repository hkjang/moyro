// EmojiPicker replaces the six-button quick palette with a two-row panel:
// the familiar QUICK_EMOJIS on top and custom emojis below. Custom emoji
// list is fetched lazily on first open and cached on window for cross-pick
// reuse (full state management would need a context provider; this keeps
// the change surgical and good enough for a typical <500-entry list).
import { useEffect, useMemo, useRef, useState } from "react";
import { api, type Emoji } from "@/api/client";

type Props = {
  token: string;
  quick: string[];
  onPick: (name: string) => void;
  onClose: () => void;
};

// Cache the list on the module to avoid refetching every time a picker
// opens. Small; a full refresh happens on page reload.
let emojiCache: Emoji[] | null = null;
let emojiPromise: Promise<Emoji[]> | null = null;

function loadEmojis(token: string): Promise<Emoji[]> {
  if (emojiCache) return Promise.resolve(emojiCache);
  if (!emojiPromise) {
    emojiPromise = api.listEmojis(token).then(
      (list) => { emojiCache = list ?? []; return emojiCache; },
      (e) => { emojiPromise = null; throw e; },
    );
  }
  return emojiPromise;
}

// Invalidate when the admin panel mutates the list.
export function invalidateEmojiCache() {
  emojiCache = null;
  emojiPromise = null;
}

// Used by reaction chips to look up a custom emoji's image URL without
// every caller passing the list down. Returns null if the emoji isn't in
// the cache (caller should fall back to :colon: rendering).
export function customEmojiByName(name: string): Emoji | null {
  if (!emojiCache) return null;
  return emojiCache.find((e) => e.name === name) ?? null;
}

const EMOJI_CHAR: Record<string, string> = {
  "+1": "👍",
  "-1": "👎",
  heart: "❤️",
  tada: "🎉",
  laughing: "😄",
  eyes: "👀",
  rocket: "🚀",
  fire: "🔥",
  clap: "👏",
  check: "✅",
};

export function EmojiPicker({ token, quick, onPick, onClose }: Props) {
  const [custom, setCustom] = useState<Emoji[]>(emojiCache ?? []);
  const [loading, setLoading] = useState(emojiCache === null);
  const [q, setQ] = useState("");
  const wrapRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    if (emojiCache) return;
    loadEmojis(token).then(
      (list) => { if (!cancelled) { setCustom(list); setLoading(false); } },
      () => { if (!cancelled) setLoading(false); },
    );
    return () => { cancelled = true; };
  }, [token]);

  // Close on outside click. The picker lives inside MessageRow, so a
  // click on the ⋯ reaction button or another message should dismiss it.
  useEffect(() => {
    function onDoc(e: MouseEvent) {
      if (!wrapRef.current) return;
      if (!wrapRef.current.contains(e.target as Node)) onClose();
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [onClose]);

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return custom;
    return custom.filter((e) => e.name.includes(needle));
  }, [custom, q]);

  return (
    <div className="emoji-picker emoji-picker-wide" ref={wrapRef}>
      <div className="emoji-picker-quick">
        {quick.map((e) => (
          <button
            key={e}
            type="button"
            className="emoji-btn"
            onClick={() => onPick(e)}
            title={`:${e}:`}
          >{EMOJI_CHAR[e] ?? `:${e}:`}</button>
        ))}
      </div>
      <div className="emoji-picker-search">
        <input
          className="field-input"
          placeholder="커스텀 이모지 검색…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          style={{ height: 30, fontSize: 12 }}
        />
      </div>
      <div className="emoji-picker-custom">
        {loading ? (
          <div className="chat-empty" style={{ padding: 8, fontSize: 12 }}>불러오는 중…</div>
        ) : filtered.length === 0 ? (
          <div className="chat-empty" style={{ padding: 8, fontSize: 12 }}>
            {q ? "일치하는 이모지 없음" : "커스텀 이모지 없음"}
          </div>
        ) : (
          filtered.map((e) => (
            <button
              key={e.id}
              type="button"
              className="emoji-btn emoji-btn-custom"
              onClick={() => onPick(e.name)}
              title={`:${e.name}:`}
            >
              <img src={api.emojiImageURL(token, e.id)} alt={e.name} />
            </button>
          ))
        )}
      </div>
    </div>
  );
}
