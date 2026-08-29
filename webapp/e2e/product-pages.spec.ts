import { expect, test, type APIRequestContext, type APIResponse, type Page } from "@playwright/test";
import path from "node:path";

const baseURL = process.env.MOYRO_BASE_URL ?? "http://127.0.0.1:8065";
const adminLogin = process.env.MOYRO_ADMIN ?? "admin@moyro.local";
const adminPassword = process.env.MOYRO_ADMIN_PASSWORD ?? "MoyroRelease!2026";
const screenshotDir = path.resolve(
  import.meta.dirname,
  process.env.MOYRO_CAPTURE_DIR ?? "../../docs/assets/screenshots",
);
const authStorageKey = "moyro.auth.session";

type AuthSession = {
  token: string;
  user: { id: string; username: string; email: string; roles?: string };
};

type SeedState = {
  teamId: string;
  channelId: string;
};

let api: APIRequestContext;
let auth: AuthSession;
let invitedAuth: AuthSession;
let seed: SeedState;

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ playwright }) => {
  api = await playwright.request.newContext({ baseURL });
  const login = await api.post("/api/v4/users/login", {
    data: { login_id: adminLogin, password: adminPassword, device_id: "playwright-release-capture" },
  });
  if (!login.ok()) {
    throw new Error(`bootstrap login failed: ${login.status()} ${await login.text()}`);
  }
  auth = await login.json() as AuthSession;
  seed = await seedProductData(api, auth);
});

test.afterAll(async () => {
  await api?.dispose();
});

test("public login page exposes the service version", async ({ page }) => {
  const issues = collectRuntimeIssues(page);
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "moyro" })).toBeVisible();
  await expect(page.locator(".login-logo[aria-hidden='true']")).toBeVisible();
  await expect(page.getByText(/^moyro v/)).toBeVisible();
  await settle(page);
  await capture(page, "login.jpg");
  assertNoRuntimeIssues(issues, "/login");
});

