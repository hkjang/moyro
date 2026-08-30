import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { readdir } from "node:fs/promises";
import path from "node:path";

const baseURL = process.env.MOYRO_BASE_URL ?? "http://127.0.0.1:8065";
const adminLogin = process.env.MOYRO_ADMIN ?? "admin@moyro.local";
const adminPassword = process.env.MOYRO_ADMIN_PASSWORD ?? "MoyroRelease!2026";
const fixtureDir = (process.env.MOYRO_PLUGIN_FIXTURE_DIR ?? "").trim();
const screenshotDir = path.resolve(
  import.meta.dirname,
  process.env.MOYRO_CAPTURE_DIR ?? "../../docs/assets/screenshots",
);
const authStorageKey = "moyro.auth.session";
const compatibilityTimeout = 60_000;

type AuthSession = {
  token: string;
  user: { id: string; username: string; email: string; roles?: string };
};

let api: APIRequestContext;
let auth: AuthSession;
let teamID = "";
let channelID = "";
let botmanArchive = "";
let chatdumpArchive = "";
let echoArchive = "";
let langflowArchive = "";

test.describe.configure({ mode: "serial" });
test.skip(!fixtureDir, "MOYRO_PLUGIN_FIXTURE_DIR is required for real plugin browser compatibility tests");

test.beforeAll(async ({ playwright }) => {
  api = await playwright.request.newContext({ baseURL });
  const login = await api.post("/api/v4/users/login", {
    data: { login_id: adminLogin, password: adminPassword, device_id: "playwright-plugin-compatibility" },
  });
  expect(login.status()).toBe(201);
  auth = await login.json() as AuthSession;

  const headers = { Authorization: `Bearer ${auth.token}` };
  const teamsResponse = await api.get("/api/v4/users/me/teams", { headers });
  expect(teamsResponse.status()).toBe(200);
  const teams = await teamsResponse.json() as Array<{ id: string }>;
  expect(teams.length).toBeGreaterThan(0);
  teamID = teams[0].id;
  const channelsResponse = await api.get(`/api/v4/users/me/teams/${teamID}/channels`, { headers });
  expect(channelsResponse.status()).toBe(200);
  const channels = await channelsResponse.json() as Array<{ id: string; name: string }>;
  expect(channels.length).toBeGreaterThan(0);
  channelID = (channels.find((channel) => channel.name === "town-square") ?? channels[0]).id;

  const fixtures = await readdir(fixtureDir);
  botmanArchive = resolveArchive(fixtures, "com.mattermost.botman-");
  chatdumpArchive = resolveArchive(fixtures, "com.hkjang.mattermost-chatdump-plugin-");
  echoArchive = resolveArchive(fixtures, "com.mattermost.echosummary-");
  langflowArchive = resolveArchive(fixtures, "com.mattermost.langflow-");
});

test.afterAll(async () => {
  await api?.dispose();
});

test("uploads and activates all four official-layout plugin archives", async ({ page }) => {
  test.setTimeout(600_000);
  const issues = collectRuntimeIssues(page);
  await installAuthenticatedSession(page, auth);
  await page.goto("/admin/integrations/plugins");
  await expect(page.getByRole("heading", { name: "플러그인", level: 1 })).toBeVisible({ timeout: compatibilityTimeout });

  await uploadArchive(page, botmanArchive, "com.mattermost.botman");
  await uploadArchive(page, chatdumpArchive, "com.hkjang.mattermost-chatdump-plugin");
  await uploadArchive(page, echoArchive, "com.mattermost.echosummary");
  await expect(page.getByLabel("vLLM Base URL")).toBeVisible({ timeout: compatibilityTimeout });
  const secret = page.locator('[data-plugin-setting-key="VLLMAPIKey"]');
  await expect(secret).toHaveAttribute("type", "password", { timeout: compatibilityTimeout });

  await uploadArchive(page, langflowArchive, "com.mattermost.langflow");
  await expect.poll(async () => page.evaluate(() => (
    Object.keys((window as Window & { __moyro_plugins__?: Record<string, unknown> }).__moyro_plugins__ ?? {}).sort()
  )), { timeout: compatibilityTimeout }).toEqual([
    "com.hkjang.mattermost-chatdump-plugin",
    "com.mattermost.botman",
    "com.mattermost.echosummary",
    "com.mattermost.langflow",
  ]);

  await settle(page);
  await capture(page, "admin-plugins-compatible.jpg");
  assertNoRuntimeIssues(issues, "real plugin upload and activation");
});

test("renders the Langflow channel action and standard RHS contract", async ({ page }) => {
  const issues = collectRuntimeIssues(page);
  await installAuthenticatedSession(page, auth);
  await page.goto(`/workspace/${teamID}/channel/${channelID}`);
  const openLangflow = page.getByRole("button", { name: "Langflow 열기" });
  await expect(openLangflow).toBeVisible({ timeout: compatibilityTimeout });
  await openLangflow.click();
  const rhs = page.getByRole("complementary", { name: "com.mattermost.langflow 플러그인 패널" });
  await expect(rhs).toBeVisible({ timeout: compatibilityTimeout });
  await expect(rhs.getByText("Ask a Langflow Bot", { exact: true })).toBeVisible({ timeout: compatibilityTimeout });
  await settle(page);
  await capture(page, "plugin-langflow-rhs.jpg");
  assertNoRuntimeIssues(issues, "Langflow RHS");
});

