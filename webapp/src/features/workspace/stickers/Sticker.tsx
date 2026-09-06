import { api } from "@/api/client";
import { AuthenticatedImage } from "@/components/AuthenticatedMedia";
import { customEmojiByName } from "@/components/EmojiPicker";
import { BUILTIN_PREFIX, CUSTOM_PREFIX, stickerById, type StickerSpec } from "./stickers";
import "./stickers.css";

const BRAND = "#3157D5";

function Eyes({ kind }: { kind: StickerSpec["eyes"] }) {
  switch (kind) {
    case "happy":
      return (
        <g fill="none" stroke="#1F2A44" strokeWidth="4" strokeLinecap="round">
          <path d="M40 58 q8 -10 16 0" />
          <path d="M72 58 q8 -10 16 0" />
        </g>
      );
    case "closed":
      return (
        <g fill="none" stroke="#1F2A44" strokeWidth="4" strokeLinecap="round">
          <path d="M40 56 h16" />
          <path d="M72 56 h16" />
        </g>
      );
    case "wide":
      return (
        <g>
          <circle cx="48" cy="56" r="8" fill="#fff" stroke="#1F2A44" strokeWidth="3" />
          <circle cx="80" cy="56" r="8" fill="#fff" stroke="#1F2A44" strokeWidth="3" />
          <circle cx="48" cy="56" r="3.5" fill="#1F2A44" />
          <circle cx="80" cy="56" r="3.5" fill="#1F2A44" />
        </g>
      );
    case "tear":
      return (
        <g>
          <circle cx="48" cy="56" r="5" fill="#1F2A44" />
          <circle cx="80" cy="56" r="5" fill="#1F2A44" />
          <path d="M44 64 q-4 10 2 12 q6 -2 2 -12 z" fill="#5FA8F5" />
          <path d="M84 64 q4 10 -2 12 q-6 -2 -2 -12 z" fill="#5FA8F5" />
        </g>
      );
    case "wink":
      return (
        <g>
          <circle cx="48" cy="56" r="5" fill="#1F2A44" />
          <path d="M72 58 q8 -10 16 0" fill="none" stroke="#1F2A44" strokeWidth="4" strokeLinecap="round" />
        </g>
      );
    case "sleepy":
      return (
        <g fill="none" stroke="#1F2A44" strokeWidth="4" strokeLinecap="round">
          <path d="M40 58 q8 6 16 0" />
          <path d="M72 58 q8 6 16 0" />
        </g>
      );
    case "star":
      return (
        <g fill="#FFB703" stroke="#1F2A44" strokeWidth="1.5">
          <path d="M48 46 l3.5 7 7.5 1 -5.5 5.3 1.3 7.7 -6.8 -3.6 -6.8 3.6 1.3 -7.7 -5.5 -5.3 7.5 -1z" />
          <path d="M80 46 l3.5 7 7.5 1 -5.5 5.3 1.3 7.7 -6.8 -3.6 -6.8 3.6 1.3 -7.7 -5.5 -5.3 7.5 -1z" />
        </g>
      );
    default:
      return (
        <g fill="#1F2A44">
          <circle cx="48" cy="56" r="5" />
          <circle cx="80" cy="56" r="5" />
        </g>
      );
  }
}

function Mouth({ kind }: { kind: StickerSpec["mouth"] }) {
  const stroke = { fill: "none", stroke: "#1F2A44", strokeWidth: 4, strokeLinecap: "round" as const };
  switch (kind) {
    case "grin":
      return <path d="M46 76 q18 22 36 0 z" fill="#1F2A44" />;
    case "open":
      return (
        <g>
          <ellipse cx="64" cy="82" rx="12" ry="10" fill="#1F2A44" />
          <ellipse cx="64" cy="87" rx="7" ry="4" fill="#F28FA8" />
        </g>
      );
    case "frown":
      return <path d="M50 86 q14 -12 28 0" {...stroke} />;
    case "flat":
      return <path d="M52 80 h24" {...stroke} />;
    case "wavy":
      return <path d="M50 80 q6 -6 12 0 t12 0" {...stroke} />;
    case "o":
      return <circle cx="64" cy="82" r="7" fill="#1F2A44" />;
    case "tongue":
      return (
        <g>
          <path d="M48 76 q16 16 32 0" {...stroke} />
          <path d="M60 80 q4 12 12 4 q-2 -6 -12 -4 z" fill="#F28FA8" />
        </g>
      );
    default:
      return <path d="M48 76 q16 14 32 0" {...stroke} />;
  }
}

