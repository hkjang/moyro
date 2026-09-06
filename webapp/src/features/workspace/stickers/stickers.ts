// Built-in emoticon packs.
//
// KakaoTalk-style emoticons are large standalone images, not inline glyphs.
// The product ships offline, so the packs are drawn as SVG at render time
// from the specs below rather than bundled as bitmaps: one consistent
// character ("모요") with an expression, an optional prop, and a caption.
//
// A message carrying an emoticon is an ordinary post: its text is the
// caption, so any Mattermost-compatible client shows something sensible, and
// `props.sticker` names the emoticon for clients that can draw it.

export type StickerEyes = "dot" | "happy" | "closed" | "wide" | "tear" | "wink" | "sleepy" | "star";
export type StickerMouth = "smile" | "grin" | "open" | "frown" | "flat" | "wavy" | "o" | "tongue";
export type StickerBrow = "none" | "angry" | "sad" | "raised";

export type StickerSpec = {
  id: string;
  pack: StickerPackId;
  caption: string;
  eyes: StickerEyes;
  mouth: StickerMouth;
  brow?: StickerBrow;
  blush?: boolean;
  sweat?: boolean;
  /** Emoji glyph drawn beside the character, e.g. "☕". */
  prop?: string;
  /** Small floating decorations: hearts, sparkles, zzz, notes. */
  float?: "hearts" | "sparkles" | "zzz" | "notes" | "confetti" | "question" | "anger";
  /** Body colour; the default is the brand blue. */
  color?: string;
};

export type StickerPackId = "feeling" | "reaction" | "work" | "greeting";

export const STICKER_PACKS: { id: StickerPackId; label: string }[] = [
  { id: "feeling", label: "감정" },
  { id: "reaction", label: "반응" },
  { id: "work", label: "업무" },
  { id: "greeting", label: "인사" },
];

/** Prefix on built-in emoticon ids inside `props.sticker`. */
export const BUILTIN_PREFIX = "moyo:";
/** Prefix for an admin-uploaded custom emoji sent as a large emoticon. */
export const CUSTOM_PREFIX = "emoji:";

const YELLOW = "#F5C451";
const PINK = "#F28FA8";
const GREEN = "#5CBF8A";
const PURPLE = "#8D7BE0";
const ORANGE = "#F2994A";

