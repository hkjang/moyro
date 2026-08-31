import { expect, test, type APIRequestContext, type APIResponse, type Page } from "@playwright/test";
import path from "node:path";

const baseURL = process.env.MOYRO_BASE_URL ?? "http://127.0.0.1:8065";
const adminLogin = process.env.MOYRO_ADMIN ?? "admin@moyro.local";
const adminPassword = process.env.MOYRO_ADMIN_PASSWORD ?? "MoyroRelease!2026";
const expectedRawVersion = (process.env.MOYRO_EXPECTED_VERSION ?? "").trim();
const expectedDisplayVersion = expectedRawVersion
  ? (expectedRawVersion.startsWith("v") ? expectedRawVersion : `v${expectedRawVersion}`)
  : "";
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
  postId: string;
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
  if (!expectedDisplayVersion) {
    throw new Error("MOYRO_EXPECTED_VERSION is required for product release verification");
  }
  await expect(page.getByText(`moyro ${expectedDisplayVersion}`, { exact: true })).toBeVisible();
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
    // The plugin runtime owns window.fetch and deliberately replaces/removes
    // caller credentials with the current Redux session. This boundary test
    // uses a disposable token while /login has no Redux session, so bypass
    // that plugin-only facade and exercise the browser HTTP contract directly.
    const sessionFetch = window.__moyro_plugin_fetch_original__ ?? window.fetch.bind(window);
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
    const logout = await sessionFetch("/api/v4/users/logout", {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
    });
    const afterLogout = await sessionFetch("/api/v4/users/me", {
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

type RoutedPage = {
  path: () => string;
  file: string;
  marker: string;
  verifyBrand?: boolean;
};

const routedPages: readonly RoutedPage[] = [
  { path: () => "/today", file: "today.jpg", marker: "오늘의 흐름" },
  { path: () => "/inbox/updates", file: "inbox-updates.jpg", marker: "통합 알림함" },
  { path: () => "/inbox/conversations", file: "inbox-conversations.jpg", marker: "통합 알림함" },
  { path: () => "/inbox/approvals", file: "inbox-approvals.jpg", marker: "통합 알림함" },
  { path: () => "/my-work/tasks", file: "my-work-tasks.jpg", marker: "내 업무" },
  { path: () => "/my-work/decisions", file: "my-work-decisions.jpg", marker: "내 업무" },
  { path: () => "/my-work/saved", file: "my-work-saved.jpg", marker: "내 업무" },
  { path: () => "/my-work/scheduled", file: "my-work-scheduled.jpg", marker: "내 업무" },
  { path: () => "/my-work/reminders", file: "my-work-reminders.jpg", marker: "내 업무" },
  { path: () => "/approvals/mine", file: "approvals-mine.jpg", marker: "승인 센터" },
  { path: () => "/approvals/review", file: "approvals-review.jpg", marker: "승인 센터" },
  { path: () => "/assistant", file: "ai-assistant.jpg", marker: "AI 대화" },
  { path: () => "/search", file: "global-search.jpg", marker: "메시지 검색" },
  { path: () => `/workspace/${seed.teamId}/channel/${seed.channelId}`, file: "workspace-channel.jpg", marker: "moyro 릴리스" },
  { path: () => "/settings/profile", file: "settings-profile.jpg", marker: "프로필" },
  { path: () => "/settings/appearance", file: "settings-appearance.jpg", marker: "화면" },
  { path: () => "/settings/notifications", file: "settings-notifications.jpg", marker: "알림" },
  { path: () => "/settings/security/sessions", file: "settings-sessions.jpg", marker: "세션" },
  { path: () => "/settings/developer/keys", file: "settings-keys.jpg", marker: "개인 키" },
  { path: () => "/settings/ai", file: "settings-ai.jpg", marker: "AI 개인화" },
  { path: () => "/admin/overview", file: "admin-overview.jpg", marker: "운영 현황" },
  { path: () => "/admin/site", file: "admin-site.jpg", marker: "사이트 설정" },
  { path: () => "/admin/auth/keycloak", file: "admin-keycloak.jpg", marker: "Keycloak SSO" },
  { path: () => "/admin/ai/providers", file: "admin-ai.jpg", marker: "AI 공급자" },
  { path: () => "/admin/security/keys", file: "admin-key-policy.jpg", marker: "키 정책" },
  { path: () => "/admin/integrations/mcp", file: "admin-mcp.jpg", marker: "MCP" },
  { path: () => "/admin/integrations/plugins", file: "admin-plugins.jpg", marker: "플러그인" },
  { path: () => "/admin/workflows/review", file: "admin-approval.jpg", marker: "검토 · 승인" },
  {
    path: () => "/admin/operations",
    file: "admin-operations.jpg",
    marker: "호환 운영 API",
    verifyBrand: false,
  },
];

test("the authenticated root and ProductShell navigation use the Flow routes", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto("/settings/profile");
  await expect(page.getByRole("heading", { name: "프로필", level: 1 })).toBeVisible();

  await page.goto("/");
  await expect.poll(() => new URL(page.url()).pathname).toBe("/today");
  await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();

  const navigation = page.getByRole("navigation", { name: "주요 기능" });
  for (const label of ["오늘", "알림함", "대화", "내 업무", "검색", "승인", "AI"]) {
    await expect(navigation.getByRole("button", { name: label, exact: true })).toBeVisible();
  }
  await expect(navigation.getByRole("button", { name: "오늘", exact: true })).toHaveAttribute("aria-current", "page");

  await page.goBack();
  await expect.poll(() => new URL(page.url()).pathname).toBe("/settings/profile");
  await page.goForward();
  await expect.poll(() => new URL(page.url()).pathname).toBe("/today");

  for (const destination of [
    { label: "알림함", path: "/inbox/updates" },
    { label: "대화", path: "/workspace", prefix: true },
    { label: "내 업무", path: "/my-work/tasks" },
    { label: "검색", path: "/search" },
    { label: "승인", path: "/approvals/mine" },
    { label: "AI", path: "/assistant" },
    { label: "오늘", path: "/today" },
  ] as const) {
    await navigation.getByRole("button", { name: destination.label, exact: true }).click();
    await expect.poll(() => {
      const pathname = new URL(page.url()).pathname;
      return "prefix" in destination && destination.prefix
        ? pathname.startsWith(destination.path)
        : pathname === destination.path;
    }, { message: `${destination.label} should navigate to ${destination.path}` }).toBe(true);
  }
  assertNoRuntimeIssues(issues, "authenticated root and ProductShell navigation");
});

test("Today uses one shared Flow read model and keeps it across Flow navigation", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const flowRequests: string[] = [];
  const todayDataPaths = new Set([
    "/api/moyro/v1/me/flow-summary",
    "/api/v4/users/me/saved_posts",
    "/api/v4/users/me/scheduled_posts",
    "/api/v4/users/me/reminders",
    "/api/moyro/v1/me/approval-requests",
    "/api/moyro/v1/reviews/approval-requests",
  ]);
  page.on("request", (request) => {
    const pathName = new URL(request.url()).pathname;
    if (todayDataPaths.has(pathName)) flowRequests.push(pathName);
  });

  await page.goto("/today");
  await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "새로고침" })).toBeEnabled();

  expect(flowRequests).toHaveLength(6);
  expect(flowRequests.filter((pathName) => pathName === "/api/moyro/v1/me/flow-summary")).toHaveLength(1);

  await page.getByRole("navigation", { name: "주요 기능" })
    .getByRole("button", { name: "알림함", exact: true })
    .click();
  await expect(page.getByRole("heading", { name: "통합 알림함" })).toBeVisible();
  expect(flowRequests.filter((pathName) => pathName === "/api/moyro/v1/me/flow-summary")).toHaveLength(1);
});