function Brow({ kind }: { kind: StickerSpec["brow"] }) {
  const stroke = { fill: "none", stroke: "#1F2A44", strokeWidth: 4, strokeLinecap: "round" as const };
  if (kind === "angry") return <g {...stroke}><path d="M38 42 l18 6" /><path d="M90 42 l-18 6" /></g>;
  if (kind === "sad") return <g {...stroke}><path d="M38 48 l18 -6" /><path d="M90 48 l-18 -6" /></g>;
  if (kind === "raised") return <g {...stroke}><path d="M38 44 q10 -8 20 -2" /><path d="M72 40 h16" /></g>;
  return null;
}

function Floating({ kind }: { kind: StickerSpec["float"] }) {
  const glyph = { hearts: "💗", sparkles: "✨", zzz: "💤", notes: "🎵", confetti: "🎊", question: "❓", anger: "💢" }[kind ?? "sparkles"];
  if (!kind) return null;
  return (
    <g className="sticker-float" fontSize="18">
      <text x="98" y="30">{glyph}</text>
      <text x="14" y="42" fontSize="13">{glyph}</text>
    </g>
  );
}

/** One built-in emoticon drawn as SVG; the caption is real text, so it stays crisp and searchable. */
export function BuiltinSticker({ spec, size = 140, showCaption = true }: { spec: StickerSpec; size?: number; showCaption?: boolean }) {
  const color = spec.color ?? BRAND;
  return (
    <svg
      className="sticker"
      viewBox="0 0 128 128"
      width={size}
      height={size}
      role="img"
      aria-label={`이모티콘: ${spec.caption}`}
    >
      <ellipse cx="66" cy="112" rx="34" ry="6" fill="rgba(0,0,0,0.08)" />
      <path
        d="M64 14 c30 0 46 20 46 46 c0 26 -18 44 -46 44 c-28 0 -46 -18 -46 -44 c0 -26 16 -46 46 -46 z"
        fill={color}
      />
      <path d="M40 30 q12 -10 30 -6" fill="none" stroke="rgba(255,255,255,0.45)" strokeWidth="5" strokeLinecap="round" />
      {spec.blush && (
        <g fill="rgba(242,143,168,0.7)">
          <ellipse cx="36" cy="70" rx="7" ry="4" />
          <ellipse cx="92" cy="70" rx="7" ry="4" />
        </g>
      )}
      <Brow kind={spec.brow} />
      <Eyes kind={spec.eyes} />
      <Mouth kind={spec.mouth} />
      {spec.sweat && <path d="M100 44 q-6 10 0 14 q6 -4 0 -14 z" fill="#5FA8F5" />}
      {spec.prop && <text x="92" y="106" fontSize="30" textAnchor="middle">{spec.prop}</text>}
      <Floating kind={spec.float} />
      {showCaption && (
        <g>
          <rect x="14" y="108" width="100" height="18" rx="9" fill="#fff" stroke="rgba(31,42,68,0.18)" />
          <text x="64" y="121" textAnchor="middle" fontSize="11" fontWeight="700" fill="#1F2A44">{spec.caption}</text>
        </g>
      )}
    </svg>
  );
}

/**
 * Renders any emoticon id from `props.sticker`. Built-ins draw from their
 * spec; a custom emoji renders its uploaded image large. Unknown ids render
 * nothing so the caller can fall back to the caption text.
 */
export function Sticker({ id, token, size = 140 }: { id: string; token: string; size?: number }) {
  if (id.startsWith(CUSTOM_PREFIX)) {
    const custom = customEmojiByName(id.slice(CUSTOM_PREFIX.length));
    if (!custom) return null;
    return (
      <AuthenticatedImage
        token={token}
        path={api.emojiImagePath(custom.id)}
        className="sticker sticker-custom"
        alt={`이모티콘: ${custom.name}`}
        style={{ width: size, height: size }}
      />
    );
  }
  if (!id.startsWith(BUILTIN_PREFIX)) return null;
  const spec = stickerById(id);
  if (!spec) return null;
  return <BuiltinSticker spec={spec} size={size} />;
}
