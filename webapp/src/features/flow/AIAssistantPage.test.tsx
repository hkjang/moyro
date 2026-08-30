// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { moyroMeApi } from "@/api/client";
import { AdminAccessProvider } from "@/features/admin/AdminAccessContext";
import { AI_CONTEXT_LIMITS, AIAssistantPage, boundedAIHistory, type ChatTurn } from "./AIAssistantPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const preferences = {
  enabled: true,
  provider_id: "internal-provider",
  model: "moyro-model",
  streaming: true,
  max_output_tokens: 2048,
  temperature: 0.3,
};

async function eventually(assertion: () => void) {
  let lastError: unknown;
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await act(async () => new Promise((resolve) => window.setTimeout(resolve, 0)));
    }
  }
  throw lastError;
}

function findButton(label: string, scope: ParentNode = document): HTMLButtonElement {
  const button = [...scope.querySelectorAll<HTMLButtonElement>("button")]
    .find((candidate) => candidate.textContent?.trim() === label);
  if (!button) throw new Error(`button not found: ${label}`);
  return button;
}

function promptInput(scope: ParentNode = document): HTMLTextAreaElement {
  const input = [...scope.querySelectorAll<HTMLTextAreaElement>("textarea")]
    .find((candidate) => candidate.getAttribute("aria-label") === "AI에게 보낼 메시지");
  if (!input) throw new Error("AI prompt input not found");
  return input;
}

