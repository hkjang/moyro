// `:name` autocomplete for the composer.
//
// Mirrors useMentionAutocomplete: the hook watches the caret, offers matches
// in a menu anchored under the textarea, and inserts on Enter/Tab. Built-in
// emoji insert their glyph (the body renders text as-is); custom emoji insert
// `:name:`, which MessageBody rewrites to the uploaded image.
import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type Emoji } from "@/api/client";
import { AuthenticatedImage } from "./AuthenticatedMedia";
import { EMOJI_CHAR, loadEmojis } from "./EmojiPicker";

export type EmojiSuggestion =
  | { kind: "builtin"; name: string; glyph: string }
  | { kind: "custom"; name: string; emoji: Emoji };

/** Minimum characters after the colon before the menu opens. One character
 *  would match most of the list and get in the way of ordinary typing. */
const MIN_QUERY = 2;
const MAX_ITEMS = 8;

/**
 * Finds a `:que` token ending at the caret. The colon must start the text or
 * follow whitespace, so times like "10:30" and URLs never open the menu.
 */
export function detectEmojiQuery(text: string, caret: number): { start: number; query: string } | null {
  const upto = text.slice(0, caret);
  const match = /(?:^|\s):([a-z0-9_+-]*)$/i.exec(upto);
  if (!match) return null;
  const query = match[1];
  if (query.length < MIN_QUERY) return null;
  return { start: caret - query.length - 1, query: query.toLowerCase() };
}

export function rankEmoji(query: string, custom: Emoji[]): EmojiSuggestion[] {
  const builtin: EmojiSuggestion[] = Object.entries(EMOJI_CHAR).map(([name, glyph]) => ({ kind: "builtin", name, glyph }));
  const customs: EmojiSuggestion[] = custom.map((emoji) => ({ kind: "custom", name: emoji.name, emoji }));
  const all = [...builtin, ...customs];
  const starts = all.filter((item) => item.name.startsWith(query));
  const contains = all.filter((item) => !item.name.startsWith(query) && item.name.includes(query));
  return [...starts, ...contains].slice(0, MAX_ITEMS);
}

export function useEmojiAutocomplete(opts: {
  token: string;
  value: string;
  setValue: (v: string) => void;
  textareaRef: React.RefObject<HTMLTextAreaElement>;
}) {
  const { token, value, setValue, textareaRef } = opts;
  const [query, setQuery] = useState<{ start: number; query: string } | null>(null);
  const [custom, setCustom] = useState<Emoji[]>([]);
  const [active, setActive] = useState(0);

  useEffect(() => {
    if (!query || !token) return;
    let cancelled = false;
    loadEmojis(token).then((list) => { if (!cancelled) setCustom(list); }, () => undefined);
    return () => { cancelled = true; };
  }, [query, token]);

  const items = useMemo(() => (query ? rankEmoji(query.query, custom) : []), [query, custom]);
  useEffect(() => { setActive(0); }, [items]);

  const onChange = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const caret = e.target.selectionStart ?? e.target.value.length;
    setQuery(detectEmojiQuery(e.target.value, caret));
  }, []);

  const insert = useCallback((item: EmojiSuggestion) => {
    if (!query) return;
    const ta = textareaRef.current;
    if (!ta) return;
    const caret = ta.selectionStart ?? value.length;
    const before = value.slice(0, query.start);
    const after = value.slice(caret);
    const inserted = (item.kind === "builtin" ? item.glyph : `:${item.name}:`) + " ";
    const next = before + inserted + after;
    setValue(next);
    setQuery(null);
    const target = before.length + inserted.length;
    requestAnimationFrame(() => {
      ta.focus();
      ta.setSelectionRange(target, target);
    });
  }, [query, value, setValue, textareaRef]);

  const handleKeyDown = useCallback((e: React.KeyboardEvent<HTMLTextAreaElement>): boolean => {
    if (!query || items.length === 0) return false;
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setActive((i) => (i + 1) % items.length);
        return true;
      case "ArrowUp":
        e.preventDefault();
        setActive((i) => (i - 1 + items.length) % items.length);
        return true;
      case "Enter":
      case "Tab":
        e.preventDefault();
        insert(items[active]);
        return true;
      case "Escape":
        e.preventDefault();
        setQuery(null);
        return true;
      default:
        return false;
    }
  }, [query, items, active, insert]);

  const render = useCallback(() => {
    if (!query || items.length === 0) return null;
    return (
      <div className="mention-menu emoji-menu" role="listbox" aria-label="이모지 자동완성">
        {items.map((item, index) => (
          <button
            key={`${item.kind}:${item.name}`}
            type="button"
            role="option"
            aria-selected={index === active}
            className={`mention-item ${index === active ? "mention-item-active" : ""}`}
            onMouseDown={(event) => { event.preventDefault(); insert(item); }}
          >
            <span className="emoji-menu-glyph" aria-hidden>
              {item.kind === "builtin"
                ? item.glyph
                : <AuthenticatedImage token={token} path={api.emojiImagePath(item.emoji.id)} className="emoji-img" alt="" />}
            </span>
            <span className="mention-name">:{item.name}:</span>
          </button>
        ))}
      </div>
    );
  }, [query, items, active, insert, token]);

  return { open: Boolean(query && items.length > 0), onChange, handleKeyDown, render };
}
