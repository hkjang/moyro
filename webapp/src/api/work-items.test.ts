// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { workItemsApi, type WorkItem } from "./work-items";

const item: WorkItem = {
  id: "work-1",
  kind: "task",
  title: "배포 확인",
  description: "",
  status: "open",
  created_by: "user-1",
  assignee_id: "user-1",
  channel_id: "channel-1",
  source_post_id: "post-1",
  due_at: 0,
  decided_at: 0,
  priority: "normal",
  completed_at: 0,
  recurrence_unit: "none",
  recurrence_interval: 0,
  occurrence_no: 0,
  dependency_ids: [],
  impact_task_ids: [],
  create_at: 1,
  update_at: 1,
  delete_at: 0,
};

afterEach(() => vi.unstubAllGlobals());

describe("workItemsApi", () => {
  it("sends the idempotency key in the standard header and request body", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ item, replayed: false }), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await workItemsApi.create("session-token", {
      kind: "task",
      title: "배포 확인",
      source_post_id: "post-1",
      idempotency_key: "request-1",
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/moyro/v1/me/work-items");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("request-1");
    expect(JSON.parse(String(init.body))).toMatchObject({
      kind: "task",
      source_post_id: "post-1",
      idempotency_key: "request-1",
    });
  });

  it("encodes cursor filters and handles a no-content delete", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [], next_cursor: "" }), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await workItemsApi.list("session-token", {
      kind: "decision",
      cursor: "cursor+/=value",
      perPage: 100,
    });
    await workItemsApi.remove("session-token", "work/id");

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/moyro/v1/me/work-items?kind=decision&cursor=cursor%2B%2F%3Dvalue&per_page=100",
    );
    expect(fetchMock.mock.calls[1][0]).toBe("/api/moyro/v1/me/work-items/work%2Fid");
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("encodes calendar filters and relation endpoints", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [], next_cursor: "" }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(item), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await workItemsApi.list("session-token", {
      kind: "task", dueFrom: 100, dueTo: 200, sort: "due",
    });
    await workItemsApi.addDependency("session-token", "work/id", "dependency/id");
    await workItemsApi.events("session-token", "work/id");

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/moyro/v1/me/work-items?kind=task&due_from=100&due_to=200&sort=due",
    );
    expect(fetchMock.mock.calls[1][0]).toBe(
      "/api/moyro/v1/me/work-items/work%2Fid/dependencies/dependency%2Fid",
    );
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe("POST");
    expect(fetchMock.mock.calls[2][0]).toBe("/api/moyro/v1/me/work-items/work%2Fid/events");
  });
});