const specs: Omit<StickerSpec, "id">[] = [
  // ── 감정 ────────────────────────────────────────────────────────────
  { pack: "feeling", caption: "좋아!", eyes: "happy", mouth: "grin", blush: true, float: "sparkles", color: YELLOW },
  { pack: "feeling", caption: "ㅋㅋㅋ", eyes: "closed", mouth: "open", blush: true, float: "notes", color: YELLOW },
  { pack: "feeling", caption: "사랑해요", eyes: "happy", mouth: "smile", blush: true, float: "hearts", color: PINK },
  { pack: "feeling", caption: "ㅠㅠ", eyes: "tear", mouth: "frown", brow: "sad" },
  { pack: "feeling", caption: "으악!", eyes: "wide", mouth: "open", brow: "angry", float: "anger", color: ORANGE },
  { pack: "feeling", caption: "헉!", eyes: "wide", mouth: "o", sweat: true },
  { pack: "feeling", caption: "부끄", eyes: "closed", mouth: "wavy", blush: true, color: PINK },
  { pack: "feeling", caption: "피곤해", eyes: "sleepy", mouth: "flat", float: "zzz", color: PURPLE },
  { pack: "feeling", caption: "뿌듯", eyes: "star", mouth: "grin", blush: true, float: "sparkles", color: YELLOW },
  { pack: "feeling", caption: "시무룩", eyes: "dot", mouth: "frown", brow: "sad", color: PURPLE },
  { pack: "feeling", caption: "감동", eyes: "tear", mouth: "smile", blush: true, float: "hearts" },
  { pack: "feeling", caption: "메롱", eyes: "wink", mouth: "tongue", blush: true, color: YELLOW },
  // ── 반응 ────────────────────────────────────────────────────────────
  { pack: "reaction", caption: "굿!", eyes: "happy", mouth: "grin", prop: "👍", color: GREEN },
  { pack: "reaction", caption: "최고!", eyes: "star", mouth: "grin", float: "sparkles", prop: "🏆", color: YELLOW },
  { pack: "reaction", caption: "짝짝짝", eyes: "happy", mouth: "open", prop: "👏", float: "sparkles" },
  { pack: "reaction", caption: "OK!", eyes: "wink", mouth: "smile", prop: "👌", color: GREEN },
  { pack: "reaction", caption: "확인했어요", eyes: "dot", mouth: "smile", prop: "✅", color: GREEN },
  { pack: "reaction", caption: "알겠어요", eyes: "happy", mouth: "smile", prop: "🙆" },
  { pack: "reaction", caption: "고마워요", eyes: "happy", mouth: "smile", blush: true, float: "hearts", prop: "🙏", color: PINK },
  { pack: "reaction", caption: "미안해요", eyes: "closed", mouth: "wavy", sweat: true, brow: "sad" },
  { pack: "reaction", caption: "축하해요", eyes: "happy", mouth: "grin", float: "confetti", prop: "🎉", color: PINK },
  { pack: "reaction", caption: "파이팅!", eyes: "star", mouth: "grin", prop: "✊", color: ORANGE },
  { pack: "reaction", caption: "응원해요", eyes: "happy", mouth: "open", prop: "📣", float: "sparkles", color: ORANGE },
  { pack: "reaction", caption: "왜요?", eyes: "dot", mouth: "o", brow: "raised", float: "question" },
  // ── 업무 ────────────────────────────────────────────────────────────
  { pack: "work", caption: "회의 중", eyes: "dot", mouth: "flat", prop: "📋", color: PURPLE },
  { pack: "work", caption: "잠시만요", eyes: "dot", mouth: "wavy", prop: "⏳", sweat: true },
  { pack: "work", caption: "밥 먹자", eyes: "happy", mouth: "open", prop: "🍚", color: YELLOW },
  { pack: "work", caption: "커피 한잔", eyes: "happy", mouth: "smile", prop: "☕", color: ORANGE },
  { pack: "work", caption: "퇴근!", eyes: "star", mouth: "grin", prop: "🎒", float: "sparkles", color: GREEN },
  { pack: "work", caption: "야근 중", eyes: "sleepy", mouth: "flat", prop: "🌙", float: "zzz", color: PURPLE },
  { pack: "work", caption: "집중 중", eyes: "dot", mouth: "flat", prop: "💻" },
  { pack: "work", caption: "완료!", eyes: "happy", mouth: "grin", prop: "✅", float: "sparkles", color: GREEN },
  { pack: "work", caption: "확인 부탁해요", eyes: "dot", mouth: "smile", prop: "📎" },
  { pack: "work", caption: "수고했어요", eyes: "happy", mouth: "smile", blush: true, float: "sparkles", prop: "🌟", color: YELLOW },
  { pack: "work", caption: "곧 갈게요", eyes: "wide", mouth: "o", prop: "🏃", sweat: true },
  { pack: "work", caption: "메모 남겼어요", eyes: "dot", mouth: "smile", prop: "📝" },
  // ── 인사 ────────────────────────────────────────────────────────────
  { pack: "greeting", caption: "안녕하세요", eyes: "happy", mouth: "smile", prop: "👋", color: YELLOW },
  { pack: "greeting", caption: "좋은 아침", eyes: "happy", mouth: "grin", prop: "☀️", color: ORANGE },
  { pack: "greeting", caption: "잘 자요", eyes: "sleepy", mouth: "smile", prop: "🌙", float: "zzz", color: PURPLE },
  { pack: "greeting", caption: "다녀올게요", eyes: "dot", mouth: "smile", prop: "👋" },
  { pack: "greeting", caption: "주말 잘 보내요", eyes: "happy", mouth: "grin", prop: "🌴", float: "sparkles", color: GREEN },
  { pack: "greeting", caption: "환영해요", eyes: "star", mouth: "grin", float: "confetti", prop: "🎊", color: PINK },
];

/** Every built-in emoticon. Ids are `<pack>-<n>` numbered within the pack
 *  and must stay stable across releases: they are stored in sent posts. */
export const STICKERS: StickerSpec[] = (() => {
  const counters: Record<string, number> = {};
  return specs.map((spec) => {
    counters[spec.pack] = (counters[spec.pack] ?? 0) + 1;
    return { ...spec, id: `${spec.pack}-${counters[spec.pack]}` };
  });
})();

const byId = new Map(STICKERS.map((spec) => [spec.id, spec]));

export function stickerById(id: string): StickerSpec | undefined {
  return byId.get(id.startsWith(BUILTIN_PREFIX) ? id.slice(BUILTIN_PREFIX.length) : id);
}

export function stickersInPack(pack: StickerPackId): StickerSpec[] {
  return STICKERS.filter((spec) => spec.pack === pack);
}

/** Reads `props.sticker` off a post; undefined when the post is not an emoticon. */
export function stickerFromProps(props: Record<string, unknown> | undefined | null): string | undefined {
  const value = props?.sticker;
  return typeof value === "string" && (value.startsWith(BUILTIN_PREFIX) || value.startsWith(CUSTOM_PREFIX)) ? value : undefined;
}
