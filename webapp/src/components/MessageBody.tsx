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
import { api, type LinkPreview } from "@/api/client";
import { AuthenticatedImage } from "./AuthenticatedMedia";
import { customEmojiByName } from "./EmojiPicker";

type Props = {
  source: string;
  token: string;
  // Phase 18: link previews attached to the post. Rendered as cards
  // below the markdown body. Undefined/empty skips the block entirely.
  linkMetadata?: LinkPreview[];
};

// Matches `:emoji_name:` where name is the Phase 13 allowed charset. The
// leading boundary avoids grabbing URLs like `http://...:8080:rest`.
const EMOJI_TOKEN = /(^|[^a-zA-Z0-9_])(:([a-z0-9_-]{1,40}):)/g;

// Rewrite `:name:` into markdown image syntax when the name resolves to
// a known custom emoji. Plain-text tokens (including leading whitespace)
// are preserved exactly so the rest of the markdown parses unchanged.
function rewriteEmojis(source: string): string {
  return source.replace(EMOJI_TOKEN, (whole, lead: string, _token: string, name: string) => {
    const emoji = customEmojiByName(name);
    if (!emoji) return whole;
    const url = api.emojiImagePath(emoji.id);
    // Alt prefix `emoji:` lets the <img> override below switch to the
    // inline emoji styling without parsing the src again.
    return `${lead}![emoji:${name}](${url})`;
  });
}

// Only allow images whose src points into our own API surface. Everything
// else (arbitrary https, javascript:, data:, file:) is dropped silently.
function isSafeImageSrc(src: string | undefined): src is string {
  if (!src) return false;
  return /^\/api\/v4\/(?:files\/[^/?#]+(?:\/thumbnail)?|emoji\/[^/?#]+\/image)$/.test(src);
}

export function MessageBody({ source, token, linkMetadata }: Props) {
  // Memoize the emoji-rewritten string so ReactMarkdown only re-parses
  // when the source actually changes, not on every parent re-render.
  const rewritten = useMemo(() => rewriteEmojis(source), [source]);
  // Only previews with at least a title are worth rendering. `fetched_at`
  // being present without a title happens on SSRF-blocked / 4xx fetches
  // (we cache the failure to avoid re-hitting).
  const previews = (linkMetadata ?? []).filter((lp) => lp.title);

  return (
    <div className="msg-body">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeSanitize]}
        components={{
          // External-link hardening. Without rel=noopener, `window.opener`
          // leaks to the destination and enables reverse-tab phishing.
          a: ({ href, children, ...rest }) => (
            <a
              {...rest}
              href={href}
              target="_blank"
              rel="noopener noreferrer"
            >
              {children}
            </a>
          ),
          // Emoji-sized inline when alt starts with `emoji:`, otherwise a
          // regular bounded image that lazy-loads.
          img: ({ src, alt }) => {
            if (!isSafeImageSrc(src)) return <>{alt ?? ""}</>;
            const isEmoji = typeof alt === "string" && alt.startsWith("emoji:");
            return (
              <AuthenticatedImage
                token={token}
                path={src}
                alt={alt ?? ""}
                loading="lazy"
                className={isEmoji ? "emoji-img" : "md-img"}
              />
            );
          },
          // Stdlib react-markdown renders both inline and block code
          // through the same `code` slot; the `inline` flag is gone in v9,
          // so we inspect the presence of `className` (block code gets a
          // language-* class from remark-gfm). This matches the react-
          // markdown v9 recipe.
          code: ({ className, children, ...rest }) => {
            const isBlock = typeof className === "string" && className.includes("language-");
            if (isBlock) {
              return (
                <pre className="md-pre">
                  <code className={className} {...rest}>{children}</code>
                </pre>
              );
            }
            return <code className="md-code-inline" {...rest}>{children}</code>;
          },
        }}
      >
        {rewritten}
      </ReactMarkdown>
      {previews.length > 0 && (
        <div className="link-previews">
          {previews.map((lp, i) => (
            <a
              key={`${lp.url}-${i}`}
              href={lp.url}
              target="_blank"
              rel="noopener noreferrer"
              className="link-preview"
            >
              {lp.image_url && (
                <AuthenticatedImage
                  token={token}
                  path={api.linkPreviewImagePath(lp.image_url)}
                  className="link-preview-img"
                  alt=""
                  loading="lazy"
                />
              )}
              <div className="link-preview-body">
                <div className="link-preview-title">{lp.title}</div>
                {lp.description && (
                  <p className="link-preview-desc">{lp.description}</p>
                )}
                <div className="link-preview-url">{hostnameOf(lp.url)}</div>
              </div>
            </a>
          ))}
        </div>
      )}
    </div>
  );
}

function hostnameOf(raw: string): string {
  try {
    return new URL(raw).hostname;
  } catch {
    return raw;
  }
}
