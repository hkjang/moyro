// @vitest-environment jsdom
import { configureStore } from "@reduxjs/toolkit";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api, compatApi, moyroMeApi, moyroReviewApi } from "@/api/client";

const workspaceHarness = vi.hoisted(() => ({
  value: {
    teams: [],
    entries: [],
    channelById: {},
    loading: false,
    error: "",
    warnings: [],
    activityRevision: 0,
    workItemRevision: 0,
    refresh: vi.fn(),
  },
}));

vi.mock("./FlowDataProvider", () => ({
  useFlowWorkspaceIndex: () => workspaceHarness.value,
}));

vi.mock("./TodayBriefing", () => ({
  TodayBriefing: () => <div data-testid="today-briefing" />,
}));

import { ApprovalCenterPage } from "./ApprovalCenterPage";
import { TodayPage } from "./TodayPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

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

describe("Flow revision consumers", () => {
  let container: HTMLDivElement;
  let root: Root;
  let store: ReturnType<typeof configureStore>;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    store = configureStore({
      reducer: () => ({ auth: { token: "session-token", user: { id: "user-1", username: "tester" } } }),
    });
    workspaceHarness.value.activityRevision = 0;
    workspaceHarness.value.workItemRevision = 0;
    workspaceHarness.value.refresh.mockReset();
    vi.spyOn(api, "listSavedPosts").mockResolvedValue({ order: [], posts: {} });
    vi.spyOn(api, "listMyScheduledPosts").mockResolvedValue([]);
    vi.spyOn(api, "listMyReminders").mockResolvedValue([]);
    vi.spyOn(moyroMeApi, "listApprovalRequests").mockResolvedValue([]);
    vi.spyOn(moyroReviewApi, "listApprovalRequests").mockResolvedValue([]);
    vi.spyOn(compatApi, "usersByIds").mockResolvedValue([]);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function render(element: ReactElement, path: string, routePath: string) {
    await act(async () => {
      root.render(
        <Provider store={store}>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path={routePath} element={element} />
            </Routes>
          </MemoryRouter>
        </Provider>,
      );
      await Promise.resolve();
    });
  }

  it("reloads Today lists immediately for activity and work-item revisions", async () => {
    const saved = vi.mocked(api.listSavedPosts);
    const scheduled = vi.mocked(api.listMyScheduledPosts);
    const reminders = vi.mocked(api.listMyReminders);
    const mine = vi.mocked(moyroMeApi.listApprovalRequests);
    const review = vi.mocked(moyroReviewApi.listApprovalRequests);
    await render(<TodayPage />, "/today", "/today");
    await eventually(() => expect(review).toHaveBeenCalledTimes(1));

    workspaceHarness.value.activityRevision += 1;
    await render(<TodayPage />, "/today", "/today");
    await eventually(() => expect(review).toHaveBeenCalledTimes(2));

    workspaceHarness.value.workItemRevision += 1;
    await render(<TodayPage />, "/today", "/today");
    await eventually(() => expect(review).toHaveBeenCalledTimes(3));

    for (const request of [saved, scheduled, reminders, mine]) {
      expect(request).toHaveBeenCalledTimes(3);
    }
  });

  it("reloads Approval Center for activity revisions without unrelated work-item churn", async () => {
    const mine = vi.mocked(moyroMeApi.listApprovalRequests);
    const review = vi.mocked(moyroReviewApi.listApprovalRequests);
    await render(<ApprovalCenterPage initialTab="mine" />, "/approvals/mine", "/approvals/:tab");
    await eventually(() => expect(review).toHaveBeenCalledTimes(1));

    workspaceHarness.value.activityRevision += 1;
    await render(<ApprovalCenterPage initialTab="mine" />, "/approvals/mine", "/approvals/:tab");
    await eventually(() => expect(review).toHaveBeenCalledTimes(2));
    expect(mine).toHaveBeenCalledTimes(2);

    workspaceHarness.value.workItemRevision += 1;
    await render(<ApprovalCenterPage initialTab="mine" />, "/approvals/mine", "/approvals/:tab");
    await act(async () => Promise.resolve());
    expect(review).toHaveBeenCalledTimes(2);
    expect(mine).toHaveBeenCalledTimes(2);
  });
});
