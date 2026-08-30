// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { activityApi, type ActivityEvent, type ActivityStatePatch } from "@/api/activity";
import { api, moyroMeApi, moyroReviewApi } from "@/api/client";

const workspaceHarness = vi.hoisted(() => ({
  value: {
    teams: [],
    entries: [],
    channelById: {
      "channel-1": {
        team: { id: "team-1", name: "operations", display_name: "운영", type: "O", create_at: 1 },
        channel: {
          id: "channel-1",
          team_id: "team-1",
          name: "alerts",
          display_name: "운영 알림",
          type: "O",
          create_at: 1,
        },
      },
    },
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

import { activitySource, UnifiedInboxPage } from "./UnifiedInboxPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const mentionEvent: ActivityEvent = {
  id: "event-mention",
  type: "mention",
  actor_id: "user-2",
  team_id: "team-1",
  channel_id: "channel-1",
  post_id: "post-1",
  title: "홍길동님이 나를 멘션했습니다",
  summary: "운영 점검 결과를 확인해 주세요.",
  create_at: 1_700_000_000_000,
  update_at: 1_700_000_000_000,
  read_at: 0,
  completed_at: 0,
  snoozed_until: 0,
};

const completedEvent: ActivityEvent = {
  ...mentionEvent,
  id: "event-completed",
  type: "task_assigned",
  title: "운영 보고서 작성",
  post_id: undefined,
  read_at: 1_700_000_001_000,
  completed_at: 1_700_000_002_000,
};

function findButton(label: string): HTMLButtonElement {
  const button = [...document.querySelectorAll<HTMLButtonElement>("button")]
    .find((candidate) => candidate.textContent?.trim() === label);
  if (!button) throw new Error(`button not found: ${label}`);
  return button;
}

function LocationProbe() {
  const location = useLocation();
  const state = location.state as { focusPostId?: string } | null;
  return <output aria-label="current location">{location.pathname}|{state?.focusPostId ?? ""}</output>;
}

describe("UnifiedInboxPage durable updates", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    workspaceHarness.value.activityRevision = 0;
    workspaceHarness.value.refresh.mockReset();
    vi.spyOn(moyroMeApi, "listApprovalRequests").mockResolvedValue([]);
    vi.spyOn(moyroReviewApi, "listApprovalRequests").mockResolvedValue([]);
    vi.spyOn(api, "listMyReminders").mockResolvedValue([]);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function renderPage(path = "/inbox/updates") {
    const store = configureStore({
      reducer: { auth: () => ({ token: "session-token", user: null }) },
    });
    await act(async () => {
      root.render(
        <Provider store={store}>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path="/inbox" element={<UnifiedInboxPage />} />
              <Route path="/inbox/:tab" element={<UnifiedInboxPage />} />
              <Route path="*" element={<LocationProbe />} />
            </Routes>
          </MemoryRouter>
        </Provider>,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  it("uses updates as the default tab and keeps completed items recoverable", async () => {
    const list = vi.spyOn(activityApi, "list").mockResolvedValue({
      events: [mentionEvent, completedEvent],
      next_cursor: "",
    });
    await renderPage("/inbox");

    expect(list).toHaveBeenCalledWith("session-token", { limit: 100 }, expect.any(AbortSignal));
    expect(container.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toContain("업데이트");
    expect(container.textContent).toContain("홍길동님이 나를 멘션했습니다");
    expect(container.textContent).toContain("멘션");
    expect(container.textContent).not.toContain("운영 보고서 작성");

    await act(async () => findButton("완료 1").click());
    expect(container.textContent).toContain("운영 보고서 작성");
    expect(container.textContent).toContain("완료 취소");
  });

  it("persists read, snooze, completion and bulk-read actions", async () => {
    const secondUnread = { ...mentionEvent, id: "event-reply", type: "thread_reply" as const, title: "새 답글" };
    vi.spyOn(activityApi, "list").mockResolvedValue({ events: [mentionEvent, secondUnread], next_cursor: "" });
    const patch = vi.spyOn(activityApi, "patch").mockImplementation(async (_token, id, state: ActivityStatePatch) => {
      const source = id === mentionEvent.id ? mentionEvent : secondUnread;
      const changedAt = Date.now();
      return {
        ...source,
        read_at: state.read === undefined ? source.read_at : state.read ? changedAt : 0,
        completed_at: state.completed === undefined ? source.completed_at : state.completed ? changedAt : 0,
        snoozed_until: state.snoozed_until ?? source.snoozed_until,
        update_at: changedAt,
      };
    });
    const markRead = vi.spyOn(activityApi, "markRead").mockResolvedValue({ updated: 1 });
    await renderPage();

    const firstRow = container.querySelector<HTMLElement>(".flow-activity-row");
    const readButton = [...firstRow?.querySelectorAll<HTMLButtonElement>("button") ?? []]
      .find((button) => button.textContent?.trim() === "읽음");
    await act(async () => readButton?.click());
    expect(patch).toHaveBeenCalledWith("session-token", mentionEvent.id, { read: true });

    await act(async () => findButton("모두 읽음").click());
    expect(markRead).toHaveBeenCalledWith("session-token", [secondUnread.id]);

    const currentFirstRow = container.querySelector<HTMLElement>(".flow-activity-row");
    const snoozeButton = [...currentFirstRow?.querySelectorAll<HTMLButtonElement>("button") ?? []]
      .find((button) => button.textContent?.trim() === "1시간 미루기");
    await act(async () => snoozeButton?.click());
    const snoozeCall = patch.mock.calls.at(-1);
    expect(snoozeCall?.[0]).toBe("session-token");
    expect(snoozeCall?.[1]).toBe(mentionEvent.id);
    expect(snoozeCall?.[2].snoozed_until).toBeGreaterThan(Date.now() + 59 * 60 * 1_000);

    await act(async () => findButton("미룬 항목 1").click());
    const snoozedRow = [...container.querySelectorAll<HTMLElement>(".flow-activity-row")]
      .find((row) => row.textContent?.includes(mentionEvent.title));
    const completeButton = [...snoozedRow?.querySelectorAll<HTMLButtonElement>("button") ?? []]
      .find((button) => button.textContent?.trim() === "완료");
    await act(async () => completeButton?.click());
    expect(patch.mock.calls.at(-1)?.[2]).toEqual({ completed: true, read: true, snoozed_until: 0 });
  });

  it("opens an access-indexed source message with exact post navigation state", async () => {
    vi.spyOn(activityApi, "list").mockResolvedValue({ events: [mentionEvent], next_cursor: "" });
    await renderPage();

    await act(async () => findButton("원문 메시지").click());
    expect(document.querySelector('output[aria-label="current location"]')?.textContent)
      .toBe("/workspace/team-1/channel/channel-1|post-1");
  });

  it("maps non-message events only to their supported product destination", () => {
    expect(activitySource({ ...mentionEvent, type: "approval_requested", resource_type: "approval_review" }, true))
      .toEqual({ label: "검토하기", path: "/approvals/review" });
    expect(activitySource({ ...mentionEvent, type: "approval_requested", resource_type: "approval" }, true))
      .toEqual({ label: "상태 보기", path: "/approvals/mine" });
    expect(activitySource({ ...mentionEvent, type: "task_assigned" }, true))
      .toEqual({ label: "작업 보기", path: "/my-work/tasks" });
    expect(activitySource({ ...mentionEvent, type: "plugin_event", channel_id: undefined, post_id: undefined }, false))
      .toBeNull();
  });
});
