import { jsx as _jsx } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { useDispatch, useSelector } from "react-redux";
import { setAuth } from "@/store/authSlice";
import { api } from "@/api/client";
import { LoginView } from "./LoginView";
import { ChatView } from "./ChatView";
export function App() {
    const token = useSelector((s) => s.auth.token);
    const dispatch = useDispatch();
    // consumingHash flips true while we're resolving a `#token=...` redirect
    // from an OAuth callback. Prevents the LoginView from flashing between
    // "not logged in" and "logged in" when the fragment is present.
    const [consumingHash, setConsumingHash] = useState(() => window.location.hash.startsWith("#token="));
    // On mount, if the URL carries a #token= fragment, the OAuth callback
    // has just bounced us home with a freshly-minted JWT. Exchange it for
    // a full user record via /users/me, hand both to authSlice, then clear
    // the hash so a reload doesn't re-trigger this path.
    useEffect(() => {
        if (!window.location.hash.startsWith("#token="))
            return;
        const tok = decodeURIComponent(window.location.hash.slice("#token=".length));
        history.replaceState(null, "", window.location.pathname + window.location.search);
        if (!tok) {
            setConsumingHash(false);
            return;
        }
        api.me(tok).then((user) => {
            dispatch(setAuth({ token: tok, user }));
            setConsumingHash(false);
        }, () => {
            // Token was bad or the server rejected it — bail back to the
            // login page and let the user retry. The OAuth error panel on
            // LoginView picks up `#oauth_error=` fragments so we can
            // optionally route through that, but a silent failure is fine
            // since a repeat of the flow will surface the underlying issue.
            setConsumingHash(false);
        });
    }, [dispatch]);
    return (_jsx("div", { style: { fontFamily: "system-ui, sans-serif", height: "100vh" }, children: consumingHash ? (_jsx("div", { className: "login-page", children: _jsx("div", { className: "login-card", children: _jsx("p", { className: "login-subtitle", children: "\uB85C\uADF8\uC778 \uC911\u2026" }) }) })) : token ? (_jsx(ChatView, {})) : (_jsx(LoginView, {})) }));
}
