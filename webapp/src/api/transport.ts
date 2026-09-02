export const COMPAT_API_BASE = "/api/v4";
export const MOYRO_API_BASE = "/api/moyro/v1";
// Non-secret UI credential-mode marker. The transport relies on the server's
// HttpOnly same-origin cookie instead of creating an Authorization header.
export const BROWSER_SESSION_TOKEN = "__moyro_browser_session__";

// A request with no bound on it can hang for as long as the browser keeps the
// socket open — on a dropped connection that is effectively forever, and the
// calling screen stays on its loading state with no way out. Every request
// therefore carries a deadline.
export const DEFAULT_TIMEOUT_MS = 30_000;
// Uploads are bounded far more loosely: the transfer time depends on the file
// and the link, not on server responsiveness.
export const UPLOAD_TIMEOUT_MS = 300_000;

// Transient failures worth one more attempt. A 5xx from a proxy or a dropped
// connection usually means "try again", while any 4xx is a decision the server
// already made and will make again.
const RETRY_STATUSES = new Set([502, 503, 504]);
const MAX_RETRIES = 2;
const RETRY_BASE_DELAY_MS = 200;

export type FetchOptions = Omit<RequestInit, "body"> & {
  body?: unknown;
  /**
   * Overrides the request deadline in milliseconds. `0` disables it, which is
   * only appropriate for a caller that manages its own `signal`.
   */
  timeoutMs?: number;
};

/** Why a request failed, for callers that distinguish transport from server. */
export type APIErrorKind = "http" | "timeout" | "network" | "aborted";

export class APIError extends Error {
  constructor(
    public readonly status: number,
    message: string,
    /**
     * `http` carries a real status; the others are transport failures where
     * `status` is 0 and the server never answered.
     */
    public readonly kind: APIErrorKind = "http",
  ) {
    super(message);
    this.name = "APIError";
  }

  /** True when retrying the same request could plausibly succeed. */
  get retryable(): boolean {
    return this.kind === "network" || this.kind === "timeout" || RETRY_STATUSES.has(this.status);
  }
}

// The transport replays a narrower set than `retryable` describes. A request
// that hit its deadline already consumed its full time budget; replaying it
// would multiply the wait the user is sitting through rather than recover from
// anything. Callers that want that trade-off can retry explicitly.
function isAutoRetryable(error: APIError): boolean {
  return error.kind === "network" || RETRY_STATUSES.has(error.status);
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

// Only requests that change nothing may be replayed. A retried POST could
// create a second post or consume an invite twice.
function isReplayable(method: string): boolean {
  const normalized = method.toUpperCase();
  return normalized === "GET" || normalized === "HEAD";
}

function delay(ms: number, signal?: AbortSignal | null): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new APIError(0, "request aborted", "aborted"));
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    function onAbort() {
      clearTimeout(timer);
      reject(new APIError(0, "request aborted", "aborted"));
    }
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

async function sendOnce(
  url: string,
  init: RequestInit,
  timeoutMs: number,
  callerSignal: AbortSignal | null | undefined,
): Promise<Response> {
  const controller = new AbortController();
  const abortFromCaller = () => controller.abort();
  let timedOut = false;
  const timer =
    timeoutMs > 0
      ? setTimeout(() => {
          timedOut = true;
          controller.abort();
        }, timeoutMs)
      : undefined;

  if (callerSignal) {
    if (callerSignal.aborted) controller.abort();
    else callerSignal.addEventListener("abort", abortFromCaller, { once: true });
  }

  try {
    return await fetch(url, { ...init, signal: controller.signal });
  } catch (error) {
    if (isAbortError(error)) {
      // A caller-driven abort is intentional and must not be retried; a
      // deadline hit is a transport failure and may be.
      if (timedOut) throw new APIError(0, `request timed out after ${timeoutMs}ms`, "timeout");
      throw new APIError(0, "request aborted", "aborted");
    }
    throw new APIError(0, error instanceof Error ? error.message : "network request failed", "network");
  } finally {
    if (timer !== undefined) clearTimeout(timer);
    callerSignal?.removeEventListener("abort", abortFromCaller);
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
  const isUpload = options.body instanceof FormData;
  if (hasBody && !isUpload) headers.set("Content-Type", "application/json");
  if (token && token !== BROWSER_SESSION_TOKEN) headers.set("Authorization", `Bearer ${token}`);

  const body = !hasBody
    ? undefined
    : isUpload
      ? (options.body as FormData)
      : JSON.stringify(options.body);

  const { timeoutMs, signal, ...rest } = options;
  const deadline = timeoutMs ?? (isUpload ? UPLOAD_TIMEOUT_MS : DEFAULT_TIMEOUT_MS);
  const url = `${base}${path}`;
  const init: RequestInit = { credentials: "same-origin", ...rest, headers, body };
  const method = init.method ?? "GET";

  let lastError: APIError | undefined;
  for (let attempt = 0; ; attempt++) {
    let response: Response;
    try {
      response = await sendOnce(url, init, deadline, signal);
    } catch (error) {
      lastError = error instanceof APIError ? error : new APIError(0, String(error), "network");
      if (attempt < MAX_RETRIES && isReplayable(method) && isAutoRetryable(lastError)) {
        await delay(RETRY_BASE_DELAY_MS * 2 ** attempt, signal);
        continue;
      }
      throw lastError;
    }

    if (!response.ok) {
      if (attempt < MAX_RETRIES && isReplayable(method) && RETRY_STATUSES.has(response.status)) {
        await delay(RETRY_BASE_DELAY_MS * 2 ** attempt, signal);
        continue;
      }
      const error = await response.json().catch(() => ({ message: response.statusText }));
      throw new APIError(response.status, error.message ?? `HTTP ${response.status}`);
    }

    if (response.status === 204) return undefined as T;
    const text = await response.text();
    return (text ? JSON.parse(text) : (undefined as unknown)) as T;
  }
}

export function compatRequest<T>(token: string | null, path: string, options: FetchOptions = {}): Promise<T> {
  return requestFrom<T>(COMPAT_API_BASE, token, path, options);
}

export function moyroRequest<T>(token: string | null, path: string, options: FetchOptions = {}): Promise<T> {
  return requestFrom<T>(MOYRO_API_BASE, token, path, options);
}
