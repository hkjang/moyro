import { BROWSER_SESSION_TOKEN } from "@/api/transport";

type TokenProvider = () => string | null;

function inputURL(input: RequestInfo | URL, origin: string): URL | null {
  try {
    const raw = input instanceof Request ? input.url : input.toString();
    return new URL(raw, origin);
  } catch {
    return null;
  }
}

function isSameOriginAuthenticatedURL(url: URL, origin: string): boolean {
  if (url.origin !== origin || url.username || url.password) return false;
  if (url.protocol !== "http:" && url.protocol !== "https:") return false;
  return /^\/plugins\/[^/]+(?:\/|$)/.test(url.pathname) || /^\/api\/v4(?:\/|$)/.test(url.pathname);
}

/**
 * Adds Moyro's current bearer credential only to same-origin plugin routes
 * and Mattermost-compatible /api/v4 calls made by bundled mattermost-redux.
 * Caller-supplied Authorization is replaced (or removed while logged out), and
 * redirects are rejected so the credential cannot cross an origin boundary.
 */
export function createAuthenticatedPluginFetch(
  upstream: typeof fetch,
  getToken: TokenProvider,
  origin: string,
): typeof fetch {
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = inputURL(input, origin);
    if (!url || !isSameOriginAuthenticatedURL(url, origin)) return upstream(input, init);

    const request = new Request(input instanceof Request ? input : url.href, init);
    const headers = new Headers(request.headers);
    // Mattermost plugins sometimes set this legacy header themselves. Moyro
    // derives actor identity from the verified bearer and never forwards a
    // caller-controlled user id to a plugin or /api/v4 handler.
    headers.delete("Mattermost-User-ID");
    const token = getToken();
    if (token && token !== BROWSER_SESSION_TOKEN) headers.set("Authorization", `Bearer ${token}`);
    else headers.delete("Authorization");

    return upstream(new Request(request, { headers, redirect: "error" }));
  };
}

export function resolvePluginBundleURL(id: string, source: string, origin: string): string {
  if (!/^[A-Za-z0-9._-]+$/.test(id)) throw new Error("invalid plugin id");
  let url: URL;
  try {
    url = new URL(source, origin);
  } catch {
    throw new Error(`invalid plugin bundle URL for ${id}`);
  }
  const expectedPrefix = `/plugins/${encodeURIComponent(id)}/`;
  if (
    url.origin !== origin ||
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.username ||
    url.password ||
    url.hash ||
    !url.pathname.startsWith(expectedPrefix)
  ) {
    throw new Error(`plugin bundle URL is outside ${expectedPrefix}`);
  }
  return url.href;
}
