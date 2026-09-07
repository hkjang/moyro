// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { User } from "@/api/client";
import { TypingIndicator } from "./ThreadPanel";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const users: Record<string, User> = {
  "u-1": { id: "u-1", username: "jane", email: "j@example.test" },
  "u-2": { id: "u-2", username: "mark", email: "m@example.test" },
};

describe("TypingIndicator", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  async function render(ids: string[], directory: Record<string, User> = users) {
    await act(async () => root.render(<TypingIndicator typingUsers={ids} users={directory} />));
    return container.textContent ?? "";
  }

  it("names the people it can and stays silent when nobody is typing", async () => {
    expect(await render([])).toBe("");
    expect(await render(["u-1"])).toContain("jane님이 입력 중");
    expect(await render(["u-1", "u-2"])).toContain("jane, mark님이 입력 중");
  });

  it("never shows a user id for a profile it has not loaded", async () => {
    const text = await render(["u-unknown"], {});
    expect(text).not.toContain("u-unkn");
    expect(text).toContain("누군가 입력 중");

    const many = await render(["u-a", "u-b"], {});
    expect(many).toContain("2명이 입력 중");
    expect(many).not.toContain("u-a");
  });

  it("counts the unnamed rest alongside the names it has", async () => {
    const text = await render(["u-1", "u-x"], users);
    expect(text).toContain("jane님 외 1명이 입력 중");
    expect(text).not.toContain("u-x");
  });
});