test("message search restores its query, team and page from a shareable URL", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  const target = `/search?q=moyro&team=${encodeURIComponent(seed.teamId)}&page=0`;
  await page.goto(target);
  await expect(page.getByRole("heading", { name: "메시지 검색" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "메시지 검색어" })).toHaveValue("moyro");
  const results = page.locator('section[aria-labelledby="global-search-results"]');
  await expect(results).toContainText("moyro 릴리스");
  await expect.poll(() => new URL(page.url()).searchParams.get("team")).toBe(seed.teamId);

  await page.reload();
  await expect(page.getByRole("textbox", { name: "메시지 검색어" })).toHaveValue("moyro");
  await expect(results).toContainText("moyro 릴리스");
  await expect.poll(() => new URL(page.url()).search).toBe(`?q=moyro&team=${encodeURIComponent(seed.teamId)}&page=0`);
  assertNoRuntimeIssues(issues, "shareable message search");
});

test("legacy and invalid Flow URLs replace redirect to canonical routes", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const redirects = [
    { legacy: `/workspace/${seed.teamId}/saved`, target: "/my-work/saved" },
    { legacy: `/workspace/${seed.teamId}/scheduled`, target: "/my-work/scheduled" },
    { legacy: "/settings/approvals/mine", target: "/approvals/mine" },
    { legacy: "/settings/approvals/review", target: "/approvals/review" },
    { legacy: "/inbox/not-a-tab", target: "/inbox/updates" },
    { legacy: "/my-work/not-a-tab", target: "/my-work/tasks" },
    { legacy: "/approvals/not-a-tab", target: "/approvals/mine" },
  ] as const;

  for (const redirect of redirects) {
    await page.goto("/today");
    await expect.poll(() => new URL(page.url()).pathname).toBe("/today");
    await page.goto(redirect.legacy);
    await expect.poll(() => new URL(page.url()).pathname).toBe(redirect.target);

    // A replace redirect removes the legacy URL from this history slot.
    await page.goBack();
    await expect.poll(() => new URL(page.url()).pathname).toBe("/today");
    await page.goForward();
    await expect.poll(() => new URL(page.url()).pathname).toBe(redirect.target);
  }
});

