import {
  COMPAT_API_BASE as BASE,
  MOYRO_API_BASE as MOYRO_BASE,
  compatRequest as request,
  moyroRequest,
  BROWSER_SESSION_TOKEN,
} from "./transport";

// Media bytes are fetched with an Authorization header and converted to a
// short-lived blob URL by the rendering component. Restrict callers to known
// read-only media surfaces so a post cannot turn an arbitrary same-origin API
// path into a credentialed fetch.
function isAuthenticatedMediaPath(path: string): boolean {
  if (!path.startsWith(`${BASE}/`)) return false;
  let parsed: URL;
  try {
    parsed = new URL(path, "https://moyro.invalid");
  } catch {
    return false;
  }
  if (parsed.origin !== "https://moyro.invalid" || parsed.hash) return false;
  const pathname = parsed.pathname;
  const noQuery = parsed.search === "";
  if (/^\/api\/v4\/files\/[^/]+(?:\/thumbnail)?$/.test(pathname)) return noQuery;
  if (/^\/api\/v4\/emoji\/[^/]+\/image$/.test(pathname)) return noQuery;
  if (/^\/api\/v4\/users\/[^/]+\/image$/.test(pathname)) {
    return [...parsed.searchParams.keys()].every((key) => key === "v") && parsed.searchParams.getAll("v").length <= 1;
  }
  if (pathname === "/api/v4/link_preview_image") {
    return [...parsed.searchParams.keys()].every((key) => key === "url") && parsed.searchParams.getAll("url").length === 1;
  }
  return false;
}

async function authenticatedMediaBlob(token: string, path: string): Promise<Blob> {
  if (!token) throw new Error("missing media credential");
  if (!isAuthenticatedMediaPath(path)) throw new Error("invalid authenticated media path");
  const headers = new Headers();
  if (token !== BROWSER_SESSION_TOKEN) headers.set("Authorization", `Bearer ${token}`);
  const res = await fetch(path, { headers, credentials: "same-origin" });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }));
    throw new Error(err.message ?? `HTTP ${res.status}`);
  }
  return res.blob();
}

// ---- Types ----

export type User = {
  id: string;
  username: string;
  email: string;
  roles?: string;
  // Picture is either an external URL (OAuth provider import) or a bare
  // file_id for a self-uploaded avatar. Empty ⇒ UI renders an initial tile.
  picture?: string;
  // Bumped on any profile edit including avatar upload — useful as the
  // `?v=` cache-buster on the image URL.
  update_at?: number;
  // Zero for active users; unix-millis for deactivated ones. Only the
  // admin `listUsers(..., {includeDeleted: true})` call populates this
  // reliably — other endpoints strip deactivated rows upstream.
  delete_at?: number;
};

export type Team = {
  id: string;
  name: string;
  display_name: string;
  type: "O" | "I";
  create_at: number;
};

export type Channel = {
  id: string;
  team_id: string;
  type: "O" | "P" | "D" | "G";
  name: string;
  display_name: string;
  header?: string;
  purpose?: string;
  create_at: number;
  update_at?: number;
  delete_at?: number;
};

export type Post = {
  id: string;
  channel_id: string;
  user_id: string;
  root_id: string;
  message: string;
  create_at: number;
  update_at: number;
  delete_at: number;
  props: Record<string, unknown>;
  file_ids?: string[];
  is_pinned?: boolean;
  // Phase 18 link previews. Up to 3 OpenGraph-extracted entries per post.
  // Populated asynchronously after post creation; clients get a
  // post_edited event when it lands.
  link_metadata?: LinkPreview[];
};

export type LinkPreview = {
  url: string;
  title?: string;
  description?: string;
  image_url?: string;
  fetched_at: number;
};

export type PostList = { order: string[]; posts: Record<string, Post> };

// The native saved-posts route preserves the Mattermost `order` envelope,
// but returns the visible posts as an array because membership filtering can
// remove rows. Keep that wire shape explicit instead of pretending every
// post-list endpoint has the same map representation.
export type SavedPostsResponse = {
  order: string[];
  posts: Post[] | Record<string, Post>;
};

export function orderedSavedPosts(result: SavedPostsResponse): Post[] {
  const rows = Array.isArray(result.posts) ? result.posts : Object.values(result.posts ?? {});
  if (!Array.isArray(result.order) || result.order.length === 0) return rows;
  const byId = Object.fromEntries(rows.map((post) => [post.id, post]));
  const ordered = result.order.map((id) => byId[id]).filter((post): post is Post => Boolean(post));
  const orderedIDs = new Set(ordered.map((post) => post.id));
  return [...ordered, ...rows.filter((post) => !orderedIDs.has(post.id))];
}

// Phase 18: ranked search result envelope.
export type SearchResult = {
  order: string[];
  posts: Record<string, Post>;
  total_hits: number;
  page: number;
};

// Phase 18: client-side-parsed filter tokens lifted out of the search
// query string and sent as explicit fields so the server doesn't have to
// re-parse free-form text.
export type SearchFilters = {
  from_user_id?: string;
  in_channel_id?: string;
  after?: number;
  before?: number;
  has_file?: boolean;
  has_link?: boolean;
};

export type Reaction = {
  user_id: string;
  post_id: string;
  emoji_name: string;
  create_at: number;
};

