// @vitest-environment jsdom
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api, compatApi } from "@/api/client";
import { documentsApi } from "@/api/documents";
import { workItemsApi } from "@/api/work-items";
import { DocumentCreationProvider } from "@/features/knowledge/DocumentCreationProvider";
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
      "텍스트 복사",
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
        priority: "normal", completed_at: 0, recurrence_unit: "none", recurrence_interval: 1,
        occurrence_no: 0, dependency_ids: [], impact_task_ids: [],
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

  it("opens the permission-checked conversation document flow from the message menu", async () => {
    const source = vi.spyOn(documentsApi, "source").mockResolvedValue({
      team_id: "team-1", channel_id: "channel-1", thread_id: "root-1", cursor_at: 123,
      posts: [{
        id: "root-1", channel_id: "channel-1", user_id: "user-1", username: "moyro-user",
        root_id: "", message: post.message, create_at: post.create_at, update_at: post.update_at,
      }],
    });

    await act(async () => root.render(
      <DocumentCreationProvider token="session-token" currentUserID="user-1">
        <MessageItem {...props} />
      </DocumentCreationProvider>,
    ));
    await openMoreMenu();
    await act(async () => {
      menuItem("대화에서 문서 만들기").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(source).toHaveBeenCalledWith("session-token", "post-1", expect.any(AbortSignal));
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("대화에서 문서 만들기");
  });
});

describe("MessageItem thread summary and copy actions", () => {
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

  it("summarises replies on a root post and opens the thread from it", async () => {
    const onOpenThread = vi.fn();
    const rootPost = { ...post, id: "root-9", root_id: "", reply_count: 3, last_reply_at: Date.now() - 60_000 };
    await act(async () => root.render(
      <MessageItem
        post={rootPost as never}
        isMe={false}
        reactions={[]}
        currentUserId="user-2"
        files={[]}
        token="t"
        onToggleReaction={vi.fn()}
        onEdit={vi.fn(async () => true)}
        onDelete={vi.fn()}
        onOpenThread={onOpenThread}
      />,
    ));
    const summary = container.querySelector<HTMLButtonElement>(".msg-thread-summary");
    expect(summary?.textContent).toContain("답글 3개");
    expect(summary?.textContent).toContain("마지막 답글");
    await act(async () => summary?.click());
    expect(onOpenThread).toHaveBeenCalledWith("root-9");
  });

  it("does not render a summary on replies or on posts without replies", async () => {
    await act(async () => root.render(
      <MessageItem
        post={{ ...post, reply_count: 5 } as never}
        isMe={false}
        reactions={[]}
        currentUserId="user-2"
        files={[]}
        token="t"
        onToggleReaction={vi.fn()}
        onEdit={vi.fn(async () => true)}
        onDelete={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    ));
    // `post` is a reply (root_id set), so even a stray count is ignored.
    expect(container.querySelector(".msg-thread-summary")).toBeNull();
  });

  it("offers link and text copy only when a permalink builder is provided", async () => {
    await act(async () => root.render(
      <MessageItem
        post={{ ...post, root_id: "" } as never}
        isMe={false}
        reactions={[]}
        currentUserId="user-2"
        files={[]}
        token="t"
        onToggleReaction={vi.fn()}
        onEdit={vi.fn(async () => true)}
        onDelete={vi.fn()}
        permalinkFor={(p) => `https://moyro.test/p/${p.id}`}
      />,
    ));
    const trigger = container.querySelector<HTMLButtonElement>('button[aria-label="메시지 작업 더보기"]');
    await act(async () => trigger?.click());
    const labels = [...document.querySelectorAll('[role="menuitem"]')].map((item) => item.textContent?.trim());
    expect(labels).toContain("링크 복사");
    expect(labels).toContain("텍스트 복사");
  });
});

describe("MessageItem emoticons", () => {
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

  const stickerPost = { ...post, root_id: "", message: "파이팅!", props: { sticker: "moyo:reaction-10" } };

  it("draws a built-in emoticon in place of the caption when enabled", async () => {
    await act(async () => root.render(
      <MessageItem
        post={stickerPost as never}
        isMe={false}
        reactions={[]}
        currentUserId="user-2"
        files={[]}
        token="t"
        onToggleReaction={vi.fn()}
        onEdit={vi.fn(async () => true)}
        onDelete={vi.fn()}
      />,
    ));
    const sticker = container.querySelector('svg.sticker[role="img"]');
    expect(sticker?.getAttribute("aria-label")).toContain("이모티콘");
    expect(container.querySelector(".msg-body")).toBeNull();
  });

  it("falls back to the caption text when the reader disabled emoticons", async () => {
    await act(async () => root.render(
      <MessageItem
        post={stickerPost as never}
        isMe={false}
        reactions={[]}
        currentUserId="user-2"
        files={[]}
        token="t"
        onToggleReaction={vi.fn()}
        onEdit={vi.fn(async () => true)}
        onDelete={vi.fn()}
        emoticonsEnabled={false}
      />,
    ));
    expect(container.querySelector("svg.sticker")).toBeNull();
    expect(container.querySelector(".msg-body")?.textContent).toContain("파이팅!");
  });
});
