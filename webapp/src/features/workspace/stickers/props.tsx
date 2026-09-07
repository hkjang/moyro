// Vector props and comic effects.
//
// Emoticons used OS emoji glyphs for the objects a character holds and the
// marks floating around them. Those render differently on every platform —
// an Apple ☕ next to a Windows ☕ next to a bare tofu box — which is exactly
// what a drawn character pack must not look like. Everything here is drawn,
// so a sticker is identical wherever it is read, and offline.
//
// Icons are authored in a 24×24 box centred on (12, 12); callers scale and
// place them. Line weight matches the character ink so props read as part of
// the same drawing.

const INK = "#22304B";
const line = { fill: "none", stroke: INK, strokeWidth: 1.8, strokeLinecap: "round" as const, strokeLinejoin: "round" as const };
const solid = { stroke: INK, strokeWidth: 1.8, strokeLinejoin: "round" as const };

/** Every object a character can hold or stand beside. */
export type StickerPropName =
  | "cup" | "bowl" | "bone" | "cookie" | "honey" | "fish" | "carrot"
  | "clock" | "hourglass" | "sun" | "moon" | "star" | "check"
  | "clipboard" | "memo" | "clip" | "tray" | "laptop" | "bag" | "megaphone"
  | "trophy" | "medal" | "crown" | "gift" | "balloon" | "ribbon"
  | "heart" | "flower" | "palm" | "ball" | "rest";

