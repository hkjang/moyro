import AxeBuilder from "@axe-core/playwright";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

const baseURL = process.env.MOYRO_BASE_URL ?? "http://127.0.0.1:8065";
const allowedOrigin = new URL(baseURL).origin;
const adminLogin = process.env.MOYRO_ADMIN ?? "admin@moyro.local";
const adminPassword = process.env.MOYRO_ADMIN_PASSWORD ?? "MoyroRelease!2026";
const authStorageKey = "moyro.auth.session";
const wcagTags = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];
const gateImpacts = new Set(["serious", "critical"]);

type AuthSession = {
  token: string;
  user: { id: string; username: string; email: string; roles?: string };
};

type WorkspaceRoute = {
  path: string;
  channelName: string;
};

type Surface = {
  name: string;
  path: () => string;
  heading: () => string;
};

let api: APIRequestContext;
let auth: AuthSession;
let workspace: WorkspaceRoute;

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ playwright }) => {
  api = await playwright.request.newContext({ baseURL });
  const login = await api.post("/api/v4/users/login", {
    data: {
      login_id: adminLogin,
      password: adminPassword,
      device_id: "playwright-accessibility-gate",
    },
  });
  if (!login.ok()) {
    throw new Error(`accessibility bootstrap login failed: ${login.status()} ${await login.text()}`);
  }
  auth = await login.json() as AuthSession;
  workspace = await resolveWorkspaceRoute(api, auth.token);
});

test.afterAll(async () => {
  await api?.dispose();
});

test.beforeEach(async ({ page }) => {
  await blockExternalRequests(page);
  await installAuthenticatedSession(page, auth);
});

const surfaces: readonly Surface[] = [
  { name: "today", path: () => "/today", heading: () => "오늘의 흐름" },
  { name: "inbox updates", path: () => "/inbox/updates", heading: () => "통합 알림함" },
  { name: "my tasks", path: () => "/my-work/tasks", heading: () => "내 업무" },
  { name: "my approvals", path: () => "/approvals/mine", heading: () => "승인 센터" },
  { name: "AI assistant", path: () => "/assistant", heading: () => "AI 대화" },
  {
    name: "workspace",
    path: () => workspace.path,
    heading: () => workspace.channelName,
  },
  { name: "profile settings", path: () => "/settings/profile", heading: () => "프로필" },
  { name: "site administration", path: () => "/admin/site", heading: () => "사이트 설정" },
];

for (const surface of surfaces) {
  test(`${surface.name} has no serious or critical WCAG A/AA violations`, async ({ page }) => {
    await openSurface(page, surface.path(), surface.heading());

    const results = await new AxeBuilder({ page })
      .withTags(wcagTags)
      .analyze();
    const violations = results.violations
      .filter((violation) => violation.impact && gateImpacts.has(violation.impact))
      .map((violation) => ({
        id: violation.id,
        impact: violation.impact,
        help: violation.help,
        helpUrl: violation.helpUrl,
        nodes: violation.nodes.map((node) => ({
          target: node.target,
          summary: node.failureSummary,
        })),
      }));

    expect(
      violations,
      `${surface.name} contains serious/critical WCAG A/AA violations`,
    ).toEqual([]);
  });
}

const zoomSurfaces: readonly Surface[] = [surfaces[0], surfaces[5]];

for (const surface of zoomSurfaces) {
  test(`${surface.name} has no horizontal overflow at a 200% zoom-equivalent viewport`, async ({ page }) => {
    // Desktop Chrome's 200% page zoom halves the CSS-pixel viewport. The
    // 720x500 viewport therefore exercises the same responsive reflow as the
    // 1440x1000 release viewport without relying on non-portable browser UI.
    await page.setViewportSize({ width: 720, height: 500 });
    await openSurface(page, surface.path(), surface.heading());

    await expect.poll(() => page.evaluate(() => ({
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth,
    }))).toEqual(expect.objectContaining({ clientWidth: 720 }));
    const overflow = await page.evaluate(() => (
      document.documentElement.scrollWidth - document.documentElement.clientWidth
    ));
    expect(overflow, `${surface.name} overflows horizontally at 200% zoom`).toBeLessThanOrEqual(1);
  });
}

test("forced-colors keeps the Flow heading and refresh action accessible", async ({ page }) => {
  await page.emulateMedia({ forcedColors: "active" });
  await openSurface(page, "/today", "오늘의 흐름");

  await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "새로고침" })).toBeVisible();
});

test("forced-colors keeps the workspace heading and composer accessible", async ({ page }) => {
  await page.emulateMedia({ forcedColors: "active" });
  await openSurface(page, workspace.path, workspace.channelName);

  await expect(page.getByRole("heading", { name: workspace.channelName })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "메시지 입력" })).toBeVisible();
});

async function resolveWorkspaceRoute(
  context: APIRequestContext,
  token: string,
): Promise<WorkspaceRoute> {
  const headers = { Authorization: `Bearer ${token}` };
  const teamsResponse = await context.get("/api/v4/users/me/teams", { headers });
  if (!teamsResponse.ok()) {
    throw new Error(`workspace team bootstrap failed: ${teamsResponse.status()} ${await teamsResponse.text()}`);
  }
  const teams = await teamsResponse.json() as Array<{ id: string }>;
  const team = teams[0];
  if (!team) throw new Error("accessibility bootstrap requires at least one team");

  const channelsResponse = await context.get(`/api/v4/users/me/teams/${team.id}/channels`, { headers });
  if (!channelsResponse.ok()) {
    throw new Error(`workspace channel bootstrap failed: ${channelsResponse.status()} ${await channelsResponse.text()}`);
  }
  const channels = await channelsResponse.json() as Array<{
    id: string;
    name: string;
    display_name: string;
    type: string;
  }>;
  const channel = channels.find((candidate) => candidate.name === "town-square")
    ?? channels.find((candidate) => candidate.type !== "D" && candidate.type !== "G")
    ?? channels[0];
  if (!channel) throw new Error("accessibility bootstrap requires at least one channel");

  return {
    path: `/workspace/${team.id}/channel/${channel.id}`,
    channelName: channel.type === "D" ? "다이렉트 메시지" : channel.display_name,
  };
}

async function installAuthenticatedSession(page: Page, session: AuthSession) {
  await page.addInitScript(({ key, value }) => {
    window.sessionStorage.setItem(key, JSON.stringify(value));
  }, { key: authStorageKey, value: session });
}

async function blockExternalRequests(page: Page) {
  await page.route("**/*", async (route) => {
    const requestURL = new URL(route.request().url());
    if (
      requestURL.origin === allowedOrigin
      || requestURL.protocol === "data:"
      || requestURL.protocol === "blob:"
    ) {
      await route.continue();
      return;
    }
    await route.abort("blockedbyclient");
  });
}

async function openSurface(page: Page, path: string, heading: string) {
  await page.goto(path);
  await expect(page.locator("main").first()).toBeVisible();
  await expect(page.getByRole("heading", { name: new RegExp(escapeRegex(heading), "i") }).first()).toBeVisible();
  await page.waitForTimeout(250);
}

function escapeRegex(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
