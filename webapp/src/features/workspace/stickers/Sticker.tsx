import { api } from "@/api/client";
import { AuthenticatedImage } from "@/components/AuthenticatedMedia";
import { customEmojiByName } from "@/components/EmojiPicker";
import { BUILTIN_PREFIX, CUSTOM_PREFIX, stickerById, type StickerPose, type StickerSpec } from "./stickers";
import "./stickers.css";

// Cartoon renderer.
//
// Every emoticon is a chibi character drawn in comic line art: a heavy dark
// outline, a big head with large highlighted eyes, a small body with arms
// and legs posed to act out the caption, cheek blush, cel-style shading, and
// the comic effects — motion lines, sweat, anger marks, sparkle bursts — that
// make a still image read as a moment. The caption sits in a speech bubble.
//
// Canvas is 128×128. The head centre is (64, 50); the body hangs below it.

const INK = "#24304A";
const STROKE = 3.2;

const CHARACTER_COLOR: Record<NonNullable<StickerSpec["character"]>, string> = {
  moyo: "#4F6FE6",
  cat: "#F5B84B",
  dog: "#D69A63",
  bear: "#B07A4E",
  rabbit: "#FBF7F1",
  duck: "#F7D34A",
};

function darken(hex: string, amount = 0.18): string {
  const n = parseInt(hex.slice(1), 16);
  const r = Math.max(0, ((n >> 16) & 255) * (1 - amount));
  const g = Math.max(0, ((n >> 8) & 255) * (1 - amount));
  const b = Math.max(0, (n & 255) * (1 - amount));
  return `rgb(${r | 0}, ${g | 0}, ${b | 0})`;
}

type Ctx = { color: string; shade: string; character: NonNullable<StickerSpec["character"]> };

const outlined = { stroke: INK, strokeWidth: STROKE, strokeLinejoin: "round" as const, strokeLinecap: "round" as const };

/* ── Body & limbs ─────────────────────────────────────────────────────── */

function Arm({ x1, y1, x2, y2, color, hand }: { x1: number; y1: number; x2: number; y2: number; color: string; hand?: "open" | "thumb" | "fist" | "none" }) {
  return (
    <g>
      <path d={`M${x1} ${y1} L${x2} ${y2}`} stroke={INK} strokeWidth={STROKE + 5} strokeLinecap="round" />
      <path d={`M${x1} ${y1} L${x2} ${y2}`} stroke={color} strokeWidth={5} strokeLinecap="round" />
      {hand !== "none" && (
        <circle cx={x2} cy={y2} r="5" fill={color} {...outlined} strokeWidth={2.4} />
      )}
      {hand === "thumb" && <path d={`M${x2 + 1} ${y2 - 4} l2 -7 l4 1 l-2 8`} fill={color} {...outlined} strokeWidth={2} />}
      {hand === "open" && (
        <g stroke={INK} strokeWidth={1.6} strokeLinecap="round">
          <path d={`M${x2 - 3} ${y2 - 5} l-1 -4`} /><path d={`M${x2} ${y2 - 6} l0 -4`} /><path d={`M${x2 + 3} ${y2 - 5} l1 -4`} />
        </g>
      )}
    </g>
  );
}

function Legs({ color, pose }: { color: string; pose: StickerPose }) {
  if (pose === "sit") {
    return (
      <g>
        <path d="M50 108 l-10 4" stroke={INK} strokeWidth={STROKE + 5} strokeLinecap="round" />
        <path d="M78 108 l10 4" stroke={INK} strokeWidth={STROKE + 5} strokeLinecap="round" />
        <path d="M50 108 l-10 4" stroke={color} strokeWidth={5} strokeLinecap="round" />
        <path d="M78 108 l10 4" stroke={color} strokeWidth={5} strokeLinecap="round" />
      </g>
    );
  }
  const spread = pose === "run" ? 10 : 0;
  const lift = pose === "run" ? 6 : 0;
  return (
    <g>
      <ellipse cx={54 - spread} cy={112 - lift} rx="8" ry="5" fill={color} {...outlined} strokeWidth={2.6} />
      <ellipse cx={74 + spread} cy={112} rx="8" ry="5" fill={color} {...outlined} strokeWidth={2.6} />
    </g>
  );
}

