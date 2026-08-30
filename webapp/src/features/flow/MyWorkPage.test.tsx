// @vitest-environment jsdom
import { configureStore } from "@reduxjs/toolkit";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api, compatApi } from "@/api/client";
import { workItemsApi, type WorkItem } from "@/api/work-items";
import { MyWorkPage } from "./MyWorkPage";

vi.mock("./FlowDataProvider", () => ({
  useFlowWorkspaceIndex: () => ({
    teams: [], entries: [], channelById: {}, loading: false, error: "", warnings: [],
    activityRevision: 0, workItemRevision: 0, refresh: vi.fn(),
  }),
}));

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function workItem(id: string, title: string): WorkItem {
  return {
    id,
    kind: "task",
    title,
    description: "",
    status: "open",
    created_by: "user-1",
    assignee_id: "user-1",
    channel_id: "channel-1",
    source_post_id: `post-${id}`,
    due_at: 0,
    decided_at: 0,
    create_at: 1,
    update_at: 1,
    delete_at: 0,
  };
}

async function eventually(assertion: () => void) {
  let lastError: unknown;
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
    }
  }
  throw lastError;
}

describe("MyWorkPage work item pagination", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.spyOn(api, "listSavedPosts").mockResolvedValue({ order: [], posts: {} });
    vi.spyOn(api, "listMyScheduledPosts").mockResolvedValue([]);
    vi.spyOn(api, "listMyReminders").mockResolvedValue([]);
    vi.spyOn(compatApi, "postsByIds").mockResolvedValue([]);
    vi.spyOn(compatApi, "usersByIds").mockResolvedValue([]);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("loads tasks and decisions independently and follows the task cursor", async () => {
    const list = vi.spyOn(workItemsApi, "list").mockImplementation(async (_token, options = {}) => {
      if (options.kind === "decision") return { items: [] };
      if (options.cursor === "task-next") return { items: [workItem("task-2", "두 번째 작업")] };
      return { items: [workItem("task-1", "첫 번째 작업")], next_cursor: "task-next" };
    });
    const store = configureStore({
      reducer: () => ({ auth: { token: "session-token", user: { id: "user-1" } } }),
    });

    await act(async () => root.render(
      <Provider store={store}>
        <MemoryRouter initialEntries={["/my-work/tasks"]}>
          <Routes>
            <Route path="/my-work/:tab" element={<MyWorkPage />} />
          </Routes>
        </MemoryRouter>
      </Provider>,
    ));
    await eventually(() => expect(container.textContent).toContain("첫 번째 작업"));
    expect(list.mock.calls.some((call) => call[1]?.kind === "task" && !call[1]?.cursor)).toBe(true);
    expect(list.mock.calls.some((call) => call[1]?.kind === "decision")).toBe(true);

    const more = [...container.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent?.trim() === "작업 더 불러오기");
    if (!more) throw new Error("task pagination button not found");
    await act(async () => {
      more.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await eventually(() => expect(container.textContent).toContain("두 번째 작업"));
    expect(list.mock.calls.some((call) => call[1]?.kind === "task" && call[1]?.cursor === "task-next")).toBe(true);
    expect(container.textContent?.match(/첫 번째 작업/g)).toHaveLength(1);
  });
});
