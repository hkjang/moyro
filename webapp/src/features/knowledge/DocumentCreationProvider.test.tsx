// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { moyroMeApi, type Post } from "@/api/client";
import { documentsApi, type DocumentRecord, type DocumentSource } from "@/api/documents";
import { APIError } from "@/api/transport";
import { DocumentCreationProvider, useDocumentCreation } from "./DocumentCreationProvider";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("@/features/admin/AdminAccessContext", () => ({
  useAdminAccess: () => ({
    loaded: true,
    permissions: new Set(["use_ai"]),
    can: (permission: string) => permission === "use_ai",
    canAny: () => false,
    hasAdminAccess: false,
  }),
}));

const post: Post = {
  id: "post-1", channel_id: "channel-1", user_id: "user-1", root_id: "", message: "배포를 준비합니다",
  create_at: 10, update_at: 10, delete_at: 0, props: {},
};

const firstSource: DocumentSource = {
  team_id: "team-1", channel_id: "channel-1", thread_id: "post-1", cursor_at: 10,
  posts: [{
    id: "post-1", channel_id: "channel-1", user_id: "user-1", username: "alice", root_id: "",
    message: "배포를 준비합니다", create_at: 10, update_at: 10,
  }],
};

const createdDocument: DocumentRecord = {
  id: "document-1", title: "회의록", body: "# 회의록", created_by: "user-1", team_id: "team-1",
  channel_id: "channel-1", source_thread_id: "post-1", source_cursor_at: 10,
  revision: 1, create_at: 20, update_at: 20, delete_at: 0, stale: false,
};

function OpenButton() {
  const creation = useDocumentCreation();
  return <button type="button" onClick={() => creation.open(post)}>문서화 열기</button>;
}

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

function button(label: string): HTMLButtonElement | undefined {
  return [...document.querySelectorAll<HTMLButtonElement>("button")]
    .find((candidate) => candidate.textContent?.trim() === label);
}

describe("DocumentCreationProvider", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.spyOn(moyroMeApi, "getAIPreferences").mockResolvedValue({
      enabled: true, streaming: true, max_output_tokens: 512, temperature: 0.2,
    });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function renderAndOpen() {
    await act(async () => root.render(
      <DocumentCreationProvider token="session-token" currentUserID="user-1"><OpenButton /></DocumentCreationProvider>,
    ));
    await act(async () => button("문서화 열기")?.click());
    await waitUntil(() => expect(document.querySelector("textarea")?.value).toContain("[M1]"));
  }

  it("retains the offline draft after an AI failure and saves the server cursor", async () => {
    vi.spyOn(documentsApi, "source").mockResolvedValue(firstSource);
    vi.spyOn(moyroMeApi, "streamAICompletion").mockRejectedValue(new Error("AI offline"));
    const create = vi.spyOn(documentsApi, "create").mockResolvedValue({ document: createdDocument, replayed: false });
    await renderAndOpen();
    const fallback = document.querySelector("textarea")?.value;

    await act(async () => button("AI로 초안 생성")?.click());
    await waitUntil(() => expect(document.body.textContent).toContain("기본 초안은 그대로 사용할 수 있습니다"));
    expect(document.querySelector("textarea")?.value).toBe(fallback);

    await act(async () => button("문서 저장")?.click());
    await waitUntil(() => expect(create).toHaveBeenCalledOnce());
    expect(create.mock.calls[0][1]).toMatchObject({
      source_post_id: "post-1",
      source_cursor_at: 10,
    });
    expect(create.mock.calls[0][1].idempotency_key).toBeTruthy();
  });

  it("reloads a changed source and requires review instead of silently saving stale content", async () => {
    const changedSource: DocumentSource = {
      ...firstSource,
      cursor_at: 20,
      posts: [...firstSource.posts, {
        id: "post-2", channel_id: "channel-1", user_id: "user-2", username: "bob", root_id: "post-1",
        message: "롤백 확인을 추가합니다", create_at: 20, update_at: 20,
      }],
    };
    vi.spyOn(documentsApi, "source").mockResolvedValueOnce(firstSource).mockResolvedValueOnce(changedSource);
    const create = vi.spyOn(documentsApi, "create").mockRejectedValue(new APIError(409, "원본 대화가 변경되었습니다."));
    await renderAndOpen();

    await act(async () => button("문서 저장")?.click());
    await waitUntil(() => expect(document.body.textContent).toContain("확인 후 저장하세요"));
    expect(document.querySelector("textarea")?.value).toContain("롤백 확인을 추가합니다");
    expect(create.mock.calls[0][1].source_cursor_at).toBe(10);
    expect(button("문서 저장")?.disabled).toBe(false);
  });
});