test("Flow tabs own labelled panels and settings layouts expose one main landmark", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  const contracts = [
    {
      path: "/inbox/conversations",
      tablist: "알림함 분류",
      prefix: "inbox",
      active: "conversations",
      values: ["updates", "conversations", "approvals", "reminders"],
    },
    {
      path: "/my-work/saved",
      tablist: "내 업무 분류",
      prefix: "my-work",
      active: "saved",
      values: ["tasks", "decisions", "saved", "scheduled", "reminders"],
    },
    {
      path: "/approvals/mine",
      tablist: "승인 센터 분류",
      prefix: "approval-center",
      active: "mine",
      values: ["mine", "review"],
    },
  ] as const;

  for (const contract of contracts) {
    await page.goto(contract.path);
    const tablist = page.getByRole("tablist", { name: contract.tablist });
    await expect(tablist).toBeVisible();
    await expect(tablist.getByRole("tab")).toHaveCount(contract.values.length);
    for (const value of contract.values) {
      const tabID = `${contract.prefix}-${value}-tab`;
      const panelID = `${contract.prefix}-${value}-panel`;
      const tab = page.locator(`#${tabID}`);
      const panel = page.locator(`#${panelID}`);
      await expect(tab).toHaveAttribute("role", "tab");
      await expect(tab).toHaveAttribute("aria-controls", panelID);
      await expect(tab).toHaveAttribute("aria-selected", value === contract.active ? "true" : "false");
      await expect(panel).toHaveAttribute("role", "tabpanel");
      await expect(panel).toHaveAttribute("aria-labelledby", tabID);
      if (value === contract.active) await expect(panel).toBeVisible();
      else await expect(panel).toBeHidden();
    }
  }

  for (const path of ["/settings/profile", "/admin/overview"] as const) {
    await page.goto(path);
    await expect(page.getByRole("main")).toHaveCount(1);
    await expect(page.getByRole("main")).toBeVisible();
  }
  assertNoRuntimeIssues(issues, "Flow tab panels and settings main landmarks");
});

test("Flow theme choices persist across today, appearance and refresh", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  const preferencePath = `/api/v4/users/${auth.user.id}/preferences`;

  try {
    await page.goto("/settings/appearance");
    await expect(page.getByRole("heading", { name: "화면", exact: true, level: 1 })).toBeVisible();

    await selectTheme(page, "밝게", "light");
    await expectFlowTheme(page, {
      mode: "light",
      brand: "#3157D5",
      page: "#F6F7F9",
    });

    await page.goto("/today");
    await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();
    await expectFlowTheme(page, {
      mode: "light",
      brand: "#3157D5",
      page: "#F6F7F9",
    });

    await page.goto("/settings/appearance");
    await page.reload();
    await expect(page.getByRole("radio", { name: "밝게" })).toBeChecked();
    await expectFlowTheme(page, {
      mode: "light",
      brand: "#3157D5",
      page: "#F6F7F9",
    });

    await selectTheme(page, "어둡게", "dark");
    await expectFlowTheme(page, {
      mode: "dark",
      brand: "#8FA6EE",
      page: "#16181D",
    });

    await page.goto("/today");
    await page.reload();
    await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();
    await expectFlowTheme(page, {
      mode: "dark",
      brand: "#8FA6EE",
      page: "#16181D",
    });

    await page.goto("/settings/appearance");
    await page.reload();
    await expect(page.getByRole("radio", { name: "어둡게" })).toBeChecked();
    await expectFlowTheme(page, {
      mode: "dark",
      brand: "#8FA6EE",
      page: "#16181D",
    });
    assertNoRuntimeIssues(issues, "Flow theme persistence");
  } finally {
    const restore = await api.put(preferencePath, {
      headers: { Authorization: `Bearer ${auth.token}` },
      data: [{
        user_id: auth.user.id,
        category: "display_settings",
        name: "theme",
        value: "system",
      }],
    });
    expect(restore.status()).toBe(200);
    if (!page.isClosed()) {
      await page.evaluate(() => {
        window.localStorage.setItem("moyro:theme", "system");
        const resolved = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
        document.documentElement.setAttribute("data-theme", resolved);
      });
    }
  }
});

