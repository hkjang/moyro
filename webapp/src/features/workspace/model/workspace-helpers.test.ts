import { describe, expect, it } from "vitest";

import type { Channel, User } from "@/api/client";
import { channelDisplayLabel, directMessagePeer } from "./workspace-helpers";

const users: Record<string, User> = {
  "u-me": { id: "u-me", username: "me", email: "me@example.test" },
  "u-peer": { id: "u-peer", username: "jane", email: "jane@example.test" },
};

function channel(overrides: Partial<Channel>): Channel {
  return { id: "c", team_id: "t", type: "O", name: "general", display_name: "General", create_at: 1, ...overrides };
}

describe("directMessagePeer", () => {
  it("returns the other participant regardless of order", () => {
    expect(directMessagePeer("u-me__u-peer", "u-me")).toBe("u-peer");
    expect(directMessagePeer("u-peer__u-me", "u-me")).toBe("u-peer");
  });
  it("returns the only id for a self-DM", () => {
    expect(directMessagePeer("u-me__u-me", "u-me")).toBe("u-me");
  });
});

describe("channelDisplayLabel", () => {
  it("names a direct message after the other person, never their id", () => {
    const dm = channel({ type: "D", name: "u-me__u-peer", display_name: "" });
    expect(channelDisplayLabel(dm, users, "u-me")).toBe("@jane");
    expect(channelDisplayLabel(dm, {}, "u-me")).toBe("다이렉트 메시지");
    expect(channelDisplayLabel(dm, {}, "u-me")).not.toContain("u-peer");
  });
  it("prefixes rooms with # and falls back to the slug when the display name is empty", () => {
    expect(channelDisplayLabel(channel({}), users, "u-me")).toBe("#General");
    expect(channelDisplayLabel(channel({ display_name: "" }), users, "u-me")).toBe("#general");
    expect(channelDisplayLabel(null, users, "u-me")).toBe("채널");
  });
});
