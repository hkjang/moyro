import { expect, test } from "@playwright/test";

const adminEmail = process.env.MOYRO_ADMIN ?? "admin@moyro.local";
const adminPassword = process.env.MOYRO_ADMIN_PASSWORD ?? "MoyroRelease!2026";
const fakeIssuer = process.env.MOYRO_FAKE_OIDC_ISSUER ?? "http://127.0.0.1:18066";

test("OIDC discovery, PKCE callback, HttpOnly exchange, and authenticated API complete in one browser flow", async ({ page, request, context, baseURL }) => {
  const login = await request.post("/api/v4/users/login", {
    data: { login_id: adminEmail, password: adminPassword },
  });
  expect(login.ok()).toBeTruthy();
  const adminToken = login.headers().token;
  expect(adminToken).toBeTruthy();
  const authHeaders = { Authorization: `Bearer ${adminToken}` };

  const currentSiteResponse = await request.get("/api/moyro/v1/admin/settings/site", { headers: authHeaders });
  expect(currentSiteResponse.ok()).toBeTruthy();
  const currentSite = await currentSiteResponse.json();
  const siteResponse = await request.patch("/api/moyro/v1/admin/settings/site", {
    headers: authHeaders,
    data: { ...currentSite, public_base_url: baseURL },
  });
  expect(siteResponse.ok(), await siteResponse.text()).toBeTruthy();

  const oidcResponse = await request.post("/api/moyro/v1/admin/oidc/providers", {
    headers: authHeaders,
    data: {
      id: "keycloak",
      kind: "keycloak",
      name: "Keycloak",
      enabled: true,
      issuer_url: fakeIssuer,
      client_id: "moyro-e2e",
      client_secret: "moyro-e2e-secret",
      scopes: ["openid", "profile", "email"],
      username_claim: "preferred_username",
      email_claim: "email",
      allow_signup: true,
      require_verified_email: true,
      allow_insecure_backchannel: false,
    },
  });
  expect(oidcResponse.ok(), await oidcResponse.text()).toBeTruthy();

  const protectedResponses: number[] = [];
  page.on("response", (response) => {
    if (response.url().includes("/api/v4/users/me")) protectedResponses.push(response.status());
  });

  await page.goto("/login");
  await page.getByRole("link", { name: "Keycloak로 계속하기" }).click();
  await expect(page).toHaveURL(/\/authorize\?/);
  await page.getByRole("button", { name: "Continue as E2E User" }).click();

  await expect(page).toHaveURL(new RegExp(`${baseURL}/today$`));
  await expect(page.getByText(/sso-e2e-user.*오늘의 흐름/).first()).toBeVisible();

  const session = await page.evaluate(async () => {
    const response = await fetch("/api/v4/users/me", { credentials: "same-origin" });
    return { status: response.status, user: await response.json(), stored: sessionStorage.getItem("moyro.auth.session") };
  });
  expect(session.status).toBe(200);
  expect(session.user).toMatchObject({ username: "sso-e2e-user", email: "sso-e2e@moyro.test" });
  expect(session.stored).toContain("__moyro_browser_session__");
  expect(session.stored).not.toMatch(/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\./);
  expect(protectedResponses).not.toContain(401);

  const cookies = await context.cookies(baseURL);
  const browserSession = cookies.find((cookie) => cookie.name === "moyro_browser_session");
  expect(browserSession).toMatchObject({ httpOnly: true, sameSite: "Lax", path: "/" });
  expect(browserSession?.value).toBeTruthy();
  expect(page.url()).not.toContain("#token=");
  expect(page.url()).not.toContain("#sso_code=");

  const metricsResponse = await request.get("/metrics");
  expect(metricsResponse.ok()).toBeTruthy();
  const metrics = await metricsResponse.text();
  expect(metrics).toContain('moyro_sso_stage_total{provider="keycloak",result="success",stage="login"} 1');
  expect(metrics).toContain('moyro_sso_stage_total{provider="keycloak",result="success",stage="callback"} 1');
  expect(metrics).toContain('moyro_sso_stage_total{provider="browser",result="success",stage="exchange"} 1');
});
