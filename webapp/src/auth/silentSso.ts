// Silent SSO: when the identity provider still has a session, sign the visitor
// in with prompt=none instead of showing the login screen. prompt=none never
// draws a page — the provider either returns a code at once or comes back with
// error=login_required, which the server turns into /login?sso=none.
//
// Everything here exists to make that attempt happen at most once. Retrying
// after a refusal bounces the browser between the provider and the app
// forever, so the guards fail closed: if a check cannot be made, do not try.

import type { SystemInfo } from "@/api/client";

// sessionStorage, not localStorage: the attempt is scoped to this tab's
// browsing session. A fresh tab tries again; a reload after a refusal does not.
export const SILENT_SSO_ATTEMPTED_KEY = "moyro.sso.silentAttempted";
export const SILENT_SSO_SIGNED_OUT_KEY = "moyro.sso.signedOut";

/** Query marker the callback appends to /login when the provider had no session. */
export const SILENT_SSO_REFUSED_PARAM = "sso";

const LOGIN_PATH = "/login";
// Browser navigations only. These mirror the server's reserved (non-SPA)
// routes — API, webhook, MCP, health, metrics — plus the OIDC callback that
// lives under /api. None of them is ever a place to start a redirect from.
const NEVER_ATTEMPT_PREFIXES = ["/api", "/hooks", "/mcp", "/healthz", "/metrics"];

function reservedPath(pathname: string): boolean {
  return NEVER_ATTEMPT_PREFIXES.some((prefix) => pathname === prefix || pathname.startsWith(prefix + "/"));
}

function readFlag(key: string): boolean {
  try {
    return window.sessionStorage.getItem(key) === "true";
  } catch {
    // Private modes and blocked site data throw here. Reading that as "not
    // yet attempted" would start the loop, so it counts as attempted.
    return true;
  }
}

function writeFlag(key: string, value: boolean) {
  try {
    if (value) window.sessionStorage.setItem(key, "true");
    else window.sessionStorage.removeItem(key);
  } catch {
    /* readFlag already fails closed when storage is unavailable */
  }
}

/** Records a deliberate sign-out so the next visit is not silently re-signed in. */
export function markSignedOut() {
  writeFlag(SILENT_SSO_SIGNED_OUT_KEY, true);
  writeFlag(SILENT_SSO_ATTEMPTED_KEY, true);
}

/** Lifts the sign-out suppression once a session exists again. */
export function clearSilentSsoState() {
  writeFlag(SILENT_SSO_SIGNED_OUT_KEY, false);
  writeFlag(SILENT_SSO_ATTEMPTED_KEY, false);
}

export function silentSsoAttempted(): boolean {
  return readFlag(SILENT_SSO_ATTEMPTED_KEY);
}

/** Only same-origin absolute paths may be restored after a silent login. */
export function safeReturnTo(value: string | null | undefined): string {
  if (!value || !value.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) return "/";
  return value;
}

export type SilentSsoLocation = Pick<Location, "pathname" | "search" | "hash">;

/**
 * Decides whether to try signing in without showing the login screen. Must
 * never answer true twice in one browsing session; every reason to stop is
 * checked before the storage flag is consulted so a thrown storage access
 * still ends in "no".
 */
export function shouldAttemptSilentSso(
  info: Pick<SystemInfo, "oidc_enabled" | "oidc_auto_login">,
  location: SilentSsoLocation,
): boolean {
  if (info.oidc_enabled !== true || info.oidc_auto_login !== true) return false;
  const { pathname, search, hash } = location;
  if (pathname === LOGIN_PATH || pathname.startsWith(LOGIN_PATH + "/")) return false;
  if (reservedPath(pathname)) return false;
  // Invite, error and callback fragments already belong to another flow.
  if (hash.startsWith("#invite=") || hash.startsWith("#oauth_error=") || hash.startsWith("#sso_code=")) return false;
  // The server leaves this marker in the address after a refusal, so the
  // decision survives a cleared sessionStorage.
  const refused = new URLSearchParams(search).get(SILENT_SSO_REFUSED_PARAM);
  if (refused === "none" || refused === "error") return false;
  if (readFlag(SILENT_SSO_SIGNED_OUT_KEY)) return false;
  if (readFlag(SILENT_SSO_ATTEMPTED_KEY)) return false;
  return true;
}

/** Builds the top-level navigation target for a prompt=none attempt. */
export function silentSsoLoginURL(returnTo: string): string {
  return `/api/moyro/v1/auth/oidc/login?prompt=none&return_to=${encodeURIComponent(safeReturnTo(returnTo))}`;
}

/**
 * Marks the attempt before navigating so a provider that answers unusually
 * fast, or a back-navigation, cannot observe an unmarked state. Top-level
 * navigation (not a hidden iframe) works with third-party cookies blocked and
 * does not depend on the provider allowing frames.
 */
export function beginSilentSso(returnTo: string) {
  writeFlag(SILENT_SSO_ATTEMPTED_KEY, true);
  window.location.assign(silentSsoLoginURL(returnTo));
}
