import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useRef, useState } from "react";
import { useDispatch } from "react-redux";
import { setAuth } from "@/store/authSlice";
import { api } from "@/api/client";
// Messages for the handful of server-side OAuth failures the callback
// surfaces via #oauth_error=... Everything else falls back to "login error".
const OAUTH_ERROR_MESSAGES = {
    state_missing: "로그인 세션이 만료되었습니다. 다시 시도해 주세요.",
    state_mismatch: "보안 상태값이 일치하지 않습니다. 다시 로그인해 주세요.",
    missing_params: "인증 콜백이 완전하지 않습니다.",
    exchange_failed: "소셜 로그인 제공자와 통신하지 못했습니다.",
    resolve_failed: "계정을 처리하지 못했습니다. 관리자에게 문의해 주세요.",
    unverified_email: "이메일이 확인되지 않아 기존 계정과 자동 연결할 수 없습니다. 기존 비밀번호로 먼저 로그인해 주세요.",
};
const PROVIDER_LABELS = {
    google: "Google로 계속하기",
    github: "GitHub으로 계속하기",
};
const DEV_AUTO_LOGIN = {
    enabled: import.meta.env.DEV && import.meta.env.VITE_MODDLE_DEV_AUTO_LOGIN !== "false",
    loginId: import.meta.env.VITE_MODDLE_DEV_LOGIN_ID || "webuser",
    username: import.meta.env.VITE_MODDLE_DEV_USERNAME || "webuser",
    email: import.meta.env.VITE_MODDLE_DEV_EMAIL || "web@x.com",
    password: import.meta.env.VITE_MODDLE_DEV_PASSWORD || "P@ssw0rd1",
};
const DEV_AUTO_LOGIN_DISABLED_KEY = "moddle.devAutoLogin.disabled";
function isDevAutoLoginDisabled() {
    try {
        return window.localStorage.getItem(DEV_AUTO_LOGIN_DISABLED_KEY) === "true";
    }
    catch {
        return false;
    }
}
export function LoginView() {
    const [mode, setMode] = useState("login");
    const [loginId, setLoginId] = useState("");
    const [username, setUsername] = useState("");
    const [email, setEmail] = useState("");
    const [password, setPassword] = useState("");
    const [error, setError] = useState(null);
    const [busy, setBusy] = useState(false);
    const [providers, setProviders] = useState([]);
    // `invite` is null until the `#invite=<id>` fragment parse completes (and
    // the server confirms it). A failed preview (expired / revoked / bad id)
    // surfaces via `inviteError` so users don't get a silent signup with no
    // team attached. `inviteId` is retained on failure so we can round-trip
    // the value to the backend if the admin re-activates the invite mid-flow,
    // but UI-wise we don't show the banner unless we have a preview.
    const [invite, setInvite] = useState(null);
    const [inviteId, setInviteId] = useState("");
    const [inviteError, setInviteError] = useState(null);
    const devAutoLoginStartedRef = useRef(false);
    const dispatch = useDispatch();
    // Development-only fast path: Vite dev sessions should land directly in
    // the chat app. If the default user is missing, create it once and retry.
    // Invite and OAuth error URLs deliberately bypass this path so those flows
    // remain testable during development. React 18 StrictMode runs this effect,
    // cleans it up, then runs it again in development; reset the guard in
    // cleanup so the second real attempt can still dispatch setAuth.
    useEffect(() => {
        if (!DEV_AUTO_LOGIN.enabled || devAutoLoginStartedRef.current)
            return;
        if (window.location.hash.startsWith("#invite="))
            return;
        if (window.location.hash.startsWith("#oauth_error="))
            return;
        if (isDevAutoLoginDisabled())
            return;
        devAutoLoginStartedRef.current = true;
        let cancelled = false;
        setMode("login");
        setLoginId(DEV_AUTO_LOGIN.loginId);
        setPassword(DEV_AUTO_LOGIN.password);
        setError(null);
        setBusy(true);
        async function loginOrCreateDevUser() {
            try {
                let res;
                try {
                    res = await api.login(DEV_AUTO_LOGIN.loginId, DEV_AUTO_LOGIN.password);
                }
                catch {
                    try {
                        await api.register(DEV_AUTO_LOGIN.username, DEV_AUTO_LOGIN.email, DEV_AUTO_LOGIN.password);
                    }
                    catch (registerErr) {
                        const msg = registerErr instanceof Error ? registerErr.message.toLowerCase() : "";
                        const likelyExistingUser = msg.includes("already") || msg.includes("duplicate") || msg.includes("exists");
                        if (!likelyExistingUser)
                            throw registerErr;
                    }
                    res = await api.login(DEV_AUTO_LOGIN.loginId, DEV_AUTO_LOGIN.password);
                }
                if (!cancelled) {
                    dispatch(setAuth({ token: res.token, user: res.user }));
                }
            }
            catch (err) {
                if (!cancelled) {
                    setError(err instanceof Error ? `Dev auto login failed: ${err.message}` : "Dev auto login failed");
                }
            }
            finally {
                if (!cancelled)
                    setBusy(false);
            }
        }
        void loginOrCreateDevUser();
        return () => {
            cancelled = true;
            devAutoLoginStartedRef.current = false;
        };
    }, [dispatch]);
    // Read enabled OAuth providers once on mount. A failure here is
    // non-fatal — we just hide the social buttons and fall back to the
    // password form so the instance stays usable.
    useEffect(() => {
        let cancelled = false;
        api.ping().then((p) => {
            if (!cancelled && Array.isArray(p.oauth_providers)) {
                setProviders(p.oauth_providers);
            }
        }, () => {
            /* ignore */
        });
        return () => {
            cancelled = true;
        };
    }, []);
    // Consume #oauth_error=... that the callback may have set when the
    // flow failed before we got a token. We clear the hash so reloading
    // doesn't re-surface the same error message indefinitely.
    useEffect(() => {
        if (!window.location.hash.startsWith("#oauth_error="))
            return;
        const code = decodeURIComponent(window.location.hash.slice("#oauth_error=".length));
        setError(OAUTH_ERROR_MESSAGES[code] ?? `소셜 로그인에 실패했습니다 (${code}).`);
        history.replaceState(null, "", window.location.pathname + window.location.search);
    }, []);
    // Detect `#invite=<id>` on mount. If the preview succeeds we auto-switch
    // to the Signup tab so the invited user doesn't have to click "회원가입"
    // manually. The hash is left in place so a page reload re-reads it;
    // clearing it on success would force a refresh to undo the mode-switch.
    useEffect(() => {
        if (!window.location.hash.startsWith("#invite="))
            return;
        const id = decodeURIComponent(window.location.hash.slice("#invite=".length));
        if (!id)
            return;
        setInviteId(id);
        let cancelled = false;
        api.getInvite(id).then((p) => {
            if (cancelled)
                return;
            setInvite(p);
            setMode("register");
            setInviteError(null);
        }, (err) => {
            if (cancelled)
                return;
            setInviteError(err instanceof Error ? err.message : "초대 링크가 유효하지 않습니다.");
        });
        return () => {
            cancelled = true;
        };
    }, []);
    async function submit(e) {
        e.preventDefault();
        setError(null);
        setBusy(true);
        try {
            if (mode === "register") {
                // Pass the invite through iff we successfully previewed it. Bogus
                // hashes were already surfaced above and would 400 here anyway.
                await api.register(username, email, password, invite ? inviteId : "");
                const res = await api.login(username, password);
                dispatch(setAuth({ token: res.token, user: res.user }));
            }
            else {
                const res = await api.login(loginId, password);
                dispatch(setAuth({ token: res.token, user: res.user }));
            }
        }
        catch (err) {
            setError(err instanceof Error ? err.message : "요청에 실패했습니다");
        }
        finally {
            setBusy(false);
        }
    }
    return (_jsx("div", { className: "login-page", children: _jsxs("div", { className: "login-card", children: [_jsxs("div", { className: "login-brand", children: [_jsx("div", { className: "login-logo", "aria-hidden": true, children: "M" }), _jsx("h1", { className: "login-title", children: "Moddle" })] }), _jsx("p", { className: "login-subtitle", children: mode === "login" ? "팀과 다시 연결하세요." : "팀을 위한 새 계정을 만드세요." }), invite && (_jsxs("div", { className: "invite-banner", role: "status", children: [_jsx("strong", { children: invite.team_display_name }), " \uD300\uC5D0 \uCD08\uB300\uB418\uC5C8\uC2B5\uB2C8\uB2E4.", _jsx("br", {}), "\uACC4\uC815\uC744 \uB9CC\uB4E4\uBA74 \uC790\uB3D9\uC73C\uB85C \uD300\uC5D0 \uD569\uB958\uD569\uB2C8\uB2E4."] })), !invite && inviteError && (_jsxs("div", { className: "login-error", role: "alert", children: ["\uCD08\uB300 \uB9C1\uD06C\uAC00 \uC720\uD6A8\uD558\uC9C0 \uC54A\uC2B5\uB2C8\uB2E4: ", inviteError] })), _jsxs("div", { className: "login-tabs", role: "tablist", children: [_jsx("button", { type: "button", role: "tab", "aria-selected": mode === "login", className: "login-tab", onClick: () => { setMode("login"); setError(null); }, children: "\uB85C\uADF8\uC778" }), _jsx("button", { type: "button", role: "tab", "aria-selected": mode === "register", className: "login-tab", onClick: () => { setMode("register"); setError(null); }, children: "\uD68C\uC6D0\uAC00\uC785" })] }), _jsxs("form", { onSubmit: submit, children: [mode === "login" ? (_jsxs("div", { className: "field", children: [_jsx("label", { htmlFor: "loginId", children: "\uC544\uC774\uB514 \uB610\uB294 \uC774\uBA54\uC77C" }), _jsx("input", { id: "loginId", autoComplete: "username", value: loginId, onChange: (e) => setLoginId(e.target.value), placeholder: "webuser", required: true })] })) : (_jsxs(_Fragment, { children: [_jsxs("div", { className: "field", children: [_jsx("label", { htmlFor: "username", children: "\uC0AC\uC6A9\uC790\uBA85" }), _jsx("input", { id: "username", autoComplete: "username", value: username, onChange: (e) => setUsername(e.target.value), placeholder: "\uC601\uBB38 \uC18C\uBB38\uC790, \uC22B\uC790", required: true })] }), _jsxs("div", { className: "field", children: [_jsx("label", { htmlFor: "email", children: "\uC774\uBA54\uC77C" }), _jsx("input", { id: "email", type: "email", autoComplete: "email", value: email, onChange: (e) => setEmail(e.target.value), placeholder: "you@company.com", required: true })] })] })), _jsxs("div", { className: "field", children: [_jsx("label", { htmlFor: "password", children: "\uBE44\uBC00\uBC88\uD638" }), _jsx("input", { id: "password", type: "password", autoComplete: mode === "login" ? "current-password" : "new-password", value: password, onChange: (e) => setPassword(e.target.value), placeholder: "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022", required: true })] }), _jsx("button", { type: "submit", className: "btn-primary", disabled: busy, children: busy ? "처리 중…" : mode === "login" ? "로그인" : "계정 만들기" }), error && _jsx("div", { className: "login-error", role: "alert", children: error })] }), providers.length > 0 && (_jsxs(_Fragment, { children: [_jsx("div", { className: "login-divider", children: _jsx("span", { children: "\uB610\uB294" }) }), _jsx("div", { className: "oauth-buttons", children: providers.map((name) => (_jsxs("a", { className: `oauth-btn oauth-btn-${name}`, href: `/api/v4/oauth/${encodeURIComponent(name)}/login`, children: [_jsx("span", { className: `oauth-icon oauth-icon-${name}`, "aria-hidden": true }), _jsx("span", { children: PROVIDER_LABELS[name] ?? `${name}로 계속하기` })] }, name))) })] })), _jsx("div", { className: "login-footer", children: "Mattermost \uD638\uD658 \u00B7 /api/v4" })] }) }));
}