function Body({ ctx, pose }: { ctx: Ctx; pose: StickerPose }) {
  const lean = pose === "run" ? -8 : pose === "slump" ? 3 : 0;
  return (
    <g transform={`rotate(${lean} 64 90)`}>
      <path d="M46 78 q0 -8 18 -8 q18 0 18 8 v20 q0 12 -18 12 q-18 0 -18 -12 z" fill={ctx.color} {...outlined} />
      <path d="M50 96 q14 10 28 0 v4 q-14 10 -28 0 z" fill={ctx.shade} opacity="0.55" />
      {ctx.character === "bear" && <ellipse cx="64" cy="94" rx="9" ry="10" fill="#EBCFB0" opacity="0.9" />}
      {ctx.character === "duck" && (
        <g>
          <path d="M46 86 q-10 4 -8 12 q6 2 10 -6 z" fill={ctx.color} {...outlined} strokeWidth={2.4} />
          <path d="M82 86 q10 4 8 12 q-6 2 -10 -6 z" fill={ctx.color} {...outlined} strokeWidth={2.4} />
        </g>
      )}
    </g>
  );
}

function Arms({ ctx, pose, prop }: { ctx: Ctx; pose: StickerPose; prop?: string }) {
  const c = ctx.color;
  switch (pose) {
    case "wave":
      return (
        <g>
          <Arm x1={48} y1={82} x2={40} y2={98} color={c} />
          <Arm x1={80} y1={80} x2={98} y2={62} color={c} hand="open" />
          <g stroke={INK} strokeWidth={2} strokeLinecap="round" opacity="0.7">
            <path d="M106 54 l6 -4" /><path d="M108 62 l7 0" /><path d="M106 70 l6 4" />
          </g>
        </g>
      );
    case "thumbs":
      return (
        <g>
          <Arm x1={48} y1={82} x2={40} y2={98} color={c} />
          <Arm x1={80} y1={80} x2={96} y2={72} color={c} hand="thumb" />
        </g>
      );
    case "cheer":
      return (
        <g>
          <Arm x1={48} y1={80} x2={34} y2={60} color={c} hand="open" />
          <Arm x1={80} y1={80} x2={94} y2={60} color={c} hand="open" />
        </g>
      );
    case "hold":
      return (
        <g>
          <Arm x1={48} y1={84} x2={56} y2={98} color={c} hand="none" />
          <Arm x1={80} y1={84} x2={72} y2={98} color={c} hand="none" />
          {prop && <text x="64" y="104" fontSize="22" textAnchor="middle">{prop}</text>}
          <circle cx="56" cy="98" r="4.5" fill={c} {...outlined} strokeWidth={2.2} />
          <circle cx="72" cy="98" r="4.5" fill={c} {...outlined} strokeWidth={2.2} />
        </g>
      );
    case "run":
      return (
        <g>
          <Arm x1={48} y1={82} x2={34} y2={90} color={c} hand="fist" />
          <Arm x1={80} y1={82} x2={92} y2={70} color={c} hand="fist" />
          <g stroke={INK} strokeWidth={2.2} strokeLinecap="round" opacity="0.6">
            <path d="M20 96 h10" /><path d="M16 104 h12" /><path d="M22 112 h8" />
          </g>
        </g>
      );
    case "slump":
      return (
        <g>
          <Arm x1={48} y1={84} x2={44} y2={104} color={c} />
          <Arm x1={80} y1={84} x2={84} y2={104} color={c} />
        </g>
      );
    case "hide":
      return (
        <g>
          <Arm x1={48} y1={82} x2={52} y2={58} color={c} hand="none" />
          <Arm x1={80} y1={82} x2={76} y2={58} color={c} hand="none" />
          <circle cx="52" cy="56" r="8" fill={c} {...outlined} strokeWidth={2.4} />
          <circle cx="76" cy="56" r="8" fill={c} {...outlined} strokeWidth={2.4} />
        </g>
      );
    case "cross":
      return (
        <g>
          <Arm x1={48} y1={82} x2={76} y2={92} color={c} hand="none" />
          <Arm x1={80} y1={82} x2={52} y2={92} color={c} hand="none" />
        </g>
      );
    case "cry":
      return (
        <g>
          <Arm x1={48} y1={82} x2={50} y2={62} color={c} hand="none" />
          <Arm x1={80} y1={82} x2={78} y2={62} color={c} hand="none" />
          <circle cx="50" cy="61" r="6" fill={c} {...outlined} strokeWidth={2.2} />
          <circle cx="78" cy="61" r="6" fill={c} {...outlined} strokeWidth={2.2} />
        </g>
      );
    case "point":
      return (
        <g>
          <Arm x1={48} y1={82} x2={40} y2={98} color={c} />
          <Arm x1={80} y1={82} x2={102} y2={80} color={c} hand="fist" />
        </g>
      );
    case "sit":
      return (
        <g>
          <Arm x1={48} y1={84} x2={44} y2={100} color={c} />
          <Arm x1={80} y1={84} x2={84} y2={100} color={c} />
        </g>
      );
    default:
      return (
        <g>
          <Arm x1={48} y1={84} x2={42} y2={100} color={c} />
          <Arm x1={80} y1={84} x2={86} y2={100} color={c} />
        </g>
      );
  }
}

