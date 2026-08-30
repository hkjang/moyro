// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { describe, expect, it } from "vitest";
import { afterEach, beforeEach, vi } from "vitest";
import { flowApi, type FlowSummary } from "@/api/flow";

const websocketHarness = vi.hoisted(() => ({
  onMessage: null as ((message: MessageEvent) => void) | null,
  reconnectSeq: 0,
}));

vi.mock("@/hooks/useWebsocket", () => ({
  useWebsocket: (_token: string | null, onMessage: (message: MessageEvent) => void) => {
    websocketHarness.onMessage = onMessage;
    return {
      status: "connected" as const,
      attempts: 0,
      reconnectSeq: websocketHarness.reconnectSeq,
      send: () => undefined,
    };
  },
}));

import {
  entriesFromFlowSummary,
  FlowWorkspaceProvider,
  shouldRefreshActivityEvents,
  shouldRefreshFlowSummary,
  shouldRefreshWorkItems,
  useFlowWorkspaceIndex,
} from "./FlowDataProvider";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function summaryFixture(): FlowSummary {
  return {
    updated_at: 1,
    counts: { unread_channels: 1, mentions: 2 },
    teams: [{ id: "team-1", name: "ops", display_name: "운영", type: "O", create_at: 1 }],
    channels: [
      { id: "channel-1", team_id: "team-1", type: "O", name: "alerts", display_name: "알림", create_at: 1 },
      { id: "orphan", team_id: "missing", type: "O", name: "hidden", display_name: "숨김", create_at: 1 },
    ],
    memberships: [{
      channel_id: "channel-1",
      user_id: "user-1",
      roles: "channel_user",
      last_viewed_at: 1,
      msg_count: 3,
      mention_count: 2,
      notify_props: {},
    }],
    top_unread_channels: [],
  };
}

function ActivityRevisionProbe() {
  const workspace = useFlowWorkspaceIndex();
  return (
    <>
      <output aria-label="activity revision">{workspace.activityRevision}</output>
      <output aria-label="work item revision">{workspace.workItemRevision}</output>
    </>
  );
}

describe("FlowDataProvider", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    websocketHarness.onMessage = null;
    websocketHarness.reconnectSeq = 0;
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("joins the single read model without leaking orphan channels", () => {
    const entries = entriesFromFlowSummary(summaryFixture());
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({
      channel: { id: "channel-1" },
      team: { id: "team-1" },
      membership: { msg_count: 3, mention_count: 2 },
    });
  });

  it("refreshes only for events that can change the shared workspace summary", () => {
    for (const event of [
      "posted",
      "unread_updated",
      "channel_viewed",
      "channel_member_updated",
      "team_updated",
    ]) {
      expect(shouldRefreshFlowSummary(event), event).toBe(true);
    }
    for (const event of ["typing", "status_change", "reaction_added", "reminder_created", undefined, 1]) {
      expect(shouldRefreshFlowSummary(event), String(event)).toBe(false);
    }
  });

  it("invalidates the durable activity feed without reloading the workspace summary", async () => {
    expect(shouldRefreshActivityEvents("activity_event")).toBe(true);
    expect(shouldRefreshActivityEvents("activity_state_changed")).toBe(true);
    expect(shouldRefreshActivityEvents("posted")).toBe(false);
    expect(shouldRefreshActivityEvents(undefined)).toBe(false);

    const getSummary = vi.spyOn(flowApi, "getSummary").mockResolvedValue(summaryFixture());
    await act(async () => {
      root.render(
        <FlowWorkspaceProvider token="session-token">
          <ActivityRevisionProbe />
        </FlowWorkspaceProvider>,
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.querySelector("output")?.textContent).toBe("0");

    await act(async () => {
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "activity_event" }) }));
    });
    expect(container.querySelector("output")?.textContent).toBe("1");

    await act(async () => {
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "activity_state_changed" }) }));
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "typing" }) }));
    });
    expect(container.querySelector("output")?.textContent).toBe("2");
    expect(getSummary).toHaveBeenCalledOnce();
  });

  it("invalidates work items for scoped websocket changes", async () => {
    expect(shouldRefreshWorkItems("work_item_changed")).toBe(true);
    expect(shouldRefreshWorkItems("work_item_deleted")).toBe(true);
    expect(shouldRefreshWorkItems("activity_event")).toBe(false);
    expect(shouldRefreshWorkItems(undefined)).toBe(false);

    vi.spyOn(flowApi, "getSummary").mockResolvedValue(summaryFixture());
    await act(async () => {
      root.render(
        <FlowWorkspaceProvider token="session-token">
          <ActivityRevisionProbe />
        </FlowWorkspaceProvider>,
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.querySelector('[aria-label="work item revision"]')?.textContent).toBe("0");

    await act(async () => {
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "work_item_changed" }) }));
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "typing" }) }));
    });
    expect(container.querySelector('[aria-label="work item revision"]')?.textContent).toBe("1");
  });

  it("keeps the route-scoped cache and coalesces WebSocket invalidations", async () => {
    const getSummary = vi.spyOn(flowApi, "getSummary").mockResolvedValue(summaryFixture());

    await act(async () => {
      root.render(<FlowWorkspaceProvider token="session-token"><div>오늘</div></FlowWorkspaceProvider>);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getSummary).toHaveBeenCalledOnce();

    // Changing only the matched child route keeps the provider mounted and
    // therefore reuses the same summary rather than issuing another request.
    await act(async () => {
      root.render(<FlowWorkspaceProvider token="session-token"><div>알림함</div></FlowWorkspaceProvider>);
    });
    expect(getSummary).toHaveBeenCalledOnce();

    await act(async () => {
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "posted" }) }));
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "unread_updated" }) }));
      await new Promise((resolve) => window.setTimeout(resolve, 300));
      await Promise.resolve();
    });
    expect(getSummary).toHaveBeenCalledTimes(2);

    await act(async () => {
      websocketHarness.onMessage?.(new MessageEvent("message", { data: JSON.stringify({ event: "typing" }) }));
      await new Promise((resolve) => window.setTimeout(resolve, 300));
    });
    expect(getSummary).toHaveBeenCalledTimes(2);

  });
});
