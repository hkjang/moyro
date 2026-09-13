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
  systemInfo: { capabilities: { drafts: { clear_on_logout: true } } } as Record<string, unknown>,
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
  useSystemInfo: () => mocks.systemInfo,
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

describe("App silent SSO", () => {
  let container: HTMLDivElement;
  let root: Root;
  let assign: ReturnType<typeof vi.fn>;

  function renderApp(preloadedToken: string | null = null) {
    const store = configureStore({
      reducer: { auth: authReducer },
      preloadedState: preloadedToken
        ? { auth: { token: preloadedToken, user: { id: "user-1", username: "u", email: "u@example.test" } } }
        : undefined,
    });
    // jsdom does not implement navigation; swap in a snapshot of the real
    // location whose assign() we can observe.
    vi.spyOn(window, "location", "get").mockReturnValue({ ...window.location, assign } as unknown as Location);
    return act(async () => {
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
  }

  beforeEach(() => {
    window.sessionStorage.clear();
    window.history.replaceState(null, "", "/workspace/team-a?thread=1");
    mocks.systemInfo = {
      loaded: true,
      oidc_enabled: true,
      oidc_auto_login: true,
      capabilities: { drafts: { clear_on_logout: true } },
    };
    mocks.me.mockReset();
    mocks.adoptBrowserSession.mockReset();
    assign = vi.fn();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    window.sessionStorage.clear();
    mocks.systemInfo = { capabilities: { drafts: { clear_on_logout: true } } };
    vi.restoreAllMocks();
  });

  it("leaves for a prompt=none login exactly once and keeps the deep link", async () => {
    await renderApp();

    expect(assign).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledWith(
      "/api/moyro/v1/auth/oidc/login?prompt=none&return_to=%2Fworkspace%2Fteam-a%3Fthread%3D1",
    );
    expect(container.textContent).toContain("로그인 중");
    expect(container.textContent).not.toContain("app router");
    expect(window.sessionStorage.getItem("moyro.sso.silentAttempted")).toBe("true");
  });

  it("shows the login screen without retrying after the callback reported no session", async () => {
    window.history.replaceState(null, "", "/login?sso=none&return_to=%2Fworkspace%2Fteam-a");
    await renderApp();

    expect(assign).not.toHaveBeenCalled();
    expect(container.textContent).toContain("app router");
  });

  it("does not try while auto_login is off, even with Keycloak enabled", async () => {
    mocks.systemInfo = { ...mocks.systemInfo, oidc_auto_login: false };
    await renderApp();

    expect(assign).not.toHaveBeenCalled();
    expect(container.textContent).toContain("app router");
  });

  it("does not try after a deliberate sign-out", async () => {
    window.sessionStorage.setItem("moyro.sso.signedOut", "true");
    await renderApp();

    expect(assign).not.toHaveBeenCalled();
    expect(container.textContent).toContain("app router");
  });

  it("does not try before the server has said whether auto_login is on", async () => {
    mocks.systemInfo = { ...mocks.systemInfo, loaded: false };
    await renderApp();

    expect(assign).not.toHaveBeenCalled();
  });

  it("does not try when a session is already being restored and lifts the sign-out flag once it is", async () => {
    window.sessionStorage.setItem("moyro.sso.signedOut", "true");
    mocks.me.mockResolvedValue({ id: "user-1", username: "u", email: "u@example.test" });
    await renderApp("__moyro_browser_session__");

    expect(assign).not.toHaveBeenCalled();
    expect(container.textContent).toContain("app router");
    expect(window.sessionStorage.getItem("moyro.sso.signedOut")).toBeNull();
  });
});
