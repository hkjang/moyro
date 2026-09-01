import { useEffect, useRef, useState } from "react";
import { useDispatch } from "react-redux";
import { setAuth } from "@/store/authSlice";
import { api, type InvitePreview } from "@/api/client";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import { BrandMark } from "@/components/brand/BrandMark";

type Mode = "login" | "register";
type CollaborationInvitePreview = InvitePreview & {
  kind?: "member" | "guest";
  channel_ids?: string[];
  guest_expires_after_seconds?: number;
  guest_file_download?: boolean;
};

// Messages for the handful of server-side OAuth failures the callback
// surfaces via #oauth_error=... Everything else falls back to "login error".
const OAUTH_ERROR_MESSAGES: Record<string, string> = {
  state_missing: "로그인 세션이 만료되었습니다. 다시 시도해 주세요.",
  state_mismatch: "보안 상태값이 일치하지 않습니다. 다시 로그인해 주세요.",
  missing_params: "인증 콜백이 완전하지 않습니다.",
  exchange_failed: "소셜 로그인 제공자와 통신하지 못했습니다.",
  session_failed: "SSO 로그인 세션을 완료하지 못했습니다. 다시 로그인해 주세요.",
  sso_restart_required: "로그인 세션을 완료할 수 없습니다. 아래 버튼으로 SSO 로그인을 새로 시작해 주세요.",
  resolve_failed: "계정을 처리하지 못했습니다. 관리자에게 문의해 주세요.",
  onboarding_failed: "SSO 그룹 기반 팀·채널 권한을 안전하게 적용하지 못했습니다. 관리자에게 문의한 뒤 다시 시도해 주세요.",
  unverified_email:
    "이메일이 확인되지 않아 기존 계정과 자동 연결할 수 없습니다. 기존 비밀번호로 먼저 로그인해 주세요.",
};

const PROVIDER_LABELS: Record<string, string> = {
  google: "Google로 계속하기",
  github: "GitHub으로 계속하기",
  keycloak: "Keycloak로 계속하기",
  oidc: "SSO로 계속하기",
};

const DEV_AUTO_LOGIN = {
  enabled: import.meta.env.DEV && import.meta.env.VITE_MOYRO_DEV_AUTO_LOGIN === "true",
  loginId: import.meta.env.VITE_MOYRO_DEV_LOGIN_ID || "webuser",
  username: import.meta.env.VITE_MOYRO_DEV_USERNAME || "webuser",
  email: import.meta.env.VITE_MOYRO_DEV_EMAIL || "web@x.com",
  password: import.meta.env.VITE_MOYRO_DEV_PASSWORD || "P@ssw0rd1",
};

const DEV_AUTO_LOGIN_DISABLED_KEY = "moyro.devAutoLogin.disabled";

function isDevAutoLoginDisabled(): boolean {
  try {
    return window.localStorage.getItem(DEV_AUTO_LOGIN_DISABLED_KEY) === "true";
  } catch {
    return false;
  }
}

