// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { APIError, DEFAULT_TIMEOUT_MS, UPLOAD_TIMEOUT_MS, compatRequest } from "./transport";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("request deadlines", () => {
  it("aborts a request that outlives the default deadline", async () => {
    vi.useFakeTimers();
    // A fetch that never settles is exactly the dropped-connection case: the
    // deadline is the only thing that can end it.
    const fetchMock = vi.fn(
      (_url: string, init: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const pending = compatRequest(null, "/hangs").catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(DEFAULT_TIMEOUT_MS + 1);

    const error = await pending;
    expect(error).toBeInstanceOf(APIError);
    expect((error as APIError).kind).toBe("timeout");
    expect((error as APIError).status).toBe(0);
  });

  it("gives uploads a longer deadline than ordinary requests", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(
      (_url: string, init: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          init.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
          setTimeout(() => resolve(jsonResponse({ ok: true })), DEFAULT_TIMEOUT_MS + 1_000);
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const body = new FormData();
    body.append("files", new Blob(["payload"]), "big.bin");
    const pending = compatRequest(null, "/files", { method: "POST", body });
    await vi.advanceTimersByTimeAsync(DEFAULT_TIMEOUT_MS + 1_000);

    await expect(pending).resolves.toEqual({ ok: true });
    expect(UPLOAD_TIMEOUT_MS).toBeGreaterThan(DEFAULT_TIMEOUT_MS);
  });

  it("honours a caller-supplied signal and reports the abort as intentional", async () => {
    const controller = new AbortController();
    const fetchMock = vi.fn(
      (_url: string, init: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const pending = compatRequest(null, "/slow", { signal: controller.signal }).catch(
      (error: unknown) => error,
    );
    controller.abort();

    const error = await pending;
    expect((error as APIError).kind).toBe("aborted");
    // An intentional abort must never be replayed.
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("bounded retry", () => {
  it("retries an idempotent read through a transient gateway failure", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ message: "bad gateway" }, 502))
      .mockResolvedValueOnce(jsonResponse({ id: "channel-1" }));
    vi.stubGlobal("fetch", fetchMock);

    const pending = compatRequest(null, "/channels/channel-1");
    await vi.advanceTimersByTimeAsync(1_000);

    await expect(pending).resolves.toEqual({ id: "channel-1" });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("never replays a write, because a retried POST could act twice", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ message: "bad gateway" }, 502));
    vi.stubGlobal("fetch", fetchMock);

    const error = await compatRequest(null, "/posts", { method: "POST", body: { message: "hi" } })
      .catch((e: unknown) => e);

    expect((error as APIError).status).toBe(502);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not retry a decision the server already made", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ message: "forbidden" }, 403));
    vi.stubGlobal("fetch", fetchMock);

    const error = await compatRequest(null, "/channels/secret").catch((e: unknown) => e);

    expect((error as APIError).status).toBe(403);
    expect((error as APIError).message).toBe("forbidden");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("gives up after a bounded number of attempts", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", fetchMock);

    const pending = compatRequest(null, "/channels").catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(5_000);

    const error = await pending;
    expect((error as APIError).kind).toBe("network");
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });
});

describe("existing transport contract", () => {
  it("still sends JSON bodies with a bearer token and same-origin credentials", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await compatRequest("token-1", "/posts", { method: "POST", body: { message: "hi" } });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v4/posts");
    expect(init.credentials).toBe("same-origin");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer token-1");
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
    expect(JSON.parse(String(init.body))).toEqual({ message: "hi" });
  });

  it("still returns undefined for a 204 and never sets a JSON content type on uploads", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(jsonResponse({ ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(compatRequest(null, "/posts/p1", { method: "DELETE" })).resolves.toBeUndefined();

    const body = new FormData();
    body.append("files", new Blob(["x"]), "x.txt");
    await compatRequest(null, "/files", { method: "POST", body });
    const [, uploadInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(new Headers(uploadInit.headers).get("Content-Type")).toBeNull();
    expect(uploadInit.body).toBe(body);
  });
});