test("mobile settings drawers navigate, close and stay within the viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);

  await page.goto("/settings/profile");
  await expectNoHorizontalOverflow(page, "/settings/profile");
  const personalMenuButton = page.getByRole("button", { name: "개인 설정 메뉴 열기" });
  await expect(personalMenuButton).toBeVisible();
  await personalMenuButton.click();
  const personalDrawer = page.locator("#personal-mobile-navigation");
  await expect(personalDrawer).toBeVisible();
  await expect(personalDrawer.getByRole("button", { name: "프로필", exact: true })).toHaveAttribute("aria-current", "page");
  await personalDrawer.getByRole("button", { name: "화면", exact: true }).click();
  await expect(page).toHaveURL(/\/settings\/appearance$/);
  await expect(personalDrawer).toBeHidden();
  await expect(personalMenuButton).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator(".personal-settings-current")).toContainText("화면");
  await expectNoHorizontalOverflow(page, "/settings/appearance");

  await page.goto("/admin/overview");
  await expectNoHorizontalOverflow(page, "/admin/overview");
  const adminMenuButton = page.getByRole("button", { name: "서비스 관리 메뉴 열기" });
  await expect(adminMenuButton).toBeVisible();
  await adminMenuButton.click();
  const adminDrawer = page.locator("#admin-mobile-navigation");
  await expect(adminDrawer).toBeVisible();
  await expect(adminDrawer.getByRole("button", { name: "운영 현황", exact: true })).toHaveAttribute("aria-current", "page");
  await adminDrawer.getByRole("button", { name: "사이트 설정", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/site$/);
  await expect(adminDrawer).toBeHidden();
  await expect(adminMenuButton).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator(".admin-settings-current")).toContainText("사이트 설정");
  await expectNoHorizontalOverflow(page, "/admin/site");

  assertNoRuntimeIssues(issues, "mobile settings drawers");
});

test("reduced motion removes ProductShell navigation transitions", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto("/today");
  await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();

  const motion = await page.locator(".product-nav-item").first().evaluate((element) => {
    const durations = getComputedStyle(element).transitionDuration
      .split(",")
      .map((value) => value.trim())
      .map((value) => value.endsWith("ms")
        ? Number.parseFloat(value)
        : Number.parseFloat(value) * 1000);
    return {
      reduced: window.matchMedia("(prefers-reduced-motion: reduce)").matches,
      durations,
    };
  });
  expect(motion.reduced).toBe(true);
  expect(motion.durations.length).toBeGreaterThan(0);
  expect(Math.max(...motion.durations)).toBeLessThanOrEqual(1);
  assertNoRuntimeIssues(issues, "reduced-motion ProductShell navigation");
});

test("ProductShell quick navigation opens with Ctrl+K and routes the selection", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto("/today");
  await expect(page.getByRole("heading", { name: /오늘의 흐름/ })).toBeVisible();

  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog", { name: /빠른 이동/ });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("textbox", { name: "화면 검색" }).fill("승인");
  const results = dialog.getByRole("list", { name: "빠른 이동 결과" });
  await expect(results.getByRole("button", { name: /승인 \/approvals$/ })).toBeVisible();
  await results.getByRole("button", { name: /승인 \/approvals$/ }).click();

  await expect(page).toHaveURL(/\/approvals\/mine$/);
  await expect(dialog).toBeHidden();
  assertNoRuntimeIssues(issues, "ProductShell quick navigation");
});

test("long Flow pages own vertical scrolling and expose their final section", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 720 });
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto("/today");
  const flowPage = page.locator(".flow-page");
  const finalSection = page.locator("#today-work");
  await expect(flowPage).toBeVisible();

  const before = await flowPage.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
    overflowY: getComputedStyle(element).overflowY,
  }));
  expect(before.overflowY).toBe("auto");
  expect(before.scrollHeight).toBeGreaterThan(before.clientHeight);
  await flowPage.evaluate((element) => element.scrollTo({ top: element.scrollHeight, behavior: "auto" }));
  await expect(finalSection).toBeInViewport();
  assertNoRuntimeIssues(issues, "Flow page internal scrolling");
});