test("security boundaries reject invite races, cross-channel replies, and non-owner deletes", async () => {
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10000)}`;
  const adminFetch = (method: string, requestPath: string, data?: unknown) => api.fetch(requestPath, {
    method,
    headers: { Authorization: `Bearer ${auth.token}` },
    data,
  });

  const inviteResponse = await adminFetch("POST", `/api/v4/teams/${seed.teamId}/invites`, {
    max_uses: 1,
    ttl_seconds: 3600,
  });
  expect(inviteResponse.status()).toBe(201);
  const invite = await inviteResponse.json() as { id: string };
  const candidates = [0, 1].map((index) => ({
    username: `invitee-${suffix}-${index}`,
    email: `invitee-${suffix}-${index}@example.invalid`,
    password: `MoyroInvite!${suffix}-${index}`,
    invite_id: invite.id,
  }));
  const registrations = await Promise.all(candidates.map((candidate) => api.post("/api/v4/users", { data: candidate })));
  expect(registrations.map((response) => response.status()).sort()).toEqual([201, 400]);
  const winnerIndex = registrations.findIndex((response) => response.status() === 201);
  expect(winnerIndex).toBeGreaterThanOrEqual(0);

  const login = await api.post("/api/v4/users/login", {
    data: { login_id: candidates[winnerIndex].email, password: candidates[winnerIndex].password },
  });
  expect(login.status()).toBe(201);
  invitedAuth = await login.json() as AuthSession;

  const ownedPostResponse = await adminFetch("POST", "/api/v4/posts", {
    channel_id: seed.channelId,
    message: `ownership boundary ${suffix}`,
    root_id: "",
    file_ids: [],
  });
  expect(ownedPostResponse.status()).toBe(201);
  const ownedPost = await ownedPostResponse.json() as { id: string };
  const foreignDelete = await api.delete(`/api/v4/posts/${ownedPost.id}`, {
    headers: { Authorization: `Bearer ${invitedAuth.token}` },
  });
  expect(foreignDelete.status()).toBe(403);
  expect((await adminFetch("GET", `/api/v4/posts/${ownedPost.id}`)).status()).toBe(200);

  const channelResponse = await adminFetch("POST", "/api/v4/channels", {
    team_id: seed.teamId,
    name: `reply-boundary-${suffix}`,
    display_name: "Reply boundary",
    type: "P",
  });
  expect(channelResponse.status()).toBe(201);
  const otherChannel = await channelResponse.json() as { id: string };
  const crossChannelReply = await adminFetch("POST", "/api/v4/posts", {
    channel_id: otherChannel.id,
    root_id: ownedPost.id,
    message: "must be rejected",
    file_ids: [],
  });
  expect(crossChannelReply.status()).toBe(400);
  expect((await adminFetch("DELETE", `/api/v4/posts/${ownedPost.id}`)).status()).toBe(200);
  expect((await adminFetch("DELETE", `/api/v4/channels/${otherChannel.id}`)).status()).toBe(200);
});

test("personal navigation and refreshed routes follow mutable permissions", async ({ page }) => {
  expect(invitedAuth?.token).toBeTruthy();
  const headers = { Authorization: `Bearer ${auth.token}` };
  const rolesResponse = await api.get("/api/moyro/v1/admin/roles", { headers });
  expect(rolesResponse.status()).toBe(200);
  const roles = await rolesResponse.json() as Array<{
    id: string;
    name: string;
    permissions: string[];
    revision: number;
  }>;
  const systemUser = roles.find((role) => role.name === "system_user");
  expect(systemUser).toBeTruthy();
  if (!systemUser) return;
  const narrowed = systemUser.permissions.filter((permission) => ![
    "manage_own_api_keys",
    "use_ai",
  ].includes(permission));
  const patch = await api.patch(`/api/moyro/v1/admin/roles/${systemUser.id}`, {
    headers,
    data: { permissions: narrowed, revision: systemUser.revision },
  });
  expect(patch.status()).toBe(200);
  const changed = await patch.json() as { revision: number };

  try {
    await installAuthenticatedSession(page, invitedAuth);
    await page.goto("/settings/developer/keys");
    await expect(page).toHaveURL(/\/settings\/profile$/);
    await expect(page.getByRole("navigation", { name: "개인 설정 메뉴" }).getByText("개인 키")).toHaveCount(0);
    await expect(page.getByRole("navigation", { name: "개인 설정 메뉴" }).getByText("AI 개인화")).toHaveCount(0);
    await page.goto("/settings/ai");
    await expect(page).toHaveURL(/\/settings\/profile$/);
  } finally {
    const restore = await api.patch(`/api/moyro/v1/admin/roles/${systemUser.id}`, {
      headers,
      data: { permissions: systemUser.permissions, revision: changed.revision },
    });
    expect(restore.status()).toBe(200);
  }
});

test("approval decisions are idempotent and execute protected writes exactly once", async () => {
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10000)}`;
  const adminHeaders = { Authorization: `Bearer ${auth.token}` };
  const reviewerHeaders = { Authorization: `Bearer ${invitedAuth.token}` };
  let keyID = "";

  type ApprovalPolicy = {
    id?: string;
    name: string;
    enabled: boolean;
    protected_actions: string[];
    reviewer_roles: string[];
    require_rejection_reason: boolean;
    allow_self_approval: boolean;
    expires_after_hours: number;
  };
  const policyResponse = await api.get("/api/moyro/v1/admin/approval-policies", {
    headers: adminHeaders,
  });
  expect(policyResponse.status()).toBe(200);
  const policies = await policyResponse.json() as ApprovalPolicy[];
  expect(policies).toHaveLength(1);
  const originalPolicy = policies[0];

  const roleCatalogResponse = await api.get("/api/moyro/v1/admin/roles", { headers: adminHeaders });
  expect(roleCatalogResponse.status()).toBe(200);
  const roleCatalog = await roleCatalogResponse.json() as Array<{ name: string; permissions: string[] }>;
  expect(roleCatalog.find((role) => role.name === "team_lead")?.permissions).toContain("review_approval");

  const teamLeadResponse = await api.put(
    `/api/v4/teams/${seed.teamId}/members/${invitedAuth.user.id}/roles`,
    { headers: adminHeaders, data: { roles: "team_user team_lead" } },
  );
  expect(teamLeadResponse.status()).toBe(200);
  const forbidSelfResponse = await api.post("/api/moyro/v1/admin/approval-policies", {
    headers: adminHeaders,
    data: { ...originalPolicy, allow_self_approval: false },
  });
  expect(forbidSelfResponse.status()).toBe(200);

  type MCPSettings = {
    enabled: boolean;
    transport: string;
    endpoint_path: string;
    allowed_tools: string[];
    allowed_resources: string[];
    required_scopes: string[];
  };
  const mcpSettingsResponse = await api.get("/api/moyro/v1/admin/settings/mcp", {
    headers: adminHeaders,
  });
  expect(mcpSettingsResponse.status()).toBe(200);
  const originalMCPSettings = await mcpSettingsResponse.json() as MCPSettings;
  const enableMCP = await api.patch("/api/moyro/v1/admin/settings/mcp", {
    headers: adminHeaders,
    data: { ...originalMCPSettings, enabled: true },
  });
  expect(enableMCP.status()).toBe(200);

  const createdKeyResponse = await api.post("/api/moyro/v1/me/api-keys", {
    headers: adminHeaders,
    data: {
      name: `approval-e2e-${suffix}`,
      scopes: ["mcp_read", "mcp_write", "request_approval"],
      ttl_days: 1,
    },
  });
  expect(createdKeyResponse.status()).toBe(201);
  const createdKey = await createdKeyResponse.json() as { id: string; secret: string };
  keyID = createdKey.id;
  expect(createdKey.secret).toMatch(/^moyro_/);
  const keyHeaders = { Authorization: `Bearer ${createdKey.secret}` };

  type ApprovalSubmission = {
    approval_required: boolean;
    request: { id: string; status: string; idempotency_key: string };
  };
  const submit = (message: string, idempotencyKey: string) => api.post(
    "/api/moyro/v1/me/approval-requests",
    {
      headers: keyHeaders,
      data: {
        action_type: "mcp.create_post",
        team_id: seed.teamId,
        resource_type: "channel",
        resource_id: seed.channelId,
        idempotency_key: idempotencyKey,
        payload: { channel_id: seed.channelId, message },
      },
    },
  );

  try {
    const approvedMessage = `approval execution ${suffix}`;
    const approvedIdempotencyKey = `approval-execute-${suffix}`;
    const firstResponse = await submit(approvedMessage, approvedIdempotencyKey);
    expect(firstResponse.status()).toBe(200);
    const first = await firstResponse.json() as ApprovalSubmission;
    expect(first.approval_required).toBe(true);
    expect(first.request.status).toBe("pending");
    expect(first.request.idempotency_key).toBe(approvedIdempotencyKey);

    const replayResponse = await submit(approvedMessage, approvedIdempotencyKey);
    expect(replayResponse.status()).toBe(200);
    const replay = await replayResponse.json() as ApprovalSubmission;
    expect(replay.request.id).toBe(first.request.id);

    const requesterQueueResponse = await api.get("/api/moyro/v1/reviews/approval-requests", {
      headers: adminHeaders,
    });
    expect(requesterQueueResponse.status()).toBe(200);
    const requesterQueue = await requesterQueueResponse.json() as Array<{ id: string }>;
    expect(requesterQueue.some((request) => request.id === first.request.id)).toBe(false);
    const reviewerQueueResponse = await api.get("/api/moyro/v1/reviews/approval-requests", {
      headers: reviewerHeaders,
    });
    expect(reviewerQueueResponse.status()).toBe(200);
    const reviewerQueue = await reviewerQueueResponse.json() as Array<{ id: string }>;
    expect(reviewerQueue.some((request) => request.id === first.request.id)).toBe(true);

    const selfApproveResponse = await api.post(
      `/api/moyro/v1/reviews/approval-requests/${first.request.id}/decision`,
      { headers: adminHeaders, data: { decision: "approve", reason: "must be a separate reviewer" } },
    );
    await expectMattermostError(selfApproveResponse, 403);

    const approveResponse = await api.post(
      `/api/moyro/v1/reviews/approval-requests/${first.request.id}/decision`,
      { headers: reviewerHeaders, data: { decision: "approve", reason: "release E2E" } },
    );
    const approveBody = await approveResponse.text();
    expect(approveResponse.status(), approveBody).toBe(200);
    const approved = JSON.parse(approveBody) as { id: string; status: string; executed_at: number };
    expect(approved.id).toBe(first.request.id);
    expect(approved.status).toBe("executed");
    expect(approved.executed_at).toBeGreaterThan(0);

    const duplicateApprove = await api.post(
      `/api/moyro/v1/reviews/approval-requests/${first.request.id}/decision`,
      { headers: reviewerHeaders, data: { decision: "approve", reason: "duplicate" } },
    );
    await expectMattermostError(duplicateApprove, 400);

    const executedReplayResponse = await submit(approvedMessage, approvedIdempotencyKey);
    expect(executedReplayResponse.status()).toBe(200);
    const executedReplay = await executedReplayResponse.json() as ApprovalSubmission;
    expect(executedReplay.request.id).toBe(first.request.id);
    expect(executedReplay.request.status).toBe("executed");

    const postsResponse = await api.get(
      `/api/v4/channels/${seed.channelId}/posts?page=0&per_page=200`,
      { headers: adminHeaders },
    );
    expect(postsResponse.status()).toBe(200);
    const posts = await postsResponse.json() as {
      order: string[];
      posts: Record<string, { id: string; message: string; props?: Record<string, unknown> }>;
    };
    const executedPosts = Object.values(posts.posts).filter((post) => post.message === approvedMessage);
    expect(executedPosts).toHaveLength(1);
    expect(executedPosts[0].props?.approval_request_id).toBe(first.request.id);

    const rejectedMessage = `rejected approval ${suffix}`;
    const rejectedResponse = await submit(rejectedMessage, `approval-reject-${suffix}`);
    expect(rejectedResponse.status()).toBe(200);
    const rejectedSubmission = await rejectedResponse.json() as ApprovalSubmission;
    const rejectDecision = await api.post(
      `/api/moyro/v1/reviews/approval-requests/${rejectedSubmission.request.id}/decision`,
      { headers: reviewerHeaders, data: { decision: "reject", reason: "release rejection path" } },
    );
    expect(rejectDecision.status()).toBe(200);
    expect((await rejectDecision.json() as { status: string }).status).toBe("rejected");

    const duplicateReject = await api.post(
      `/api/moyro/v1/reviews/approval-requests/${rejectedSubmission.request.id}/decision`,
      { headers: reviewerHeaders, data: { decision: "reject", reason: "duplicate" } },
    );
    await expectMattermostError(duplicateReject, 400);

    const afterRejectResponse = await api.get(
      `/api/v4/channels/${seed.channelId}/posts?page=0&per_page=200`,
      { headers: adminHeaders },
    );
    expect(afterRejectResponse.status()).toBe(200);
    const afterReject = await afterRejectResponse.json() as {
      posts: Record<string, { message: string }>;
    };
    expect(Object.values(afterReject.posts).filter((post) => post.message === rejectedMessage)).toHaveLength(0);
    const cleanupExecuted = await api.delete(`/api/v4/posts/${executedPosts[0].id}`, {
      headers: adminHeaders,
    });
    expect(cleanupExecuted.status()).toBe(200);
  } finally {
    const restorePolicy = await api.post("/api/moyro/v1/admin/approval-policies", {
      headers: adminHeaders,
      data: originalPolicy,
    });
    expect(restorePolicy.status()).toBe(200);
    const restoreTeamRole = await api.put(
      `/api/v4/teams/${seed.teamId}/members/${invitedAuth.user.id}/roles`,
      { headers: adminHeaders, data: { roles: "team_user" } },
    );
    expect(restoreTeamRole.status()).toBe(200);
    const restoreMCP = await api.patch("/api/moyro/v1/admin/settings/mcp", {
      headers: adminHeaders,
      data: originalMCPSettings,
    });
    expect(restoreMCP.status()).toBe(200);
    if (keyID) {
      const revoke = await api.delete(`/api/moyro/v1/me/api-keys/${keyID}`, { headers: adminHeaders });
      expect(revoke.status()).toBe(204);
    }
  }
});

