// @vitest-environment jsdom
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MessageComposer } from "./MessageComposer";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const mention = vi.hoisted(() => ({
  handleKeyDown: vi.fn((_event: unknown) => false),
  onChange: vi.fn(),
}));

function memoryStorage(): Storage {
  const values = new Map<string, string>();
  return {
    get length() {
      return values.size;
    },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => {
      values.delete(key);
    },
    setItem: (key, value) => {
      values.set(key, String(value));
    },
  };
}

vi.mock("@/components/MentionPicker", () => ({
  useMentionAutocomplete: () => ({
    open: false,
    onChange: mention.onChange,
    handleKeyDown: mention.handleKeyDown,
    render: () => null,
  }),
}));

function messageInput(container: HTMLElement): HTMLTextAreaElement {
  const input = container.querySelector<HTMLTextAreaElement>('textarea[aria-label="메시지 입력"]');
  if (!input) throw new Error("message input not found");
  return input;
}

function sendButton(container: HTMLElement): HTMLButtonElement {
  const button = container.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!button) throw new Error("send button not found");
  return button;
}

async function changeValue(input: HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
  if (!setter) throw new Error("textarea value setter not found");
  await act(async () => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function dispatchKeyDown(
  input: HTMLTextAreaElement,
  options: {
    shiftKey?: boolean;
    isComposing?: boolean;
    keyCode?: number;
  } = {},
): Promise<KeyboardEvent> {
  const event = new KeyboardEvent("keydown", {
    key: "Enter",
    code: "Enter",
    bubbles: true,
    cancelable: true,
    shiftKey: options.shiftKey,
  });
  if (options.isComposing !== undefined) {
    Object.defineProperty(event, "isComposing", { configurable: true, value: options.isComposing });
  }
  if (options.keyCode !== undefined) {
    Object.defineProperty(event, "keyCode", { configurable: true, value: options.keyCode });
  }
  await act(async () => {
    input.dispatchEvent(event);
    await Promise.resolve();
  });
  return event;
}

function mockOnSend() {
  return vi.fn(async (_message: string, _fileIds: string[]) => true);
}

describe("MessageComposer keyboard submission", () => {
  let container: HTMLDivElement;
  let root: Root;
  let onSend: ReturnType<typeof mockOnSend>;

  const baseProps: Omit<ComponentProps<typeof MessageComposer>, "onSend"> = {
    token: "user-token",
    channelID: "channel-id",
    destinationLabel: "#general에 전송",
    canUseAI: false,
    aiPermissionLoaded: true,
    aiStatusLabel: "AI 사용 불가",
    onTyping: vi.fn(),
    onUpload: vi.fn(async () => []),
  };

  beforeEach(async () => {
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: memoryStorage(),
    });
    Object.defineProperty(globalThis, "sessionStorage", {
      configurable: true,
      value: memoryStorage(),
    });
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    onSend = mockOnSend();
    mention.handleKeyDown.mockReset();
    mention.handleKeyDown.mockReturnValue(false);
    mention.onChange.mockReset();
    await act(async () => root.render(<MessageComposer {...baseProps} onSend={onSend} />));
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("disables submission for empty and whitespace-only messages", async () => {
    const input = messageInput(container);
    expect(sendButton(container).disabled).toBe(true);

    await changeValue(input, "   \n\t");
    expect(sendButton(container).disabled).toBe(true);
    await dispatchKeyDown(input);

    expect(onSend).not.toHaveBeenCalled();

    await changeValue(input, " 전송할 메시지 ");
    expect(sendButton(container).disabled).toBe(false);
  });

  it("labels a persisted draft as stored on this device", async () => {
    await act(async () => root.render(
      <MessageComposer {...baseProps} userId="user-id" onSend={onSend} />,
    ));
    const input = messageInput(container);
    await changeValue(input, "로컬 초안");
    await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));

    // Blur persists immediately, but the badge must not move nearby actions
    // during the pointer sequence that caused the blur.
    expect(container.textContent).not.toContain("이 기기에 초안 저장됨");
    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 550));
    });
    expect(container.textContent).toContain("이 기기에 초안 저장됨");
  });

  it("removes both storage copies after an in-flight send completes across a scope change", async () => {
    let resolveSend: ((sent: boolean) => void) | undefined;
    onSend.mockImplementation(() => new Promise<boolean>((resolve) => {
      resolveSend = resolve;
    }));
    await act(async () => root.render(
      <MessageComposer {...baseProps} userId="user-id" onSend={onSend} />,
    ));
    const input = messageInput(container);
    await changeValue(input, "전송할 초안");
    await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));
    const submittedKey = "moyro:draft:user-id:channel-id:root";
    sessionStorage.setItem(submittedKey, localStorage.getItem(submittedKey) ?? "session copy");

    await act(async () => sendButton(container).click());
    expect(onSend).toHaveBeenCalledOnce();
    await act(async () => root.render(
      <MessageComposer {...baseProps} channelID="next-channel" userId="user-id" onSend={onSend} />,
    ));
    await act(async () => {
      resolveSend?.(true);
      await Promise.resolve();
    });

    expect(localStorage.getItem(submittedKey)).toBeNull();
    expect(sessionStorage.getItem(submittedKey)).toBeNull();
  });

  it("does not send while an IME composition is active and sends after it ends", async () => {
    const input = messageInput(container);
    await changeValue(input, "한글 메시지");

    await act(async () => {
      input.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "한" }));
    });
    await dispatchKeyDown(input);
    expect(onSend).not.toHaveBeenCalled();

    await act(async () => {
      input.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "한글" }));
    });
    const lineBreak = await dispatchKeyDown(input, { shiftKey: true });
    expect(lineBreak.defaultPrevented).toBe(false);
    expect(onSend).not.toHaveBeenCalled();

    await dispatchKeyDown(input);
    expect(onSend).toHaveBeenCalledOnce();
    expect(onSend).toHaveBeenCalledWith("한글 메시지", []);
  });

  it("honors native composition flags used by Korean IMEs", async () => {
    const input = messageInput(container);
    await changeValue(input, "조합 중");

    await dispatchKeyDown(input, { isComposing: true });
    await dispatchKeyDown(input, { keyCode: 229 });

    expect(onSend).not.toHaveBeenCalled();
  });

  it("lets mention selection consume Enter before composition guards", async () => {
    const input = messageInput(container);
    await changeValue(input, "@hong");
    mention.handleKeyDown.mockImplementation((event: unknown) => {
      (event as { preventDefault: () => void }).preventDefault();
      return true;
    });

    const event = await dispatchKeyDown(input, { isComposing: true });

    expect(mention.handleKeyDown).toHaveBeenCalledOnce();
    expect(event.defaultPrevented).toBe(true);
    expect(onSend).not.toHaveBeenCalled();
  });
});
