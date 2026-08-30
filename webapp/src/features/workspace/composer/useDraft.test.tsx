// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const draftPolicy = vi.hoisted(() => ({
  storage_mode: "local" as "local" | "session" | "disabled",
  retention_days: 7,
  clear_on_logout: true,
}));

vi.mock("@/features/system/SystemInfoContext", () => ({
  useSystemInfo: () => ({ capabilities: { drafts: draftPolicy } }),
}));

import {
  clearMoyroDraftsForUser,
  DEFAULT_DRAFT_TTL_MS,
  DRAFT_CLEANUP_INTERVAL_MS,
  useDraft,
} from "./useDraft";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const DRAFT_KEY = "moyro:draft:user:channel:root";
const NOW = 2_000_000_000_000;

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

function DraftHarness({ draftKey = DRAFT_KEY }: { draftKey?: string | null }) {
  const [value, setValue] = useState("");
  const draft = useDraft(draftKey, value, setValue);
  return (
    <div>
      <textarea
        aria-label="draft"
        value={value}
        onChange={(event) => {
          draft.stage(event.target.value);
          setValue(event.target.value);
        }}
        onBlur={draft.flush}
      />
      <output data-testid="saved">{draft.hasSaved ? "saved" : "unsaved"}</output>
      <button type="button" data-testid="clear-saved" onClick={draft.clearSaved}>clear</button>
    </div>
  );
}

function draftInput(container: HTMLElement): HTMLTextAreaElement {
  const input = container.querySelector<HTMLTextAreaElement>('textarea[aria-label="draft"]');
  if (!input) throw new Error("draft input not found");
  return input;
}