/* ── Head & features ──────────────────────────────────────────────────── */

function HeadShape({ ctx }: { ctx: Ctx }) {
  const { color, shade, character } = ctx;
  return (
    <g>
      {character === "cat" && (
        <g>
          <path d="M34 36 l2 -24 l20 14 z" fill={color} {...outlined} />
          <path d="M94 36 l-2 -24 l-20 14 z" fill={color} {...outlined} />
          <path d="M39 32 l1 -13 l11 8 z" fill="#F8C3CF" />
          <path d="M89 32 l-1 -13 l-11 8 z" fill="#F8C3CF" />
        </g>
      )}
      {character === "bear" && (
        <g>
          <circle cx="36" cy="26" r="11" fill={color} {...outlined} />
          <circle cx="92" cy="26" r="11" fill={color} {...outlined} />
          <circle cx="36" cy="26" r="5.5" fill="#EBCFB0" />
          <circle cx="92" cy="26" r="5.5" fill="#EBCFB0" />
        </g>
      )}
      {character === "rabbit" && (
        <g>
          <path d="M44 32 q-8 -30 6 -30 q12 2 8 30 z" fill={color} {...outlined} />
          <path d="M84 32 q8 -30 -6 -30 q-12 2 -8 30 z" fill={color} {...outlined} />
          <path d="M47 28 q-4 -20 4 -20 q6 2 4 20 z" fill="#F8C3CF" />
          <path d="M81 28 q4 -20 -4 -20 q-6 2 -4 20 z" fill="#F8C3CF" />
        </g>
      )}
      {character === "dog" && (
        <g>
          <path d="M34 40 q-14 22 0 40 q12 6 14 -8 z" fill={darken(color, 0.28)} {...outlined} />
          <path d="M94 40 q14 22 0 40 q-12 6 -14 -8 z" fill={darken(color, 0.28)} {...outlined} />
        </g>
      )}
      {character === "duck" && (
        <path d="M58 20 q6 -12 12 0 q-3 -4 -6 2 q-3 -6 -6 -2 z" fill={color} {...outlined} strokeWidth={2.4} />
      )}
      <ellipse cx="64" cy="50" rx="32" ry="30" fill={color} {...outlined} />
      <path d="M38 62 q26 26 52 0 q-2 12 -26 14 q-24 -2 -26 -14 z" fill={shade} opacity="0.45" />
      <ellipse cx="50" cy="32" rx="9" ry="5" fill="#fff" opacity="0.35" transform="rotate(-20 50 32)" />
      {character === "cat" && (
        <g stroke={INK} strokeWidth="2" strokeLinecap="round" opacity="0.8">
          <path d="M26 56 h12" /><path d="M26 63 h12" /><path d="M90 56 h12" /><path d="M90 63 h12" />
        </g>
      )}
      {character === "dog" && (
        <g>
          <ellipse cx="64" cy="66" rx="13" ry="9" fill="#F5E6D6" {...outlined} strokeWidth={2.4} />
          <ellipse cx="64" cy="61" rx="5" ry="3.8" fill={INK} />
        </g>
      )}
      {character === "bear" && (
        <g>
          <ellipse cx="64" cy="66" rx="12" ry="8.5" fill="#EBCFB0" {...outlined} strokeWidth={2.4} />
          <ellipse cx="64" cy="61" rx="4.5" ry="3.4" fill={INK} />
        </g>
      )}
      {(character === "cat" || character === "rabbit") && <path d="M60 62 l4 4 l4 -4 z" fill="#E8899A" />}
    </g>
  );
}

