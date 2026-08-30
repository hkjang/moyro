import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";

const baseURL = process.env.MOYRO_BASE_URL ?? "http://127.0.0.1:8065";
const adminLogin = process.env.MOYRO_ADMIN ?? "admin@moyro.local";
const adminPassword = process.env.MOYRO_ADMIN_PASSWORD ?? "MoyroRelease!2026";
const authStorageKey = "moyro.auth.session";
const desktopViewport = { width: 1440, height: 1000 };
const mobileViewport = { width: 430, height: 932 };

type AuthSession = {
  token: string;
  user: { id: string; username: string; email: string; roles?: string };
};

type WorkspaceRoute = {
  teamID: string;
  channelID: string;
  channelName: string;
};

type VisualTheme = "light" | "dark";

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
      device_id: "playwright-visual-regression",
    },
  });
  if (!login.ok()) {
    throw new Error(`visual regression bootstrap login failed: ${login.status()} ${await login.text()}`);
  }
  auth = await login.json() as AuthSession;
  workspace = await resolveWorkspaceRoute(api, auth);
});

test.afterAll(async () => {
  await api?.dispose();
});

for (const visual of [
  {
    name: "today light",
    path: "/today",
    heading: /오늘의 흐름/,
    theme: "light",
    snapshot: "flow-today-light-full.png",
    masks: (page: Page) => [page.locator(".flow-page-header .flow-description").first()],
  },
  {
    name: "inbox updates dark",
    path: "/inbox/updates",
    heading: "통합 알림함",
    theme: "dark",
    snapshot: "flow-inbox-updates-dark-full.png",
    masks: (page: Page) => [page.locator("#inbox-updates .flow-activity-row .flow-item-subtitle")],
  },
  {
    name: "my work tasks light",
    path: "/my-work/tasks",
    heading: "내 업무",
    theme: "light",
    snapshot: "flow-my-work-tasks-light-full.png",
    masks: (page: Page) => [page.locator("#work-tasks .flow-list-row .flow-item-subtitle")],
  },
  {
    name: "approval center mine light",
    path: "/approvals/mine",
    heading: "승인 센터",
    theme: "light",
    snapshot: "flow-approval-center-mine-light-full.png",
    masks: (page: Page) => [
      page.locator("#approval-list-mine .flow-card > .flow-toolbar .flow-item-subtitle"),
      page.locator("#approval-list-mine .flow-card > .flow-item-subtitle"),
    ],
  },
  {
    name: "admin site light",
    path: "/admin/site",
    heading: "사이트 설정",
    theme: "light",
    snapshot: "admin-site-light-full.png",
    masks: () => [],
  },
] as const) {
  test(`${visual.name} remains visually stable`, async ({ page }) => {
    await page.setViewportSize(desktopViewport);
    const externalRequests = await prepareAuthenticatedPage(page, visual.theme);
    await openStableRoute(page, visual.path, visual.heading);
    await expect(page).toHaveScreenshot(visual.snapshot, snapshotOptions(visual.theme, visual.masks(page), true));
    expect(externalRequests, `${visual.path} attempted an external browser request`).toEqual([]);
  });
}

test("mobile workspace at 430x932 remains visually stable", async ({ page }) => {
  await page.setViewportSize(mobileViewport);
  const externalRequests = await prepareAuthenticatedPage(page, "light");
  await openStableRoute(
    page,
    `/workspace/${encodeURIComponent(workspace.teamID)}/channel/${encodeURIComponent(workspace.channelID)}`,
    workspace.channelName,
    ".chat-header-title",
  );
  await expect(page.locator(".workspace-message-composer")).toBeVisible();
  await page.addStyleTag({
    content: `
      .chat-header .avatar,
      .workspace-message-item .avatar {
        background-color: #10b981 !important;
      }
    `,
  });
  await expect(page).toHaveScreenshot(
    "workspace-channel-mobile-light-430x932.png",
    snapshotOptions("light", [page.locator(".msg-time")], false),
  );
  expect(externalRequests, "mobile workspace attempted an external browser request").toEqual([]);
});

