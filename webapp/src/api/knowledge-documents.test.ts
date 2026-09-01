// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { documentsApi, type DocumentRecord } from "./documents";
import { knowledgeApi } from "./knowledge";

const document: DocumentRecord = {
  id: "document/1",
  title: "운영 문서",
  body: "# 내용",
  created_by: "user-1",
  team_id: "team-1",
  channel_id: "channel-1",
  source_thread_id: "post-1",
  source_cursor_at: 100,
  revision: 2,
  create_at: 10,
  update_at: 20,
  delete_at: 0,
  stale: false,
};

afterEach(() => vi.unstubAllGlobals());

describe("knowledgeApi", () => {
  it("posts the explicit team and optional channel scope", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ sources: [], total_hits: 0 }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await knowledgeApi.search("session-token", {
      query: "장애 대응",
      team_id: "team-1",
      channel_id: "channel-1",
      limit: 20,
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/moyro/v1/me/knowledge/search");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      query: "장애 대응",
      team_id: "team-1",
      channel_id: "channel-1",
      limit: 20,
    });
  });
});

describe("documentsApi", () => {
  it("sends the source cursor and idempotency key in both supported locations", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ document, replayed: false }), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await documentsApi.create("session-token", {
      title: "운영 문서",
      body: "# 내용",
      source_post_id: "post-1",
      source_cursor_at: 100,
      idempotency_key: "request-1",
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/moyro/v1/me/documents");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("request-1");
    expect(JSON.parse(String(init.body))).toMatchObject({
      source_post_id: "post-1",
      source_cursor_at: 100,
      idempotency_key: "request-1",
    });
  });

  it("encodes identifiers and includes the optimistic revision on mutation", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(document), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await documentsApi.patch("session-token", "document/1", {
      body: "# 최신 내용",
      source_cursor_at: 200,
      expected_revision: 2,
    });
    await documentsApi.remove("session-token", "document/1", 3);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/moyro/v1/me/documents/document%2F1");
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      body: "# 최신 내용",
      source_cursor_at: 200,
      expected_revision: 2,
    });
    expect(fetchMock.mock.calls[1][0]).toBe("/api/moyro/v1/me/documents/document%2F1?revision=3");
  });
});
