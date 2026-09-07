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

import type { StickerPropName } from "./props";

export type StickerEyes = "dot" | "happy" | "closed" | "wide" | "tear" | "wink" | "sleepy" | "star";
export type StickerMouth = "smile" | "grin" | "open" | "frown" | "flat" | "wavy" | "o" | "tongue";
export type StickerBrow = "none" | "angry" | "sad" | "raised";

/** Who is drawn. Each character has its own body, features, and voice. */
export type StickerCharacter = "moyo" | "cat" | "dog" | "bear" | "rabbit" | "duck";

export type StickerSpec = {
  id: string;
  pack: StickerPackId;
  /** Defaults to the brand blob "모요". */
  character?: StickerCharacter;
  caption: string;
  /**
   * Words a reader might type that mean this emoticon. Matched against the
   * end of the composer text as the reader types, so "고마워" offers 고마워요
   * before it is sent. The caption is always a keyword too.
   */
  keywords?: string[];
  eyes: StickerEyes;
  mouth: StickerMouth;
  brow?: StickerBrow;
  blush?: boolean;
  sweat?: boolean;
  /** Vector object the character holds or stands beside. */
  prop?: StickerPropName;
  /** Small floating decorations: hearts, sparkles, zzz, notes. */
  float?: "hearts" | "sparkles" | "zzz" | "notes" | "confetti" | "question" | "anger";
  /** Body colour; the default is the character's own. */
  color?: string;
  /**
   * What the body is doing. Drawn as arms, legs, and posture in the cartoon
   * renderer; the default is a relaxed stance with arms down.
   */
  pose?: StickerPose;
};

export type StickerPose =
  | "stand"    // arms down
  | "wave"     // one arm up, hand open, motion lines
  | "thumbs"   // one arm up with a thumbs-up
  | "cheer"    // both arms up
  | "hold"     // both hands in front holding the prop
  | "run"      // leaning forward, legs apart, motion lines
  | "slump"    // shoulders down, head tilted
  | "hide"     // hands over the face
  | "cross"    // arms crossed
  | "cry"      // hands rubbing the eyes
  | "point"    // one arm forward
  | "sit";     // sitting, legs forward

export type StickerPackId = "feeling" | "reaction" | "work" | "greeting" | "cat" | "dog" | "bear" | "rabbit" | "duck";

