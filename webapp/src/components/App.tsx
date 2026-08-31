import { useEffect, useRef, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import type { RootState } from "@/store";
import { clearAuth, setAuth } from "@/store/authSlice";
import { api } from "@/api/client";
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

  // New callbacks carry only a short-lived, one-time code. The public exchange
  // returns user + local session atomically, removing the fragile /users/me
  // probe and keeping the reusable bearer token out of browser history.
  // Legacy #token callbacks remain accepted during a coordinated upgrade.
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

    const session = callback.kind === "code"
      ? api.exchangeSSOCode(callback.value)
      : api.me(callback.value).then((user) => ({ token: callback.value, user }));
    session.then(
      (result) => {
        dispatch(setAuth(result));
        setConsumingHash(false);
      },
      () => {
        history.replaceState(null, "", "/login#oauth_error=session_failed");
        dispatch(clearAuth());
        setConsumingHash(false);
      },
    );
  }, [dispatch]);

  useEffect(() => {
    const storedToken = initialTokenRef.current;
    if (!storedToken || initialSSOCallbackRef.current) return;
    api.me(storedToken).then(
      (user) => dispatch(setAuth({ token: storedToken, user })),
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
