// MentionPicker + useMentionAutocomplete — @mention dropdown for the
// chat composer and post-edit textarea.
//
// Flow:
// 1. Caller owns the textarea and its `value` state. It calls
//    `useMentionAutocomplete({...})` and passes the returned `onChange`
//    and `handleKeyDown` into the textarea's event props.
// 2. On every keystroke we scan back from the caret for an unbroken run
//    of mention-charset characters ending in `@`. If found, that's the
//    active query.
// 3. Matching users are fetched (debounced, channel-scoped, cached for
//    60s) via `api.channelMembersAutocomplete`. The picker only renders
//    once the request returns at least one result.
// 4. Arrow-Up/Down moves the active row; Enter/Tab commits the selection
//    by replacing the `@query` span with `@username ` (trailing space).
//    Escape dismisses without inserting. `handleKeyDown` returns true
//    when it consumed the event so callers can skip their own handling.
// 5. Clicking a row uses `onMouseDown` (not `onClick`) so the textarea
//    doesn't lose focus between the click starting and the insert happening,
//    which would otherwise move the caret and break setSelectionRange.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, type User } from "@/api/client";

// Mention-charset: matches Mattermost's username rules (lowercase letters,
// digits, dash, dot, underscore). We accept uppercase too — the server
// normalises to lowercase on the way in.
const USERNAME_CHARS = /^[a-zA-Z0-9._-]$/;

// Module-level cache keyed by channelID → prefix → {items, fetchedAt}.
// Module scope (not component state) keeps the cache alive across
// re-mounts when the user switches channels and comes back.
type CacheEntry = { at: number; items: User[] };
const mentionCache = new Map<string, Map<string, CacheEntry>>();
const CACHE_TTL_MS = 60_000;

function cacheGet(channelID: string, prefix: string): User[] | null {
  const inner = mentionCache.get(channelID);
  if (!inner) return null;
  const hit = inner.get(prefix);
  if (!hit) return null;
  if (Date.now() - hit.at > CACHE_TTL_MS) {
    inner.delete(prefix);
    return null;
  }
  return hit.items;
}

function cacheSet(channelID: string, prefix: string, items: User[]) {
  let inner = mentionCache.get(channelID);
  if (!inner) {
    inner = new Map();
    mentionCache.set(channelID, inner);
  }
  inner.set(prefix, { at: Date.now(), items });
}

// Locate the `@query` token ending at `caret` in `text`. Returns the
// starting index of `@` and the query text, or null if we're not in a
// mention context. Rules:
//   - The `@` must be at position 0 or immediately after whitespace.
//   - Between `@` and caret, only USERNAME_CHARS are allowed.
//   - An empty query (bare `@`) is still valid and triggers "show all
//     members" once the user types one character.
function detectMentionQuery(
  text: string,
  caret: number,
): { start: number; query: string } | null {
  let i = caret - 1;
  while (i >= 0) {
    const ch = text[i];
    if (ch === "@") break;
    if (!USERNAME_CHARS.test(ch)) return null;
    i -= 1;
  }
  if (i < 0) return null;
  if (i > 0) {
    const before = text[i - 1];
    if (before !== " " && before !== "\t" && before !== "\n") return null;
  }
  return { start: i, query: text.slice(i + 1, caret) };
}

export type MentionAutocomplete = {
  // True when the picker should render. Callers can use this to add/remove
  // a position-relative wrapper so the absolutely-positioned picker anchors
  // correctly.
  open: boolean;
  onChange: (e: React.ChangeEvent<HTMLTextAreaElement>) => void;
  handleKeyDown: (e: React.KeyboardEvent<HTMLTextAreaElement>) => boolean;
  // Renders the picker element. Place next to the textarea inside a
  // `position: relative` wrapper (.mention-picker-host).
  render: () => React.ReactNode;
};

export function useMentionAutocomplete(opts: {
  token: string;
  channelID: string | null;
  value: string;
  setValue: (v: string) => void;
  textareaRef: React.RefObject<HTMLTextAreaElement>;
}): MentionAutocomplete {
  const { token, channelID, value, setValue, textareaRef } = opts;
  const [query, setQuery] = useState<{ start: number; text: string } | null>(null);
  const [items, setItems] = useState<User[]>([]);
  const [active, setActive] = useState(0);
  // Request counter so a slow-returning older fetch doesn't overwrite a
  // newer one's results when the user types fast.
  const reqSeq = useRef(0);

  useEffect(() => {
    if (!query || !channelID || !token) {
      setItems([]);
      return;
    }
    // Require at least one character before firing — an empty `@` with
    // nothing after would otherwise pull the entire channel roster.
    if (query.text.length === 0) {
      setItems([]);
      return;
    }
    const cached = cacheGet(channelID, query.text);
    if (cached) {
      setItems(cached);
      setActive(0);
      return;
    }
    const mySeq = ++reqSeq.current;
    const t = window.setTimeout(() => {
      api
        .channelMembersAutocomplete(token, channelID, query.text, 8)
        .then(
          (res) => {
            if (mySeq !== reqSeq.current) return;
            cacheSet(channelID, query.text, res);
            setItems(res);
            setActive(0);
          },
          () => {
            /* transient failure — leave picker empty, it'll retry next keystroke */
          },
        );
    }, 120);
    return () => window.clearTimeout(t);
  }, [query, channelID, token]);

  const onChange = useCallback(
    (e: React.ChangeEvent<HTMLTextAreaElement>) => {
      const text = e.target.value;
      const caret = e.target.selectionStart ?? text.length;
      const q = detectMentionQuery(text, caret);
      setQuery(q ? { start: q.start, text: q.query } : null);
    },
    [],
  );

  const insert = useCallback(
    (user: User) => {
      if (!query) return;
      const ta = textareaRef.current;
      if (!ta) return;
      // Read fresh caret off the live element rather than trusting stale
      // props — the user may have moved the caret before clicking a row.
      const caret = ta.selectionStart ?? value.length;
      const before = value.slice(0, query.start);
      const after = value.slice(caret);
      const inserted = `@${user.username} `;
      const next = before + inserted + after;
      setValue(next);
      setQuery(null);
      // Restore caret after React flushes the new value — otherwise our
      // setSelectionRange targets the pre-update DOM value.
      const targetCaret = before.length + inserted.length;
      requestAnimationFrame(() => {
        ta.focus();
        ta.setSelectionRange(targetCaret, targetCaret);
      });
    },
    [query, value, setValue, textareaRef],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>): boolean => {
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
    },
    [query, items, active, insert],
  );

  const open = query !== null && items.length > 0;

  const render = useCallback((): React.ReactNode => {
    if (!open) return null;
    return (
      <MentionPicker items={items} activeIndex={active} onPick={insert} />
    );
  }, [open, items, active, insert]);

  return { open, onChange, handleKeyDown, render };
}

function MentionPicker({
  items,
  activeIndex,
  onPick,
}: {
  items: User[];
  activeIndex: number;
  onPick: (u: User) => void;
}) {
  return (
    <div className="mention-picker" role="listbox" aria-label="mentions">
      {items.map((u, i) => (
        <button
          key={u.id}
          type="button"
          role="option"
          aria-selected={i === activeIndex}
          className={`mention-item ${
            i === activeIndex ? "mention-item-active" : ""
          }`}
          // onMouseDown (not onClick) so the textarea keeps focus during
          // the click — onClick fires after blur, which kills setSelectionRange.
          onMouseDown={(e) => {
            e.preventDefault();
            onPick(u);
          }}
        >
          <span className="mention-at">@</span>
          <span className="mention-name">{u.username}</span>
        </button>
      ))}
    </div>
  );
}
