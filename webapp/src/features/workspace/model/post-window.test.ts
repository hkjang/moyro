import { describe, expect, it } from "vitest";

import type { Post } from "@/api/client";
import { MAX_RETAINED_POSTS, appendLivePost, boundPostWindow } from "./post-window";

function post(id: string, createAt: number): Post {
  return {
    id,
    channel_id: "channel-1",
    user_id: "user-1",
    root_id: "",
    message: id,
    props: {},
    file_ids: [],
    is_pinned: false,
    create_at: createAt,
    update_at: createAt,
    delete_at: 0,
    link_metadata: [],
  } as unknown as Post;
}

function series(count: number): Post[] {
  return Array.from({ length: count }, (_, i) => post(`p${i}`, i));
}

describe("appendLivePost", () => {
  it("appends a new post at the end", () => {
    const next = appendLivePost([post("a", 1)], post("b", 2));
    expect(next.map((p) => p.id)).toEqual(["a", "b"]);
  });

  it("returns the same array for a duplicate so React can skip the re-render", () => {
    const current = [post("a", 1)];
    expect(appendLivePost(current, post("a", 1))).toBe(current);
  });

  it("drops the oldest post once the window is full, keeping the newest", () => {
    const full = series(MAX_RETAINED_POSTS);
    const next = appendLivePost(full, post("newest", MAX_RETAINED_POSTS));

    expect(next).toHaveLength(MAX_RETAINED_POSTS);
    expect(next[next.length - 1].id).toBe("newest");
    // The oldest entry is the one that left.
    expect(next[0].id).toBe("p1");
    expect(next.some((p) => p.id === "p0")).toBe(false);
  });

  it("stays bounded across a long live session", () => {
    let posts = series(MAX_RETAINED_POSTS);
    for (let i = 0; i < 1_000; i++) {
      posts = appendLivePost(posts, post(`live-${i}`, MAX_RETAINED_POSTS + i));
    }
    expect(posts).toHaveLength(MAX_RETAINED_POSTS);
    expect(posts[posts.length - 1].id).toBe("live-999");
  });
});

describe("boundPostWindow", () => {
  it("leaves a short list untouched", () => {
    const short = series(10);
    expect(boundPostWindow(short)).toBe(short);
  });

  it("keeps the newest entries when a rebuilt list overflows", () => {
    const long = series(MAX_RETAINED_POSTS + 25);
    const bounded = boundPostWindow(long);
    expect(bounded).toHaveLength(MAX_RETAINED_POSTS);
    expect(bounded[bounded.length - 1].id).toBe(`p${MAX_RETAINED_POSTS + 24}`);
    expect(bounded[0].id).toBe("p25");
  });

  it("is above one loaded page so ordinary reading never trims", () => {
    // The channel loads 60 posts on open; trimming at that size would fight
    // the initial render instead of bounding a long-lived session.
    expect(MAX_RETAINED_POSTS).toBeGreaterThan(60);
  });
});

describe("appendLivePost with paged history", () => {
  it("keeps history the reader loaded and only sheds one for one past it", () => {
    const paged = series(MAX_RETAINED_POSTS + 100);
    const next = appendLivePost(paged, post("live", MAX_RETAINED_POSTS + 100));
    expect(next).toHaveLength(MAX_RETAINED_POSTS + 100);
    expect(next[0].id).toBe("p1");
    expect(next[next.length - 1].id).toBe("live");
  });
});
