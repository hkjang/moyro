// @vitest-environment jsdom
import { StrictMode, act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { authReducer } from "@/store/authSlice";
import { APIError } from "@/api/transport";

const mocks = vi.hoisted(() => ({
  exchangeSSOCode: vi.fn(),
  me: vi.fn(),
  adoptBrowserSession: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    exchangeSSOCode: mocks.exchangeSSOCode,
    me: mocks.me,
    adoptBrowserSession: mocks.adoptBrowserSession,
  },
}));
vi.mock("@/app/AppRouter", () => ({ AppRouter: () => <div>app router</div> }));
vi.mock("@/features/system/SystemInfoContext", () => ({
  useSystemInfo: () => ({ capabilities: { drafts: { clear_on_logout: true } } }),
}));
vi.mock("@/features/workspace/composer/useDraft", () => ({
  clearMoyroDraftsForUser: vi.fn(),
}));

import { App, exchangeSSOCodeWithRetry } from "./App";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("App SSO callback", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    window.sessionStorage.clear();
    window.history.replaceState(null, "", "/workspace/team-a#sso_code=one-time-code");
    mocks.exchangeSSOCode.mockReset().mockResolvedValue({
    token: "__moyro_browser_session__",
      user: { id: "user-1", username: "sso-user", email: "sso@example.test" },
    });
    mocks.me.mockReset();
    mocks.adoptBrowserSession.mockReset();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    window.sessionStorage.clear();
    vi.restoreAllMocks();
  });

  it("exchanges the callback once and never probes /users/me", async () => {
    const store = configureStore({ reducer: { auth: authReducer } });

    await act(async () => {
      root.render(
        <StrictMode>
          <Provider store={store}>
            <App />
          </Provider>
        </StrictMode>,
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.exchangeSSOCode).toHaveBeenCalledTimes(1);
    expect(mocks.exchangeSSOCode).toHaveBeenCalledWith("one-time-code");
    expect(mocks.me).not.toHaveBeenCalled();
    expect(store.getState().auth).toMatchObject({
      token: "__moyro_browser_session__",
      user: { id: "user-1" },
    });
    expect(window.location.pathname).toBe("/workspace/team-a");
    expect(window.location.hash).toBe("");
  });

  it("clears stale local auth and surfaces a retryable error when exchange fails", async () => {
    mocks.exchangeSSOCode.mockRejectedValueOnce(new APIError(401, "expired"));
    const store = configureStore({
      reducer: { auth: authReducer },
      preloadedState: {
        auth: {
          token: "stale-session-token",
          user: { id: "old-user", username: "old", email: "old@example.test" },
        },
      },
    });

    await act(async () => {
      root.render(
        <StrictMode>
          <Provider store={store}>
            <App />
          </Provider>
        </StrictMode>,
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.exchangeSSOCode).toHaveBeenCalledTimes(1);
    expect(store.getState().auth).toEqual({ token: null, user: null });
    expect(window.location.pathname).toBe("/login");
    expect(window.location.hash).toBe("#oauth_error=sso_restart_required");
  });

  it("retries a transient exchange failure without starting a redirect loop", async () => {
    vi.useFakeTimers();
    mocks.exchangeSSOCode
      .mockRejectedValueOnce(new TypeError("network"))
      .mockResolvedValueOnce({
        token: "__moyro_browser_session__",
        user: { id: "user-1", username: "sso-user", email: "sso@example.test" },
      });
    const session = exchangeSSOCodeWithRetry("one-time-code");
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(150);
    await expect(session).resolves.toMatchObject({ token: "__moyro_browser_session__" });
    expect(mocks.exchangeSSOCode).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });
});
