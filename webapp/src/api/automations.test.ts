// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { automationsApi } from "./automations";

afterEach(() => vi.unstubAllGlobals());

describe("automationsApi", () => {
  it("encodes identifiers and sends revisions", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: "rule", actions: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await automationsApi.update("token", "rule/id", {
      name: "TODO", channel_id: "channel", enabled: true,
      match_type: "contains", match_value: "todo", revision: 3,
      actions: [{ type: "reminder", config: { remind_offset_minutes: 10 } }],
    });
    await automationsApi.runs("token", "rule/id", 500);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/moyro/v1/me/automation-rules/rule%2Fid");
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toMatchObject({ revision: 3 });
    expect(fetchMock.mock.calls[1][0]).toBe("/api/moyro/v1/me/automation-rules/rule%2Fid/runs?limit=100");
  });
});
