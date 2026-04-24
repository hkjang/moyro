import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
// MessageBody renders a chat post's `message` string as sanitized
// GitHub-flavoured markdown. Kept as a dedicated component (rather than
// inlining into MessageRow) so we can memoize expensive parsing and so
// unit/smoke testing the render is easy.
//
// Safety stance:
// - `rehype-sanitize` with the default schema blocks <script>, <iframe>,
//   <style>, on*-event attributes, and javascript: URLs.
// - We further restrict <img> to `src` values under our own `/api/v4/`
//   origin so a malicious post body can't load a tracker pixel from an
//   external host and leak the reader's IP.
// - Links open in a new tab with `noopener noreferrer`.
//
// Custom-emoji inline rendering:
// - Before handing the string to ReactMarkdown we replace `:emoji_name:`
//   tokens with a standard markdown image syntax pointing at our emoji
//   endpoint. This plays well with surrounding markdown (`**bold :fire:
//   text**` survives) because the image is just another inline node.
// - We only rewrite tokens whose name is present in the Phase 13 emoji
//   cache; unknown `:whatever:` stays as literal text so we don't create
//   broken image requests.
import { useMemo } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeSanitize from "rehype-sanitize";
import { api } from "@/api/client";
import { customEmojiByName } from "./EmojiPicker";
// Matches `:emoji_name:` where name is the Phase 13 allowed charset. The
// leading boundary avoids grabbing URLs like `http://...:8080:rest`.
const EMOJI_TOKEN = /(^|[^a-zA-Z0-9_])(:([a-z0-9_-]{1,40}):)/g;
// Rewrite `:name:` into markdown image syntax when the name resolves to
// a known custom emoji. Plain-text tokens (including leading whitespace)
// are preserved exactly so the rest of the markdown parses unchanged.
function rewriteEmojis(source, token) {
    return source.replace(EMOJI_TOKEN, (whole, lead, _token, name) => {
        const emoji = customEmojiByName(name);
        if (!emoji)
            return whole;
        const url = api.emojiImageURL(token, emoji.id);
        // Alt prefix `emoji:` lets the <img> override below switch to the
        // inline emoji styling without parsing the src again.
        return `${lead}![emoji:${name}](${url})`;
    });
}
// Only allow images whose src points into our own API surface. Everything
// else (arbitrary https, javascript:, data:, file:) is dropped silently.
function isSafeImageSrc(src) {
    if (!src)
        return false;
    // Relative path under /api/v4/ is ours. A leading slash is the
    // canonical form the emoji/file endpoints return.
    return src.startsWith("/api/v4/");
}
export function MessageBody({ source, token, linkMetadata }) {
    // Memoize the emoji-rewritten string so ReactMarkdown only re-parses
    // when the source actually changes, not on every parent re-render.
    const rewritten = useMemo(() => rewriteEmojis(source, token), [source, token]);
    // Only previews with at least a title are worth rendering. `fetched_at`
    // being present without a title happens on SSRF-blocked / 4xx fetches
    // (we cache the failure to avoid re-hitting).
    const previews = (linkMetadata ?? []).filter((lp) => lp.title);
    return (_jsxs("div", { className: "msg-body", children: [_jsx(ReactMarkdown, { remarkPlugins: [remarkGfm], rehypePlugins: [rehypeSanitize], components: {
                    // External-link hardening. Without rel=noopener, `window.opener`
                    // leaks to the destination and enables reverse-tab phishing.
                    a: ({ href, children, ...rest }) => (_jsx("a", { ...rest, href: href, target: "_blank", rel: "noopener noreferrer", children: children })),
                    // Emoji-sized inline when alt starts with `emoji:`, otherwise a
                    // regular bounded image that lazy-loads.
                    img: ({ src, alt }) => {
                        if (!isSafeImageSrc(src))
                            return _jsx(_Fragment, { children: alt ?? "" });
                        const isEmoji = typeof alt === "string" && alt.startsWith("emoji:");
                        return (_jsx("img", { src: src, alt: alt ?? "", loading: "lazy", className: isEmoji ? "emoji-img" : "md-img" }));
                    },
                    // Stdlib react-markdown renders both inline and block code
                    // through the same `code` slot; the `inline` flag is gone in v9,
                    // so we inspect the presence of `className` (block code gets a
                    // language-* class from remark-gfm). This matches the react-
                    // markdown v9 recipe.
                    code: ({ className, children, ...rest }) => {
                        const isBlock = typeof className === "string" && className.includes("language-");
                        if (isBlock) {
                            return (_jsx("pre", { className: "md-pre", children: _jsx("code", { className: className, ...rest, children: children }) }));
                        }
                        return _jsx("code", { className: "md-code-inline", ...rest, children: children });
                    },
                }, children: rewritten }), previews.length > 0 && (_jsx("div", { className: "link-previews", children: previews.map((lp, i) => (_jsxs("a", { href: lp.url, target: "_blank", rel: "noopener noreferrer", className: "link-preview", children: [lp.image_url && (_jsx("img", { className: "link-preview-img", src: api.linkPreviewImageURL(lp.image_url), alt: "", loading: "lazy" })), _jsxs("div", { className: "link-preview-body", children: [_jsx("div", { className: "link-preview-title", children: lp.title }), lp.description && (_jsx("p", { className: "link-preview-desc", children: lp.description })), _jsx("div", { className: "link-preview-url", children: hostnameOf(lp.url) })] })] }, `${lp.url}-${i}`))) }))] }));
}
function hostnameOf(raw) {
    try {
        return new URL(raw).hostname;
    }
    catch {
        return raw;
    }
}