async function changeTextarea(input: HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
  if (!setter) throw new Error("textarea value setter not found");
  await act(async () => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("AIAssistantPage", () => {
  let container: HTMLDivElement;
  let root: Root;
  let scrollTo: ReturnType<typeof vi.fn>;

  it("keeps only the newest bounded conversation context", () => {
    const history: ChatTurn[] = Array.from({ length: AI_CONTEXT_LIMITS.turns + 8 }, (_, index) => ({
      id: String(index),
      role: index % 2 === 0 ? "user" : "assistant",
      content: `turn-${index}`,
    }));
    const bounded = boundedAIHistory(history);
    expect(bounded.length).toBeLessThanOrEqual(AI_CONTEXT_LIMITS.turns);
    expect(bounded.at(-1)?.content).toBe(`turn-${history.length - 1}`);
    expect(bounded[0]?.role).toBe("user");

    const oversized = boundedAIHistory([{
      id: "latest",
      role: "user",
      content: `drop-${"a".repeat(AI_CONTEXT_LIMITS.characters)}-keep`,
    }]);
    expect(Array.from(oversized[0].content)).toHaveLength(AI_CONTEXT_LIMITS.characters);
    expect(oversized[0].content.endsWith("-keep")).toBe(true);
  });

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    scrollTo = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: scrollTo });
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn(async () => undefined) },
    });
    vi.spyOn(moyroMeApi, "getPermissions").mockResolvedValue({ permissions: ["use_ai"] });
    vi.spyOn(moyroMeApi, "getAIPreferences").mockResolvedValue(preferences);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function renderPage() {
    const store = configureStore({
      reducer: { auth: () => ({ token: "session-token", user: null }) },
    });
    await act(async () => {
      root.render(
        <Provider store={store}>
          <AdminAccessProvider>
            <MemoryRouter>
              <AIAssistantPage />
            </MemoryRouter>
          </AdminAccessProvider>
        </Provider>,
      );
      await Promise.resolve();
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });
    await eventually(() => expect(promptInput(container).disabled).toBe(false));
  }

  it("renders streamed Markdown safely, copies the response, and keeps technical data collapsed", async () => {
    const markdown = [
      "# 검토 결과",
      "- 첫 번째 항목",
      "- 두 번째 항목",
      "",
      "| 구분 | 상태 |",
      "| --- | --- |",
      "| 배포 | 완료 |",
      "",
      "```ts",
      "const ready = true;",
      "```",
      "",
      "<script>window.__unsafe = true</script>",
      "![외부 이미지](https://tracker.example/pixel.png)",
      "[위험 링크](javascript:alert(1))",
    ].join("\n");
    vi.spyOn(moyroMeApi, "streamAICompletion").mockImplementation(async (_token, _value, onDelta) => {
      onDelta(markdown);
    });
    await renderPage();

    const details = container.querySelector<HTMLDetailsElement>(".flow-ai-details");
    expect(details?.open).toBe(false);
    expect(details?.querySelector("summary")?.textContent).toBe("AI 사용 세부 정보");
    expect(container.textContent).toContain("현재 대화의 텍스트만 전송됩니다");

    await act(async () => findButton("요약", container).click());
    expect(promptInput(container).value).toContain("핵심과 후속 조치 중심");
    await changeTextarea(promptInput(container), "배포 기록을 검토해 줘");
    await act(async () => findButton("보내기", container).click());
    await eventually(() => expect(container.textContent).toContain("검토 결과"));

    expect(container.querySelector(".flow-ai-markdown ul")).not.toBeNull();
    expect(container.querySelector(".flow-ai-markdown table")).not.toBeNull();
    expect(container.querySelector(".flow-ai-code-block")?.textContent).toContain("const ready = true;");
    expect(container.querySelector(".flow-ai-markdown script")).toBeNull();
    expect(container.querySelector(".flow-ai-markdown img")).toBeNull();
    expect(container.querySelector<HTMLAnchorElement>('.flow-ai-markdown a[href^="javascript:"]')).toBeNull();
    expect(scrollTo).toHaveBeenCalled();

    await act(async () => findButton("응답 복사", container).click());
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(markdown);
    expect(container.textContent).toContain("AI 응답을 클립보드에 복사했습니다.");
  });

  it("regenerates the last response and branches the conversation from an edited user turn", async () => {
    const responses = ["첫 번째 응답", "다시 만든 응답", "수정된 질문의 응답"];
    const stream = vi.spyOn(moyroMeApi, "streamAICompletion").mockImplementation(async (_token, _value, onDelta) => {
      onDelta(responses[stream.mock.calls.length - 1] ?? "응답");
    });
    await renderPage();

    await changeTextarea(promptInput(container), "원래 질문");
    await act(async () => findButton("보내기", container).click());
    await eventually(() => expect(container.textContent).toContain("첫 번째 응답"));

    await act(async () => findButton("다시 생성", container).click());
    await eventually(() => expect(container.textContent).toContain("다시 만든 응답"));
    expect(container.textContent).not.toContain("첫 번째 응답");
    expect(stream.mock.calls[1]?.[1].messages).toEqual([{ role: "user", content: "원래 질문" }]);

    await act(async () => findButton("수정 후 재전송", container).click());
    const editInput = [...container.querySelectorAll<HTMLTextAreaElement>("textarea")]
      .find((candidate) => candidate.getAttribute("aria-label") === "사용자 메시지 수정");
    if (!editInput) throw new Error("edited prompt input not found");
    await changeTextarea(editInput, "수정한 질문");
    await act(async () => findButton("수정하여 전송", container).click());
    await eventually(() => expect(container.textContent).toContain("수정된 질문의 응답"));

    expect(container.textContent).not.toContain("다시 만든 응답");
    expect(stream.mock.calls[2]?.[1].messages).toEqual([{ role: "user", content: "수정한 질문" }]);
  });

  it("submits with the accessible Ctrl+Enter keyboard shortcut", async () => {
    const stream = vi.spyOn(moyroMeApi, "streamAICompletion").mockImplementation(async (_token, _value, onDelta) => {
      onDelta("키보드 응답");
    });
    await renderPage();
    const input = promptInput(container);
    await changeTextarea(input, "키보드 질문");

    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }));
      await Promise.resolve();
    });

    await eventually(() => expect(stream).toHaveBeenCalledOnce());
    expect(stream.mock.calls[0]?.[1].messages).toEqual([{ role: "user", content: "키보드 질문" }]);
  });
});