const PROPS: Record<StickerPropName, React.ReactNode> = {
  cup: (
    <g>
      <path d="M5 9 h11 v7 a4 4 0 0 1 -4 4 h-3 a4 4 0 0 1 -4 -4 z" fill="#F3F5F9" {...solid} />
      <path d="M16 11 h2.5 a2.5 2.5 0 0 1 0 5 H16" {...line} />
      <path d="M6 9 h9 v2 h-9 z" fill="#C0703C" />
      <path d="M8 5 q1.5 -2 0 -3.5" {...line} strokeWidth={1.4} />
      <path d="M12 5 q1.5 -2 0 -3.5" {...line} strokeWidth={1.4} />
    </g>
  ),
  bowl: (
    <g>
      <path d="M3 10 h18 v2 a9 8 0 0 1 -18 0 z" fill="#F3F5F9" {...solid} />
      <ellipse cx="12" cy="10" rx="9" ry="2.6" fill="#fff" {...solid} />
      <ellipse cx="12" cy="9.6" rx="6" ry="1.6" fill="#F6E7C8" />
      <path d="M9 6 q1.5 -2.5 0 -4" {...line} strokeWidth={1.4} />
      <path d="M14 6 q1.5 -2.5 0 -4" {...line} strokeWidth={1.4} />
    </g>
  ),
  bone: (
    <g>
      <path d="M6 9 a2.6 2.6 0 1 1 2 4 l8 0 a2.6 2.6 0 1 1 2 -4 a2.6 2.6 0 1 1 -2 -4 l-8 0 a2.6 2.6 0 1 1 -2 4 z" fill="#F5EFE4" {...solid} />
    </g>
  ),
  cookie: (
    <g>
      <circle cx="12" cy="12" r="8" fill="#D9A05B" {...solid} />
      <g fill="#6B4423">
        <circle cx="9" cy="9" r="1.4" /><circle cx="15" cy="11" r="1.4" /><circle cx="11" cy="15" r="1.4" />
      </g>
    </g>
  ),
  honey: (
    <g>
      <path d="M6 9 h12 v8 a3 3 0 0 1 -3 3 h-6 a3 3 0 0 1 -3 -3 z" fill="#F0B33C" {...solid} />
      <path d="M5 6 h14 v3 h-14 z" fill="#F5EFE4" {...solid} />
      <path d="M9 12 q3 3 6 0" {...line} strokeWidth={1.4} />
    </g>
  ),
  fish: (
    <g>
      <path d="M3 12 q5 -6 12 0 q-7 6 -12 0 z" fill="#8FC7EA" {...solid} />
      <path d="M15 12 l5 -4 v8 z" fill="#8FC7EA" {...solid} />
      <circle cx="7" cy="11" r="1.1" fill={INK} />
    </g>
  ),
  carrot: (
    <g>
      <path d="M11 8 l4 0 l-2 12 z" fill="#F0863C" {...solid} />
      <path d="M11 8 q-2 -5 1 -6 q1 3 2 2 q1 3 -1 4 z" fill="#5FB56B" {...solid} />
    </g>
  ),
  clock: (
    <g>
      <circle cx="12" cy="13" r="8" fill="#F3F5F9" {...solid} />
      <path d="M12 8 v5 l4 2" {...line} />
      <path d="M6 4 l3 2" {...line} /><path d="M18 4 l-3 2" {...line} />
    </g>
  ),
  hourglass: (
    <g>
      <path d="M6 3 h12 M6 21 h12" {...line} />
      <path d="M7 3 q0 6 5 9 q-5 3 -5 9 h10 q0 -6 -5 -9 q5 -3 5 -9 z" fill="#F3F5F9" {...solid} />
      <path d="M9.5 18 q2.5 -3 5 0 z" fill="#F0B33C" />
    </g>
  ),
  sun: (
    <g>
      <circle cx="12" cy="12" r="5.5" fill="#F7CE45" {...solid} />
      <g {...line}>
        <path d="M12 2 v2.5" /><path d="M12 19.5 v2.5" /><path d="M2 12 h2.5" /><path d="M19.5 12 h2.5" />
        <path d="M5 5 l1.8 1.8" /><path d="M17.2 17.2 l1.8 1.8" /><path d="M19 5 l-1.8 1.8" /><path d="M6.8 17.2 l-1.8 1.8" />
      </g>
    </g>
  ),
  moon: (
    <g>
      <path d="M17 4 a9 9 0 1 0 3 11 a7 7 0 0 1 -3 -11 z" fill="#F7CE45" {...solid} />
    </g>
  ),
  star: (
    <path d="M12 2.5 l2.9 6 6.6 .9 -4.8 4.6 1.2 6.5 -5.9 -3.2 -5.9 3.2 1.2 -6.5 -4.8 -4.6 6.6 -.9z" fill="#F7CE45" {...solid} />
  ),
  check: (
    <g>
      <circle cx="12" cy="12" r="9" fill="#4CAF7D" {...solid} />
      <path d="M7.5 12.5 l3 3 6 -6.5" fill="none" stroke="#fff" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" />
    </g>
  ),
  clipboard: (
    <g>
      <rect x="5" y="4" width="14" height="17" rx="2" fill="#F3F5F9" {...solid} />
      <rect x="9" y="2" width="6" height="4" rx="1.4" fill="#C7CEDB" {...solid} />
      <g {...line} strokeWidth={1.5}><path d="M8.5 11 h7" /><path d="M8.5 14.5 h7" /><path d="M8.5 18 h4" /></g>
    </g>
  ),
  memo: (
    <g>
      <rect x="4" y="4" width="13" height="16" rx="1.6" fill="#FFF6D6" {...solid} />
      <g {...line} strokeWidth={1.5}><path d="M7 9 h7" /><path d="M7 12.5 h7" /><path d="M7 16 h4" /></g>
      <path d="M15 17 l6 -6 l2 2 l-6 6 l-2.6 .6 z" fill="#F0863C" {...solid} />
    </g>
  ),
  clip: (
    <path d="M17 7 v9 a5 5 0 0 1 -10 0 V6 a3 3 0 0 1 6 0 v9.5 a1.4 1.4 0 0 1 -2.8 0 V7" {...line} strokeWidth={2} />
  ),
  tray: (
    <g>
      <path d="M3 13 h5 l1.5 3 h5 l1.5 -3 h5 v5 a2 2 0 0 1 -2 2 H5 a2 2 0 0 1 -2 -2 z" fill="#C7CEDB" {...solid} />
      <path d="M12 3 v7 M9 8 l3 3 3 -3" {...line} />
    </g>
  ),
  laptop: (
    <g>
      <rect x="5" y="5" width="14" height="10" rx="1.4" fill="#F3F5F9" {...solid} />
      <rect x="7" y="7" width="10" height="6" fill="#5B7FE0" />
      <path d="M2.5 17 h19 l-1.5 3 h-16 z" fill="#C7CEDB" {...solid} />
    </g>
  ),
  bag: (
    <g>
      <path d="M5 8 h14 v11 a2 2 0 0 1 -2 2 H7 a2 2 0 0 1 -2 -2 z" fill="#5FB56B" {...solid} />
      <path d="M9 8 V6 a3 3 0 0 1 6 0 v2" {...line} />
      <path d="M5 13 h14" {...line} strokeWidth={1.5} />
    </g>
  ),
  megaphone: (
    <g>
      <path d="M4 10 h4 l9 -5 v14 l-9 -5 H4 z" fill="#F0863C" {...solid} />
      <path d="M8 14 v5 h3 v-4" fill="#F0863C" {...solid} />
      <g {...line} strokeWidth={1.4}><path d="M19 9 l3 -1" /><path d="M19 12 h3" /><path d="M19 15 l3 1" /></g>
    </g>
  ),
  trophy: (
    <g>
      <path d="M7 3 h10 v6 a5 5 0 0 1 -10 0 z" fill="#F7CE45" {...solid} />
      <path d="M7 4 H4 v2 a3 3 0 0 0 3 3" {...line} />
      <path d="M17 4 h3 v2 a3 3 0 0 1 -3 3" {...line} />
      <path d="M11 14 h2 v4 h-2 z" fill="#F7CE45" {...solid} />
      <path d="M8 18 h8 v3 H8 z" fill="#D9A05B" {...solid} />
    </g>
  ),
  medal: (
    <g>
      <path d="M8 2 l3 7 M16 2 l-3 7" {...line} strokeWidth={2.2} />
      <circle cx="12" cy="15" r="6.5" fill="#F7CE45" {...solid} />
      <circle cx="12" cy="15" r="3" fill="#E0A92E" />
    </g>
  ),
  crown: (
    <g>
      <path d="M3 17 l1.5 -11 l4.5 5 l3 -7 l3 7 l4.5 -5 L21 17 z" fill="#F7CE45" {...solid} />
      <path d="M3 17 h18 v3 H3 z" fill="#E0A92E" {...solid} />
    </g>
  ),
  gift: (
    <g>
      <rect x="3" y="9" width="18" height="12" rx="1.6" fill="#E9748A" {...solid} />
      <rect x="2" y="6" width="20" height="4" rx="1.4" fill="#F292A6" {...solid} />
      <path d="M12 6 V21" {...line} strokeWidth={2} />
      <path d="M12 6 q-5 -5 -1 -4 q3 1 1 4 z M12 6 q5 -5 1 -4 q-3 1 -1 4 z" fill="#F7CE45" {...solid} strokeWidth={1.4} />
    </g>
  ),
  balloon: (
    <g>
      <ellipse cx="12" cy="9" rx="6.5" ry="8" fill="#E9748A" {...solid} />
      <path d="M12 17 l-1.5 2 h3 z" fill="#E9748A" {...solid} />
      <path d="M12 19 q3 3 0 5" {...line} strokeWidth={1.4} />
      <ellipse cx="9.5" cy="6" rx="1.8" ry="2.6" fill="#fff" opacity="0.5" transform="rotate(-20 9.5 6)" />
    </g>
  ),
  ribbon: (
    <g>
      <path d="M12 12 q-7 -7 -2 -9 q4 1 2 9 z M12 12 q7 -7 2 -9 q-4 1 -2 9 z" fill="#E9748A" {...solid} />
      <circle cx="12" cy="12" r="2.4" fill="#F292A6" {...solid} />
      <path d="M10 14 l-2 7 M14 14 l2 7" {...line} />
    </g>
  ),
  heart: (
    <path d="M12 21 C4 15 2 10 5 6.5 C7.5 3.5 11 5 12 7.5 C13 5 16.5 3.5 19 6.5 C22 10 20 15 12 21 z" fill="#E9748A" {...solid} />
  ),
  flower: (
    <g>
      <g fill="#F7CE45" {...solid} strokeWidth={1.4}>
        <ellipse cx="12" cy="5" rx="3" ry="4" /><ellipse cx="12" cy="15" rx="3" ry="4" />
        <ellipse cx="7" cy="10" rx="4" ry="3" /><ellipse cx="17" cy="10" rx="4" ry="3" />
      </g>
      <circle cx="12" cy="10" r="3" fill="#F0863C" {...solid} strokeWidth={1.4} />
      <path d="M12 19 v4" {...line} strokeWidth={2} />
    </g>
  ),
  palm: (
    <g>
      <path d="M11 10 q1 6 0 12 h2 q-1 -6 0 -12 z" fill="#C0703C" {...solid} strokeWidth={1.4} />
      <g fill="#5FB56B" {...solid} strokeWidth={1.4}>
        <path d="M12 9 q-8 -5 -10 1 q6 -2 10 1 z" /><path d="M12 9 q8 -5 10 1 q-6 -2 -10 1 z" />
        <path d="M12 9 q-4 -8 2 -8 q1 5 -2 8 z" />
      </g>
    </g>
  ),
  ball: (
    <g>
      <circle cx="12" cy="12" r="8.5" fill="#D8E84A" {...solid} />
      <path d="M4 8 q6 4 0 8" {...line} strokeWidth={1.5} />
      <path d="M20 8 q-6 4 0 8" {...line} strokeWidth={1.5} />
    </g>
  ),
  rest: (
    <g>
      <path d="M3 16 h18 v4 H3 z" fill="#C7CEDB" {...solid} />
      <path d="M4 16 v-4 a2 2 0 0 1 2 -2 h12 a2 2 0 0 1 2 2 v4" fill="#8FA0C0" {...solid} />
      <path d="M6 10 h5 v-2 a1.6 1.6 0 0 0 -1.6 -1.6 H7.6 A1.6 1.6 0 0 0 6 8 z" fill="#F3F5F9" {...solid} strokeWidth={1.4} />
    </g>
  ),
};