test("renders EchoSummary user settings and persists mattermost-redux preferences", async ({ page }) => {
  const issues = collectRuntimeIssues(page);
  await installAuthenticatedSession(page, auth);
  await page.goto("/settings/plugins/com.mattermost.echosummary");
  await expect(page.getByRole("heading", { level: 1, name: "Echo Summary" })).toBeVisible({ timeout: compatibilityTimeout });
  await expect(page.getByText("전날 대화 요약 DM", { exact: true })).toBeVisible();
  const deliveryInput = page.getByPlaceholder("09:00,13:30");
  await deliveryInput.fill("08:30, 16:45");
  const saved = page.waitForResponse((response) => (
    response.request().method() === "PUT" && /\/api\/v4\/users\/[^/]+\/preferences$/.test(response.url())
  ));
  await page.getByRole("button", { name: "저장", exact: true }).click();
  expect((await saved).status()).toBe(200);
  await expect(page.getByText("개인 발송 시간이 저장되었습니다.", { exact: true })).toBeVisible();

  const preferences = await api.get(`/api/v4/users/${auth.user.id}/preferences/pp_com.mattermost.echosummary`, {
    headers: { Authorization: `Bearer ${auth.token}` },
  });
  expect(preferences.status()).toBe(200);
  expect(await preferences.json()).toContainEqual(expect.objectContaining({
    category: "pp_com.mattermost.echosummary",
    name: "delivery_times",
    value: "08:30,16:45",
  }));

  await settle(page);
  await capture(page, "settings-plugin-echosummary.jpg");
  assertNoRuntimeIssues(issues, "EchoSummary user settings");
});

function resolveArchive(entries: string[], prefix: string): string {
  const candidates = entries.filter((entry) => entry.startsWith(prefix) && entry.endsWith(".tar.gz"));
  if (candidates.length !== 1) {
    throw new Error(`expected exactly one ${prefix}*.tar.gz fixture, got ${candidates.join(", ") || "none"}`);
  }
  return path.join(fixtureDir, candidates[0]);
}

async function uploadArchive(page: Page, archive: string, pluginID: string) {
  const input = page.getByLabel("플러그인 tar.gz 선택");
  await input.setInputFiles(archive);
  await page.getByRole("checkbox", { name: "신뢰 코드 및 서명 미검증 실행 확인" }).check();
  const response = page.waitForResponse((candidate) => (
    candidate.request().method() === "POST" && new URL(candidate.url()).pathname === "/api/v4/plugins"
  ));
  await page.getByRole("button", { name: "플러그인 업로드", exact: true }).click();
  const uploaded = await response;
  expect(uploaded.status()).toBe(201);
  const result = await uploaded.json() as { id?: string; version?: string; runtime?: string; state?: string };
  expect(result).toEqual(expect.objectContaining({
    id: pluginID,
    runtime: "mattermost_v1",
    state: "running",
  }));
  expect(result.version).toBeTruthy();
  const row = page.getByRole("listitem").filter({ hasText: pluginID });
  await expect(row).toHaveCount(1, { timeout: compatibilityTimeout });
  await expect(row).toContainText(`v${result.version} · mattermost_v1`, { timeout: compatibilityTimeout });
  await expect(row.getByText("running", { exact: true })).toBeVisible({ timeout: compatibilityTimeout });
}

async function installAuthenticatedSession(page: Page, session: AuthSession) {
  await page.addInitScript(({ key, value }) => {
    window.sessionStorage.setItem(key, JSON.stringify(value));
  }, { key: authStorageKey, value: session });
}

function collectRuntimeIssues(page: Page) {
  const issues: string[] = [];
  page.on("pageerror", (error) => issues.push(`pageerror: ${error.message}`));
  page.on("console", (message) => {
    if (message.type() === "error") issues.push(`console: ${message.text()}`);
  });
  page.on("response", (response) => {
    if (response.status() >= 400 && response.url().startsWith(baseURL)) {
      issues.push(`http ${response.status()}: ${response.request().method()} ${response.url()}`);
    }
  });
  return issues;
}

function assertNoRuntimeIssues(issues: string[], surface: string) {
  expect(issues, `${surface} emitted browser/API errors:\n${issues.join("\n")}`).toEqual([]);
}

async function settle(page: Page) {
  await page.waitForLoadState("domcontentloaded");
  await page.waitForTimeout(900);
}

async function capture(page: Page, filename: string) {
  await page.screenshot({
    path: path.join(screenshotDir, filename),
    type: "jpeg",
    quality: 88,
    fullPage: true,
  });
}