export type FileInfo = {
  id: string;
  user_id: string;
  post_id: string;
  channel_id: string;
  name: string;
  extension: string;
  size: number;
  mime_type: string;
  // Phase 13: thumbnail metadata. When has_thumbnail=true, /files/{id}/thumbnail
  // serves a 360px-longest-edge JPEG derived from the original on upload.
  has_thumbnail?: boolean;
  width?: number;
  height?: number;
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type Emoji = {
  id: string;
  name: string;
  creator_id: string;
  file_id: string;
  create_at: number;
  delete_at: number;
};

export type UploadResponse = {
  file_infos: FileInfo[];
  client_ids: string[];
};

export type ChannelMember = {
  channel_id: string;
  user_id: string;
  roles: string;
  last_viewed_at: number;
  create_at: number;
};

export type ChannelMemberWithCounts = {
  channel_id: string;
  user_id: string;
  roles: string;
  last_viewed_at: number;
  msg_count: number;
  mention_count: number;
  notify_props: ChannelNotifyProps;
};

export type ChannelNotifyProps = {
  // "all" | "mentions" | "none"
  desktop?: string;
  // "all" | "mention"  (mention => muted-ish)
  mark_unread?: string;
  [k: string]: unknown;
};

export type UserStatusValue = "online" | "away" | "dnd" | "offline";

export type UserStatus = {
  user_id: string;
  status: UserStatusValue;
  manual: boolean;
  last_activity_at: number;
};

export type CommandResponse = {
  response_type: "in_channel" | "ephemeral";
  text: string;
  post?: Post;
};

// ---- Phase 16 types ----

// Invite preview returned by the public GET /invites/{id} endpoint. Also
// used for the register-with-invite banner on LoginView.
export type InvitePreview = {
  id: string;
  team_id: string;
  team_display_name: string;
  team_name: string;
  expires_at: number;
};

// Full invite record, returned by the admin list/create endpoints. `url`
// is the server-computed shareable URL (already includes PublicBaseURL or
// is relative when none is configured).
export type Invite = {
  id: string;
  team_id: string;
  created_by: string;
  max_uses: number;
  use_count: number;
  expires_at: number;
  create_at: number;
  url: string;
};

export type AuditEntry = {
  id: number;
  actor_id: string;
  action: string;
  target: string;
  // payload is returned by the server as a raw JSON fragment so the shape
  // varies by action. Treat it as unknown at the TS boundary.
  payload: unknown;
  create_at: number;
};

export type SessionRow = {
  id: string;
  user_id: string;
  device_id: string;
  expires_at: number;
  create_at: number;
  is_current: boolean;
};

// ---- Phase 19 types ----

// A scheduled post pending delivery. sent_at stays <= 0 while pending
// (0 = untouched, -1 = in-flight dispatch); once positive the row is
// historical and no longer surfaced by ListPending.
export type ScheduledPost = {
  id: string;
  user_id: string;
  channel_id: string;
  root_id: string;
  message: string;
  file_ids: string[];
  props: Record<string, unknown>;
  send_at: number;
  create_at: number;
  sent_at: number;
  error_text: string;
  status: "pending" | "processing" | "retry" | "succeeded" | "dead" | "cancelled" | string;
  claimed_at?: number;
  lease_until?: number;
  attempt_count: number;
  next_attempt_at?: number;
  last_error_code?: string;
  last_error_text?: string;
  result_post_id?: string;
};

export type Reminder = {
  id: string;
  user_id: string;
  post_id: string;
  remind_at: number;
  create_at: number;
  delivered_at: number;
};

// ---- API ----

// The /system/ping payload the server builds from enabled services. New
// fields should be kept optional so older builds of the webapp don't break
// when deployed against a newer server.
export type SystemPing = {
  status: string;
  ActiveSearchBackend?: string;
  oauth_providers?: string[];
  version?: string;
  build_hash?: string;
  build_date?: string;
};

export type SystemInfo = {
  name: string;
  version: string;
  build_hash?: string;
  build_date?: string;
  oidc_enabled?: boolean;
  oidc_provider_name?: string;
  approval_enabled?: boolean;
  local_signup_enabled?: boolean;
  capabilities?: {
    email_digest?: {
      configured: boolean;
      enabled: boolean;
    };
		drafts?: {
			storage_mode: "local" | "session" | "disabled";
			retention_days: number;
			clear_on_logout: boolean;
		};
  };
};

export type ClientConfig = {
  Version?: string;
  BuildNumber?: string;
  BuildDate?: string;
  BuildHash?: string;
  SiteName?: string;
  [key: string]: string | undefined;
};

export const api = {
  // auth / session
  //
  // `inviteId` is optional; when present the server validates + consumes the
  // invite in the same tx as the INSERT, then auto-joins the target team and
  // its default channels. Invalid/expired/revoked tokens 400 before the user
  // row is created.
  register: (username: string, email: string, password: string, inviteId = "") =>
    request<User>(null, "/users", {
      method: "POST",
      body: inviteId
        ? { username, email, password, invite_id: inviteId }
        : { username, email, password },
    }),
  login: (login_id: string, password: string) =>
    moyroRequest<{ user: User }>(null, "/auth/session/login", {
      method: "POST",
      body: { login_id, password },
    }).then(({ user }) => ({ token: BROWSER_SESSION_TOKEN, user })),
  exchangeSSOCode: (code: string) =>
    moyroRequest<{ user: User }>(null, "/auth/sso/session", {
      method: "POST",
      body: { code },
    }).then(({ user }) => ({ token: BROWSER_SESSION_TOKEN, user })),
  adoptBrowserSession: (token: string) =>
    moyroRequest<{ user: User }>(token, "/auth/session/adopt", { method: "POST" })
      .then(({ user }) => ({ token: BROWSER_SESSION_TOKEN, user })),
  me: (token: string) => request<User>(token, "/users/me"),
  ping: () => request<SystemPing>(null, "/system/ping"),
  clientConfig: () => request<ClientConfig>(null, "/config/client"),
  logout: (token: string) =>
    moyroRequest<{ status: string }>(token, "/auth/session/logout", { method: "POST" }),

  // user directory
  //
  // `includeDeleted` is admin-only on the server (non-admins silently get
  // the active-only slice), but exposing it on the public signature keeps
  // call sites simple — the IntegrationsPanel "사용자" tab uses it to
  // surface reactivate buttons.
  listUsers: (token: string, page = 0, perPage = 60, includeDeleted = false) => {
    const qs = new URLSearchParams();
    qs.set("page", String(page));
    qs.set("per_page", String(perPage));
    if (includeDeleted) qs.set("include_deleted", "true");
    return request<User[]>(token, `/users?${qs.toString()}`);
  },
  getUser: (token: string, userId: string) =>
    request<User>(token, `/users/${userId}`),
  getUserByUsername: (token: string, username: string) =>
    request<User>(token, `/users/username/${encodeURIComponent(username)}`),
  searchUsers: (token: string, term: string, limit = 20) =>
    request<User[]>(token, "/users/search", {
      method: "POST",
      body: { term, limit },
    }),

  // profile
  updateProfile: (token: string, username: string, email: string) =>
    request<User>(token, "/users/me", {
      method: "PUT",
      body: { username, email },
    }),
  // Self-upload profile picture. Server caps size at 512KB and enforces
  // image/* mime. Returns the refreshed User — dispatch setAuth to update
  // sidebar + message rows in one pass.
  uploadProfileImage: (token: string, file: File) => {
    const fd = new FormData();
    fd.append("image", file, file.name);
    return request<User>(token, "/users/me/image", { method: "POST", body: fd });
  },
  // URL for any user's avatar. When picture is empty the server 404s and
  // the consumer <img onError> falls back to initials. We include update_at
  // as the cache-buster so a new upload invalidates the browser cache.
  userImagePath: (userId: string, version?: number | string) => {
    const suffix = version !== undefined ? `?v=${encodeURIComponent(String(version))}` : "";
    return `${BASE}/users/${encodeURIComponent(userId)}/image${suffix}`;
  },
  updatePassword: (token: string, current_password: string, new_password: string) =>
    request<{ status: string }>(token, "/users/me/password", {
      method: "PUT",
      body: { current_password, new_password },
    }),

  // Phase 17 — email notification prefs (currently just daily digest toggle).
  // Shape is intentionally small; the server stores it as JSONB so adding
  // more keys later is schema-free.
  getEmailPrefs: (token: string) =>
    request<{ digest_enabled: boolean }>(token, "/users/me/email_prefs"),
  updateEmailPrefs: (token: string, prefs: { digest_enabled: boolean }) =>
    request<{ digest_enabled: boolean }>(token, "/users/me/email_prefs", {
      method: "PUT",
      body: prefs,
    }),

  // user status
  getUserStatus: (token: string, userId: string) =>
    request<UserStatus>(token, `/users/${userId}/status`),
  getUserStatusesByIDs: (token: string, ids: string[]) =>
    request<UserStatus[]>(token, `/users/statuses/ids`, {
      method: "POST",
      body: ids,
    }),
  updateMyStatus: (token: string, status: UserStatusValue, manual = true) =>
    request<UserStatus>(token, `/users/me/status`, {
      method: "PUT",
      body: { status, manual },
    }),

  // teams
  listTeams: (token: string) => request<Team[]>(token, "/users/me/teams"),
  createTeam: (token: string, name: string, display_name: string) =>
    request<Team>(token, "/teams", {
      method: "POST",
      body: { name, display_name, type: "O" },
    }),
  // Phase 18: ranked + filtered search. Filters are parsed client-side
  // from search tokens (`from:`, `in:`, `before:`, `after:`, `has:file`,
  // `has:link`) and lifted into explicit fields so the server doesn't
  // re-parse the free-form query.
  searchPosts: (
    token: string,
    teamId: string,
    terms: string,
    opts: { page?: number; perPage?: number; filters?: SearchFilters } = {},
  ) =>
    request<SearchResult>(token, `/teams/${teamId}/posts/search`, {
      method: "POST",
      body: {
        terms,
        page: opts.page ?? 0,
        per_page: opts.perPage ?? 20,
        ...(opts.filters ?? {}),
      },
    }),

  // Phase 18: saved posts (personal bookmarks). The native route returns
  // an order envelope with an array after membership filtering; tolerate a
  // map too for Mattermost-compatible deployments.
  listSavedPosts: (token: string, limit = 20, offset = 0) =>
    request<SavedPostsResponse>(
      token,
      `/users/me/saved_posts?limit=${limit}&offset=${offset}`,
    ),
  savedPostsByIds: (token: string, postIds: string[]) =>
    request<Record<string, boolean>>(token, `/users/me/saved_posts/ids`, {
      method: "POST",
      body: { post_ids: postIds },
    }),
  savePost: (token: string, postId: string) =>
    request<{ status: string; saved: boolean }>(
      token,
      `/users/me/saved_posts/${postId}`,
      { method: "POST" },
    ),
  unsavePost: (token: string, postId: string) =>
    request<{ status: string; saved: boolean }>(
      token,
      `/users/me/saved_posts/${postId}`,
      { method: "DELETE" },
    ),

  // Phase 18: public channel discovery. Lists channels of type 'O' the
  // caller hasn't joined yet, filtered by optional `q` (ILIKE on name +
  // display_name). `joinChannel` already exists (Phase 3) for the action.
  discoverChannels: (
    token: string,
    teamId: string,
    opts: { q?: string; limit?: number; offset?: number } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.q) qs.set("q", opts.q);
    qs.set("limit", String(opts.limit ?? 20));
    qs.set("offset", String(opts.offset ?? 0));
    return request<Channel[]>(
      token,
      `/teams/${teamId}/channels/discover?${qs.toString()}`,
    );
  },

  // Phase 18: protected link-preview image proxy. The rendering component
  // fetches this path with Authorization and exposes only a blob URL to DOM.
  linkPreviewImagePath: (rawURL: string) =>
    `${BASE}/link_preview_image?url=${encodeURIComponent(rawURL)}`,

  // Phase 19: scheduled messages. createScheduledPost returns the fresh
  // row; the server also fires a scheduled_post_created WS so peer tabs
  // update without a refetch.
  createScheduledPost: (
    token: string,
    body: {
      channel_id: string;
      root_id?: string;
      message: string;
      file_ids?: string[];
      props?: Record<string, unknown>;
      send_at: number;
    },
  ) =>
    request<ScheduledPost>(token, `/scheduled_posts`, {
      method: "POST",
      body,
    }),
  listMyScheduledPosts: (token: string) =>
    request<ScheduledPost[]>(token, `/users/me/scheduled_posts`),
  deleteScheduledPost: (token: string, id: string) =>
    request<{ status: string }>(token, `/scheduled_posts/${id}`, {
      method: "DELETE",
    }),

  // Phase 19: post reminders. Caller must be a channel member of the post;
  // the server enforces that and 404s otherwise.
  createPostReminder: (token: string, postId: string, remindAt: number) =>
    request<Reminder>(token, `/posts/${postId}/remind_me`, {
      method: "POST",
      body: { remind_at: remindAt },
    }),
  listMyReminders: (token: string) =>
    request<Reminder[]>(token, `/users/me/reminders`),
  deleteReminder: (token: string, id: string) =>
    request<{ status: string }>(token, `/users/me/reminders/${id}`, {
      method: "DELETE",
    }),

  // channels
  //
  // When `includeDeleted` is true the server drops the `delete_at = 0` filter
  // so archived channels show up with a non-zero `delete_at`. Used to drive
  // the "보관된 채널 보기" sidebar toggle.
  listChannels: (token: string, teamId: string, includeDeleted = false) =>
    request<Channel[]>(
      token,
      `/users/me/teams/${teamId}/channels${includeDeleted ? "?include_deleted=true" : ""}`,
    ),
  createChannel: (token: string, teamId: string, name: string, display_name: string) =>
    request<Channel>(token, "/channels", {
      method: "POST",
      body: { team_id: teamId, name, display_name, type: "O" },
    }),
  createDirectChannel: (token: string, userIds: string[]) =>
    request<Channel>(token, "/channels/direct", {
      method: "POST",
      body: userIds,
    }),
  getChannel: (token: string, channelId: string) =>
    request<Channel>(token, `/channels/${channelId}`),
  patchChannel: (
    token: string,
    channelId: string,
    patch: { display_name?: string; header?: string; purpose?: string },
  ) =>
    request<Channel>(token, `/channels/${channelId}`, {
      method: "PUT",
      body: patch,
    }),
  viewChannel: (token: string, channelId: string) =>
    request<{ status: string; last_viewed_at: number }>(
      token,
      `/channels/${channelId}/view`,
      { method: "POST" },
    ),

  // channel members
  listChannelMembers: (token: string, channelId: string) =>
    request<ChannelMember[]>(token, `/channels/${channelId}/members`),
  listMyChannelMembers: (token: string, teamId: string) =>
    request<ChannelMemberWithCounts[]>(
      token,
      `/users/me/teams/${teamId}/channels/members`,
    ),
  getMyChannelNotifyProps: (token: string, channelId: string) =>
    request<ChannelNotifyProps>(
      token,
      `/channels/${channelId}/members/me/notify_props`,
    ),
  setMyChannelNotifyProps: (
    token: string,
    channelId: string,
    props: ChannelNotifyProps,
  ) =>
    request<ChannelNotifyProps>(
      token,
      `/channels/${channelId}/members/me/notify_props`,
      { method: "PUT", body: props },
    ),
  addChannelMember: (token: string, channelId: string, userId: string) =>
    request<ChannelMember>(token, `/channels/${channelId}/members`, {
      method: "POST",
      body: { user_id: userId },
    }),
  // Phase 18: self-join a public channel during the discovery flow.
  // Distinct from addChannelMember — outsiders can't hit that route
  // because it requires prior membership for spam control.
  joinChannel: (token: string, channelId: string) =>
    request<ChannelMember>(token, `/channels/${channelId}/join`, {
      method: "POST",
    }),
  // Channel-scoped mention autocomplete. `prefix` is the (empty-trimmed)
  // token after `@` in the Composer. Returns at most `limit` users who are
  // members of the channel and whose username matches. Server caps at 25.
  channelMembersAutocomplete: (
    token: string,
    channelId: string,
    prefix: string,
    limit = 8,
  ) =>
    request<User[]>(
      token,
      `/channels/${channelId}/members/autocomplete?prefix=${encodeURIComponent(
        prefix,
      )}&limit=${limit}`,
    ),
  removeChannelMember: (token: string, channelId: string, userId: string) =>
    request<{ status: string }>(token, `/channels/${channelId}/members/${userId}`, {
      method: "DELETE",
    }),

  // posts
  listPosts: (token: string, channelId: string, page = 0, perPage = 60) =>
    request<PostList>(
      token,
      `/channels/${channelId}/posts?page=${page}&per_page=${perPage}`,
    ),
  createPost: (
    token: string,
    channelId: string,
    message: string,
    rootId = "",
    fileIds: string[] = [],
  ) =>
    request<Post>(token, `/posts`, {
      method: "POST",
      body: { channel_id: channelId, message, root_id: rootId, file_ids: fileIds },
    }),
  updatePost: (token: string, postId: string, message: string, props?: Record<string, unknown>) =>
    request<Post>(token, `/posts/${postId}`, {
      method: "PUT",
      body: { id: postId, message, props: props ?? {} },
    }),
  deletePost: (token: string, postId: string) =>
    request<{ status: string }>(token, `/posts/${postId}`, { method: "DELETE" }),
  pinPost: (token: string, postId: string) =>
    request<Post>(token, `/posts/${postId}/pin`, { method: "POST" }),
  unpinPost: (token: string, postId: string) =>
    request<Post>(token, `/posts/${postId}/unpin`, { method: "POST" }),
  listPinned: (token: string, channelId: string) =>
    request<PostList>(token, `/channels/${channelId}/pinned`),
  listThread: (token: string, postId: string) =>
    request<PostList>(token, `/posts/${postId}/thread`),

  // reactions
  addReaction: (token: string, postId: string, userId: string, emoji: string) =>
    request<Reaction>(token, `/reactions`, {
      method: "POST",
      body: { post_id: postId, user_id: userId, emoji_name: emoji },
    }),
  listReactions: (token: string, postId: string) =>
    request<Reaction[]>(token, `/posts/${postId}/reactions`),
  removeReaction: (token: string, postId: string, userId: string, emoji: string) =>
    request<{ status: string }>(
      token,
      `/users/${userId}/posts/${postId}/reactions/${encodeURIComponent(emoji)}`,
      { method: "DELETE" },
    ),

  // slash commands
  executeCommand: (token: string, teamId: string, channelId: string, command: string) =>
    request<CommandResponse>(token, "/commands/execute", {
      method: "POST",
      body: { team_id: teamId, channel_id: channelId, command },
    }),

  // files
  uploadFiles: (token: string, channelId: string, files: File[]) => {
    const fd = new FormData();
    for (const f of files) fd.append("files", f, f.name);
    return request<UploadResponse>(
      token,
      `/files?channel_id=${encodeURIComponent(channelId)}`,
      { method: "POST", body: fd },
    );
  },
  fileInfo: (token: string, fileId: string) =>
    request<FileInfo>(token, `/files/${fileId}/info`),
  fileDownloadPath: (fileId: string) =>
    `${BASE}/files/${encodeURIComponent(fileId)}`,
  fileThumbnailPath: (fileId: string) =>
    `${BASE}/files/${encodeURIComponent(fileId)}/thumbnail`,
  authenticatedMediaBlob,

  // Custom emojis. Creation uses multipart (so we can't reuse the JSON
  // helper), List/delete are plain JSON.
  listEmojis: (token: string, page = 0, perPage = 200) =>
    request<Emoji[]>(token, `/emoji?page=${page}&per_page=${perPage}`),
  createEmoji: (token: string, name: string, image: File) => {
    const fd = new FormData();
    fd.append("name", name);
    fd.append("image", image, image.name);
    return request<Emoji>(token, "/emoji", { method: "POST", body: fd });
  },
  deleteEmoji: (token: string, emojiId: string) =>
    request<{ status: string }>(token, `/emoji/${emojiId}`, { method: "DELETE" }),
  emojiImagePath: (emojiId: string) =>
    `${BASE}/emoji/${encodeURIComponent(emojiId)}/image`,

  // ---- Phase 16: invites (self-signup preview) ----
  //
  // Public endpoint — no token. Rate-limited 5/s per IP on the server. Used
  // by LoginView when it spots an `#invite=<id>` fragment in the URL so we
  // can show "X 팀에 초대되었습니다" before the user even registers.
  getInvite: (inviteId: string) =>
    request<InvitePreview>(null, `/invites/${encodeURIComponent(inviteId)}`),

  // ---- Phase 16: self session management ----
  listMySessions: (token: string) =>
    request<SessionRow[]>(token, "/users/me/sessions"),
  revokeSession: (token: string, sessionId: string) =>
    request<{ status: string }>(
      token,
      `/users/me/sessions/${encodeURIComponent(sessionId)}`,
      { method: "DELETE" },
    ),
  // Server preserves the bearer token's own session and kills all others.
  revokeOtherSessions: (token: string) =>
    request<{ status: string; revoked: number }>(
      token,
      `/users/me/sessions`,
      { method: "DELETE" },
    ),

  // ---- Phase 16: channel archive / restore ----
  //
  // Archive soft-deletes by stamping `channels.delete_at`; Restore zeroes it.
  // The server broadcasts `channel_deleted` / `channel_restored` WS events so
  // every connected client can fix up its sidebar without a reload.
  archiveChannel: (token: string, channelId: string) =>
    request<{ status: string }>(
      token,
      `/channels/${encodeURIComponent(channelId)}/archive`,
      { method: "POST" },
    ),
  restoreChannel: (token: string, channelId: string) =>
    request<{ status: string }>(
      token,
      `/channels/${encodeURIComponent(channelId)}/restore`,
      { method: "POST" },
    ),
};

