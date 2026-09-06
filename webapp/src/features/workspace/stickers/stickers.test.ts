import { describe, expect, it } from "vitest";

import { BUILTIN_PREFIX, STICKERS, STICKER_PACKS, stickerById, stickerFromProps, stickersInPack } from "./stickers";

describe("built-in emoticon packs", () => {
  it("ship a broad set with stable, unique ids and captions", () => {
    expect(STICKERS.length).toBeGreaterThanOrEqual(40);
    const ids = new Set(STICKERS.map((spec) => spec.id));
    expect(ids.size).toBe(STICKERS.length);
    for (const spec of STICKERS) {
      expect(spec.caption.trim().length).toBeGreaterThan(0);
      expect(STICKER_PACKS.some((pack) => pack.id === spec.pack)).toBe(true);
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
