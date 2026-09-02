// Credentialed media fetching for surfaces that cannot use a bare <img src>.
//
// Split out of the former single `client.ts`. `client.ts` re-exports every
// symbol here, so callers keep importing from `@/api/client`.
import { COMPAT_API_BASE as BASE, BROWSER_SESSION_TOKEN } from "./transport";

// Media bytes are fetched with an Authorization header and converted to a
// short-lived blob URL by the rendering component. Restrict callers to known
// read-only media surfaces so a post cannot turn an arbitrary same-origin API
// path into a credentialed fetch.
function isAuthenticatedMediaPath(path: string): boolean {
  if (!path.startsWith(`${BASE}/`)) return false;
  let parsed: URL;
  try {
    parsed = new URL(path, "https://moyro.invalid");
  } catch {
    return false;
  }
  if (parsed.origin !== "https://moyro.invalid" || parsed.hash) return false;
  const pathname = parsed.pathname;
  const noQuery = parsed.search === "";
  if (/^\/api\/v4\/files\/[^/]+(?:\/thumbnail)?$/.test(pathname)) return noQuery;
  if (/^\/api\/v4\/emoji\/[^/]+\/image$/.test(pathname)) return noQuery;
  if (/^\/api\/v4\/users\/[^/]+\/image$/.test(pathname)) {
    return [...parsed.searchParams.keys()].every((key) => key === "v") && parsed.searchParams.getAll("v").length <= 1;
  }
  if (pathname === "/api/v4/link_preview_image") {
    return [...parsed.searchParams.keys()].every((key) => key === "url") && parsed.searchParams.getAll("url").length === 1;
  }
  return false;
}

export async function authenticatedMediaBlob(token: string, path: string): Promise<Blob> {
  if (!token) throw new Error("missing media credential");
  if (!isAuthenticatedMediaPath(path)) throw new Error("invalid authenticated media path");
  const headers = new Headers();
  if (token !== BROWSER_SESSION_TOKEN) headers.set("Authorization", `Bearer ${token}`);
  const res = await fetch(path, { headers, credentials: "same-origin" });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }));
    throw new Error(err.message ?? `HTTP ${res.status}`);
  }
  return res.blob();
}
