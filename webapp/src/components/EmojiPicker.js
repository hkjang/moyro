import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
// EmojiPicker replaces the six-button quick palette with a two-row panel:
// the familiar QUICK_EMOJIS on top and custom emojis below. Custom emoji
// list is fetched lazily on first open and cached on window for cross-pick
// reuse (full state management would need a context provider; this keeps
// the change surgical and good enough for a typical <500-entry list).
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/api/client";
// Cache the list on the module to avoid refetching every time a picker
// opens. Small; a full refresh happens on page reload.
let emojiCache = null;
let emojiPromise = null;
function loadEmojis(token) {
    if (emojiCache)
        return Promise.resolve(emojiCache);
    if (!emojiPromise) {
        emojiPromise = api.listEmojis(token).then((list) => { emojiCache = list ?? []; return emojiCache; }, (e) => { emojiPromise = null; throw e; });
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
export function customEmojiByName(name) {
    if (!emojiCache)
        return null;
    return emojiCache.find((e) => e.name === name) ?? null;
}
const EMOJI_CHAR = {
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
export function EmojiPicker({ token, quick, onPick, onClose }) {
    const [custom, setCustom] = useState(emojiCache ?? []);
    const [loading, setLoading] = useState(emojiCache === null);
    const [q, setQ] = useState("");
    const wrapRef = useRef(null);
    useEffect(() => {
        let cancelled = false;
        if (emojiCache)
            return;
        loadEmojis(token).then((list) => { if (!cancelled) {
            setCustom(list);
            setLoading(false);
        } }, () => { if (!cancelled)
            setLoading(false); });
        return () => { cancelled = true; };
    }, [token]);
    // Close on outside click. The picker lives inside MessageRow, so a
    // click on the ⋯ reaction button or another message should dismiss it.
    useEffect(() => {
        function onDoc(e) {
            if (!wrapRef.current)
                return;
            if (!wrapRef.current.contains(e.target))
                onClose();
        }
        document.addEventListener("mousedown", onDoc);
        return () => document.removeEventListener("mousedown", onDoc);
    }, [onClose]);
    const filtered = useMemo(() => {
        const needle = q.trim().toLowerCase();
        if (!needle)
            return custom;
        return custom.filter((e) => e.name.includes(needle));
    }, [custom, q]);
    return (_jsxs("div", { className: "emoji-picker emoji-picker-wide", ref: wrapRef, children: [_jsx("div", { className: "emoji-picker-quick", children: quick.map((e) => (_jsx("button", { type: "button", className: "emoji-btn", onClick: () => onPick(e), title: `:${e}:`, children: EMOJI_CHAR[e] ?? `:${e}:` }, e))) }), _jsx("div", { className: "emoji-picker-search", children: _jsx("input", { className: "field-input", placeholder: "\uCEE4\uC2A4\uD140 \uC774\uBAA8\uC9C0 \uAC80\uC0C9\u2026", value: q, onChange: (e) => setQ(e.target.value), style: { height: 30, fontSize: 12 } }) }), _jsx("div", { className: "emoji-picker-custom", children: loading ? (_jsx("div", { className: "chat-empty", style: { padding: 8, fontSize: 12 }, children: "\uBD88\uB7EC\uC624\uB294 \uC911\u2026" })) : filtered.length === 0 ? (_jsx("div", { className: "chat-empty", style: { padding: 8, fontSize: 12 }, children: q ? "일치하는 이모지 없음" : "커스텀 이모지 없음" })) : (filtered.map((e) => (_jsx("button", { type: "button", className: "emoji-btn emoji-btn-custom", onClick: () => onPick(e.name), title: `:${e.name}:`, children: _jsx("img", { src: api.emojiImageURL(token, e.id), alt: e.name }) }, e.id)))) })] }));
}