test("logout revokes the HTTP session immediately and closes its live WebSocket", async ({ page }) => {
  const login = await api.post("/api/v4/users/login", {
    data: { login_id: adminLogin, password: adminPassword, device_id: "playwright-logout-boundary" },
  });
  expect(login.status()).toBe(201);
  const disposable = await login.json() as AuthSession;

  await page.goto("/login");
  const lifecycle = await page.evaluate(async (token) => {
    const socket = new WebSocket(
      `${window.location.protocol === "https:" ? "wss" : "ws"}://${window.location.host}/api/v4/websocket`,
    );
    const closed = new Promise<number>((resolve) => {
      socket.addEventListener("close", () => resolve(performance.now()), { once: true });
    });
    await new Promise<void>((resolve, reject) => {
      const timer = window.setTimeout(() => reject(new Error("WebSocket authentication timed out")), 5_000);
      socket.addEventListener("open", () => {
        socket.send(JSON.stringify({
          seq: 1,
          action: "authentication_challenge",
          data: { token },
        }));
      }, { once: true });
      socket.addEventListener("message", (event) => {
        const reply = JSON.parse(String(event.data)) as { status?: string; seq_reply?: number };
        if (reply.status === "OK" && reply.seq_reply === 1) {
          window.clearTimeout(timer);
          resolve();
        }
      });
      socket.addEventListener("error", () => reject(new Error("WebSocket authentication failed")), { once: true });
    });

    const logoutStarted = performance.now();
    const logout = await fetch("/api/v4/users/logout", {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
    });
    const afterLogout = await fetch("/api/v4/users/me", {
      headers: { Authorization: `Bearer ${token}` },
    });
    const closedAt = await Promise.race([
      closed,
      new Promise<number>((resolve) => window.setTimeout(() => resolve(-1), 5_000)),
    ]);
    if (socket.readyState !== WebSocket.CLOSED) socket.close();
    return {
      logoutStatus: logout.status,
      afterLogoutStatus: afterLogout.status,
      websocketClosed: closedAt >= 0,
      websocketCloseMs: closedAt >= 0 ? closedAt - logoutStarted : -1,
    };
  }, disposable.token);

  expect(lifecycle.logoutStatus).toBe(200);
  expect(lifecycle.afterLogoutStatus).toBe(401);
  expect(lifecycle.websocketClosed).toBe(true);
  expect(lifecycle.websocketCloseMs).toBeGreaterThanOrEqual(0);
  expect(lifecycle.websocketCloseMs).toBeLessThan(5_000);

  const revokedToken = await api.get("/api/v4/users/me", {
    headers: { Authorization: `Bearer ${disposable.token}` },
  });
  await expectMattermostError(revokedToken, 401);
});