function Eyes({ kind, hide }: { kind: StickerSpec["eyes"]; hide: boolean }) {
  if (hide) return null;
  const highlight = (cx: number, cy: number) => <circle cx={cx + 2} cy={cy - 2.5} r="2" fill="#fff" />;
  switch (kind) {
    case "happy":
      return (
        <g fill="none" stroke={INK} strokeWidth="3.4" strokeLinecap="round">
          <path d="M44 50 q7 -9 14 0" /><path d="M70 50 q7 -9 14 0" />
        </g>
      );
    case "closed":
      return (
        <g fill="none" stroke={INK} strokeWidth="3.4" strokeLinecap="round">
          <path d="M44 50 q7 5 14 0" /><path d="M70 50 q7 5 14 0" />
        </g>
      );
    case "sleepy":
      return (
        <g fill="none" stroke={INK} strokeWidth="3.4" strokeLinecap="round">
          <path d="M44 52 h14" /><path d="M70 52 h14" />
          <path d="M46 46 q5 3 10 0" opacity="0.5" strokeWidth="2" /><path d="M72 46 q5 3 10 0" opacity="0.5" strokeWidth="2" />
        </g>
      );
    case "wide":
      return (
        <g>
          <ellipse cx="51" cy="50" rx="9" ry="10" fill="#fff" {...outlined} strokeWidth={2.4} />
          <ellipse cx="77" cy="50" rx="9" ry="10" fill="#fff" {...outlined} strokeWidth={2.4} />
          <circle cx="51" cy="51" r="4" fill={INK} /><circle cx="77" cy="51" r="4" fill={INK} />
          {highlight(51, 50)}{highlight(77, 50)}
        </g>
      );
    case "tear":
      return (
        <g>
          <ellipse cx="51" cy="50" rx="7.5" ry="8.5" fill="#fff" {...outlined} strokeWidth={2.4} />
          <ellipse cx="77" cy="50" rx="7.5" ry="8.5" fill="#fff" {...outlined} strokeWidth={2.4} />
          <circle cx="51" cy="52" r="4" fill={INK} /><circle cx="77" cy="52" r="4" fill={INK} />
          {highlight(51, 50)}{highlight(77, 50)}
          <path d="M46 60 q-5 12 2 14 q7 -2 2 -14 z" fill="#63B3F5" {...outlined} strokeWidth={1.6} />
          <path d="M82 60 q5 12 -2 14 q-7 -2 -2 -14 z" fill="#63B3F5" {...outlined} strokeWidth={1.6} />
        </g>
      );
    case "wink":
      return (
        <g>
          <ellipse cx="51" cy="50" rx="7" ry="8" fill="#fff" {...outlined} strokeWidth={2.4} />
          <circle cx="51" cy="51" r="3.8" fill={INK} />{highlight(51, 50)}
          <path d="M70 50 q7 -9 14 0" fill="none" stroke={INK} strokeWidth="3.4" strokeLinecap="round" />
        </g>
      );
    case "star":
      return (
        <g fill="#FFB703" {...outlined} strokeWidth={1.8}>
          <path d="M51 40 l3.6 7.4 8 1.1 -5.8 5.6 1.4 8 -7.2 -3.8 -7.2 3.8 1.4 -8 -5.8 -5.6 8 -1.1z" />
          <path d="M77 40 l3.6 7.4 8 1.1 -5.8 5.6 1.4 8 -7.2 -3.8 -7.2 3.8 1.4 -8 -5.8 -5.6 8 -1.1z" />
        </g>
      );
    default:
      return (
        <g>
          <ellipse cx="51" cy="50" rx="7" ry="8" fill="#fff" {...outlined} strokeWidth={2.4} />
          <ellipse cx="77" cy="50" rx="7" ry="8" fill="#fff" {...outlined} strokeWidth={2.4} />
          <circle cx="51" cy="51" r="3.8" fill={INK} /><circle cx="77" cy="51" r="3.8" fill={INK} />
          {highlight(51, 50)}{highlight(77, 50)}
        </g>
      );
  }
}