async function changeValue(input: HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
  if (!setter) throw new Error("textarea value setter not found");
  await act(async () => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("useDraft local retention", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: memoryStorage(),
    });
	Object.defineProperty(globalThis, "sessionStorage", {
		configurable: true,
		value: memoryStorage(),
	});
    localStorage.clear();
	sessionStorage.clear();
	draftPolicy.storage_mode = "local";
	draftPolicy.retention_days = 7;
	draftPolicy.clear_on_logout = true;
    vi.spyOn(Date, "now").mockReturnValue(NOW);
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    localStorage.clear();
	sessionStorage.clear();
  });

  it("flushes a versioned envelope without synchronously shifting the saved-state layout", async () => {
    await act(async () => root.render(<DraftHarness />));
    const input = draftInput(container);
    await changeValue(input, "기기 로컬 초안");
    await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));

    expect(JSON.parse(localStorage.getItem(DRAFT_KEY) ?? "null")).toEqual({
      version: 1,
      value: "기기 로컬 초안",
      updated_at: NOW,
    });
    expect(container.querySelector('[data-testid="saved"]')?.textContent).toBe("unsaved");
    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 550));
    });
    expect(container.querySelector('[data-testid="saved"]')?.textContent).toBe("saved");
  });

  it("hydrates a fresh envelope without extending its update time", async () => {
    const updatedAt = NOW - DEFAULT_DRAFT_TTL_MS + 1;
    localStorage.setItem(DRAFT_KEY, JSON.stringify({
      version: 1,
      value: "유효한 초안",
      updated_at: updatedAt,
    }));

    await act(async () => root.render(<DraftHarness />));

    expect(draftInput(container).value).toBe("유효한 초안");
    expect(JSON.parse(localStorage.getItem(DRAFT_KEY) ?? "null").updated_at).toBe(updatedAt);
    expect(container.querySelector('[data-testid="saved"]')?.textContent).toBe("saved");
  });

  it("removes an expired envelope immediately instead of restoring it", async () => {
    localStorage.setItem(DRAFT_KEY, JSON.stringify({
      version: 1,
      value: "만료된 초안",
      updated_at: NOW - DEFAULT_DRAFT_TTL_MS,
    }));

    await act(async () => root.render(<DraftHarness />));

    expect(draftInput(container).value).toBe("");
    expect(localStorage.getItem(DRAFT_KEY)).toBeNull();
    expect(container.querySelector('[data-testid="saved"]')?.textContent).toBe("unsaved");
  });

  it("cleans every expired draft in the active user's namespace on initial load", async () => {
    const expired = JSON.stringify({
      version: 1,
      value: "만료됨",
      updated_at: NOW - DEFAULT_DRAFT_TTL_MS,
    });
    const fresh = JSON.stringify({
      version: 1,
      value: "유효함",
      updated_at: NOW - DEFAULT_DRAFT_TTL_MS + 1,
    });
    const localExpiredKey = "moyro:draft:user:channel-2:root";
    const sessionExpiredKey = "moyro:draft:edit:user:post-2";
    const freshKey = "moyro:draft:user:channel-3:root";
    const otherUserKey = "moyro:draft:other:channel-4:root";
    localStorage.setItem(localExpiredKey, expired);
    sessionStorage.setItem(sessionExpiredKey, expired);
    localStorage.setItem(freshKey, fresh);
    sessionStorage.setItem(otherUserKey, expired);

    await act(async () => root.render(<DraftHarness />));

    expect(localStorage.getItem(localExpiredKey)).toBeNull();
    expect(sessionStorage.getItem(sessionExpiredKey)).toBeNull();
    expect(localStorage.getItem(freshKey)).not.toBeNull();
    expect(sessionStorage.getItem(otherUserKey)).toBe(expired);
  });

  it("periodically expires the user's drafts in both stores under session policy", async () => {
    draftPolicy.storage_mode = "session";
    const localKey = "moyro:draft:user:channel-2:root";
    const sessionKey = "moyro:draft:edit:user:post-2";
    const fresh = JSON.stringify({ version: 1, value: "잠시 유효", updated_at: NOW });
    localStorage.setItem(localKey, fresh);
    sessionStorage.setItem(sessionKey, fresh);
    let intervalHandler: (() => void) | null = null;
    const setInterval = vi.spyOn(window, "setInterval").mockImplementation((handler: TimerHandler, timeout?: number) => {
      expect(timeout).toBe(DRAFT_CLEANUP_INTERVAL_MS);
      if (typeof handler === "function") intervalHandler = () => handler();
      return 42;
    });

    await act(async () => root.render(<DraftHarness />));
    expect(setInterval).toHaveBeenCalled();
    expect(localStorage.getItem(localKey)).not.toBeNull();
    expect(sessionStorage.getItem(sessionKey)).not.toBeNull();

    vi.mocked(Date.now).mockReturnValue(NOW + DEFAULT_DRAFT_TTL_MS);
    await act(async () => intervalHandler?.());

    expect(localStorage.getItem(localKey)).toBeNull();
    expect(sessionStorage.getItem(sessionKey)).toBeNull();
  });

  it("clearSaved removes the current key from local and session storage", async () => {
    await act(async () => root.render(<DraftHarness />));
    localStorage.setItem(DRAFT_KEY, "local copy");
    sessionStorage.setItem(DRAFT_KEY, "session copy");

    const clear = container.querySelector<HTMLButtonElement>('[data-testid="clear-saved"]');
    await act(async () => clear?.click());

    expect(localStorage.getItem(DRAFT_KEY)).toBeNull();
    expect(sessionStorage.getItem(DRAFT_KEY)).toBeNull();
  });

  it("loads a legacy plain string and migrates it to the bounded envelope", async () => {
    localStorage.setItem(DRAFT_KEY, "이전 버전 초안");

    await act(async () => root.render(<DraftHarness />));

    expect(draftInput(container).value).toBe("이전 버전 초안");
    expect(JSON.parse(localStorage.getItem(DRAFT_KEY) ?? "null")).toEqual({
      version: 1,
      value: "이전 버전 초안",
      updated_at: NOW,
    });
  });

  it("keeps composing usable when browser storage reads or writes fail", async () => {
    const getItem = vi.spyOn(localStorage, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    await act(async () => root.render(<DraftHarness />));
    expect(draftInput(container).value).toBe("");
    getItem.mockRestore();

    const setItem = vi.spyOn(localStorage, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    const input = draftInput(container);
    await changeValue(input, "저장 실패에도 유지");
    await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));

    expect(input.value).toBe("저장 실패에도 유지");
    expect(container.querySelector('[data-testid="saved"]')?.textContent).toBe("unsaved");
    expect(setItem).toHaveBeenCalled();
  });

	it("uses session storage and removes a stale local copy when policy requires it", async () => {
		draftPolicy.storage_mode = "session";
		localStorage.setItem(DRAFT_KEY, "로컬에 남은 초안");
		await act(async () => root.render(<DraftHarness />));
		const input = draftInput(container);
		await changeValue(input, "세션 전용 초안");
		await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));

		expect(localStorage.getItem(DRAFT_KEY)).toBeNull();
		expect(JSON.parse(sessionStorage.getItem(DRAFT_KEY) ?? "null").value).toBe("세션 전용 초안");
	});

	it("disables persistence and clears existing Moyro drafts in both stores", async () => {
		draftPolicy.storage_mode = "disabled";
		localStorage.setItem(DRAFT_KEY, "로컬 초안");
		sessionStorage.setItem("moyro:draft:other:channel:root", "세션 초안");
		await act(async () => root.render(<DraftHarness />));
		const input = draftInput(container);
		await changeValue(input, "저장하면 안 되는 내용");
		await act(async () => input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));

		expect(input.value).toBe("저장하면 안 되는 내용");
		expect(localStorage.getItem(DRAFT_KEY)).toBeNull();
		expect(sessionStorage.getItem("moyro:draft:other:channel:root")).toBeNull();
		expect(container.querySelector('[data-testid="saved"]')?.textContent).toBe("unsaved");
	});

	it("honors the administrator retention period and clears only the logging-out user", async () => {
		draftPolicy.retention_days = 1;
		localStorage.setItem(DRAFT_KEY, JSON.stringify({
			version: 1,
			value: "하루 지난 초안",
			updated_at: NOW - 24 * 60 * 60 * 1_000,
		}));
		localStorage.setItem("moyro:draft:other:channel:root", "다른 사용자");
		sessionStorage.setItem("moyro:draft:edit:user:post-1", "사용자 편집 초안");
		await act(async () => root.render(<DraftHarness />));
		expect(draftInput(container).value).toBe("");

		clearMoyroDraftsForUser("user");
		expect(localStorage.getItem(DRAFT_KEY)).toBeNull();
		expect(sessionStorage.getItem("moyro:draft:edit:user:post-1")).toBeNull();
		expect(localStorage.getItem("moyro:draft:other:channel:root")).toBe("다른 사용자");
	});
});