test("core REST contracts preserve 400, 401, 403, 404 and offset pagination", async () => {
  const adminHeaders = { Authorization: `Bearer ${auth.token}` };

  const unauthenticated = await api.get("/api/v4/users/me");
  await expectMattermostError(unauthenticated, 401);

  const invalidSearch = await api.post(`/api/v4/teams/${seed.teamId}/posts/search`, {
    headers: adminHeaders,
    data: { terms: "", page: 0, per_page: 20 },
  });
  await expectMattermostError(invalidSearch, 400);

  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10000)}`;
  const privateChannelResponse = await api.post("/api/v4/channels", {
    headers: adminHeaders,
    data: {
      team_id: seed.teamId,
      name: `contract-private-${suffix}`,
      display_name: "Contract private",
      type: "P",
    },
  });
  expect(privateChannelResponse.status()).toBe(201);
  const privateChannel = await privateChannelResponse.json() as { id: string };
  const forbidden = await api.get(`/api/v4/channels/${privateChannel.id}`, {
    headers: { Authorization: `Bearer ${invitedAuth.token}` },
  });
  await expectMattermostError(forbidden, 403);

  const missing = await api.get(`/api/v4/posts/missing-${suffix}`, { headers: adminHeaders });
  await expectMattermostError(missing, 404);

  const createdIDs: string[] = [];
  for (let index = 0; index < 4; index += 1) {
    const response = await api.post("/api/v4/posts", {
      headers: adminHeaders,
      data: {
        channel_id: seed.channelId,
        message: `pagination contract ${suffix} ${index}`,
        root_id: "",
        file_ids: [],
      },
    });
    expect(response.status()).toBe(201);
    createdIDs.push((await response.json() as { id: string }).id);
    await new Promise((resolve) => setTimeout(resolve, 4));
  }

  type PostPage = { order: string[]; posts: Record<string, { id: string }> };
  const firstPageResponse = await api.get(
    `/api/v4/channels/${seed.channelId}/posts?page=0&per_page=2`,
    { headers: adminHeaders },
  );
  const secondPageResponse = await api.get(
    `/api/v4/channels/${seed.channelId}/posts?page=1&per_page=2`,
    { headers: adminHeaders },
  );
  expect(firstPageResponse.status()).toBe(200);
  expect(secondPageResponse.status()).toBe(200);
  const firstPage = await firstPageResponse.json() as PostPage;
  const secondPage = await secondPageResponse.json() as PostPage;
  expect(firstPage.order).toHaveLength(2);
  expect(secondPage.order).toHaveLength(2);
  for (const id of [...firstPage.order, ...secondPage.order]) {
    expect((firstPage.posts[id] ?? secondPage.posts[id])?.id).toBe(id);
  }
  expect(new Set([...firstPage.order, ...secondPage.order]).size).toBe(4);
  expect([...firstPage.order, ...secondPage.order].sort()).toEqual([...createdIDs].sort());

  const emptyPageResponse = await api.get(
    `/api/v4/channels/${seed.channelId}/posts?page=1000000&per_page=2`,
    { headers: adminHeaders },
  );
  expect(emptyPageResponse.status()).toBe(200);
  const emptyPage = await emptyPageResponse.json() as PostPage;
  expect(emptyPage.order).toEqual([]);
  expect(emptyPage.posts).toEqual({});

  for (const id of createdIDs) {
    const cleanup = await api.delete(`/api/v4/posts/${id}`, { headers: adminHeaders });
    expect(cleanup.status()).toBe(200);
  }
  const cleanupPrivateChannel = await api.delete(`/api/v4/channels/${privateChannel.id}`, {
    headers: adminHeaders,
  });
  expect(cleanupPrivateChannel.status()).toBe(200);
});

const routedPages = [
  { path: () => `/workspace/${seed.teamId}/channel/${seed.channelId}`, file: "workspace-channel.jpg", marker: "moyro 릴리스" },
  { path: () => `/workspace/${seed.teamId}/saved`, file: "workspace-saved.jpg", marker: "저장" },
  { path: () => `/workspace/${seed.teamId}/scheduled`, file: "workspace-scheduled.jpg", marker: "예약" },
  { path: () => "/settings/profile", file: "settings-profile.jpg", marker: "프로필" },
  { path: () => "/settings/appearance", file: "settings-appearance.jpg", marker: "화면" },
  { path: () => "/settings/notifications", file: "settings-notifications.jpg", marker: "알림" },
  { path: () => "/settings/security/sessions", file: "settings-sessions.jpg", marker: "세션" },
  { path: () => "/settings/developer/keys", file: "settings-keys.jpg", marker: "개인 키" },
  { path: () => "/settings/ai", file: "settings-ai.jpg", marker: "AI 개인화" },
  { path: () => "/settings/approvals/mine", file: "settings-approvals-mine.jpg", marker: "내 승인 요청" },
  { path: () => "/settings/approvals/review", file: "settings-approvals-review.jpg", marker: "검토 대기" },
  { path: () => "/admin/overview", file: "admin-overview.jpg", marker: "관리 개요" },
  { path: () => "/admin/site", file: "admin-site.jpg", marker: "사이트 설정" },
  { path: () => "/admin/auth/keycloak", file: "admin-keycloak.jpg", marker: "Keycloak SSO" },
  { path: () => "/admin/ai/providers", file: "admin-ai.jpg", marker: "AI 공급자" },
  { path: () => "/admin/security/keys", file: "admin-key-policy.jpg", marker: "키 정책" },
  { path: () => "/admin/integrations/mcp", file: "admin-mcp.jpg", marker: "MCP" },
  { path: () => "/admin/workflows/review", file: "admin-approval.jpg", marker: "검토 · 승인" },
] as const;

for (const route of routedPages) {
  test(`${route.file} renders and survives refresh`, async ({ page }) => {
    await installAuthenticatedSession(page, auth);
    const issues = collectRuntimeIssues(page);
    const target = route.path();
    await page.goto(target);
    await expect(page.getByText(route.marker, { exact: false }).first()).toBeVisible();
    await expect(page.locator(".side-brand-logo, .moyro-mark").first()).toBeVisible();
    await settle(page);
    expect(new URL(page.url()).pathname).toBe(target);
    await page.reload();
    await expect(page.getByText(route.marker, { exact: false }).first()).toBeVisible();
    await settle(page);
    expect(new URL(page.url()).pathname).toBe(target);
    await capture(page, route.file);
    assertNoRuntimeIssues(issues, target);
  });
}

test("profile context menu shows version, admin and optional approval entries", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  await page.getByRole("button", { name: "계정 메뉴 열기" }).click();
  await expect(page.getByLabel(/서비스 버전/)).toBeVisible();
  await expect(page.locator(".user-menu-version-brand svg[aria-hidden='true']")).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /서비스 관리/ })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /내 승인 요청/ })).toBeVisible();
  await capture(page, "workspace-profile-menu.jpg");
  assertNoRuntimeIssues(issues, "profile context menu");
});

test("legacy operations console renders without browser errors", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto("/admin/operations");
  await expect(page.locator("body")).toContainText(/관리|시스템|운영/);
  await settle(page);
  expect(new URL(page.url()).pathname).toBe("/admin/operations");
  await capture(page, "admin-operations.jpg");
  assertNoRuntimeIssues(issues, "/admin/operations");
});

test("representative pages remain usable at a mobile viewport", async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 932 });
  const publicIssues = collectRuntimeIssues(page);
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "moyro" })).toBeVisible();
  await expect(page.locator(".login-logo[aria-hidden='true']")).toBeVisible();
  await settle(page);
  await capture(page, "mobile-login.jpg");
  assertNoRuntimeIssues(publicIssues, "mobile /login");

  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  for (const [target, file, marker] of [
    [`/workspace/${seed.teamId}/channel/${seed.channelId}`, "mobile-workspace.jpg", "moyro 릴리스"],
    ["/settings/profile", "mobile-settings-profile.jpg", "프로필"],
    ["/admin/site", "mobile-admin-site.jpg", "사이트 설정"],
  ] as const) {
    await page.goto(target);
    await expect(page.getByText(marker, { exact: false }).first()).toBeVisible();
    await settle(page);
    const layout = await page.evaluate(() => ({
      viewport: window.innerWidth,
      document: document.documentElement.scrollWidth,
    }));
    expect(layout.document, `${target} overflows the mobile viewport`).toBeLessThanOrEqual(layout.viewport);
    await capture(page, file);
  }
  assertNoRuntimeIssues(issues, "mobile authenticated pages");
});

async function installAuthenticatedSession(page: Page, session: AuthSession) {
  await page.addInitScript(({ key, value }) => {
    window.sessionStorage.setItem(key, JSON.stringify(value));
  }, { key: authStorageKey, value: session });
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

async function expectMattermostError(response: APIResponse, status: number) {
  expect(response.status()).toBe(status);
  expect(response.headers()["content-type"]).toContain("application/json");
  const body = await response.json() as { id?: string; message?: string; status_code?: number };
  expect(body.status_code).toBe(status);
  expect(body.id).toBeTruthy();
  expect(body.message).toBeTruthy();
}

async function seedProductData(context: APIRequestContext, session: AuthSession): Promise<SeedState> {
  const jsonWithBearer = async <T>(bearer: string, method: string, requestPath: string, data?: unknown): Promise<T> => {
    const response = await context.fetch(requestPath, {
      method,
      headers: { Authorization: `Bearer ${bearer}` },
      data,
    });
    if (!response.ok()) {
      throw new Error(`${method} ${requestPath}: ${response.status()} ${await response.text()}`);
    }
    return await response.json() as T;
  };
  const json = <T>(method: string, requestPath: string, data?: unknown) =>
    jsonWithBearer<T>(session.token, method, requestPath, data);

  const teams = await json<Array<{ id: string }>>("GET", "/api/v4/users/me/teams");
  expect(teams.length).toBeGreaterThan(0);
  const teamId = teams[0].id;
  const channels = await json<Array<{ id: string; name: string }>>(
    "GET",
    `/api/v4/users/me/teams/${teamId}/channels`,
  );
  expect(channels.length).toBeGreaterThan(0);
  const channel = channels.find((item) => item.name === "town-square") ?? channels[0];
  const channelId = channel.id;

  const list = await json<{ order: string[]; posts: Record<string, { id: string; message: string }> }>(
    "GET",
    `/api/v4/channels/${channelId}/posts?page=0&per_page=60`,
  );
  let seededPost = Object.values(list.posts).find((post) => post.message.includes("moyro 릴리스"));
  if (!seededPost) {
    const messages = [
      "moyro 릴리스 검증을 시작합니다. 채널과 스레드의 맥락을 한 화면에서 확인하세요.",
      "오프라인 배포 이미지는 웹 UI와 Go 서비스를 함께 담고 있습니다.",
      "관리 설정은 사이트, Keycloak SSO, AI, 키 정책, MCP, 승인 흐름으로 분리됩니다.",
      "읽기 쉬운 글자 크기와 새로고침 후에도 유지되는 URL 경로를 기본으로 제공합니다.",
    ];
    for (const message of messages) {
      const post = await json<{ id: string; message: string }>("POST", "/api/v4/posts", {
        channel_id: channelId,
        message,
        root_id: "",
        file_ids: [],
      });
      seededPost ??= post;
    }
  }
  if (seededPost) {
    await json("POST", `/api/v4/users/me/saved_posts/${seededPost.id}`);
  }

  const scheduled = await json<Array<{ message: string }>>("GET", "/api/v4/users/me/scheduled_posts");
  if (!scheduled.some((item) => item.message.includes("오프라인 운영 점검"))) {
    await json("POST", "/api/v4/scheduled_posts", {
      channel_id: channelId,
      root_id: "",
      message: "내일 오전 오프라인 운영 점검 결과를 공유합니다.",
      file_ids: [],
      props: {},
      send_at: Date.now() + 24 * 60 * 60 * 1000,
    });
  }

  await json("PATCH", "/api/moyro/v1/admin/settings/site", {
    site_name: "moyro",
    public_base_url: baseURL,
    allowed_outgoing_hosts: [],
  });

  await json("POST", "/api/moyro/v1/admin/approval-policies", {
    name: "팀장 검토",
    enabled: true,
    protected_actions: ["mcp.create_post", "mcp.reply_to_thread"],
    reviewer_roles: ["team_lead", "system_admin"],
    require_rejection_reason: true,
    allow_self_approval: true,
    expires_after_hours: 72,
  });
  const approvals = await json<Array<{ id: string }>>("GET", "/api/moyro/v1/me/approval-requests");
  if (approvals.length === 0) {
    const keys = await json<Array<{ id: string; name: string }>>("GET", "/api/moyro/v1/me/api-keys");
    const existing = keys.find((key) => key.name === "릴리스 검증 키");
    const credential = existing
      ? await json<{ secret: string }>("POST", `/api/moyro/v1/me/api-keys/${existing.id}/rotate`)
      : await json<{ secret: string }>("POST", "/api/moyro/v1/me/api-keys", {
          name: "릴리스 검증 키",
          scopes: ["mcp_read", "mcp_write", "request_approval"],
          ttl_days: 90,
        });
    await jsonWithBearer(credential.secret, "POST", "/api/moyro/v1/me/approval-requests", {
      action_type: "mcp.create_post",
      team_id: teamId,
      resource_type: "channel",
      resource_id: channelId,
      idempotency_key: "release-capture-create-post",
      payload: {
        channel_id: channelId,
        message: "검토 승인 후 게시되는 운영 공지 예시입니다.",
      },
    });
  }
  return { teamId, channelId };
}