function Mouth({ kind, ctx }: { kind: StickerSpec["mouth"]; ctx: Ctx }) {
  const line = { fill: "none", stroke: INK, strokeWidth: 3.2, strokeLinecap: "round" as const };
  if (ctx.character === "duck") {
    const open = kind === "open" || kind === "grin" || kind === "o";
    const sad = kind === "frown" || kind === "wavy";
    return (
      <g>
        <path d={sad ? "M44 74 q20 -10 40 0 q-2 8 -20 8 q-18 0 -20 -8 z" : "M44 70 q20 -8 40 0 q-2 10 -20 10 q-18 0 -20 -10 z"} fill="#F49A3C" {...outlined} strokeWidth={2.6} />
        {open && <path d="M48 72 q16 4 32 0 q-3 5 -16 5 q-13 0 -16 -5 z" fill="#B8552A" />}
      </g>
    );
  }
  const y = ctx.character === "dog" || ctx.character === "bear" ? 74 : 68;
  switch (kind) {
    case "grin":
      return (
        <g>
          <path d={`M48 ${y} q16 20 32 0 z`} fill={INK} />
          <path d={`M52 ${y + 1} h24 v4 q-12 4 -24 0 z`} fill="#fff" />
          <path d={`M55 ${y + 10} q9 6 18 0 z`} fill="#F28FA8" />
        </g>
      );
    case "open":
      return (
        <g>
          <ellipse cx="64" cy={y + 8} rx="11" ry="9" fill={INK} />
          <ellipse cx="64" cy={y + 12} rx="6" ry="3.5" fill="#F28FA8" />
        </g>
      );
    case "frown":
      return <path d={`M52 ${y + 8} q12 -10 24 0`} {...line} />;
    case "flat":
      return <path d={`M53 ${y + 4} h22`} {...line} />;
    case "wavy":
      return <path d={`M51 ${y + 4} q6 -6 12 0 t12 0`} {...line} />;
    case "o":
      return <ellipse cx="64" cy={y + 6} rx="6" ry="7" fill={INK} />;
    case "tongue":
      return (
        <g>
          <path d={`M50 ${y} q14 16 28 0`} {...line} />
          <path d={`M60 ${y + 4} q4 12 12 4 q-2 -6 -12 -4 z`} fill="#F28FA8" {...outlined} strokeWidth={2} />
        </g>
      );
    default:
      return <path d={`M50 ${y} q14 12 28 0`} {...line} />;
  }
}

function Brow({ kind }: { kind: StickerSpec["brow"] }) {
  const line = { fill: "none", stroke: INK, strokeWidth: 3.4, strokeLinecap: "round" as const };
  if (kind === "angry") return <g {...line}><path d="M42 36 l16 6" /><path d="M86 36 l-16 6" /></g>;
  if (kind === "sad") return <g {...line}><path d="M42 42 l16 -6" /><path d="M86 42 l-16 -6" /></g>;
  if (kind === "raised") return <g {...line}><path d="M42 38 q8 -8 16 -2" /><path d="M70 34 h16" /></g>;
  return null;
}

