// Core chat surface: users, teams, channels, posts, files, and search.
//
// Split out of the former single `client.ts`. `client.ts` re-exports every
// symbol here, so callers keep importing from `@/api/client`.
import {
  COMPAT_API_BASE as BASE,
  BROWSER_SESSION_TOKEN,
  moyroRequest,
  compatRequest as request,
} from "./transport";
import { authenticatedMediaBlob } from "./media";


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
  // Admin include-deleted responses populate this reliably.
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
