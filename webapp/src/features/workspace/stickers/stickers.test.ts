import { describe, expect, it } from "vitest";

import { BUILTIN_PREFIX, STICKERS, STICKER_PACKS, stickerById, stickerFromProps, stickersInPack, suggestStickers, textIsOnlyKeyword } from "./stickers";

describe("built-in emoticon packs", () => {
  it("ship a broad set with stable, unique ids and captions", () => {
    expect(STICKERS.length).toBeGreaterThanOrEqual(100);
    const ids = new Set(STICKERS.map((spec) => spec.id));
    expect(ids.size).toBe(STICKERS.length);
    for (const spec of STICKERS) {
      expect(spec.caption.trim().length).toBeGreaterThan(0);
      expect(STICKER_PACKS.some((pack) => pack.id === spec.pack)).toBe(true);
    }
  });

  it("gives every character its own voice through distinct captions", () => {
    for (const character of ["cat", "dog", "bear", "rabbit", "duck"] as const) {
      const captions = STICKERS.filter((spec) => spec.character === character).map((spec) => spec.caption);
      expect(captions.length).toBeGreaterThanOrEqual(12);
      expect(new Set(captions).size).toBe(captions.length);
    }
  });

  it("fill every pack so no tab in the picker is empty", () => {
    for (const pack of STICKER_PACKS) {
      expect(stickersInPack(pack.id).length).toBeGreaterThanOrEqual(6);
    }
  });

  it("resolves ids with and without the wire prefix", () => {
    const first = STICKERS[0];
    expect(stickerById(first.id)).toBe(first);
    expect(stickerById(`${BUILTIN_PREFIX}${first.id}`)).toBe(first);
    expect(stickerById("moyo:nope")).toBeUndefined();
  });

  it("only treats prefixed string props as emoticons", () => {
    expect(stickerFromProps({ sticker: "moyo:feeling-1" })).toBe("moyo:feeling-1");
    expect(stickerFromProps({ sticker: "emoji:partyparrot" })).toBe("emoji:partyparrot");
    expect(stickerFromProps({ sticker: "feeling-1" })).toBeUndefined();
    expect(stickerFromProps({ sticker: 3 })).toBeUndefined();
    expect(stickerFromProps(undefined)).toBeUndefined();
  });
});

describe("suggestStickers", () => {
  it("offers emoticons for what the reader is typing, most specific first", () => {
    const captions = suggestStickers("오늘 정말 고마워").map((spec) => spec.caption);
    expect(captions[0]).toBe("고마워요");
    expect(suggestStickers("퇴근합니다!").map((spec) => spec.caption)).toContain("퇴근!");
    expect(suggestStickers("확인 부탁드려요").map((spec) => spec.caption)[0]).toBe("확인 부탁해요");
  });

  it("matches Korean laughter, English words, and captions themselves", () => {
    expect(suggestStickers("ㅋㅋㅋㅋ").map((spec) => spec.caption)).toContain("ㅋㅋㅋ");
    expect(suggestStickers("thanks a lot").map((spec) => spec.caption)).toContain("고마워요");
    expect(suggestStickers("커피 한잔").map((spec) => spec.caption)[0]).toBe("커피 한잔");
  });

  it("looks only at the tail of a long message", () => {
    const text = "아까 커피 마셨고 이제 회의 들어갑니다 그리고 나서 보고서 쓰고 정리하겠습니다";
    const captions = suggestStickers(text).map((spec) => spec.caption);
    expect(captions).not.toContain("커피 한잔");
  });

  it("stays quiet for very short or unrelated text", () => {
    expect(suggestStickers("ㅋ")).toEqual([]);
    expect(suggestStickers("배포 스크립트 경로 확인해서")).not.toEqual([]);
    expect(suggestStickers("xyzzy plugh")).toEqual([]);
  });

  it("never returns more than the strip can show", () => {
    expect(suggestStickers("좋아 굿 최고 짱").length).toBeLessThanOrEqual(6);
  });
});

describe("textIsOnlyKeyword", () => {
  it("is true when the text is just the word that summoned the emoticon", () => {
    const thanks = STICKERS.find((spec) => spec.caption === "고마워요")!;
    expect(textIsOnlyKeyword("고마워", thanks)).toBe(true);
    expect(textIsOnlyKeyword("고마워!!", thanks)).toBe(true);
    expect(textIsOnlyKeyword("오늘 회의 자료 고마워", thanks)).toBe(false);
  });
});
