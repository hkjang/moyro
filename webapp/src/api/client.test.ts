// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("SSO session exchange", () => {
  it("posts the browser-bound code without sending it as a bearer credential", async () => {
    const response = {
      user: { id: "user-1", username: "sso-user", email: "sso@example.test" },
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(response), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.exchangeSSOCode("one-time-code")).resolves.toEqual({
      token: "__moyro_browser_session__",
      user: response.user,
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/moyro/v1/auth/sso/session");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Authorization")).toBeNull();
    expect(init.credentials).toBe("same-origin");
    expect(JSON.parse(String(init.body))).toEqual({ code: "one-time-code" });
  });
});

describe("browser password login", () => {
  it("uses the cookie-only native endpoint", async () => {
    const response = {
      user: { id: "user-2", username: "local-user", email: "local@example.test" },
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(response), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.login("local-user", "password")).resolves.toEqual({
      token: "__moyro_browser_session__",
      user: response.user,
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/moyro/v1/auth/session/login");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("same-origin");
    expect(new Headers(init.headers).get("Authorization")).toBeNull();
    expect(JSON.parse(String(init.body))).toEqual({ login_id: "local-user", password: "password" });
  });
});