// ---- Phase 12 types ----

export type Bot = {
  user_id: string;
  username: string;
  display_name: string;
  description: string;
  owner_id: string;
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type PAT = {
  id: string;
  user_id: string;
  description: string;
  create_at: number;
  last_used_at: number;
  revoked_at: number;
};

// CreatedPAT is returned exactly once on creation — stash the plaintext
// immediately; there is no way to recover it afterwards.
export type CreatedPAT = PAT & { token: string };

export type IncomingWebhook = {
  id: string;
  creator_id: string;
  channel_id: string;
  team_id: string;
  display_name: string;
  username: string;
  icon_url: string;
  channel_locked: boolean;
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type OutgoingWebhook = {
  id: string;
  token: string;
  creator_id: string;
  team_id: string;
  channel_id: string;
  trigger_words: string[];
  trigger_when: number;
  callback_urls: string[];
  display_name: string;
  content_type: string;
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type AdminConfigSnapshot = Record<string, Record<string, unknown>>;

export type AdminClusterNode = {
  id: string;
  status: string;
  hostname?: string;
  version?: string;
  server_version?: string;
  last_ping_at?: number;
  busy?: boolean;
  [key: string]: unknown;
};

export type AdminPlugin = {
  id?: string;
  plugin_id?: string;
  name?: string;
  version?: string;
  state?: string;
  description?: string;
	 enabled?: boolean;
	 runtime?: string;
	 error?: string;
	 manifest?: Record<string, unknown>;
  [key: string]: unknown;
};

export type AdminPluginStatus = {
  plugin_id: string;
  state: string;
};

export type PluginWebappBundle = {
  id: string;
  version: string;
  url: string;
};

export type PluginInstallResult = {
  id: string;
  version: string;
  state: string;
  enabled: boolean;
  runtime: string;
  sha256: string;
  replaced: boolean;
};

export type PluginConfiguration = {
  configuration: Record<string, unknown>;
  schema: Record<string, unknown>;
};

export type PluginManagementCapabilities = {
  management_enabled: boolean;
  uploads_enabled: boolean;
};

export const pluginApi = {
  listWebapps: (token: string) =>
    request<PluginWebappBundle[]>(token, "/plugins/webapp"),
};

export type AdminRole = {
  id: string;
  name: string;
  display_name: string;
  description: string;
  permissions: string[];
  scheme_managed?: boolean;
  built_in?: boolean;
  create_at?: number;
  update_at?: number;
};

export type AdminJob = {
  id: string;
  type: string;
  status: string;
  create_at: number;
  start_at?: number;
  last_activity_at?: number;
  progress?: number;
  data?: Record<string, unknown>;
};

export type AdminCompatRecord = Record<string, unknown>;

export type AdminAccessControlSearchResult = {
  policies?: AdminCompatRecord[];
  total_count?: number;
  [key: string]: unknown;
};

export const integrationsApi = {
  // bots
  listBots: (token: string) => request<Bot[]>(token, "/bots"),
  createBot: (token: string, username: string, display_name: string, description: string) =>
    request<Bot>(token, "/bots", {
      method: "POST",
      body: { username, display_name, description },
    }),
  disableBot: (token: string, botId: string) =>
    request<{ status: string }>(token, `/bots/${botId}`, { method: "DELETE" }),

  // PATs
  listTokens: (token: string, userId: string) =>
    request<PAT[]>(token, `/users/${userId}/tokens`),
  createToken: (token: string, userId: string, description: string) =>
    request<CreatedPAT>(token, `/users/${userId}/tokens`, {
      method: "POST",
      body: { description },
    }),
  revokeToken: (token: string, tokenId: string) =>
    request<{ status: string }>(token, `/tokens/${tokenId}/revoke`, { method: "POST" }),

  // incoming webhooks
  listIncoming: (token: string) => request<IncomingWebhook[]>(token, "/hooks/incoming"),
  createIncoming: (
    token: string,
    channel_id: string,
    display_name: string,
    username = "",
    icon_url = "",
    channel_locked = true,
  ) =>
    request<IncomingWebhook>(token, "/hooks/incoming", {
      method: "POST",
      body: { channel_id, display_name, username, icon_url, channel_locked },
    }),
  deleteIncoming: (token: string, hookId: string) =>
    request<{ status: string }>(token, `/hooks/incoming/${hookId}`, { method: "DELETE" }),

  // outgoing webhooks
  listOutgoing: (token: string) => request<OutgoingWebhook[]>(token, "/hooks/outgoing"),
  createOutgoing: (
    token: string,
    team_id: string,
    channel_id: string,
    trigger_words: string[],
    callback_urls: string[],
    display_name = "",
    trigger_when = 0,
    content_type = "application/json",
  ) =>
    request<OutgoingWebhook>(token, "/hooks/outgoing", {
      method: "POST",
      body: {
        team_id,
        channel_id,
        trigger_words,
        callback_urls,
        display_name,
        trigger_when,
        content_type,
      },
    }),
  deleteOutgoing: (token: string, hookId: string) =>
    request<{ status: string }>(token, `/hooks/outgoing/${hookId}`, { method: "DELETE" }),

  // ---- Phase 16: team invites (admin) ----
  //
  // `maxUses = 0` means unlimited within the TTL window. `ttlSeconds` is
  // converted server-side to an expires_at; no client-side clock is trusted.
  createInvite: (
    token: string,
    teamId: string,
    maxUses: number,
    ttlSeconds: number,
  ) =>
    request<Invite>(token, `/teams/${encodeURIComponent(teamId)}/invites`, {
      method: "POST",
      body: { max_uses: maxUses, ttl_seconds: ttlSeconds },
    }),
  listInvites: (token: string, teamId: string) =>
    request<Invite[]>(token, `/teams/${encodeURIComponent(teamId)}/invites`),
  revokeInvite: (token: string, teamId: string, inviteId: string) =>
    request<{ status: string }>(
      token,
      `/teams/${encodeURIComponent(teamId)}/invites/${encodeURIComponent(inviteId)}`,
      { method: "DELETE" },
    ),

  // ---- Phase 16: user deactivate / reactivate ----
  //
  // Deactivate also drops sessions + kicks live WS sockets server-side, so
  // the target's other tabs get a close frame within the read timeout.
  // Reactivate is admin-only and just clears users.delete_at.
  deactivateUser: (token: string, userId: string) =>
    request<{ status: string }>(
      token,
      `/users/${encodeURIComponent(userId)}`,
      { method: "DELETE" },
    ),
  reactivateUser: (token: string, userId: string) =>
    request<{ status: string }>(
      token,
      `/users/${encodeURIComponent(userId)}/reactivate`,
      { method: "POST" },
    ),

  // ---- Phase 16: audit log browse ----
  //
  // `actionPrefix` matches against the leading part of audit.action (e.g.
  // `user.` catches `user.create`/`user.deactivate`/`user.reactivate`).
  // `actor` is a username; the server resolves it to an actor_id and returns
  // an empty list on unknown usernames so typos don't 500.
  listAuditLogs: (
    token: string,
    opts: { limit?: number; actionPrefix?: string; actor?: string } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.limit) qs.set("limit", String(opts.limit));
    if (opts.actionPrefix) qs.set("action_prefix", opts.actionPrefix);
    if (opts.actor) qs.set("actor", opts.actor);
    const tail = qs.toString();
    return request<AuditEntry[]>(token, `/audit/logs${tail ? `?${tail}` : ""}`);
  },
};

export const adminApi = {
  getConfig: (token: string) => request<AdminConfigSnapshot>(token, "/config"),
  reloadConfig: (token: string) =>
    request<{ status: string }>(token, "/config/reload", { method: "POST" }),
  listLogs: (token: string, limit = 20) =>
    request<string[]>(token, `/logs?logs_per_page=${encodeURIComponent(String(limit))}`),
  postLog: (token: string, level: string, message: string) =>
    request<{ status: string }>(token, "/logs", {
      method: "POST",
      body: { level, message },
    }),
  clusterStatus: (token: string) => request<AdminClusterNode[]>(token, "/cluster/status"),
  getServerBusy: (token: string) => request<{ busy: boolean }>(token, "/server_busy"),
  setServerBusy: (token: string) =>
    request<{ status: string }>(token, "/server_busy", { method: "POST" }),
  clearServerBusy: (token: string) =>
    request<{ status: string }>(token, "/server_busy", { method: "DELETE" }),

  listPlugins: (token: string) => request<AdminPlugin[]>(token, "/plugins"),
  getPluginManagementCapabilities: (token: string) =>
    request<PluginManagementCapabilities>(token, "/plugins/capabilities"),
  listPluginStatuses: (token: string) =>
    request<AdminPluginStatus[]>(token, "/plugins/statuses"),
  enablePlugin: (token: string, pluginId: string) =>
    request<{ status: string }>(token, `/plugins/${encodeURIComponent(pluginId)}/enable`, {
      method: "POST",
    }),
  disablePlugin: (token: string, pluginId: string) =>
    request<{ status: string }>(token, `/plugins/${encodeURIComponent(pluginId)}/disable`, {
      method: "POST",
    }),
  uploadPlugin: (token: string, file: File, replace = false) => {
    const body = new FormData();
    body.set("plugin", file, file.name);
    return request<PluginInstallResult>(
      token,
      `/plugins${replace ? "?force=true" : ""}`,
      { method: "POST", body },
    );
  },
  deletePlugin: (token: string, pluginId: string) =>
    request<{ status: string }>(token, `/plugins/${encodeURIComponent(pluginId)}`, {
      method: "DELETE",
    }),
  getPluginConfiguration: (token: string, pluginId: string) =>
    request<PluginConfiguration>(
      token,
      `/plugins/${encodeURIComponent(pluginId)}/configuration`,
    ),
  updatePluginConfiguration: (
    token: string,
    pluginId: string,
    configuration: Record<string, unknown>,
  ) => request<{ status: string }>(
    token,
    `/plugins/${encodeURIComponent(pluginId)}/configuration`,
    { method: "PUT", body: { configuration } },
  ),

  listRoles: (token: string) => request<AdminRole[]>(token, "/roles"),
  patchRole: (token: string, roleId: string, permissions: string[]) =>
    request<AdminRole>(token, `/roles/${encodeURIComponent(roleId)}/patch`, {
      method: "PUT",
      body: { permissions },
    }),

  listJobs: (token: string) => request<AdminJob[]>(token, "/jobs"),
  createJob: (token: string, type: string) =>
    request<AdminJob>(token, "/jobs", { method: "POST", body: { type } }),
  cancelJob: (token: string, jobId: string) =>
    request<AdminJob>(token, `/jobs/${encodeURIComponent(jobId)}/cancel`, { method: "POST" }),

  getLicenseRenewal: (token: string) => request<AdminCompatRecord>(token, "/license/renewal"),
  getLicenseLoadMetric: (token: string) => request<AdminCompatRecord>(token, "/license/load_metric"),
  listLDAPGroups: (token: string) => request<AdminCompatRecord[]>(token, "/ldap/groups"),
  testLDAPConnection: (token: string) =>
    request<AdminCompatRecord>(token, "/ldap/test_connection", { method: "POST" }),
  getSAMLCertificateStatus: (token: string) =>
    request<AdminCompatRecord>(token, "/saml/certificate/status"),

  listRemoteClusters: (token: string) => request<AdminCompatRecord[]>(token, "/remotecluster"),
  listSchemes: (token: string) => request<AdminCompatRecord[]>(token, "/schemes"),
  listGroups: (token: string) => request<AdminCompatRecord[]>(token, "/groups"),
  listDataRetentionPolicies: (token: string) =>
    request<AdminCompatRecord[]>(token, "/data_retention/policies"),
  getDataRetentionPolicy: (token: string) =>
    request<AdminCompatRecord>(token, "/data_retention/policy"),
  listComplianceReports: (token: string) =>
    request<AdminCompatRecord[]>(token, "/compliance/reports"),
  getContentFlaggingConfig: (token: string) =>
    request<AdminCompatRecord>(token, "/content_flagging/config"),
  listContentFlaggingFields: (token: string) =>
    request<AdminCompatRecord[]>(token, "/content_flagging/fields"),
  searchAccessControlPolicies: (token: string) =>
    request<AdminAccessControlSearchResult>(token, "/access_control_policies/search", {
      method: "POST",
      body: { term: "", page: 0, per_page: 50 },
    }),
  listAccessControlCELFields: (token: string) =>
    request<AdminCompatRecord[]>(token, "/access_control_policies/cel/autocomplete/fields"),
};

export function openWebSocket(): WebSocket {
  const scheme = window.location.protocol === "https:" ? "wss" : "ws";
  const url = `${scheme}://${window.location.host}/api/v4/websocket`;
  return new WebSocket(url);
}

// ---- Phase 21: Mattermost-shaped preferences ----
//
// Each preference is a (user_id, category, name) -> value triple. Value is
// always a string in the official contract, even when it carries JSON —
// callers stringify before save, parse on read. Common categories the UI
// relies on: "display_settings" (theme, message_display), "favorite_channel"
// (name=channel_id, value="true").

export type Preference = {
  user_id: string;
  category: string;
  name: string;
  value: string;
};

export const prefsApi = {
  list: (token: string, userId = "me") =>
    request<Preference[]>(token, `/users/${encodeURIComponent(userId)}/preferences`),
  listCategory: (token: string, category: string, userId = "me") =>
    request<Preference[]>(
      token,
      `/users/${encodeURIComponent(userId)}/preferences/${encodeURIComponent(category)}`,
    ),
  getOne: (token: string, category: string, name: string, userId = "me") =>
    request<Preference>(
      token,
      `/users/${encodeURIComponent(userId)}/preferences/${encodeURIComponent(
        category,
      )}/name/${encodeURIComponent(name)}`,
    ),
  upsert: (token: string, prefs: Preference[], userId = "me") =>
    request<{ status: string }>(token, `/users/${encodeURIComponent(userId)}/preferences`, {
      method: "PUT",
      body: prefs,
    }),
  remove: (token: string, prefs: Preference[], userId = "me") =>
    request<{ status: string }>(
      token,
      `/users/${encodeURIComponent(userId)}/preferences/delete`,
      { method: "POST", body: prefs },
    ),
};

// ---- Phase 21: Mattermost-compat user / team / channel / post helpers ----
//
// These all mirror official Mattermost v4 endpoint shapes so the eventual
// goal of plugging the official desktop/mobile client into moyro moves
// closer with each release.

export type ChannelStats = {
  channel_id: string;
  member_count: number;
  guest_count: number;
  pinnedpost_count: number;
  files_count: number;
};

export type TeamStats = {
  team_id: string;
  total_member_count: number;
  active_member_count: number;
};

export type TeamMember = {
  team_id: string;
  user_id: string;
  roles: string;
  create_at: number;
  delete_at: number;
};

export type UsersAutocompleteResponse = {
  users: User[];
  out_of_channel: User[];
};

export const compatApi = {
  // Users
  autocompleteUsers: (token: string, name: string, limit = 25) =>
    request<UsersAutocompleteResponse>(
      token,
      `/users/autocomplete?name=${encodeURIComponent(name)}&limit=${limit}`,
    ),
  usersByIds: (token: string, ids: string[]) =>
    request<User[]>(token, `/users/ids`, { method: "POST", body: ids }),
  usersByUsernames: (token: string, names: string[]) =>
    request<User[]>(token, `/users/usernames`, { method: "POST", body: names }),
  userByEmail: (token: string, email: string) =>
    request<User>(token, `/users/email/${encodeURIComponent(email)}`),

  // Teams
  teamByName: (token: string, name: string) =>
    request<Team>(token, `/teams/name/${encodeURIComponent(name)}`),
  teamStats: (token: string, teamId: string) =>
    request<TeamStats>(token, `/teams/${encodeURIComponent(teamId)}/stats`),
  teamMembers: (token: string, teamId: string, page = 0, perPage = 60) =>
    request<TeamMember[]>(
      token,
      `/teams/${encodeURIComponent(teamId)}/members?page=${page}&per_page=${perPage}`,
    ),

  // Channels
  channelStats: (token: string, channelId: string) =>
    request<ChannelStats>(token, `/channels/${encodeURIComponent(channelId)}/stats`),
  channelByName: (token: string, teamId: string, channelName: string) =>
    request<Channel>(
      token,
      `/teams/${encodeURIComponent(teamId)}/channels/name/${encodeURIComponent(channelName)}`,
    ),
  searchChannels: (token: string, teamId: string, term: string) =>
    request<Channel[]>(token, `/teams/${encodeURIComponent(teamId)}/channels/search`, {
      method: "POST",
      body: { term },
    }),
  autocompleteChannels: (token: string, teamId: string, name: string) =>
    request<Channel[]>(
      token,
      `/teams/${encodeURIComponent(teamId)}/channels/autocomplete?name=${encodeURIComponent(name)}`,
    ),

  // Posts
  postsByIds: (token: string, ids: string[]) =>
    request<Post[]>(token, `/posts/ids`, { method: "POST", body: ids }),
  patchPost: (
    token: string,
    postId: string,
    patch: { message?: string; props?: Record<string, unknown>; file_ids?: string[] },
  ) =>
    request<Post>(token, `/posts/${encodeURIComponent(postId)}/patch`, {
      method: "PUT",
      body: patch,
    }),

  // Phase 22 — Mattermost API v4 compatibility wave 2.
  searchTeams: (token: string, term: string, page = 0, perPage = 25) =>
    request<Team[]>(token, `/teams/search`, {
      method: "POST",
      body: { term, page, per_page: perPage },
    }),
  teamNameExists: (token: string, name: string) =>
    request<{ exists: boolean }>(
      token,
      `/teams/name/${encodeURIComponent(name)}/exists`,
    ),
  listUserChannelMembers: (token: string, userId = "me") =>
    request<ChannelMember[]>(
      token,
      `/users/${encodeURIComponent(userId)}/channel_members`,
    ),
  channelMembersByIds: (token: string, channelIds: string[], userId = "me") =>
    request<ChannelMember[]>(
      token,
      `/users/${encodeURIComponent(userId)}/channels/members`,
      { method: "POST", body: { channel_ids: channelIds } },
    ),
  // Cursor-mode listPosts variants. Mattermost uses these for incremental
  // sync; the regular paged path remains available via api.listPosts.
  listPostsSince: (token: string, channelId: string, since: number, perPage = 60) =>
    request<{ order: string[]; posts: Record<string, Post> }>(
      token,
      `/channels/${encodeURIComponent(channelId)}/posts?since=${since}&per_page=${perPage}`,
    ),
  listPostsBefore: (token: string, channelId: string, postId: string, perPage = 60) =>
    request<{ order: string[]; posts: Record<string, Post> }>(
      token,
      `/channels/${encodeURIComponent(channelId)}/posts?before=${encodeURIComponent(postId)}&per_page=${perPage}`,
    ),
  listPostsAfter: (token: string, channelId: string, postId: string, perPage = 60) =>
    request<{ order: string[]; posts: Record<string, Post> }>(
      token,
      `/channels/${encodeURIComponent(channelId)}/posts?after=${encodeURIComponent(postId)}&per_page=${perPage}`,
    ),
};

// Phase 22 — channel sidebar categories.
export type SidebarCategoryType = "favorites" | "channels" | "direct_messages" | "custom";

export type SidebarCategory = {
  id: string;
  user_id: string;
  team_id: string;
  type: SidebarCategoryType;
  display_name: string;
  sort_order: number;
  sorting: "alpha" | "recent" | "manual";
  muted: boolean;
  collapsed: boolean;
  channel_ids: string[];
};

export type OrderedSidebarCategories = {
  categories: SidebarCategory[];
  order: string[];
};

export const sidebarApi = {
  list: (token: string, teamId: string, userId = "me") =>
    request<OrderedSidebarCategories>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories`,
    ),
  order: (token: string, teamId: string, userId = "me") =>
    request<string[]>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/order`,
    ),
  updateOrder: (token: string, teamId: string, order: string[], userId = "me") =>
    request<string[]>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/order`,
      { method: "PUT", body: order },
    ),
  get: (token: string, teamId: string, categoryId: string, userId = "me") =>
    request<SidebarCategory>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/${encodeURIComponent(categoryId)}`,
    ),
  create: (
    token: string,
    teamId: string,
    body: { display_name: string; channel_ids: string[] },
    userId = "me",
  ) =>
    request<SidebarCategory>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories`,
      { method: "POST", body },
    ),
  update: (
    token: string,
    teamId: string,
    category: SidebarCategory,
    userId = "me",
  ) =>
    request<SidebarCategory>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/${encodeURIComponent(category.id)}`,
      { method: "PUT", body: category },
    ),
  updateBulk: (
    token: string,
    teamId: string,
    categories: SidebarCategory[],
    userId = "me",
  ) =>
    request<SidebarCategory[]>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories`,
      { method: "PUT", body: categories },
    ),
  remove: (token: string, teamId: string, categoryId: string, userId = "me") =>
    request<{ status: string }>(
      token,
      `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/${encodeURIComponent(categoryId)}`,
      { method: "DELETE" },
    ),
};

// Phase 22 — user-level notify_props.
export type UserNotifyProps = Record<string, string>;

export const notifyApi = {
  get: (token: string, userId = "me") =>
    request<UserNotifyProps>(
      token,
      `/users/${encodeURIComponent(userId)}/notify_props`,
    ),
  put: (token: string, props: UserNotifyProps, userId = "me") =>
    request<UserNotifyProps>(
      token,
      `/users/${encodeURIComponent(userId)}/notify_props`,
      { method: "PUT", body: props },
    ),
};

// Phase 33 — Custom profile attributes. Admin-defined fields ("Department",
// "Phone", etc.) that every user can fill in. Field definitions are global
// (admin-curated); values are per-user. Values are stored as opaque JSONB
// on the server so future field types round-trip without a migration.
export type CustomProfileField = {
  id: string;
  name: string;
  type: string;
  target_id?: string;
  target_type?: string;
  attrs: Record<string, unknown>;
  sort_order: number;
  create_at: number;
  update_at: number;
  delete_at: number;
};

// Values are stored opaque so a "select" field can be a string while a
// "multi_select" can be an array — the consuming UI casts based on field.type.
export type CustomProfileValues = Record<string, unknown>;

export const customProfileApi = {
  listFields: (token: string) =>
    request<CustomProfileField[]>(token, `/custom_profile_attributes/fields`),
  createField: (
    token: string,
    field: { name: string; type?: string; attrs?: Record<string, unknown> },
  ) =>
    request<CustomProfileField>(token, `/custom_profile_attributes/fields`, {
      method: "POST",
      body: field,
    }),
  patchField: (
    token: string,
    fieldId: string,
    patch: { name?: string; type?: string; attrs?: Record<string, unknown>; sort_order?: number },
  ) =>
    request<CustomProfileField>(
      token,
      `/custom_profile_attributes/fields/${encodeURIComponent(fieldId)}`,
      { method: "PATCH", body: patch },
    ),
  deleteField: (token: string, fieldId: string) =>
    request<void>(
      token,
      `/custom_profile_attributes/fields/${encodeURIComponent(fieldId)}`,
      { method: "DELETE" },
    ),
  // Caller's own value blob (the "/values" PATCH path that the official
  // Mattermost client uses for self-edits).
  patchMyValues: (token: string, values: CustomProfileValues) =>
    request<CustomProfileValues>(token, `/custom_profile_attributes/values`, {
      method: "PATCH",
      body: values,
    }),
  getUserValues: (token: string, userId = "me") =>
    request<CustomProfileValues>(
      token,
      `/users/${encodeURIComponent(userId)}/custom_profile_attributes`,
    ),
  patchUserValues: (token: string, values: CustomProfileValues, userId = "me") =>
    request<CustomProfileValues>(
      token,
      `/users/${encodeURIComponent(userId)}/custom_profile_attributes`,
      { method: "PATCH", body: values },
    ),
};

// ---- Moyro-native management API ---------------------------------------
// These endpoints intentionally live outside the Mattermost compatibility
// surface. They back product-specific settings while `/api/v4` remains a
// stable compatibility boundary for existing clients and integrations.

export type SecretConfigured = { configured: boolean };

export type OIDCProviderSettings = {
  id?: string;
  kind: "keycloak";
  name: string;
  enabled: boolean;
  issuer_url: string;
  client_id: string;
  client_secret?: string;
  client_secret_state?: SecretConfigured;
  scopes: string[];
  username_claim: string;
  email_claim: string;
  allow_signup: boolean;
  require_verified_email: boolean;
  allow_insecure_backchannel: boolean;
  ca_certificate_pem?: string;
  redirect_url?: string;
  discovery_status?: "unknown" | "ready" | "error";
  last_tested_at?: number;
};

export type AIProviderSettings = {
  id?: string;
  name: string;
  enabled: boolean;
  api_type: "openai-compatible" | "openai";
  base_url: string;
  model: string;
  api_key?: string;
  api_key_state?: SecretConfigured;
  streaming_default: boolean;
  context_window_tokens: number;
  max_output_tokens: number;
  timeout_seconds: number;
  status?: "unknown" | "ready" | "error";
  last_tested_at?: number;
};

export type KeyPolicySettings = {
  enabled: boolean;
  allowed_scopes: string[];
  default_scopes: string[];
  default_ttl_days: number;
  max_ttl_days: number;
  rotation_days: number;
  rotation_grace_hours: number;
  allow_personal_keys: boolean;
  allow_scope_self_service: boolean;
};

export type SiteSettings = {
  site_name: string;
  public_base_url: string;
  allowed_outgoing_hosts: string[];
  trusted_proxy_cidrs: string[];
  local_signup_enabled: boolean;
	draft_storage_mode: "local" | "session" | "disabled";
	draft_retention_days: number;
	draft_clear_on_logout: boolean;
};

export type RBACPermission = {
  name: string;
  description: string;
  resource_type: string;
  built_in: boolean;
};

export type EffectivePermissions = {
  permissions: string[];
};

export type RBACRole = {
  id: string;
  name: string;
  display_name: string;
  description: string;
  scope_type: string;
  built_in: boolean;
  permissions: string[];
  revision: number;
  create_at: number;
  update_at: number;
};

export type MCPSettings = {
  enabled: boolean;
  transport: "streamable-http";
  endpoint_path: string;
  allowed_tools: string[];
  allowed_resources: string[];
  required_scopes: string[];
};

export type ApprovalPolicy = {
  id?: string;
  name: string;
  enabled: boolean;
  protected_actions: string[];
  reviewer_roles: string[];
  require_rejection_reason: boolean;
  allow_self_approval: boolean;
  expires_after_hours: number;
};

export type ApprovalRequestServerPreview = {
  title: string;
  risk_level: "low" | "medium" | "high" | "unknown";
  actor: { type: string; display_name: string };
  target: { type: string; display_name: string };
  changes: Array<{ label: string; after: string }>;
  policy: { name: string; reason: string };
  secrets_redacted: boolean;
};

export type ApprovalRequest = {
  id: string;
  policy_id: string;
  action_type: string;
  requester_id: string;
  team_id: string;
  resource_type: string;
  resource_id: string;
  preview?: ApprovalRequestServerPreview;
  // Compatibility fallback for pre-preview Moyro servers. Current native
  // browser APIs omit this field so execution credentials never reach the UI.
  payload?: unknown;
  status: string;
  idempotency_key?: string;
  create_at: number;
  update_at: number;
  decided_at: number;
  executed_at: number;
  expires_at: number;
};

export type PersonalAPIKey = {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  status: "active" | "grace" | "revoked" | "expired";
  created_at: number;
  last_used_at?: number;
  expires_at?: number;
};

export type PersonalAPIKeySecret = PersonalAPIKey & { secret: string };

export type AdminAPIKey = {
  id: string;
  user_id: string;
  username: string;
  email: string;
  name: string;
  prefix: string;
  kind: "user" | "service" | "mcp";
  status: "active" | "grace" | "revoked" | "expired";
  scopes: string[];
  created_at: number;
  last_used_at: number;
  expires_at: number;
  revoked_at: number;
};

export type PersonalAIPreferences = {
  enabled: boolean;
  provider_id?: string;
  model?: string;
  streaming: boolean;
  max_output_tokens: number;
  temperature: number;
};

export type AICompletionRequest = {
  model?: string;
  messages: { role: "system" | "user" | "assistant"; content: string }[];
  max_output_tokens?: number;
  temperature?: number;
  stream?: true;
};

export const publicMoyroApi = {
  systemInfo: () => moyroRequest<SystemInfo>(null, "/system/info"),
};

export const moyroAdminApi = {
  getSettings: <T>(token: string, section: "site" | "key-policy" | "mcp") =>
    moyroRequest<T>(token, `/admin/settings/${encodeURIComponent(section)}`),
  patchSettings: <T>(token: string, section: "site" | "key-policy" | "mcp", value: T) =>
    moyroRequest<T>(token, `/admin/settings/${encodeURIComponent(section)}`, {
      method: "PATCH",
      body: value,
    }),

  listPermissions: (token: string) =>
    moyroRequest<RBACPermission[]>(token, "/admin/permissions"),
  listRoles: (token: string) =>
    moyroRequest<RBACRole[]>(token, "/admin/roles"),
  patchRole: (token: string, id: string, value: { permissions: string[]; revision: number }) =>
    moyroRequest<RBACRole>(token, `/admin/roles/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),

  listAPIKeys: (token: string, page = 0, perPage = 100) =>
    moyroRequest<AdminAPIKey[]>(
      token,
      `/admin/api-keys?page=${encodeURIComponent(String(page))}&per_page=${encodeURIComponent(String(perPage))}`,
    ),
  revokeAPIKey: (token: string, id: string) =>
    moyroRequest<void>(token, `/admin/api-keys/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  listOIDCProviders: (token: string) =>
    moyroRequest<OIDCProviderSettings[]>(token, "/admin/oidc/providers"),
  createOIDCProvider: (token: string, value: OIDCProviderSettings) =>
    moyroRequest<OIDCProviderSettings>(token, "/admin/oidc/providers", {
      method: "POST",
      body: value,
    }),
  patchOIDCProvider: (token: string, id: string, value: Partial<OIDCProviderSettings>) =>
    moyroRequest<OIDCProviderSettings>(token, `/admin/oidc/providers/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
  testOIDCProvider: (token: string, value: OIDCProviderSettings) =>
    moyroRequest<{ ok: boolean; issuer?: string; message?: string }>(
      token,
      "/admin/oidc/providers/test",
      { method: "POST", body: value },
    ),

  listAIProviders: (token: string) =>
    moyroRequest<AIProviderSettings[]>(token, "/admin/ai/providers"),
  createAIProvider: (token: string, value: AIProviderSettings) =>
    moyroRequest<AIProviderSettings>(token, "/admin/ai/providers", {
      method: "POST",
      body: value,
    }),
  patchAIProvider: (token: string, id: string, value: Partial<AIProviderSettings>) =>
    moyroRequest<AIProviderSettings>(token, `/admin/ai/providers/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
  testAIProvider: (token: string, value: AIProviderSettings) =>
    moyroRequest<{ ok: boolean; model?: string; message?: string }>(
      token,
      "/admin/ai/providers/test",
      { method: "POST", body: value },
    ),

  listApprovalPolicies: (token: string) =>
    moyroRequest<ApprovalPolicy[]>(token, "/admin/approval-policies"),
  createApprovalPolicy: (token: string, value: ApprovalPolicy) =>
    moyroRequest<ApprovalPolicy>(token, "/admin/approval-policies", {
      method: "POST",
      body: value,
    }),
  patchApprovalPolicy: (token: string, id: string, value: Partial<ApprovalPolicy>) =>
    moyroRequest<ApprovalPolicy>(token, `/admin/approval-policies/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
};

export const moyroMeApi = {
  getPermissions: (token: string) =>
    moyroRequest<EffectivePermissions>(token, "/me/permissions"),
  listApprovalRequests: (token: string, status = "") =>
    moyroRequest<ApprovalRequest[]>(
      token,
      `/me/approval-requests${status ? `?status=${encodeURIComponent(status)}` : ""}`,
    ),
  getAIPreferences: (token: string) =>
    moyroRequest<PersonalAIPreferences>(token, "/me/ai-preferences"),
  patchAIPreferences: (token: string, value: PersonalAIPreferences) =>
    moyroRequest<PersonalAIPreferences>(token, "/me/ai-preferences", {
      method: "PATCH",
      body: value,
    }),
  listAPIKeys: (token: string) =>
    moyroRequest<PersonalAPIKey[]>(token, "/me/api-keys"),
  createAPIKey: (
    token: string,
    value: { name: string; scopes: string[]; ttl_days?: number },
  ) =>
    moyroRequest<PersonalAPIKeySecret>(token, "/me/api-keys", {
      method: "POST",
      body: value,
    }),
  patchAPIKey: (token: string, id: string, value: { name?: string; scopes?: string[] }) =>
    moyroRequest<PersonalAPIKey>(token, `/me/api-keys/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: value,
    }),
  deleteAPIKey: (token: string, id: string) =>
    moyroRequest<void>(token, `/me/api-keys/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),
  rotateAPIKey: (token: string, id: string) =>
    moyroRequest<PersonalAPIKeySecret>(token, `/me/api-keys/${encodeURIComponent(id)}/rotate`, {
      method: "POST",
    }),
  streamAICompletion: async (
    token: string,
    value: AICompletionRequest,
    onDelta: (delta: string) => void,
    signal?: AbortSignal,
  ): Promise<void> => {
    const res = await fetch(`${MOYRO_BASE}/me/ai/completions`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
        Accept: "text/event-stream",
      },
      body: JSON.stringify({ ...value, stream: true }),
      signal,
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({ message: res.statusText }));
      throw new Error(err.message ?? `HTTP ${res.status}`);
    }
    if (!res.body) throw new Error("streaming response body is unavailable");

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    const emitData = (data: string) => {
      if (!data || data === "[DONE]") return;
      try {
        const parsed = JSON.parse(data) as {
          delta?: string | { text?: string; content?: string };
          content?: string;
          choices?: { delta?: { content?: string }; text?: string }[];
        };
        const delta = typeof parsed.delta === "string"
          ? parsed.delta
          : parsed.delta?.text
            ?? parsed.delta?.content
            ?? parsed.choices?.[0]?.delta?.content
            ?? parsed.choices?.[0]?.text
            ?? parsed.content
            ?? "";
        if (delta) onDelta(delta);
      } catch {
        onDelta(data);
      }
    };
    while (true) {
      const { value: chunk, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(chunk, { stream: true });
      const lines = buffer.split(/\r?\n/);
      buffer = lines.pop() ?? "";
      for (const line of lines) {
        if (!line.startsWith("data:")) continue;
        emitData(line.slice(5).trim());
      }
    }
    const tail = buffer.trim();
    if (tail.startsWith("data:")) emitData(tail.slice(5).trim());
  },
};

export const moyroReviewApi = {
  listApprovalRequests: (token: string, status = "") =>
    moyroRequest<ApprovalRequest[]>(
      token,
      `/reviews/approval-requests${status ? `?status=${encodeURIComponent(status)}` : ""}`,
    ),
  decideApprovalRequest: (
    token: string,
    id: string,
    value: { decision: "approve" | "reject"; reason: string },
  ) => moyroRequest<ApprovalRequest>(
    token,
    `/reviews/approval-requests/${encodeURIComponent(id)}/decision`,
    { method: "POST", body: value },
  ),
};
