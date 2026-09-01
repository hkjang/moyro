export const COMPAT_API_BASE = "/api/v4";
export const MOYRO_API_BASE = "/api/moyro/v1";
// Non-secret UI credential-mode marker. The transport relies on the server's
// HttpOnly same-origin cookie instead of creating an Authorization header.
export const BROWSER_SESSION_TOKEN = "__moyro_browser_session__";

export type FetchOptions = Omit<RequestInit, "body"> & { body?: unknown };

export class APIError extends Error {
  constructor(public readonly status: number, message: string) {
    super(message);
    this.name = "APIError";
  }
}

async function requestFrom<T>(
  base: string,
  token: string | null,
  path: string,
  options: FetchOptions = {},
): Promise<T> {
  const headers = new Headers(options.headers);
  const hasBody = options.body !== undefined;
  if (hasBody && !(options.body instanceof FormData)) headers.set("Content-Type", "application/json");
  if (token && token !== BROWSER_SESSION_TOKEN) headers.set("Authorization", `Bearer ${token}`);

  const body = !hasBody
    ? undefined
    : options.body instanceof FormData
      ? options.body
      : JSON.stringify(options.body);
  const response = await fetch(`${base}${path}`, { credentials: "same-origin", ...options, headers, body });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ message: response.statusText }));
    throw new APIError(response.status, error.message ?? `HTTP ${response.status}`);
  }
  if (response.status === 204) return undefined as T;
  const text = await response.text();
  return (text ? JSON.parse(text) : (undefined as unknown)) as T;
}

export function compatRequest<T>(token: string | null, path: string, options: FetchOptions = {}): Promise<T> {
  return requestFrom<T>(COMPAT_API_BASE, token, path, options);
}

export function moyroRequest<T>(token: string | null, path: string, options: FetchOptions = {}): Promise<T> {
  return requestFrom<T>(MOYRO_API_BASE, token, path, options);
}
