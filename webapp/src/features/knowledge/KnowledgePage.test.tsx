// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { moyroMeApi } from "@/api/client";
import { documentsApi } from "@/api/documents";
import { knowledgeApi } from "@/api/knowledge";
import { KnowledgePage } from "./KnowledgePage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("react-redux", () => ({
  useSelector: (selector: (state: unknown) => unknown) => selector({
    auth: { token: "session-token", user: { id: "user-1", username: "alice" } },
  }),
}));

vi.mock("@/features/admin/AdminAccessContext", () => ({
  useAdminAccess: () => ({
    loaded: true,
    permissions: new Set(["use_ai"]),
    can: (permission: string) => permission === "use_ai",
    canAny: () => false,
    hasAdminAccess: false,
  }),
}));

vi.mock("@/features/flow/FlowDataProvider", () => ({
  useFlowWorkspaceIndex: () => ({
    teams: [{ id: "team-1", display_name: "운영팀", name: "ops" }],
    entries: [{
      team: { id: "team-1", display_name: "운영팀", name: "ops" },
      channel: { id: "channel-1", team_id: "team-1", display_name: "배포", name: "deploy" },
    }],
    channelById: {
      "channel-1": {
        team: { id: "team-1", display_name: "운영팀", name: "ops" },
        channel: { id: "channel-1", team_id: "team-1", display_name: "배포", name: "deploy" },
      },
    },
    loading: false,
    error: "",
    warnings: [],
    activityRevision: 0,
    workItemRevision: 0,
    refresh: vi.fn(),
  }),
}));

async function settle(rounds = 1) {
  for (let index = 0; index < rounds; index += 1) {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
  }
}

async function waitUntil(assertion: () => void) {
  let lastError: unknown;
  for (let index = 0; index < 30; index += 1) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await settle();
    }
  }
  throw lastError;
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("KnowledgePage", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.spyOn(documentsApi, "list").mockResolvedValue([]);
    vi.spyOn(moyroMeApi, "getAIPreferences").mockResolvedValue({
      enabled: true, streaming: true, max_output_tokens: 512, temperature: 0.2,
    });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("keeps authoritative source cards visible when optional AI generation fails", async () => {
    vi.spyOn(knowledgeApi, "search").mockResolvedValue({
      total_hits: 2,
      sources: [
        {
          ref: "M1", kind: "message", id: "post-1", post_id: "post-1", source_thread_id: "post-1",
          team_id: "team-1", channel_id: "channel-1", excerpt: "검증된 배포 메시지",
          author_id: "user-1", author_name: "alice", create_at: 1, update_at: 1, rank: 1,
        },
        {
          ref: "D1", kind: "document", id: "document-1", document_id: "document-1", source_thread_id: "post-1",
          title: "배포 절차", team_id: "team-1", channel_id: "channel-1", excerpt: "검증된 배포 문서",
          author_id: "user-1", author_name: "alice", create_at: 1, update_at: 2, rank: 1,
        },
      ],
    });
    vi.spyOn(moyroMeApi, "streamAICompletion").mockRejectedValue(new Error("AI provider unavailable"));

    await act(async () => root.render(<MemoryRouter><KnowledgePage /></MemoryRouter>));
    await settle(3);
    const input = container.querySelector<HTMLInputElement>('input[aria-label="질문 또는 검색어"]')
      ?? container.querySelectorAll<HTMLInputElement>("input")[2];
    expect(input).toBeTruthy();
    await act(async () => setInputValue(input, "배포 방법"));
    const searchButton = [...container.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent?.trim() === "검색");
    expect(searchButton).toBeTruthy();
    await act(async () => searchButton?.click());

    await waitUntil(() => expect(container.textContent).toContain("AI provider unavailable"));
    expect(container.textContent).toContain("검증된 배포 메시지");
    expect(container.textContent).toContain("검증된 배포 문서");
    expect(container.querySelector('[data-source-ref="M1"]')).not.toBeNull();
    expect(container.querySelector('[data-source-ref="D1"]')).not.toBeNull();
    expect(moyroMeApi.streamAICompletion).toHaveBeenCalledOnce();
  });
});
