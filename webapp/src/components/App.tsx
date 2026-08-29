import { useEffect, useRef, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import type { RootState } from "@/store";
import { clearAuth, setAuth } from "@/store/authSlice";
import { api } from "@/api/client";
import { AppRouter } from "@/app/AppRouter";

export function App() {
  const token = useSelector((s: RootState) => s.auth.token);
  const dispatch = useDispatch();
  const initialTokenRef = useRef(token);
  // consumingHash flips true while we're resolving a `#token=...` redirect
  // from an OAuth callback. Prevents the LoginView from flashing between
  // "not logged in" and "logged in" when the fragment is present.
  const [consumingHash, setConsumingHash] = useState<boolean>(
    () => window.location.hash.startsWith("#token="),
  );
  const [restoringSession, setRestoringSession] = useState<boolean>(
    () => Boolean(initialTokenRef.current) && !window.location.hash.startsWith("#token="),
  );

  // On mount, if the URL carries a #token= fragment, the OAuth callback
  // has just bounced us home with a freshly-minted JWT. Exchange it for
  // a full user record via /users/me, hand both to authSlice, then clear
  // the hash so a reload doesn't re-trigger this path.
  useEffect(() => {
    if (!window.location.hash.startsWith("#token=")) return;
    const tok = decodeURIComponent(window.location.hash.slice("#token=".length));
    history.replaceState(null, "", window.location.pathname + window.location.search);
    if (!tok) {
      setConsumingHash(false);
      return;
    }
    api.me(tok).then(
      (user) => {
        dispatch(setAuth({ token: tok, user }));
        setConsumingHash(false);
      },
      () => {
        // Token was bad or the server rejected it — bail back to the
        // login page and let the user retry. The OAuth error panel on
        // LoginView picks up `#oauth_error=` fragments so we can
        // optionally route through that, but a silent failure is fine
        // since a repeat of the flow will surface the underlying issue.
        setConsumingHash(false);
      },
    );
  }, [dispatch]);

  useEffect(() => {
    const storedToken = initialTokenRef.current;
    if (!storedToken || window.location.hash.startsWith("#token=")) return;
    api.me(storedToken).then(
      (user) => dispatch(setAuth({ token: storedToken, user })),
      () => dispatch(clearAuth()),
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