test("workspace drafts stay destination-scoped and survive a failed send", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const headers = { Authorization: `Bearer ${auth.token}` };
  const channelsResponse = await api.get(`/api/v4/users/me/teams/${seed.teamId}/channels`, { headers });
  expect(channelsResponse.status()).toBe(200);
  const channels = await channelsResponse.json() as Array<{ id: string; display_name: string }>;
  const alternate = channels.find((channel) => channel.id !== seed.channelId);
  expect(alternate, "draft isolation requires two joined channels").toBeTruthy();
  if (!alternate) return;

  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  const composer = page.getByRole("textbox", { name: "메시지 입력" });
  await expect(composer).toBeVisible();
  const firstDraft = `첫 채널 전용 초안 ${Date.now()}`;
  await composer.fill(firstDraft);

  await page.getByRole("button", { name: new RegExp(alternate.display_name) }).first().click();
  await expect(page).toHaveURL(new RegExp(`/workspace/${seed.teamId}/channel/${alternate.id}$`));
  await expect(composer).toHaveValue("");

  const original = channels.find((channel) => channel.id === seed.channelId);
  expect(original).toBeTruthy();
  if (!original) return;
  await page.getByRole("button", { name: new RegExp(original.display_name) }).first().click();
  await expect(page).toHaveURL(new RegExp(`/workspace/${seed.teamId}/channel/${seed.channelId}$`));
  await expect(composer).toHaveValue(firstDraft);

  await page.route("**/api/v4/posts", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({
        id: "moyro.e2e.expected_send_failure",
        message: "expected send failure",
        status_code: 503,
      }),
    });
  });
  await page.getByRole("button", { name: "전송", exact: true }).click();
  await expect(page.locator(".composer-send-error")).toContainText("유지됩니다");
  await expect(composer).toHaveValue(firstDraft);

  // A successful send clears both the controlled value and its durable copy;
  // changing destinations afterwards must not resurrect the submitted text.
  await page.unroute("**/api/v4/posts");
  const sentResponsePromise = page.waitForResponse((response) =>
    response.request().method() === "POST"
    && new URL(response.url()).pathname === "/api/v4/posts",
  );
  await page.getByRole("button", { name: "전송", exact: true }).click();
  const sentResponse = await sentResponsePromise;
  expect(sentResponse.status()).toBe(201);
  const sentPost = await sentResponse.json() as { id: string };
  await expect(composer).toHaveValue("");
  const rootDraftKey = `moyro:draft:${auth.user.id}:${seed.channelId}:root`;
  await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), rootDraftKey)).toBeNull();

  await page.getByRole("button", { name: new RegExp(alternate.display_name) }).first().click();
  await page.getByRole("button", { name: new RegExp(original.display_name) }).first().click();
  await expect(composer).toHaveValue("");
  const cleanupResponse = await api.delete(`/api/v4/posts/${sentPost.id}`, { headers });
  expect(cleanupResponse.status()).toBe(200);

  // Leaving the workspace before the debounce expires still flushes the last
  // input. Explicitly clearing it then unmounting must not save it again.
  const unmountDraft = `언마운트 보존 초안 ${Date.now()}`;
  await composer.fill(unmountDraft);
  const primaryNavigation = page.getByRole("navigation", { name: "주요 기능" });
  await primaryNavigation.getByRole("button", { name: "오늘", exact: true }).click();
  await expect(page).toHaveURL(/\/today$/);
  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  await expect(composer).toHaveValue(unmountDraft);

  await page.getByRole("button", { name: "저장된 초안 지우기" }).click();
  await expect(composer).toHaveValue("");
  await primaryNavigation.getByRole("button", { name: "오늘", exact: true }).click();
  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  await expect(composer).toHaveValue("");
});

test("Korean IME confirmation Enter never submits the workspace composer", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  let postCreates = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/v4/posts") {
      postCreates += 1;
    }
  });
  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  const composer = page.getByRole("textbox", { name: "메시지 입력" });
  await composer.fill("한글 조합 확인");

  await composer.evaluate((element) => {
    element.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "인" }));
    const enter = new KeyboardEvent("keydown", {
      key: "Enter",
      code: "Enter",
      bubbles: true,
      cancelable: true,
    });
    Object.defineProperty(enter, "isComposing", { configurable: true, value: true });
    element.dispatchEvent(enter);
  });

  await expect(composer).toHaveValue("한글 조합 확인");
  await expect.poll(() => postCreates).toBe(0);
  await composer.evaluate((element) => {
    element.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "입력" }));
  });
});