/** Draws a prop at `size`, centred on (cx, cy). */
export function StickerProp({ name, cx, cy, size }: { name: StickerPropName; cx: number; cy: number; size: number }) {
  const scale = size / 24;
  return (
    <g transform={`translate(${cx - size / 2} ${cy - size / 2}) scale(${scale})`}>
      {PROPS[name]}
    </g>
  );
}

export type StickerFloat = "hearts" | "sparkles" | "zzz" | "notes" | "confetti" | "question" | "anger";

/** One floating comic mark, drawn at (x, y) with the given size. */
export function FloatMark({ kind, x, y, size }: { kind: StickerFloat; x: number; y: number; size: number }) {
  const scale = size / 24;
  const marks: Record<StickerFloat, React.ReactNode> = {
    hearts: <path d="M12 20 C5 15 3.5 10.5 6 7.5 C8 5 11.2 6.3 12 8.4 C12.8 6.3 16 5 18 7.5 C20.5 10.5 19 15 12 20 z" fill="#E9748A" {...solid} strokeWidth={1.6} />,
    sparkles: <path d="M12 1 l2.4 7.6 L22 12 l-7.6 3.4 L12 23 l-2.4 -7.6 L2 12 l7.6 -3.4 z" fill="#F7CE45" {...solid} strokeWidth={1.6} />,
    zzz: (
      <g fill="none" stroke="#5B7FE0" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round">
        <path d="M4 6 h7 l-7 8 h7" /><path d="M13 13 h6 l-6 7 h6" />
      </g>
    ),
    notes: (
      <g fill="#5B7FE0" {...solid} strokeWidth={1.4}>
        <ellipse cx="7" cy="18" rx="3.4" ry="2.6" transform="rotate(-20 7 18)" />
        <path d="M9.6 17.4 V4 l9 -2 v13" fill="none" stroke="#5B7FE0" strokeWidth="2.2" strokeLinecap="round" />
        <ellipse cx="16" cy="15" rx="3.4" ry="2.6" transform="rotate(-20 16 15)" />
      </g>
    ),
    confetti: (
      <g {...solid} strokeWidth={1.2}>
        <rect x="2" y="3" width="4" height="4" rx="1" fill="#E9748A" transform="rotate(20 4 5)" />
        <rect x="16" y="5" width="4" height="4" rx="1" fill="#5FB56B" transform="rotate(-25 18 7)" />
        <rect x="9" y="14" width="4" height="4" rx="1" fill="#F7CE45" transform="rotate(15 11 16)" />
        <rect x="18" y="16" width="3.5" height="3.5" rx="1" fill="#5B7FE0" transform="rotate(35 20 18)" />
      </g>
    ),
    question: (
      <g fill={INK}>
        <path d="M8 8 a4 4 0 1 1 5.2 3.8 c-1 .4 -1.2 1 -1.2 2.2 h-2.4 c0 -2.2 .6 -3 2 -3.6 A1.8 1.8 0 1 0 10.4 8 z" />
        <circle cx="10.8" cy="18.5" r="1.8" />
      </g>
    ),
    anger: (
      <g fill="#DE4B5C">
        <path d="M4 4 l5 3 l-1 -5 l4 4 l1 -5 l2 5 l4 -3 l-2 5 l5 1 l-5 2 l2 4 l-5 -2 l-1 4 l-3 -4 l-3 3 l1 -5 l-5 1 l3 -4 z" />
      </g>
    ),
  };
  return (
    <g transform={`translate(${x - size / 2} ${y - size / 2}) scale(${scale})`}>
      {marks[kind]}
    </g>
  );
}
