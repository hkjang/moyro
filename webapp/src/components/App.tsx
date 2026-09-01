import { useEffect, useRef, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import type { RootState } from "@/store";
import { clearAuth, setAuth } from "@/store/authSlice";
import { api } from "@/api/client";
import { APIError, BROWSER_SESSION_TOKEN } from "@/api/transport";
import { AppRouter } from "@/app/AppRouter";
import { isSSOCallbackFragment, parseSSOCallbackFragment } from "@/auth/ssoCallback";
import { useSystemInfo } from "@/features/system/SystemInfoContext";
import { clearMoyroDraftsForUser } from "@/features/workspace/composer/useDraft";

export function App() {
  const token = useSelector((s: RootState) => s.auth.token);
  const user = useSelector((s: RootState) => s.auth.user);
  const systemInfo = useSystemInfo();
  const dispatch = useDispatch();
  const initialTokenRef = useRef(token);
  const initialUserRef = useRef(user);
  const initialSSOCallbackRef = useRef(isSSOCallbackFragment(window.location.hash));
  const clearDraftsOnLogoutRef = useRef(
    systemInfo.capabilities?.drafts?.clear_on_logout !== false,
  );
  clearDraftsOnLogoutRef.current = systemInfo.capabilities?.drafts?.clear_on_logout !== false;
  // Keep the login screen from flashing while an OAuth/OIDC callback is
  // exchanged for the local user and session in one request.
  const [consumingHash, setConsumingHash] = useState<boolean>(
    () => initialSSOCallbackRef.current,
  );
  const [restoringSession, setRestoringSession] = useState<boolean>(
    () => Boolean(initialTokenRef.current) && !initialSSOCallbackRef.current,
  );

  // Callbacks carry only a short-lived, browser-bound code. Retry transient
  // transport/5xx failures twice; terminal failures require an explicit new
  // provider flow, avoiding silent redirect loops.
  useEffect(() => {
    const callback = parseSSOCallbackFragment(window.location.hash);
    if (!callback) return;
    history.replaceState(null, "", window.location.pathname + window.location.search);
    if (callback.kind === "invalid") {
      history.replaceState(null, "", "/login#oauth_error=session_failed");
      dispatch(clearAuth());
      setConsumingHash(false);
      return;
    }

    const session = exchangeSSOCodeWithRetry(callback.value);
    session.then(
      (result) => {
        dispatch(setAuth(result));
        setConsumingHash(false);
      },
      () => {
        history.replaceState(null, "", "/login#oauth_error=sso_restart_required");
        dispatch(clearAuth());
        setConsumingHash(false);
      },
    );
  }, [dispatch]);

  useEffect(() => {
    const storedToken = initialTokenRef.current;
    if (!storedToken || initialSSOCallbackRef.current) return;
    const restore = storedToken === BROWSER_SESSION_TOKEN
      ? api.me(storedToken).then((user) => ({ token: storedToken, user }))
      : api.adoptBrowserSession(storedToken);
    restore.then(
      (session) => dispatch(setAuth(session)),
      () => {
        if (
          initialUserRef.current?.id
          && clearDraftsOnLogoutRef.current
        ) {
          clearMoyroDraftsForUser(initialUserRef.current.id);
        }
        dispatch(clearAuth());
      },
    ).finally(() => setRestoringSession(false));
  }, [dispatch]);

  return (
    <div style={{ height: "100vh" }}>
      {consumingHash || restoringSession ? (
        <div className="login-page">
          <div className="login-card">
            <p className="login-subtitle">로그인 중…</p>
          </div>
        </div>
      ) : (
        <AppRouter />
      )}
    </div>
  );
}

function waitForRetry(delay: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, delay));
}

function retryableSSOError(error: unknown): boolean {
  return error instanceof TypeError || (error instanceof APIError && error.status >= 500);
}

export async function exchangeSSOCodeWithRetry(code: string) {
  const delays = [150, 400];
  for (let attempt = 0; ; attempt += 1) {
    try {
      return await api.exchangeSSOCode(code);
    } catch (error) {
      if (attempt >= delays.length || !retryableSSOError(error)) throw error;
      await waitForRetry(delays[attempt]);
    }
  }
}
