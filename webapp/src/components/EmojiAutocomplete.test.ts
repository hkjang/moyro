import { describe, expect, it } from "vitest";

import { detectEmojiQuery, rankEmoji } from "./EmojiAutocomplete";

describe("detectEmojiQuery", () => {
  it("opens after two characters following a colon at a word start", () => {
    expect(detectEmojiQuery("hello :ta", 9)).toEqual({ start: 6, query: "ta" });
    expect(detectEmojiQuery(":sm", 3)).toEqual({ start: 0, query: "sm" });
  });

  it("ignores a lone colon and colons inside times and URLs", () => {
    expect(detectEmojiQuery("at :", 4)).toBeNull();
    expect(detectEmojiQuery("meet at 10:30", 13)).toBeNull();
    expect(detectEmojiQuery("http://ex", 9)).toBeNull();
  });

  it("only looks at text before the caret", () => {
    expect(detectEmojiQuery(":ta later", 3)).toEqual({ start: 0, query: "ta" });
    expect(detectEmojiQuery("done :ta", 4)).toBeNull();
  });
});

describe("rankEmoji", () => {
  it("prefers prefix matches and includes custom emoji", () => {
    const custom = [{ id: "e1", name: "partyparrot", creator_id: "u", create_at: 1 }] as never;
    const names = rankEmoji("part", custom).map((item) => item.name);
    expect(names[0]).toBe("party");
    expect(names).toContain("partyparrot");
  });

  it("caps the list", () => {
    expect(rankEmoji("a", []).length).toBeLessThanOrEqual(8);
  });
});
