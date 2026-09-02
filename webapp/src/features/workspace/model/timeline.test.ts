import { describe, expect, it } from "vitest";

import type { Post } from "@/api/client";
import { CONTINUATION_WINDOW_MS, buildTimeline } from "./timeline";

const day = new Date(2026, 7, 30, 13, 0).getTime();

function post(id: string, userId: string, createAt: number, extra: Partial<Post> = {}): Post {
  return {
    id,
    channel_id: "channel-1",
    user_id: userId,
    root_id: "",
    message: id,
    props: {},
    file_ids: [],
    is_pinned: false,
    create_at: createAt,
    update_at: createAt,
    delete_at: 0,
    link_metadata: [],
    ...extra,
  } as unknown as Post;
}

function kinds(items: ReturnType<typeof buildTimeline>): string[] {
  return items.map((item) => (item.kind === "post" ? `${item.post.id}${item.continuation ? "+" : ""}` : item.kind));
}

describe("buildTimeline", () => {
  it("opens with a date separator and groups quick follow-ups by the same author", () => {
    const items = buildTimeline([
      post("a", "u1", day),
      post("b", "u1", day + 60_000),
      post("c", "u2", day + 90_000),
      post("d", "u1", day + 120_000),
    ], { now: day });

    expect(kinds(items)).toEqual(["date", "a", "b+", "c", "d"]);
    expect(items[0]).toMatchObject({ kind: "date", label: "오늘" });
  });

  it("breaks a group once the continuation window elapses", () => {
    const items = buildTimeline([
      post("a", "u1", day),
      post("b", "u1", day + CONTINUATION_WINDOW_MS),
    ], { now: day });
    expect(kinds(items)).toEqual(["date", "a", "b"]);
  });

  it("inserts a separator at every day boundary and never groups across it", () => {
    const midnight = new Date(2026, 7, 31, 0, 0).getTime();
    const items = buildTimeline([
      post("a", "u1", midnight - 30_000),
      post("b", "u1", midnight + 30_000),
    ], { now: midnight });
    expect(kinds(items)).toEqual(["date", "a", "date", "b"]);
    expect(items[0]).toMatchObject({ label: "어제" });
    expect(items[2]).toMatchObject({ label: "오늘" });
  });

  it("places one unread marker before the first post by someone else after last view", () => {
    const items = buildTimeline([
      post("seen", "u2", day),
      post("mine", "me", day + 10_000),
      post("new1", "u2", day + 20_000),
      post("new2", "u2", day + 30_000),
    ], { now: day, unreadSince: day + 5_000, currentUserId: "me" });

    expect(kinds(items)).toEqual(["date", "seen", "mine", "unread", "new1", "new2+"]);
  });

  it("suppresses the unread marker when nothing is newer than the last view", () => {
    const items = buildTimeline([post("a", "u2", day)], { now: day, unreadSince: day + 1, currentUserId: "me" });
    expect(kinds(items)).toEqual(["date", "a"]);
  });

  it("does not group a webhook posting under a different display name with its bot user", () => {
    const items = buildTimeline([
      post("a", "bot", day, { props: { from_webhook: "true", override_username: "deploys" } }),
      post("b", "bot", day + 1_000, { props: { from_webhook: "true", override_username: "alerts" } }),
    ], { now: day });
    expect(kinds(items)).toEqual(["date", "a", "b"]);
  });

  it("does not group a thread reply with a root post", () => {
    const items = buildTimeline([
      post("root", "u1", day),
      post("reply", "u1", day + 1_000, { root_id: "root" }),
    ], { now: day });
    expect(kinds(items)).toEqual(["date", "root", "reply"]);
  });

  it("renders an empty list as nothing", () => {
    expect(buildTimeline([])).toEqual([]);
  });
});