test("workspace AI rewrite previews changes before applying them", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  const original = "내일까지 배포 결과를 확인 부탁드립니다.";
  const rewritten = "내일까지 배포 결과를 확인해 주시면 감사하겠습니다.";
  let completions = 0;
  await page.route("**/api/moyro/v1/me/ai/completions", async (route) => {
    completions += 1;
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream; charset=utf-8",
      body: `data: ${JSON.stringify({ delta: rewritten })}\n\ndata: [DONE]\n\n`,
    });
  });

  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  const composer = page.getByRole("textbox", { name: "메시지 입력" });
  await composer.fill(original);
  await page.getByRole("button", { name: "정중하게", exact: true }).click();
  const preview = page.getByRole("region", { name: "AI 메시지 수정안" });
  await expect(preview).toContainText(original);
  await expect(preview).toContainText(rewritten);
  await expect(composer).toHaveValue(original);
  expect(completions).toBe(1);

  await preview.getByRole("button", { name: "적용", exact: true }).click();
  await expect(preview).toBeHidden();
  await expect(composer).toHaveValue(rewritten);
  assertNoRuntimeIssues(issues, "workspace AI rewrite preview");
});

test("Flow source links load and focus the exact message", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto("/my-work/saved");
  const sourceRow = page.locator(".flow-list-row").filter({ hasText: "moyro 릴리스" }).first();
  await expect(sourceRow).toBeVisible();

  // Prove the targeted /posts/ids fallback rather than relying on the message
  // already being present in the channel's normal first page.
  await page.route(`**/api/v4/channels/${seed.channelId}/posts?*`, async (route) => {
    const response = await route.fetch();
    const body = await response.json() as { order: string[]; posts: Record<string, unknown> };
    body.order = body.order.filter((id) => id !== seed.postId);
    delete body.posts[seed.postId];
    await route.fulfill({ response, json: body });
  });
  let exactLookups = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/v4/posts/ids") exactLookups += 1;
  });

  await sourceRow.getByRole("button", { name: "메시지 열기" }).click();
  await expect(page).toHaveURL(new RegExp(`/workspace/${seed.teamId}/channel/${seed.channelId}$`));
  const target = page.locator(`#channel-post-${seed.postId}`);
  await expect(target).toBeVisible();
  await expect(target).toBeFocused();
  expect(exactLookups).toBeGreaterThan(0);
  assertNoRuntimeIssues(issues, "Flow exact message navigation");
});

for (const route of routedPages) {
  test(`${route.file} renders and survives refresh`, async ({ page }) => {
    await installAuthenticatedSession(page, auth);
    const issues = collectRuntimeIssues(page);
    const target = route.path();
    await page.goto(target);
    await expect(page.getByText(route.marker, { exact: false }).first()).toBeVisible();
    if (route.verifyBrand !== false) {
      const brand = page.getByRole("button", { name: "오늘로 이동", exact: true });
      await expect(brand).toBeVisible();
      await expect(brand.locator("svg")).toBeVisible();
    }
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

test("workspace context info supports tabs, keyboard close and focus return", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  const aiCompletionRequests: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/moyro/v1/me/ai/completions") {
      aiCompletionRequests.push(request.url());
    }
  });

  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  await expect(page.getByText("moyro 릴리스", { exact: false }).first()).toBeVisible();
  await settle(page);

  const opener = page.getByRole("button", { name: "채널 정보 패널 열기" });
  await expect(opener).toBeVisible();
  await opener.click();

  const panel = page.getByRole("complementary", { name: "컨텍스트 패널" });
  const tabs = panel.getByRole("tablist", { name: "채널 컨텍스트" });
  const infoTab = tabs.getByRole("tab", { name: "정보", exact: true });
  const filesTab = tabs.getByRole("tab", { name: "최근 파일", exact: true });
  await expect(panel).toBeVisible();
  await expect(tabs.getByRole("tab")).toHaveCount(4);
  await expect(infoTab).toHaveAttribute("aria-selected", "true");
  await expect(infoTab).toBeFocused();
  await expect(panel.getByRole("tabpanel", { name: "정보", exact: true })).toBeVisible();
  await expect(panel.getByRole("region", { name: "채널 정보" })).toBeVisible();

  await infoTab.press("ArrowLeft");
  await expect(filesTab).toHaveAttribute("aria-selected", "true");
  await expect(filesTab).toBeFocused();
  await filesTab.press("ArrowRight");
  await expect(infoTab).toHaveAttribute("aria-selected", "true");
  await expect(infoTab).toBeFocused();

  await settle(page);
  await capture(page, "workspace-context-info.jpg");
  await infoTab.press("Escape");
  await expect(panel).toBeHidden();
  await expect(opener).toBeFocused();
  await expect(opener).toHaveAttribute("aria-pressed", "false");
  expect(aiCompletionRequests).toEqual([]);
  assertNoRuntimeIssues(issues, "desktop workspace context info");
});

