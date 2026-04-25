const BASE = "/api/v4";

type FetchOpts = Omit<RequestInit, "body"> & { body?: unknown };

async function request<T>(token: string | null, path: string, opts: FetchOpts = {}): Promise<T> {
  const headers = new Headers(opts.headers);
  const hasBody = opts.body !== undefined;
  if (hasBody && !(opts.body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
  }
  if (token) headers.set("Authorization", `Bearer ${token}`);

  let body: BodyInit | undefined;
  if (!hasBody) {
    body = undefined;
  } else if (opts.body instanceof FormData) {
    body = opts.body;
  } else {
    body = JSON.stringify(opts.body);
  }

  const res = await fetch(`${BASE}${path}`, { ...opts, headers, body });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }));
    throw new Error(err.message ?? `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : (undefined as unknown)) as T;
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
    request<{ token: string; user: User }>(null, "/users/login", {
      method: "POST",
      body: { login_id, password },
    }),
  me: (token: string) => request<User>(token, "/users/me"),
  ping: () => request<SystemPing>(null, "/system/ping"),
  logout: (token: string) =>
    request<{ status: string }>(token, "/users/logout", { method: "POST" }),

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
  userImageURL: (userId: string, version?: number | string) => {
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

  // Phase 18: saved posts (personal bookmarks). List returns the same
  // shape as channel post listings so the UI can reuse MessageRow.
  listSavedPosts: (token: string, limit = 20, offset = 0) =>
    request<PostList>(
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

  // Phase 18: link preview image proxy URL builder. Kept as a URL (not a
  // fetch) because the browser embeds it in <img src> — the <img> does
  // the GET and bypasses our JSON request wrapper.
  linkPreviewImageURL: (rawURL: string) =>
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
  fileDownloadURL: (token: string, fileId: string) =>
    // Direct link used in <a href>/<img src>. Token is passed as query arg since
    // the <a href> download flow can't set Authorization headers.
    `${BASE}/files/${fileId}?access_token=${encodeURIComponent(token)}`,
  fileThumbnailURL: (token: string, fileId: string) =>
    `${BASE}/files/${fileId}/thumbnail?access_token=${encodeURIComponent(token)}`,

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
  emojiImageURL: (token: string, emojiId: string) =>
    `${BASE}/emoji/${emojiId}/image?access_token=${encodeURIComponent(token)}`,

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

// ---- Admin/operator compatibility types ----

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
  [key: string]: unknown;
};

export type AdminPluginStatus = {
  plugin_id: string;
  state: string;
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

// ---- Phase 12 API extensions ----
//
// Mutating the frozen `api` literal above would force a reorganisation of
// the whole file; instead, extend it as a secondary export and merge at
// the call site. Works cleanly because `api` is a const, not a class.

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
};

export function openWebSocket(token: string): WebSocket {
  const scheme = window.location.protocol === "https:" ? "wss" : "ws";
  const url = `${scheme}://${window.location.host}/api/v4/websocket?access_token=${encodeURIComponent(token)}`;
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
// goal of plugging the official desktop/mobile client into Moddle moves
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