export function LoginView() {
  const systemInfo = useSystemInfo();
  const [mode, setMode] = useState<Mode>("login");
  const [loginId, setLoginId] = useState("");
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [ssoRestartRequired, setSSORestartRequired] = useState(false);
  const [providers, setProviders] = useState<string[]>([]);
  // `invite` is null until the `#invite=<id>` fragment parse completes (and
  // the server confirms it). A failed preview (expired / revoked / bad id)
  // surfaces via `inviteError` so users don't get a silent signup with no
  // team attached. `inviteId` is retained on failure so we can round-trip
  // the value to the backend if the admin re-activates the invite mid-flow,
  // but UI-wise we don't show the banner unless we have a preview.
  const [invite, setInvite] = useState<CollaborationInvitePreview | null>(null);
  const [inviteId, setInviteId] = useState("");
  const [inviteError, setInviteError] = useState<string | null>(null);
  const devAutoLoginStartedRef = useRef(false);
  const dispatch = useDispatch();
  const authProviders = providers.length > 0
    ? providers
    : systemInfo.oidc_enabled
      ? [systemInfo.oidc_provider_name === "Keycloak" ? "keycloak" : "oidc"]
      : [];
  const canRegister = Boolean(invite || systemInfo.local_signup_enabled);
  const returnTo = window.location.pathname === "/login"
    ? "/today"
    : window.location.pathname + window.location.search;
  const oidcLoginURL = `/api/moyro/v1/auth/oidc/login?return_to=${encodeURIComponent(
    returnTo,
  )}`;
  const providerLoginURL = (name: string) => name === "keycloak" || name === "oidc"
    ? oidcLoginURL
    : `/api/v4/oauth/${encodeURIComponent(name)}/login?return_to=${encodeURIComponent(returnTo)}`;

  useEffect(() => {
    if (systemInfo.loaded && !canRegister && mode === "register") {
      setMode("login");
    }
  }, [canRegister, mode, systemInfo.loaded]);

  // Development-only fast path: Vite dev sessions should land directly in
  // the chat app. If the default user is missing, create it once and retry.
  // Invite and OAuth error URLs deliberately bypass this path so those flows
  // remain testable during development. React 18 StrictMode runs this effect,
  // cleans it up, then runs it again in development; reset the guard in
  // cleanup so the second real attempt can still dispatch setAuth.
  useEffect(() => {
    if (!DEV_AUTO_LOGIN.enabled || devAutoLoginStartedRef.current) return;
    if (window.location.hash.startsWith("#invite=")) return;
    if (window.location.hash.startsWith("#oauth_error=")) return;
    if (isDevAutoLoginDisabled()) return;

    devAutoLoginStartedRef.current = true;
    let cancelled = false;

    setMode("login");
    setLoginId(DEV_AUTO_LOGIN.loginId);
    setPassword(DEV_AUTO_LOGIN.password);
    setError(null);
    setBusy(true);

    async function loginOrCreateDevUser() {
      try {
        let res: Awaited<ReturnType<typeof api.login>>;
        try {
          res = await api.login(DEV_AUTO_LOGIN.loginId, DEV_AUTO_LOGIN.password);
        } catch {
          try {
            await api.register(
              DEV_AUTO_LOGIN.username,
              DEV_AUTO_LOGIN.email,
              DEV_AUTO_LOGIN.password,
            );
          } catch (registerErr) {
            const msg = registerErr instanceof Error ? registerErr.message.toLowerCase() : "";
            const likelyExistingUser =
              msg.includes("already") || msg.includes("duplicate") || msg.includes("exists");
            if (!likelyExistingUser) throw registerErr;
          }
          res = await api.login(DEV_AUTO_LOGIN.loginId, DEV_AUTO_LOGIN.password);
        }
        if (!cancelled) {
          dispatch(setAuth({ token: res.token, user: res.user }));
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? `Dev auto login failed: ${err.message}` : "Dev auto login failed");
        }
      } finally {
        if (!cancelled) setBusy(false);
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
    api.ping().then(
      (p) => {
        if (!cancelled && Array.isArray(p.oauth_providers)) {
          setProviders(p.oauth_providers);
        }
      },
      () => {
        /* ignore */
      },
    );
    return () => {
      cancelled = true;
    };
  }, []);

  // Consume #oauth_error=... that the callback may have set when the
  // flow failed before we got a token. We clear the hash so reloading
  // doesn't re-surface the same error message indefinitely.
  useEffect(() => {
    if (!window.location.hash.startsWith("#oauth_error=")) return;
    const code = decodeURIComponent(
      window.location.hash.slice("#oauth_error=".length),
    );
    setSSORestartRequired(code === "sso_restart_required");
    setError(OAUTH_ERROR_MESSAGES[code] ?? `소셜 로그인에 실패했습니다 (${code}).`);
    history.replaceState(null, "", window.location.pathname + window.location.search);
  }, []);

  // Detect `#invite=<id>` on mount. If the preview succeeds we auto-switch
  // to the Signup tab so the invited user doesn't have to click "회원가입"
  // manually. The hash is left in place so a page reload re-reads it;
  // clearing it on success would force a refresh to undo the mode-switch.
  useEffect(() => {
    if (!window.location.hash.startsWith("#invite=")) return;
    const id = decodeURIComponent(window.location.hash.slice("#invite=".length));
    if (!id) return;
    setInviteId(id);
    let cancelled = false;
    api.getInvite(id).then(
      (p) => {
        if (cancelled) return;
        setInvite(p);
        setMode("register");
        setInviteError(null);
      },
      (err: unknown) => {
        if (cancelled) return;
        setInviteError(
          err instanceof Error ? err.message : "초대 링크가 유효하지 않습니다.",
        );
      },
    );
    return () => {
      cancelled = true;
    };
  }, []);

  async function submit(e: React.FormEvent) {
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
      } else {
        const res = await api.login(loginId, password);
        dispatch(setAuth({ token: res.token, user: res.user }));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "요청에 실패했습니다");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-page">
      <div className="login-card">
        <div className="login-brand">
          <BrandMark className="login-logo" size={38} />
          <h1 className="login-title">moyro</h1>
        </div>
        <p className="login-subtitle">
          {mode === "login" ? "팀과 다시 연결하세요." : "팀을 위한 새 계정을 만드세요."}
        </p>

        {invite && (
          <div className="invite-banner" role="status">
            <strong>{invite.team_display_name}</strong> 팀에 초대되었습니다.
            <br />
            {invite.kind === "guest"
              ? `외부 게스트로 허용 채널 ${invite.channel_ids?.length ?? 0}개에만 가입합니다${invite.guest_file_download === false ? ". 원본 파일 다운로드는 제한됩니다." : "."}`
              : "계정을 만들면 자동으로 팀에 합류합니다."}
          </div>
        )}
        {!invite && inviteError && (
          <div className="login-error" role="alert">
            초대 링크가 유효하지 않습니다: {inviteError}
          </div>
        )}

        <div className="login-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={mode === "login"}
            className="login-tab"
            onClick={() => { setMode("login"); setError(null); }}
          >
            로그인
          </button>
          {canRegister && (
            <button
              type="button"
              role="tab"
              aria-selected={mode === "register"}
              className="login-tab"
              onClick={() => { setMode("register"); setError(null); }}
            >
              회원가입
            </button>
          )}
        </div>

        <form onSubmit={submit}>
          {mode === "login" ? (
            <div className="field">
              <label htmlFor="loginId">아이디 또는 이메일</label>
              <input
                id="loginId"
                autoComplete="username"
                value={loginId}
                onChange={(e) => setLoginId(e.target.value)}
                placeholder="webuser"
                required
              />
            </div>
          ) : (
            <>
              <div className="field">
                <label htmlFor="username">사용자명</label>
                <input
                  id="username"
                  autoComplete="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="영문 소문자, 숫자"
                  required
                />
              </div>
              <div className="field">
                <label htmlFor="email">이메일</label>
                <input
                  id="email"
                  type="email"
                  autoComplete="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@company.com"
                  required
                />
              </div>
            </>
          )}

          <div className="field">
            <label htmlFor="password">비밀번호</label>
            <input
              id="password"
              type="password"
              autoComplete={mode === "login" ? "current-password" : "new-password"}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••••"
              minLength={mode === "register" ? 12 : undefined}
              maxLength={72}
              required
            />
          </div>

          <button type="submit" className="btn-primary" disabled={busy}>
            {busy ? "처리 중…" : mode === "login" ? "로그인" : "계정 만들기"}
          </button>

          {error && <div className="login-error" role="alert">{error}</div>}
          {ssoRestartRequired && authProviders.length > 0 && (
            <a className="btn-primary" href={providerLoginURL(authProviders[0])}>
              SSO 로그인 다시 시작
            </a>
          )}
        </form>

        {authProviders.length > 0 && (
          <>
            <div className="login-divider"><span>또는</span></div>
            <div className="oauth-buttons">
              {authProviders.map((name) => (
                <a
                  key={name}
                  className={`oauth-btn oauth-btn-${name}`}
                  href={providerLoginURL(name)}
                >
                  <span className={`oauth-icon oauth-icon-${name}`} aria-hidden />
                  <span>{PROVIDER_LABELS[name] ?? `${name}로 계속하기`}</span>
                </a>
              ))}
            </div>
          </>
        )}

        <div className="login-footer">
          <strong>moyro {displayVersion(systemInfo.version)}</strong>
          {systemInfo.build_hash && <> · {systemInfo.build_hash.slice(0, 8)}</>}
          <br />
          Mattermost 호환 · /api/v4
        </div>
      </div>
    </div>
  );
}