test("mobile workspace context is full-screen, modal and keyboard contained", async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 932 });
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  const aiCompletionRequests: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/moyro/v1/me/ai/completions") {
      aiCompletionRequests.push(request.url());
    }
  });

  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  await expect(page.getByText("moyro 릴리스", { exact: false }).first()).toBeVisible();
  await settle(page);

  const opener = page.getByRole("button", { name: "채널 정보 패널 열기" });
  await expect(opener).toBeVisible();
  await opener.click();

  const panel = page.getByRole("complementary", { name: "컨텍스트 패널" });
  const mobileNavigation = page.getByRole("navigation", { name: "모바일 탐색" });
  const tabs = panel.getByRole("tablist", { name: "채널 컨텍스트" });
  const threadTab = tabs.getByRole("tab", { name: "스레드", exact: true });
  const infoTab = tabs.getByRole("tab", { name: "정보", exact: true });
  const closeButton = panel.getByRole("button", { name: "컨텍스트 패널 닫기" });
  await expect(panel).toBeVisible();
  await expect(mobileNavigation).toBeVisible();
  await expect(infoTab).toHaveAttribute("aria-selected", "true");
  await expect(infoTab).toBeFocused();
  await expect(panel.getByRole("region", { name: "채널 정보" })).toBeVisible();

  const panelLayout = await panel.evaluate((element) => {
    const layer = element.parentElement;
    if (!layer) throw new Error("context panel layer is missing");
    const rect = layer.getBoundingClientRect();
    return {
      left: Math.round(rect.left),
      top: Math.round(rect.top),
      width: Math.round(rect.width),
      height: Math.round(rect.height),
      zIndex: Number.parseInt(getComputedStyle(layer).zIndex, 10),
    };
  });
  const mobileNavigationZIndex = await mobileNavigation.evaluate((element) =>
    Number.parseInt(getComputedStyle(element).zIndex, 10));
  expect(panelLayout).toMatchObject({ left: 0, top: 0, width: 430, height: 932 });
  expect(Number.isFinite(panelLayout.zIndex)).toBe(true);
  expect(panelLayout.zIndex).toBeGreaterThan(mobileNavigationZIndex);

  await closeButton.focus();
  await page.keyboard.press("Tab");
  await expect(threadTab).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(closeButton).toBeFocused();
  await infoTab.focus();
  await expectNoHorizontalOverflow(page, "mobile workspace context info");

  await settle(page);
  await capture(page, "mobile-workspace-context.jpg");
  await infoTab.press("Escape");
  await expect(panel).toBeHidden();
  await expect(opener).toBeFocused();
  await expect(opener).toHaveAttribute("aria-pressed", "false");
  expect(aiCompletionRequests).toEqual([]);
  assertNoRuntimeIssues(issues, "mobile workspace context info");
});

test("profile context menu shows version, admin and optional approval entries", async ({ page }) => {
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);
  await page.getByRole("button", { name: "계정 메뉴 열기" }).click();
  const accountMenu = page.getByRole("menu", { name: "계정 메뉴" });
  const menuScroll = page.locator(".user-menu-scroll");
  await expect(page.getByLabel(`서비스 버전 ${expectedDisplayVersion}`, { exact: true })).toBeVisible();
  await expect(page.locator(".user-menu-version-brand svg[aria-hidden='true']")).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /로그아웃/ })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /서비스 관리/ })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /내 승인 요청/ })).toBeVisible();
  const menuBounds = await accountMenu.boundingBox();
  const viewportHeight = await page.evaluate(() => window.innerHeight);
  expect(menuBounds).not.toBeNull();
  expect((menuBounds?.y ?? 0) + (menuBounds?.height ?? 0)).toBeLessThanOrEqual(viewportHeight);
  await page.getByRole("menuitem", { name: /세션 관리/ }).scrollIntoViewIfNeeded();
  await expect(page.getByRole("menuitem", { name: /세션 관리/ })).toBeVisible();
  await menuScroll.evaluate((element) => { element.scrollTop = 0; });
  await capture(page, "workspace-profile-menu.jpg");
  assertNoRuntimeIssues(issues, "profile context menu");
});