async function resolveWorkspaceRoute(context: APIRequestContext, session: AuthSession): Promise<WorkspaceRoute> {
  const headers = { Authorization: `Bearer ${session.token}` };
  const teamsResponse = await context.get("/api/v4/users/me/teams", { headers });
  if (!teamsResponse.ok()) {
    throw new Error(`visual regression team lookup failed: ${teamsResponse.status()} ${await teamsResponse.text()}`);
  }
  const teams = await teamsResponse.json() as Array<{ id: string; delete_at?: number }>;
  const activeTeams = teams.filter((team) => (team.delete_at ?? 0) === 0);
  if (activeTeams.length === 0) throw new Error("visual regression requires an accessible active team");

  for (const team of activeTeams) {
    const channelsResponse = await context.get(
      `/api/v4/users/me/teams/${encodeURIComponent(team.id)}/channels`,
      { headers },
    );
    if (!channelsResponse.ok()) {
      throw new Error(`visual regression channel lookup failed: ${channelsResponse.status()} ${await channelsResponse.text()}`);
    }
    const channels = await channelsResponse.json() as Array<{
      id: string;
      name: string;
      display_name: string;
      delete_at?: number;
    }>;
    const activeChannels = channels.filter((channel) => (channel.delete_at ?? 0) === 0);
    const channel = activeChannels.find((item) => item.name === "town-square") ?? activeChannels[0];
    if (channel) {
      return {
        teamID: team.id,
        channelID: channel.id,
        channelName: channel.display_name || channel.name,
      };
    }
  }
  throw new Error("visual regression requires an accessible active channel");
}

async function prepareAuthenticatedPage(page: Page, theme: VisualTheme): Promise<string[]> {
  const externalRequests: string[] = [];
  const applicationOrigin = new URL(baseURL).origin;
  const applicationHost = new URL(baseURL).host;
  const preferencePath = `/api/v4/users/${encodeURIComponent(auth.user.id)}/preferences/display_settings`;

  await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
  await page.addInitScript(({ key, session, fixedTheme }) => {
    window.sessionStorage.setItem(key, JSON.stringify(session));
    window.localStorage.setItem("moyro:theme", fixedTheme);
  }, { key: authStorageKey, session: auth, fixedTheme: theme });

  page.on("request", (request) => {
    const requestURL = new URL(request.url());
    if (["http:", "https:", "ws:", "wss:"].includes(requestURL.protocol) && requestURL.host !== applicationHost) {
      externalRequests.push(`${request.method()} ${request.url()}`);
    }
  });

  await page.route("**/*", async (route) => {
    const request = route.request();
    const requestURL = new URL(request.url());
    if (["http:", "https:"].includes(requestURL.protocol) && requestURL.origin !== applicationOrigin) {
      await route.abort("blockedbyclient");
      return;
    }
    if (request.method() === "GET" && requestURL.pathname === preferencePath) {
      const response = await route.fetch();
      if (!response.ok()) {
        await route.fulfill({ response });
        return;
      }
      const preferences = await response.json() as Array<{
        user_id: string;
        category: string;
        name: string;
        value: string;
      }>;
      await route.fulfill({
        response,
        json: [
          ...preferences.filter((preference) => preference.name !== "theme"),
          {
            user_id: auth.user.id,
            category: "display_settings",
            name: "theme",
            value: theme,
          },
        ],
      });
      return;
    }
    await route.continue();
  });
  return externalRequests;
}

async function openStableRoute(
  page: Page,
  path: string,
  heading: string | RegExp,
  headingSelector?: string,
) {
  await page.goto(path, { waitUntil: "domcontentloaded" });
  const marker = headingSelector
    ? page.locator(headingSelector).filter({ hasText: heading }).first()
    : page.getByRole("heading", { name: heading }).first();
  await expect(marker).toBeVisible();
  await expect(page.locator(".flow-loading:visible, [role='progressbar']:visible")).toHaveCount(0);
  await page.addStyleTag({ content: snapshotStabilizationCSS });
  await page.evaluate(async () => {
    await document.fonts.ready;
    window.scrollTo(0, 0);
  });
  await page.waitForTimeout(150);
}

function snapshotOptions(theme: VisualTheme, mask: Locator[], fullPage: boolean) {
  return {
    animations: "disabled" as const,
    caret: "hide" as const,
    fullPage,
    mask,
    maskColor: theme === "dark" ? "#30343D" : "#E2E5EB",
    maxDiffPixelRatio: 0.002,
    scale: "css" as const,
    timeout: 15_000,
  };
}

const snapshotStabilizationCSS = `
  html { scroll-behavior: auto !important; }
  *, *::before, *::after {
    animation-delay: 0s !important;
    animation-duration: 0s !important;
    transition-delay: 0s !important;
    transition-duration: 0s !important;
    caret-color: transparent !important;
  }
  .MuiSkeleton-root,
  .MuiSnackbar-root,
  .reminder-toast-stack,
  .typing-indicator,
  [role="tooltip"] {
    visibility: hidden !important;
  }
`;
