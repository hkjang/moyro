// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  SILENT_SSO_ATTEMPTED_KEY,
  SILENT_SSO_SIGNED_OUT_KEY,
  beginSilentSso,
  clearSilentSsoState,
  markSignedOut,
  safeReturnTo,
  shouldAttemptSilentSso,
  silentSsoAttempted,
  silentSsoLoginURL,
} from "./silentSso";

const ON = { oidc_enabled: true, oidc_auto_login: true };
const at = (pathname: string, search = "", hash = "") => ({ pathname, search, hash });

describe("shouldAttemptSilentSso", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
  });

  afterEach(() => {
    window.sessionStorage.clear();
    vi.restoreAllMocks();
  });

  it("tries once on an ordinary page when the administrator turned auto_login on", () => {
    expect(shouldAttemptSilentSso(ON, at("/today"))).toBe(true);
    expect(shouldAttemptSilentSso(ON, at("/workspace/team-a/channel-b", "?thread=1"))).toBe(true);
    expect(shouldAttemptSilentSso(ON, at("/"))).toBe(true);
  });

  it("does nothing while auto_login is off, which is the default", () => {
    expect(shouldAttemptSilentSso({ oidc_enabled: true, oidc_auto_login: false }, at("/today"))).toBe(false);
    expect(shouldAttemptSilentSso({ oidc_enabled: true }, at("/today"))).toBe(false);
    expect(shouldAttemptSilentSso({ oidc_enabled: false, oidc_auto_login: true }, at("/today"))).toBe(false);
    expect(shouldAttemptSilentSso({}, at("/today"))).toBe(false);
  });

  it("never starts from the login, callback, API, MCP or health paths", () => {
    for (const pathname of [
      "/login",
      "/login/",
      "/api/moyro/v1/auth/oidc/callback",
      "/api",
      "/api/v4/users/me",
      "/hooks/abc",
      "/mcp",
      "/mcp/stream",
      "/healthz",
      "/metrics",
    ]) {
      expect(shouldAttemptSilentSso(ON, at(pathname)), pathname).toBe(false);
    }
    // Only a whole path segment counts as reserved.
    expect(shouldAttemptSilentSso(ON, at("/apiary"))).toBe(true);
    expect(shouldAttemptSilentSso(ON, at("/logins"))).toBe(true);
  });

  it("leaves invite, error and callback fragments to their own flows", () => {
    expect(shouldAttemptSilentSso(ON, at("/today", "", "#invite=abc"))).toBe(false);
    expect(shouldAttemptSilentSso(ON, at("/today", "", "#oauth_error=state_mismatch"))).toBe(false);
    expect(shouldAttemptSilentSso(ON, at("/today", "", "#sso_code=one-time"))).toBe(false);
  });

  it("honours the refusal marker the callback leaves in the address even with empty storage", () => {
    expect(shouldAttemptSilentSso(ON, at("/today", "?sso=none"))).toBe(false);
    expect(shouldAttemptSilentSso(ON, at("/today", "?return_to=%2Fx&sso=none"))).toBe(false);
    expect(shouldAttemptSilentSso(ON, at("/today", "?sso=error"))).toBe(false);
    expect(shouldAttemptSilentSso(ON, at("/today", "?sso=other"))).toBe(true);
  });

  it("tries at most once per tab session", () => {
    expect(shouldAttemptSilentSso(ON, at("/today"))).toBe(true);
    const assign = vi.fn();
    vi.spyOn(window, "location", "get").mockReturnValue({ ...window.location, assign } as unknown as Location);
    beginSilentSso("/today");
    expect(assign).toHaveBeenCalledTimes(1);
    expect(silentSsoAttempted()).toBe(true);
    expect(shouldAttemptSilentSso(ON, at("/today"))).toBe(false);
    expect(shouldAttemptSilentSso(ON, at("/workspace"))).toBe(false);
  });

  it("stays quiet after a deliberate sign-out until a session exists again", () => {
    markSignedOut();
    expect(window.sessionStorage.getItem(SILENT_SSO_SIGNED_OUT_KEY)).toBe("true");
    expect(window.sessionStorage.getItem(SILENT_SSO_ATTEMPTED_KEY)).toBe("true");
    expect(shouldAttemptSilentSso(ON, at("/today"))).toBe(false);

    clearSilentSsoState();
    expect(window.sessionStorage.getItem(SILENT_SSO_SIGNED_OUT_KEY)).toBeNull();
    expect(window.sessionStorage.getItem(SILENT_SSO_ATTEMPTED_KEY)).toBeNull();
    expect(shouldAttemptSilentSso(ON, at("/today"))).toBe(true);
  });

  it("treats unreadable storage as already attempted", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    expect(shouldAttemptSilentSso(ON, at("/today"))).toBe(false);
    expect(silentSsoAttempted()).toBe(true);
  });

  it("does not throw when storage cannot be written", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("quota", "QuotaExceededError");
    });
    expect(() => markSignedOut()).not.toThrow();
  });
});

describe("silent SSO navigation", () => {
  it("only restores same-origin absolute paths", () => {
    expect(safeReturnTo("/workspace/team-a?x=1")).toBe("/workspace/team-a?x=1");
    expect(safeReturnTo("/")).toBe("/");
    for (const unsafe of ["", null, undefined, "//attacker.example/", "/\\attacker.example", "https://attacker.example/", "today"]) {
      expect(safeReturnTo(unsafe), String(unsafe)).toBe("/");
    }
  });

  it("asks the server for prompt=none and carries the deep link", () => {
    expect(silentSsoLoginURL("/workspace/team-a?x=1")).toBe(
      "/api/moyro/v1/auth/oidc/login?prompt=none&return_to=%2Fworkspace%2Fteam-a%3Fx%3D1",
    );
    expect(silentSsoLoginURL("//attacker.example/")).toBe("/api/moyro/v1/auth/oidc/login?prompt=none&return_to=%2F");
  });
});