function Effects({ spec }: { spec: StickerSpec }) {
  const items: React.ReactNode[] = [];
  const glyph = spec.float && { hearts: "💗", sparkles: "✨", zzz: "💤", notes: "🎵", confetti: "🎊", question: "❓", anger: "💢" }[spec.float];
  if (glyph) {
    items.push(<text key="f1" x="100" y="26" fontSize="18">{glyph}</text>);
    items.push(<text key="f2" x="10" y="40" fontSize="13">{glyph}</text>);
  }
  if (spec.sweat) items.push(<path key="sweat" d="M100 40 q-6 10 0 14 q6 -4 0 -14 z" fill="#63B3F5" {...outlined} strokeWidth={1.6} />);
  if (spec.brow === "angry" || spec.float === "anger") {
    items.push(<text key="bang" x="18" y="24" fontSize="16" fontWeight="900" fill={INK}>!!</text>);
  }
  if (spec.eyes === "wide" && spec.mouth === "o") {
    items.push(<g key="shock" stroke={INK} strokeWidth="2.4" strokeLinecap="round"><path d="M26 24 l6 6" /><path d="M20 34 l8 2" /><path d="M102 24 l-6 6" /><path d="M108 34 l-8 2" /></g>);
  }
  if (spec.prop && spec.pose !== "hold") {
    items.push(<text key="prop" x="104" y="104" fontSize="24" textAnchor="middle">{spec.prop}</text>);
  }
  return <g className="sticker-float">{items}</g>;
}

function Bubble({ caption }: { caption: string }) {
  const width = Math.min(112, Math.max(44, caption.length * 10 + 16));
  const x = 64 - width / 2;
  return (
    <g>
      <rect x={x} y="112" width={width} height="16" rx="8" fill="#fff" {...outlined} strokeWidth={2} />
      <text x="64" y="123.5" textAnchor="middle" fontSize="10.5" fontWeight="800" fill={INK}>{caption}</text>
    </g>
  );
}

/** One built-in emoticon drawn as cartoon SVG; the caption is real text, so it stays crisp and searchable. */
export function BuiltinSticker({ spec, size = 140, showCaption = true }: { spec: StickerSpec; size?: number; showCaption?: boolean }) {
  const character = spec.character ?? "moyo";
  const color = spec.color ?? CHARACTER_COLOR[character];
  const ctx: Ctx = { color, shade: darken(color), character };
  const pose = spec.pose ?? "stand";
  const tilt = pose === "slump" ? 8 : pose === "run" ? -10 : pose === "cheer" ? -4 : 0;
  return (
    <svg
      className="sticker"
      viewBox="0 0 128 128"
      width={size}
      height={size}
      role="img"
      aria-label={`이모티콘: ${spec.caption}`}
    >
      {/* The figure sits a little above the caption bubble so feet and bubble never overlap. */}
      <g transform="translate(0 -7)">
      <ellipse cx="64" cy="117" rx="30" ry="4" fill="rgba(36,48,74,0.12)" />
      <Body ctx={ctx} pose={pose} />
      <Legs color={color} pose={pose} />
      <g transform={`rotate(${tilt} 64 50)`}>
        <HeadShape ctx={ctx} />
        {spec.blush && (
          <g fill="#F7A1B5" opacity="0.85">
            <ellipse cx="40" cy="60" rx="6" ry="3.5" /><ellipse cx="88" cy="60" rx="6" ry="3.5" />
          </g>
        )}
        <Brow kind={spec.brow} />
        <Eyes kind={spec.eyes} hide={pose === "hide"} />
        <Mouth kind={spec.mouth} ctx={ctx} />
      </g>
      <Arms ctx={ctx} pose={pose} prop={spec.prop} />
      <Effects spec={spec} />
      </g>
      {showCaption && <Bubble caption={spec.caption} />}
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
