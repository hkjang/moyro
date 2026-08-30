// @vitest-environment jsdom
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api, compatApi } from "@/api/client";
import { workItemsApi } from "@/api/work-items";
import { WorkItemCreationProvider } from "@/features/work-items/WorkItemCreationProvider";
import { MessageItem } from "./MessageItem";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const post = {
  id: "post-1",
  channel_id: "channel-1",
  user_id: "user-1",
  root_id: "root-1",
  message: "모바일 메시지 작업 테스트",
  create_at: 1_700_000_000_000,
  update_at: 1_700_000_000_000,
  delete_at: 0,
  props: {},
};

function menuItem(label: string): HTMLElement {
  const match = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')]
    .find((item) => item.textContent?.trim() === label);
  if (!match) throw new Error(`menu item not found: ${label}`);
  return match;
}

describe("MessageItem mobile and keyboard actions", () => {
  let container: HTMLDivElement;
  let root: Root;
  let props: ComponentProps<typeof MessageItem>;
  let originalLocalStorage: PropertyDescriptor | undefined;

  beforeEach(() => {
    const stored = new Map<string, string>();
    originalLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: {
        get length() { return stored.size; },
        clear: () => stored.clear(),
        getItem: (key: string) => stored.get(key) ?? null,
        key: (index: number) => [...stored.keys()][index] ?? null,
        removeItem: (key: string) => stored.delete(key),
        setItem: (key: string, value: string) => stored.set(key, value),
      } satisfies Storage,
    });
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    props = {
      post,
      isMe: true,
      author: { id: "user-1", username: "moyro-user", email: "user@example.invalid" },
      reactions: [],
      currentUserId: "user-1",
      files: [],
      token: "session-token",
      onToggleReaction: vi.fn(),
      onEdit: vi.fn().mockResolvedValue(true),
      onDelete: vi.fn(),
      onOpenThread: vi.fn(),
      onToggleSaved: vi.fn(),
      onRemindMe: vi.fn(),
    };
    vi.spyOn(api, "listEmojis").mockResolvedValue([]);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    if (originalLocalStorage) {
      Object.defineProperty(globalThis, "localStorage", originalLocalStorage);
    } else {
      Reflect.deleteProperty(globalThis, "localStorage");
    }
    vi.restoreAllMocks();
  });

  async function renderMessage() {
    await act(async () => root.render(<MessageItem {...props} />));
  }

  async function openMoreMenu(): Promise<HTMLButtonElement> {
    const trigger = container.querySelector<HTMLButtonElement>('button[aria-label="메시지 작업 더보기"]');
    if (!trigger) throw new Error("more actions trigger not found");
    await act(async () => {
      trigger.focus();
      trigger.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    return trigger;
  }

  it("keeps one semantic more trigger available without hover and exposes every action as a menu item", async () => {
    await renderMessage();

    const triggers = container.querySelectorAll<HTMLButtonElement>('button[aria-label="메시지 작업 더보기"]');
    expect(triggers).toHaveLength(1);
    expect(triggers[0].classList.contains("message-action-more")).toBe(true);
    expect(triggers[0].getAttribute("aria-haspopup")).toBe("menu");
    expect(triggers[0].getAttribute("aria-expanded")).toBe("false");
    expect(container.querySelectorAll(".message-action-primary")).toHaveLength(3);

    const trigger = await openMoreMenu();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    const menu = document.querySelector<HTMLElement>('[role="menu"][aria-label="메시지 작업 더보기"]');
    expect(menu).not.toBeNull();
    expect([...menu?.querySelectorAll('[role="menuitem"]') ?? []].map((item) => item.textContent?.trim())).toEqual([
      "리액션 추가",
      "스레드 열기",
      "저장",
      "나중에 알림",
      "편집",
      "삭제",
    ]);

    await act(async () => {
      menuItem("스레드 열기").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(props.onOpenThread).toHaveBeenCalledWith("root-1");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  it("runs touch menu actions and opens the keyboard-reachable reaction picker", async () => {
    await renderMessage();

    await openMoreMenu();
    await act(async () => menuItem("저장").click());
    expect(props.onToggleSaved).toHaveBeenCalledOnce();

    await openMoreMenu();
    await act(async () => menuItem("나중에 알림").click());
    expect(props.onRemindMe).toHaveBeenCalledOnce();

    await openMoreMenu();
    await act(async () => menuItem("삭제").click());
    expect(props.onDelete).toHaveBeenCalledWith("post-1");

    await openMoreMenu();
    await act(async () => menuItem("리액션 추가").click());
    const picker = container.querySelector<HTMLElement>('[role="dialog"][aria-label="리액션 선택"]');
    expect(picker).not.toBeNull();
    const quickReaction = picker?.querySelector<HTMLButtonElement>('button[title=":+1:"]');
    expect(quickReaction).not.toBeNull();
    await act(async () => quickReaction?.click());
    expect(props.onToggleReaction).toHaveBeenCalledWith("+1");
    expect(container.querySelector('[role="dialog"][aria-label="리액션 선택"]')).toBeNull();
  });

  it("closes the menu with Escape and returns focus to the trigger", async () => {
    await renderMessage();
    const trigger = await openMoreMenu();
    const menu = document.querySelector<HTMLElement>('[role="menu"][aria-label="메시지 작업 더보기"]');
    if (!menu) throw new Error("more actions menu not found");

    await act(async () => {
      menu.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps owner-only editing reachable through the more menu", async () => {
    await renderMessage();
    await openMoreMenu();
    await act(async () => menuItem("편집").click());

    const editor = container.querySelector<HTMLTextAreaElement>('textarea[aria-label="메시지 편집"]');
    expect(editor).not.toBeNull();
    expect(editor?.value).toBe(post.message);
    expect(container.querySelector('button[aria-label="메시지 작업 더보기"]')).toBeNull();
  });

  it("opens a durable task form from the message menu and preserves the source id", async () => {
    vi.spyOn(api, "listChannelMembers").mockResolvedValue([{
      channel_id: "channel-1", user_id: "user-1", roles: "channel_user", last_viewed_at: 0, create_at: 1,
    }]);
    vi.spyOn(compatApi, "usersByIds").mockResolvedValue([
      { id: "user-1", username: "moyro-user", email: "user@example.invalid" },
    ]);
    const created = vi.spyOn(workItemsApi, "create").mockResolvedValue({
      replayed: false,
      item: {
        id: "task-1", kind: "task", title: post.message, description: "", status: "open",
        created_by: "user-1", assignee_id: "user-1", channel_id: "channel-1",
        source_post_id: "post-1", due_at: 0, decided_at: 0,
        create_at: 1, update_at: 1, delete_at: 0,
      },
    });

    await act(async () => root.render(
      <WorkItemCreationProvider token="session-token" currentUserID="user-1">
        <MessageItem {...props} />
      </WorkItemCreationProvider>,
    ));
    await openMoreMenu();
    await act(async () => {
      menuItem("작업으로 만들기").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("작업으로 만들기");

    const submit = [...document.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent?.trim() === "작업 만들기");
    if (!submit) throw new Error("task submit button not found");
    await act(async () => {
      submit.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(created).toHaveBeenCalledOnce();
    expect(created.mock.calls[0][1]).toMatchObject({
      kind: "task",
      title: post.message,
      assignee_id: "user-1",
      source_post_id: "post-1",
    });
    expect(created.mock.calls[0][1].idempotency_key).toBeTruthy();
  });
});