test("mobile messages expose a persistent, keyboard-safe action menu", async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 932 });
  await installAuthenticatedSession(page, auth);
  const issues = collectRuntimeIssues(page);
  await page.goto(`/workspace/${seed.teamId}/channel/${seed.channelId}`);

  const message = page.locator(".workspace-message-item")
    .filter({ hasText: "moyro 릴리스" })
    .first();
  await expect(message).toBeVisible();
  const trigger = message.getByRole("button", { name: "메시지 작업 더보기" });
  await expect(trigger).toBeVisible();
  const hitTarget = await trigger.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { width: rect.width, height: rect.height };
  });
  expect(hitTarget.width).toBeGreaterThanOrEqual(44);
  expect(hitTarget.height).toBeGreaterThanOrEqual(44);
  await expect(message.locator(".message-action-primary").first()).toBeHidden();

  await trigger.click();
  const menu = page.getByRole("menu", { name: "메시지 작업 더보기" });
  await expect(menu).toBeVisible();
  for (const label of ["리액션 추가", "스레드 열기", "저장", "나중에 알림"]) {
    await expect(menu.getByRole("menuitem", { name: label })).toBeVisible();
  }
  await settle(page);
  await capture(page, "mobile-message-actions.jpg");
  await page.keyboard.press("Escape");
  await expect(menu).toBeHidden();
  await expect(trigger).toBeFocused();
  assertNoRuntimeIssues(issues, "mobile message action menu");
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
    ["/today", "mobile-today.jpg", "오늘의 흐름"],
    ["/inbox/updates", "mobile-inbox.jpg", "통합 알림함"],
    ["/my-work/tasks", "mobile-my-work.jpg", "내 업무"],
    ["/approvals/mine", "mobile-approvals.jpg", "승인 센터"],
    ["/settings/profile", "mobile-settings-profile.jpg", "프로필"],
    ["/admin/site", "mobile-admin-site.jpg", "사이트 설정"],
  ] as const) {
    await page.goto(target);
    await expect(page.getByText(marker, { exact: false }).filter({ visible: true }).first()).toBeVisible();
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

async function selectTheme(page: Page, label: "밝게" | "어둡게", value: "light" | "dark") {
  const responsePromise = page.waitForResponse((response) =>
    response.request().method() === "PUT"
    && /\/api\/v4\/users\/[^/]+\/preferences$/.test(new URL(response.url()).pathname),
  );
  await page.getByRole("radio", { name: label }).check();
  const response = await responsePromise;
  expect(response.status()).toBe(200);
  await expect(page.getByRole("radio", { name: label })).toBeChecked();
  await expect.poll(() => page.evaluate(() => window.localStorage.getItem("moyro:theme"))).toBe(value);
}

async function expectFlowTheme(page: Page, expected: {
  mode: "light" | "dark";
  brand: string;
  page: string;
}) {
  await expect.poll(async () => page.evaluate(() => {
    const styles = getComputedStyle(document.documentElement);
    return {
      mode: document.documentElement.getAttribute("data-theme"),
      brand: styles.getPropertyValue("--flow-color-brand").trim().toUpperCase(),
      page: styles.getPropertyValue("--flow-color-page").trim().toUpperCase(),
    };
  })).toEqual(expected);
}

async function expectNoHorizontalOverflow(page: Page, surface: string) {
  const dimensions = await page.evaluate(() => ({
    viewport: window.innerWidth,
    document: document.documentElement.scrollWidth,
  }));
  expect(dimensions.document, `${surface} overflows the mobile viewport`).toBeLessThanOrEqual(dimensions.viewport);
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
  if (!channels.some((item) => item.id !== channelId)) {
    await json<{ id: string; name: string }>("POST", "/api/v4/channels", {
      team_id: teamId,
      name: "release-testing",
      display_name: "Release testing",
      type: "O",
    });
  }

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

    const reminders = await json<Array<{ post_id: string; remind_at: number }>>(
      "GET",
      "/api/v4/users/me/reminders",
    );
    if (!reminders.some((reminder) => reminder.post_id === seededPost.id)) {
      await json("POST", `/api/v4/posts/${seededPost.id}/remind_me`, {
        remind_at: Date.now() + 30 * 24 * 60 * 60 * 1000,
      });
    }
  }
  if (!seededPost) throw new Error("product seed did not produce a source message");

  for (const workItem of [
    {
      kind: "task",
      title: "오프라인 배포 점검 결과 확인",
      description: "릴리스 이미지 검증 결과를 확인하고 상태를 갱신합니다.",
      assignee_id: session.user.id,
      due_at: Date.now() + 2 * 24 * 60 * 60 * 1000,
      idempotency_key: "release-capture-task",
    },
    {
      kind: "decision",
      title: "v0.2.4은 검증된 단일 오프라인 자산으로 배포",
      description: "PostgreSQL, 브라우저, 플러그인 호환과 재시작 검증을 모두 통과한 자산만 배포합니다.",
      assignee_id: "",
      due_at: 0,
      idempotency_key: "release-capture-decision",
    },
  ] as const) {
    const page = await json<{ items: Array<{ title: string }> }>(
      "GET",
      `/api/moyro/v1/me/work-items?kind=${workItem.kind}&per_page=100`,
    );
    if (!page.items.some((item) => item.title === workItem.title)) {
      await json("POST", "/api/moyro/v1/me/work-items", {
        ...workItem,
        source_post_id: seededPost.id,
      });
    }
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
  return { teamId, channelId, postId: seededPost.id };
}