export const STICKER_PACKS: { id: StickerPackId; label: string }[] = [
  { id: "feeling", label: "감정" },
  { id: "reaction", label: "반응" },
  { id: "work", label: "업무" },
  { id: "greeting", label: "인사" },
  { id: "cat", label: "냥이" },
  { id: "dog", label: "멍이" },
  { id: "bear", label: "곰돌이" },
  { id: "rabbit", label: "토끼" },
  { id: "duck", label: "오리" },
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
  { pack: "feeling", caption: "좋아!", pose: "thumbs", keywords: ["좋아", "좋다", "좋네", "굿", "최고", "만족"], eyes: "happy", mouth: "grin", blush: true, float: "sparkles", color: YELLOW },
  { pack: "feeling", caption: "ㅋㅋㅋ", pose: "stand", keywords: ["ㅋㅋ", "ㅎㅎ", "웃겨", "웃김", "빵터", "lol", "haha"], eyes: "closed", mouth: "open", blush: true, float: "notes", color: YELLOW },
  { pack: "feeling", caption: "사랑해요", pose: "stand", keywords: ["사랑", "럽", "love", "하트", "좋아해"], eyes: "happy", mouth: "smile", blush: true, float: "hearts", color: PINK },
  { pack: "feeling", caption: "ㅠㅠ", pose: "cry", keywords: ["ㅠㅠ", "ㅜㅜ", "슬퍼", "슬프", "울고", "눈물", "힘들", "속상"], eyes: "tear", mouth: "frown", brow: "sad" },
  { pack: "feeling", caption: "으악!", pose: "point", keywords: ["으악", "화나", "화났", "짜증", "열받", "빡", "분노"], eyes: "wide", mouth: "open", brow: "angry", float: "anger", color: ORANGE },
  { pack: "feeling", caption: "헉!", pose: "point", keywords: ["헉", "헐", "놀랐", "놀라", "깜짝", "대박", "진짜?"], eyes: "wide", mouth: "o", sweat: true },
  { pack: "feeling", caption: "부끄", pose: "hide", keywords: ["부끄", "쑥스", "민망", "창피"], eyes: "closed", mouth: "wavy", blush: true, color: PINK },
  { pack: "feeling", caption: "피곤해", pose: "slump", keywords: ["피곤", "지쳤", "지친", "힘들다", "기진"], eyes: "sleepy", mouth: "flat", float: "zzz", color: PURPLE },
  { pack: "feeling", caption: "뿌듯", pose: "cheer", keywords: ["뿌듯", "해냈", "성공", "이뤘", "자랑"], eyes: "star", mouth: "grin", blush: true, float: "sparkles", color: YELLOW },
  { pack: "feeling", caption: "시무룩", pose: "slump", keywords: ["시무룩", "우울", "축 처", "기운없", "허탈"], eyes: "dot", mouth: "frown", brow: "sad", color: PURPLE },
  { pack: "feeling", caption: "감동", pose: "cry", keywords: ["감동", "눈물난", "찡", "울컥"], eyes: "tear", mouth: "smile", blush: true, float: "hearts" },
  { pack: "feeling", caption: "메롱", pose: "stand", keywords: ["메롱", "장난", "농담", "ㅋ장난"], eyes: "wink", mouth: "tongue", blush: true, color: YELLOW },
  // ── 반응 ────────────────────────────────────────────────────────────
  { pack: "reaction", caption: "굿!", pose: "thumbs", keywords: ["굿", "good", "좋아요", "따봉", "엄지"], eyes: "happy", mouth: "grin",  color: GREEN },
  { pack: "reaction", caption: "최고!", pose: "thumbs", keywords: ["최고", "짱", "베스트", "best", "대단"], eyes: "star", mouth: "grin", float: "sparkles", prop: "trophy", color: YELLOW },
  { pack: "reaction", caption: "짝짝짝", pose: "cheer", keywords: ["짝짝", "박수", "브라보", "클랩", "축하드"], eyes: "happy", mouth: "open",  float: "sparkles" },
  { pack: "reaction", caption: "OK!", pose: "thumbs", keywords: ["ok", "오케이", "오키", "옼", "넵", "넹", "네네"], eyes: "wink", mouth: "smile",  color: GREEN },
  { pack: "reaction", caption: "확인했어요", pose: "thumbs", keywords: ["확인했", "확인 완료", "확인함", "봤어", "체크", "봤습니다"], eyes: "dot", mouth: "smile", prop: "check", color: GREEN },
  { pack: "reaction", caption: "알겠어요", pose: "thumbs", keywords: ["알겠", "알았", "이해했", "네 알", "알겠습니다"], eyes: "happy", mouth: "smile" },
  { pack: "reaction", caption: "고마워요", pose: "stand", keywords: ["고마워", "고맙", "감사", "땡큐", "thanks", "thx", "감사합니다"], eyes: "happy", mouth: "smile", blush: true, float: "hearts",  color: PINK },
  { pack: "reaction", caption: "미안해요", pose: "cry", keywords: ["미안", "죄송", "송구", "sorry", "쏘리"], eyes: "closed", mouth: "wavy", sweat: true, brow: "sad" },
  { pack: "reaction", caption: "축하해요", pose: "cheer", keywords: ["축하", "생일", "경축", "congrat", "기념"], eyes: "happy", mouth: "grin", float: "confetti", prop: "gift", color: PINK },
  { pack: "reaction", caption: "파이팅!", pose: "cheer", keywords: ["파이팅", "화이팅", "힘내", "아자", "fighting", "가즈아"], eyes: "star", mouth: "grin",  color: ORANGE },
  { pack: "reaction", caption: "응원해요", pose: "cheer", keywords: ["응원", "잘할", "잘 될", "할 수 있", "믿어"], eyes: "happy", mouth: "open", prop: "megaphone", float: "sparkles", color: ORANGE },
  { pack: "reaction", caption: "왜요?", pose: "cross", keywords: ["왜요", "왜?", "뭐지", "무슨", "어떻게?", "궁금"], eyes: "dot", mouth: "o", brow: "raised", float: "question" },
  // ── 업무 ────────────────────────────────────────────────────────────
  { pack: "work", caption: "회의 중", pose: "hold", keywords: ["회의", "미팅", "미팅중", "meeting", "콜중"], eyes: "dot", mouth: "flat", prop: "clipboard", color: PURPLE },
  { pack: "work", caption: "잠시만요", pose: "hold", keywords: ["잠시만", "잠깐", "잠깐만", "기다려", "곧", "조금만"], eyes: "dot", mouth: "wavy", prop: "hourglass", sweat: true },
  { pack: "work", caption: "밥 먹자", pose: "hold", keywords: ["밥", "점심", "저녁", "식사", "먹자", "배고", "맛점"], eyes: "happy", mouth: "open", prop: "bowl", color: YELLOW },
  { pack: "work", caption: "커피 한잔", pose: "hold", keywords: ["커피", "카페", "티타임", "coffee", "라떼", "아아"], eyes: "happy", mouth: "smile", prop: "cup", color: ORANGE },
  { pack: "work", caption: "퇴근!", pose: "cheer", keywords: ["퇴근", "칼퇴", "집에", "퇴근합니다", "먼저 가"], eyes: "star", mouth: "grin", prop: "bag", float: "sparkles", color: GREEN },
  { pack: "work", caption: "야근 중", pose: "hold", keywords: ["야근", "늦게", "밤샘", "새벽", "야근중"], eyes: "sleepy", mouth: "flat", prop: "moon", float: "zzz", color: PURPLE },
  { pack: "work", caption: "집중 중", pose: "hold", keywords: ["집중", "작업중", "작업 중", "코딩", "몰입", "바쁨", "바빠"], eyes: "dot", mouth: "flat", prop: "laptop" },
  { pack: "work", caption: "완료!", pose: "thumbs", keywords: ["완료", "끝났", "끝", "done", "마무리", "처리했", "했어요"], eyes: "happy", mouth: "grin", prop: "check", float: "sparkles", color: GREEN },
  { pack: "work", caption: "확인 부탁해요", pose: "hold", keywords: ["확인 부탁", "확인해", "봐주", "검토 부탁", "리뷰 부탁", "확인 요청"], eyes: "dot", mouth: "smile", prop: "clip" },
  { pack: "work", caption: "수고했어요", pose: "stand", keywords: ["수고", "고생", "잘했", "훌륭", "굿잡", "good job"], eyes: "happy", mouth: "smile", blush: true, float: "sparkles", prop: "star", color: YELLOW },
  { pack: "work", caption: "곧 갈게요", pose: "run", keywords: ["곧 갈", "가는 중", "출발", "이동중", "가고 있", "도착"], eyes: "wide", mouth: "o",  sweat: true },
  { pack: "work", caption: "메모 남겼어요", pose: "hold", keywords: ["메모", "남겼", "적어", "기록", "노트"], eyes: "dot", mouth: "smile", prop: "memo" },
  // ── 인사 ────────────────────────────────────────────────────────────
  { pack: "greeting", caption: "안녕하세요", pose: "wave", keywords: ["안녕", "하이", "hello", "hi", "반가", "처음 뵙"], eyes: "happy", mouth: "smile",  color: YELLOW },
  { pack: "greeting", caption: "좋은 아침", pose: "stand", keywords: ["좋은 아침", "굿모닝", "아침", "출근", "morning"], eyes: "happy", mouth: "grin", prop: "sun", color: ORANGE },
  { pack: "greeting", caption: "잘 자요", pose: "slump", keywords: ["잘 자", "굿나잇", "잘자", "자러", "취침", "night"], eyes: "sleepy", mouth: "smile", prop: "moon", float: "zzz", color: PURPLE },
  { pack: "greeting", caption: "다녀올게요", pose: "wave", keywords: ["다녀올", "다녀오", "외출", "잠깐 나갔", "자리 비"], eyes: "dot", mouth: "smile" },
  { pack: "greeting", caption: "주말 잘 보내요", pose: "cheer", keywords: ["주말", "불금", "휴일", "연휴", "쉬세요"], eyes: "happy", mouth: "grin", prop: "palm", float: "sparkles", color: GREEN },
  { pack: "greeting", caption: "환영해요", pose: "wave", keywords: ["환영", "웰컴", "welcome", "합류", "입사"], eyes: "star", mouth: "grin", float: "confetti", prop: "balloon", color: PINK },
  // ── 냥이 — 도도하고 시크한 고양이, 말끝에 "냥" ─────────────────────────
  { pack: "cat", character: "cat", caption: "좋다냥", pose: "thumbs", eyes: "happy", mouth: "smile", blush: true, float: "hearts", keywords: ["좋아", "좋다", "만족", "냥"] },
  { pack: "cat", character: "cat", caption: "싫다냥", pose: "cross", eyes: "closed", mouth: "flat", brow: "raised", keywords: ["싫어", "싫다", "별로", "안 할래", "거절"] },
  { pack: "cat", character: "cat", caption: "졸리다냥", pose: "slump", eyes: "sleepy", mouth: "wavy", float: "zzz", keywords: ["졸려", "졸리", "잠와", "하품", "피곤"] },
  { pack: "cat", character: "cat", caption: "밥 달라냥", pose: "hold", eyes: "wide", mouth: "open", prop: "fish", keywords: ["배고", "밥", "점심", "저녁", "간식", "먹을"] },
  { pack: "cat", character: "cat", caption: "관심 없다냥", pose: "slump", eyes: "dot", mouth: "flat", keywords: ["관심", "글쎄", "몰라", "노관심", "그러든가"] },
  { pack: "cat", character: "cat", caption: "화났다냥", pose: "point", eyes: "wide", mouth: "frown", brow: "angry", float: "anger", keywords: ["화나", "화났", "열받", "빡", "짜증"] },
  { pack: "cat", character: "cat", caption: "간식 내놔", pose: "hold", eyes: "star", mouth: "grin", prop: "cookie", keywords: ["간식", "츄르", "디저트", "과자", "내놔"] },
  { pack: "cat", character: "cat", caption: "잘했다냥", pose: "thumbs", eyes: "happy", mouth: "grin", float: "sparkles", prop: "crown", keywords: ["잘했", "칭찬", "수고", "굿잡", "훌륭"] },
  { pack: "cat", character: "cat", caption: "알겠다냥", pose: "thumbs", eyes: "wink", mouth: "smile", keywords: ["알겠", "알았", "오케이", "ok", "넵"] },
  { pack: "cat", character: "cat", caption: "고맙다냥", pose: "stand", eyes: "happy", mouth: "smile", blush: true, prop: "ribbon", keywords: ["고마워", "고맙", "감사", "땡큐"] },
  { pack: "cat", character: "cat", caption: "미안하다냥", pose: "cry", eyes: "tear", mouth: "wavy", sweat: true, keywords: ["미안", "죄송", "sorry", "실수"] },
  { pack: "cat", character: "cat", caption: "퇴근한다냥", pose: "cheer", eyes: "star", mouth: "grin", prop: "moon", float: "sparkles", keywords: ["퇴근", "칼퇴", "집에", "먼저 가"] },
  // ── 멍이 — 신나고 다정한 강아지, 말끝에 "멍" ─────────────────────────
  { pack: "dog", character: "dog", caption: "산책 가자!", pose: "cheer", eyes: "star", mouth: "tongue", prop: "bone", float: "sparkles", keywords: ["산책", "나가", "나갈래", "외출", "바람 쐬"] },
  { pack: "dog", character: "dog", caption: "반가워멍!", pose: "wave", eyes: "happy", mouth: "tongue", blush: true,  keywords: ["반가", "안녕", "하이", "hello", "오랜만"] },
  { pack: "dog", character: "dog", caption: "기다릴게멍", pose: "hold", eyes: "dot", mouth: "smile", prop: "hourglass", keywords: ["기다릴", "기다려", "천천히 와", "언제 와", "대기"] },
  { pack: "dog", character: "dog", caption: "잘했어멍!", pose: "thumbs", eyes: "happy", mouth: "grin", float: "confetti", prop: "medal", keywords: ["잘했", "대단", "최고", "짱", "굿"] },
  { pack: "dog", character: "dog", caption: "사랑해멍", pose: "stand", eyes: "happy", mouth: "tongue", blush: true, float: "hearts", keywords: ["사랑", "love", "좋아해", "하트", "럽"] },
  { pack: "dog", character: "dog", caption: "배고파멍", pose: "stand", eyes: "tear", mouth: "open", prop: "bone", keywords: ["배고", "밥", "먹자", "점심", "야식"] },
  { pack: "dog", character: "dog", caption: "심심해멍", pose: "stand", eyes: "dot", mouth: "flat", float: "question", keywords: ["심심", "할 거 없", "지루", "뭐 하지", "놀아줘"] },
  { pack: "dog", character: "dog", caption: "놀자!", pose: "cheer", eyes: "star", mouth: "open", prop: "ball", float: "sparkles", keywords: ["놀자", "놀래", "게임", "한판", "재밌"] },
  { pack: "dog", character: "dog", caption: "슬퍼멍", pose: "cry", eyes: "tear", mouth: "frown", brow: "sad", keywords: ["슬퍼", "슬프", "ㅠㅠ", "속상", "울고"] },
  { pack: "dog", character: "dog", caption: "확인했어멍", pose: "thumbs", eyes: "dot", mouth: "smile", prop: "check", keywords: ["확인했", "확인 완료", "봤어", "체크", "봤습니다"] },
  { pack: "dog", character: "dog", caption: "파이팅멍!", pose: "cheer", eyes: "star", mouth: "grin",  keywords: ["파이팅", "화이팅", "힘내", "아자", "가즈아"] },
  { pack: "dog", character: "dog", caption: "잘 자멍", pose: "slump", eyes: "sleepy", mouth: "smile", float: "zzz", prop: "moon", keywords: ["잘 자", "잘자", "굿나잇", "취침", "자러"] },
  // ── 곰돌이 — 느긋하고 따뜻한 곰 ──────────────────────────────────────
  { pack: "bear", character: "bear", caption: "괜찮아요", pose: "stand", eyes: "happy", mouth: "smile", blush: true, keywords: ["괜찮", "걱정 마", "문제없", "노프라블럼", "됐어"] },
  { pack: "bear", character: "bear", caption: "천천히 해요", pose: "hold", eyes: "dot", mouth: "smile", prop: "cup", keywords: ["천천히", "급할 것", "여유", "느긋", "서두르지"] },
  { pack: "bear", character: "bear", caption: "안아줄게요", pose: "stand", eyes: "closed", mouth: "smile", blush: true, float: "hearts", keywords: ["안아", "토닥", "위로", "힘들지", "괜찮아질"] },
  { pack: "bear", character: "bear", caption: "든든하죠", pose: "thumbs", eyes: "happy", mouth: "grin",  keywords: ["든든", "믿어", "맡겨", "책임", "걱정 없"] },
  { pack: "bear", character: "bear", caption: "꿀잠 자요", pose: "hold", eyes: "sleepy", mouth: "smile", float: "zzz", prop: "honey", keywords: ["꿀잠", "잘 자", "굿나잇", "푹 쉬", "자요"] },
  { pack: "bear", character: "bear", caption: "배불러요", pose: "hold", eyes: "closed", mouth: "grin", blush: true, prop: "honey", keywords: ["배불", "잘 먹었", "맛있었", "든든하게", "식사 완료"] },
  { pack: "bear", character: "bear", caption: "힘내요", pose: "stand", eyes: "happy", mouth: "smile", prop: "flower", float: "sparkles", keywords: ["힘내", "응원", "파이팅", "화이팅", "기운"] },
  { pack: "bear", character: "bear", caption: "고마워요", pose: "stand", eyes: "happy", mouth: "smile", blush: true, prop: "gift", keywords: ["고마워", "고맙", "감사", "땡큐", "thanks"] },
  { pack: "bear", character: "bear", caption: "죄송해요", pose: "cry", eyes: "closed", mouth: "wavy", sweat: true, brow: "sad", keywords: ["죄송", "미안", "sorry", "실례", "양해"] },
  { pack: "bear", character: "bear", caption: "잠시 쉴게요", pose: "slump", eyes: "sleepy", mouth: "flat", prop: "rest", keywords: ["쉴게", "휴식", "잠깐 쉬", "브레이크", "자리 비"] },
  { pack: "bear", character: "bear", caption: "커피 타임", pose: "hold", eyes: "happy", mouth: "smile", prop: "cup", keywords: ["커피", "카페", "티타임", "coffee", "아아"] },
  { pack: "bear", character: "bear", caption: "수고 많았어요", pose: "stand", eyes: "happy", mouth: "grin", float: "sparkles", prop: "star", keywords: ["수고", "고생", "잘했", "고마웠", "훌륭"] },
  // ── 토끼 — 발랄하고 급한 토끼 ─────────────────────────────────────────
  { pack: "rabbit", character: "rabbit", caption: "빨리빨리!", pose: "run", eyes: "wide", mouth: "open", prop: "clock", sweat: true, keywords: ["빨리", "서둘러", "급해", "얼른", "어서"] },
  { pack: "rabbit", character: "rabbit", caption: "깜짝이야!", pose: "point", eyes: "wide", mouth: "o", float: "anger", keywords: ["깜짝", "놀랐", "헉", "헐", "엥"] },
  { pack: "rabbit", character: "rabbit", caption: "두근두근", pose: "stand", eyes: "happy", mouth: "o", blush: true, float: "hearts", keywords: ["두근", "설레", "떨려", "긴장", "기대"] },
  { pack: "rabbit", character: "rabbit", caption: "당근이지!", pose: "thumbs", eyes: "star", mouth: "grin", prop: "carrot", keywords: ["당근", "당연", "물론", "그럼", "당연하지"] },
  { pack: "rabbit", character: "rabbit", caption: "헐 대박", pose: "cheer", eyes: "wide", mouth: "open", float: "sparkles", keywords: ["대박", "헐", "미쳤", "장난 아니", "실화"] },
  { pack: "rabbit", character: "rabbit", caption: "울지 마", pose: "cry", eyes: "dot", mouth: "smile", prop: "heart", keywords: ["울지", "괜찮아", "토닥", "위로", "힘들지"] },
  { pack: "rabbit", character: "rabbit", caption: "칭찬해줘", pose: "stand", eyes: "star", mouth: "grin", blush: true, float: "sparkles", keywords: ["칭찬", "잘했지", "어때", "봐봐", "나 좀"] },
  { pack: "rabbit", character: "rabbit", caption: "부끄부끄", pose: "hide", eyes: "closed", mouth: "wavy", blush: true, keywords: ["부끄", "쑥스", "민망", "창피", "얼굴 빨개"] },
  { pack: "rabbit", character: "rabbit", caption: "OK 접수!", pose: "thumbs", eyes: "wink", mouth: "grin", prop: "tray", keywords: ["접수", "ok", "오케이", "받았", "확인했"] },
  { pack: "rabbit", character: "rabbit", caption: "야근이라니", pose: "hold", eyes: "tear", mouth: "frown", brow: "sad", prop: "moon", keywords: ["야근", "늦게", "밤샘", "집에 못", "새벽"] },
  { pack: "rabbit", character: "rabbit", caption: "점심 뭐 먹지", pose: "hold", eyes: "dot", mouth: "o", float: "question", prop: "bowl", keywords: ["점심", "뭐 먹", "메뉴", "밥", "식사"] },
  { pack: "rabbit", character: "rabbit", caption: "주말이다!", pose: "cheer", eyes: "star", mouth: "grin", float: "confetti", prop: "balloon", keywords: ["주말", "불금", "휴일", "연휴", "쉬는 날"] },
  // ── 오리 — 엉뚱하고 웃긴 오리, "꽥" ─────────────────────────────────────
  { pack: "duck", character: "duck", caption: "꽥!", pose: "point", eyes: "wide", mouth: "open", float: "anger", keywords: ["꽥", "악", "으악", "아니", "뭐야"] },
  { pack: "duck", character: "duck", caption: "몰라요 꽥", pose: "cross", eyes: "dot", mouth: "flat", float: "question", keywords: ["몰라", "모르겠", "글쎄", "노답", "모름"] },
  { pack: "duck", character: "duck", caption: "됐고 밥", pose: "hold", eyes: "closed", mouth: "flat", prop: "bowl", keywords: ["됐고", "밥", "일단 밥", "먹고", "배고"] },
  { pack: "duck", character: "duck", caption: "어이없꽥", pose: "cross", eyes: "dot", mouth: "wavy", brow: "raised", keywords: ["어이없", "황당", "어처구니", "말도 안", "실화냐"] },
  { pack: "duck", character: "duck", caption: "잘가꽥", pose: "wave", eyes: "happy", mouth: "smile",  keywords: ["잘가", "안녕", "바이", "bye", "다음에"] },
  { pack: "duck", character: "duck", caption: "컨디션 최상", pose: "cheer", eyes: "star", mouth: "grin", float: "sparkles",  keywords: ["컨디션", "최상", "쌩쌩", "기분 좋", "에너지"] },
  { pack: "duck", character: "duck", caption: "회의 그만", pose: "hold", eyes: "sleepy", mouth: "frown", prop: "clipboard", keywords: ["회의", "미팅", "그만", "회의 또", "언제 끝"] },
  { pack: "duck", character: "duck", caption: "대충 살자", pose: "slump", eyes: "closed", mouth: "smile", prop: "rest", keywords: ["대충", "귀찮", "느긋", "나중에", "적당히"] },
  { pack: "duck", character: "duck", caption: "아무튼 완료", pose: "thumbs", eyes: "wink", mouth: "grin", prop: "check", keywords: ["완료", "끝", "done", "했음", "마무리"] },
  { pack: "duck", character: "duck", caption: "내 잘못 아님", pose: "cross", eyes: "dot", mouth: "o", sweat: true, keywords: ["잘못", "내 탓", "아님", "책임", "억울"] },
  { pack: "duck", character: "duck", caption: "굿나잇 꽥", pose: "thumbs", eyes: "sleepy", mouth: "smile", float: "zzz", prop: "moon", keywords: ["굿나잇", "잘 자", "잘자", "자러", "취침"] },
  { pack: "duck", character: "duck", caption: "환영 꽥", pose: "wave", eyes: "happy", mouth: "open", float: "confetti", prop: "balloon", keywords: ["환영", "웰컴", "welcome", "합류", "입사"] },
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

/** Longest stretch of trailing text considered when suggesting. */
const SUGGEST_WINDOW = 24;
const SUGGEST_MAX = 6;

function normalize(text: string): string {
  return text.toLowerCase().replace(/[\s.,!?~…'"()]+/g, "");
}

/**
 * Emoticons whose keywords appear in what the reader just typed. Only the
 * tail of the text is considered, so a long message does not keep offering
 * an emoticon for a word from three sentences ago. Longer keyword hits rank
 * first because they are the more specific match ("확인 부탁" over "확인").
 */
export function suggestStickers(text: string): StickerSpec[] {
  const tail = normalize(text.slice(-SUGGEST_WINDOW));
  if (tail.length < 2) return [];
  const scored: { spec: StickerSpec; score: number }[] = [];
  for (const spec of STICKERS) {
    const words = [spec.caption, ...(spec.keywords ?? [])].map(normalize).filter((word) => word.length >= 2);
    let best = 0;
    for (const word of words) {
      const at = tail.lastIndexOf(word);
      if (at < 0) continue;
      // A hit nearer the caret is what the reader is typing now.
      const recency = (at + word.length) / tail.length;
      best = Math.max(best, word.length * 10 + recency * 5);
    }
    if (best > 0) scored.push({ spec, score: best });
  }
  return scored.sort((a, b) => b.score - a.score).slice(0, SUGGEST_MAX).map((entry) => entry.spec);
}

/**
 * True when the typed text is essentially just the matched keyword — the
 * emoticon then carries the whole message and the composer can be cleared
 * after sending it.
 */
export function textIsOnlyKeyword(text: string, spec: StickerSpec): boolean {
  const whole = normalize(text);
  if (whole.length === 0 || whole.length > 12) return false;
  return [spec.caption, ...(spec.keywords ?? [])].map(normalize).some((word) => word.length >= 2 && whole === word);
}
