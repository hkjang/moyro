const BASE = "/api/v4";
async function request(token, path, opts = {}) {
    const headers = new Headers(opts.headers);
    const hasBody = opts.body !== undefined;
    if (hasBody && !(opts.body instanceof FormData)) {
        headers.set("Content-Type", "application/json");
    }
    if (token)
        headers.set("Authorization", `Bearer ${token}`);
    let body;
    if (!hasBody) {
        body = undefined;
    }
    else if (opts.body instanceof FormData) {
        body = opts.body;
    }
    else {
        body = JSON.stringify(opts.body);
    }
    const res = await fetch(`${BASE}${path}`, { ...opts, headers, body });
    if (!res.ok) {
        const err = await res.json().catch(() => ({ message: res.statusText }));
        throw new Error(err.message ?? `HTTP ${res.status}`);
    }
    if (res.status === 204)
        return undefined;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined);
}
export const api = {
    // auth / session
    //
    // `inviteId` is optional; when present the server validates + consumes the
    // invite in the same tx as the INSERT, then auto-joins the target team and
    // its default channels. Invalid/expired/revoked tokens 400 before the user
    // row is created.
    register: (username, email, password, inviteId = "") => request(null, "/users", {
        method: "POST",
        body: inviteId
            ? { username, email, password, invite_id: inviteId }
            : { username, email, password },
    }),
    login: (login_id, password) => request(null, "/users/login", {
        method: "POST",
        body: { login_id, password },
    }),
    me: (token) => request(token, "/users/me"),
    ping: () => request(null, "/system/ping"),
    logout: (token) => request(token, "/users/logout", { method: "POST" }),
    // user directory
    //
    // `includeDeleted` is admin-only on the server (non-admins silently get
    // the active-only slice), but exposing it on the public signature keeps
    // call sites simple — the IntegrationsPanel "사용자" tab uses it to
    // surface reactivate buttons.
    listUsers: (token, page = 0, perPage = 60, includeDeleted = false) => {
        const qs = new URLSearchParams();
        qs.set("page", String(page));
        qs.set("per_page", String(perPage));
        if (includeDeleted)
            qs.set("include_deleted", "true");
        return request(token, `/users?${qs.toString()}`);
    },
    getUser: (token, userId) => request(token, `/users/${userId}`),
    getUserByUsername: (token, username) => request(token, `/users/username/${encodeURIComponent(username)}`),
    searchUsers: (token, term, limit = 20) => request(token, "/users/search", {
        method: "POST",
        body: { term, limit },
    }),
    // profile
    updateProfile: (token, username, email) => request(token, "/users/me", {
        method: "PUT",
        body: { username, email },
    }),
    // Self-upload profile picture. Server caps size at 512KB and enforces
    // image/* mime. Returns the refreshed User — dispatch setAuth to update
    // sidebar + message rows in one pass.
    uploadProfileImage: (token, file) => {
        const fd = new FormData();
        fd.append("image", file, file.name);
        return request(token, "/users/me/image", { method: "POST", body: fd });
    },
    // URL for any user's avatar. When picture is empty the server 404s and
    // the consumer <img onError> falls back to initials. We include update_at
    // as the cache-buster so a new upload invalidates the browser cache.
    userImageURL: (userId, version) => {
        const suffix = version !== undefined ? `?v=${encodeURIComponent(String(version))}` : "";
        return `${BASE}/users/${encodeURIComponent(userId)}/image${suffix}`;
    },
    updatePassword: (token, current_password, new_password) => request(token, "/users/me/password", {
        method: "PUT",
        body: { current_password, new_password },
    }),
    // Phase 17 — email notification prefs (currently just daily digest toggle).
    // Shape is intentionally small; the server stores it as JSONB so adding
    // more keys later is schema-free.
    getEmailPrefs: (token) => request(token, "/users/me/email_prefs"),
    updateEmailPrefs: (token, prefs) => request(token, "/users/me/email_prefs", {
        method: "PUT",
        body: prefs,
    }),
    // user status
    getUserStatus: (token, userId) => request(token, `/users/${userId}/status`),
    getUserStatusesByIDs: (token, ids) => request(token, `/users/statuses/ids`, {
        method: "POST",
        body: ids,
    }),
    updateMyStatus: (token, status, manual = true) => request(token, `/users/me/status`, {
        method: "PUT",
        body: { status, manual },
    }),
    // teams
    listTeams: (token) => request(token, "/users/me/teams"),
    createTeam: (token, name, display_name) => request(token, "/teams", {
        method: "POST",
        body: { name, display_name, type: "O" },
    }),
    // Phase 18: ranked + filtered search. Filters are parsed client-side
    // from search tokens (`from:`, `in:`, `before:`, `after:`, `has:file`,
    // `has:link`) and lifted into explicit fields so the server doesn't
    // re-parse the free-form query.
    searchPosts: (token, teamId, terms, opts = {}) => request(token, `/teams/${teamId}/posts/search`, {
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
    listSavedPosts: (token, limit = 20, offset = 0) => request(token, `/users/me/saved_posts?limit=${limit}&offset=${offset}`),
    savedPostsByIds: (token, postIds) => request(token, `/users/me/saved_posts/ids`, {
        method: "POST",
        body: { post_ids: postIds },
    }),
    savePost: (token, postId) => request(token, `/users/me/saved_posts/${postId}`, { method: "POST" }),
    unsavePost: (token, postId) => request(token, `/users/me/saved_posts/${postId}`, { method: "DELETE" }),
    // Phase 18: public channel discovery. Lists channels of type 'O' the
    // caller hasn't joined yet, filtered by optional `q` (ILIKE on name +
    // display_name). `joinChannel` already exists (Phase 3) for the action.
    discoverChannels: (token, teamId, opts = {}) => {
        const qs = new URLSearchParams();
        if (opts.q)
            qs.set("q", opts.q);
        qs.set("limit", String(opts.limit ?? 20));
        qs.set("offset", String(opts.offset ?? 0));
        return request(token, `/teams/${teamId}/channels/discover?${qs.toString()}`);
    },
    // Phase 18: link preview image proxy URL builder. Kept as a URL (not a
    // fetch) because the browser embeds it in <img src> — the <img> does
    // the GET and bypasses our JSON request wrapper.
    linkPreviewImageURL: (rawURL) => `${BASE}/link_preview_image?url=${encodeURIComponent(rawURL)}`,
    // Phase 19: scheduled messages. createScheduledPost returns the fresh
    // row; the server also fires a scheduled_post_created WS so peer tabs
    // update without a refetch.
    createScheduledPost: (token, body) => request(token, `/scheduled_posts`, {
        method: "POST",
        body,
    }),
    listMyScheduledPosts: (token) => request(token, `/users/me/scheduled_posts`),
    deleteScheduledPost: (token, id) => request(token, `/scheduled_posts/${id}`, {
        method: "DELETE",
    }),
    // Phase 19: post reminders. Caller must be a channel member of the post;
    // the server enforces that and 404s otherwise.
    createPostReminder: (token, postId, remindAt) => request(token, `/posts/${postId}/remind_me`, {
        method: "POST",
        body: { remind_at: remindAt },
    }),
    listMyReminders: (token) => request(token, `/users/me/reminders`),
    deleteReminder: (token, id) => request(token, `/users/me/reminders/${id}`, {
        method: "DELETE",
    }),
    // channels
    //
    // When `includeDeleted` is true the server drops the `delete_at = 0` filter
    // so archived channels show up with a non-zero `delete_at`. Used to drive
    // the "보관된 채널 보기" sidebar toggle.
    listChannels: (token, teamId, includeDeleted = false) => request(token, `/users/me/teams/${teamId}/channels${includeDeleted ? "?include_deleted=true" : ""}`),
    createChannel: (token, teamId, name, display_name) => request(token, "/channels", {
        method: "POST",
        body: { team_id: teamId, name, display_name, type: "O" },
    }),
    createDirectChannel: (token, userIds) => request(token, "/channels/direct", {
        method: "POST",
        body: userIds,
    }),
    getChannel: (token, channelId) => request(token, `/channels/${channelId}`),
    patchChannel: (token, channelId, patch) => request(token, `/channels/${channelId}`, {
        method: "PUT",
        body: patch,
    }),
    viewChannel: (token, channelId) => request(token, `/channels/${channelId}/view`, { method: "POST" }),
    // channel members
    listChannelMembers: (token, channelId) => request(token, `/channels/${channelId}/members`),
    listMyChannelMembers: (token, teamId) => request(token, `/users/me/teams/${teamId}/channels/members`),
    getMyChannelNotifyProps: (token, channelId) => request(token, `/channels/${channelId}/members/me/notify_props`),
    setMyChannelNotifyProps: (token, channelId, props) => request(token, `/channels/${channelId}/members/me/notify_props`, { method: "PUT", body: props }),
    addChannelMember: (token, channelId, userId) => request(token, `/channels/${channelId}/members`, {
        method: "POST",
        body: { user_id: userId },
    }),
    // Phase 18: self-join a public channel during the discovery flow.
    // Distinct from addChannelMember — outsiders can't hit that route
    // because it requires prior membership for spam control.
    joinChannel: (token, channelId) => request(token, `/channels/${channelId}/join`, {
        method: "POST",
    }),
    // Channel-scoped mention autocomplete. `prefix` is the (empty-trimmed)
    // token after `@` in the Composer. Returns at most `limit` users who are
    // members of the channel and whose username matches. Server caps at 25.
    channelMembersAutocomplete: (token, channelId, prefix, limit = 8) => request(token, `/channels/${channelId}/members/autocomplete?prefix=${encodeURIComponent(prefix)}&limit=${limit}`),
    removeChannelMember: (token, channelId, userId) => request(token, `/channels/${channelId}/members/${userId}`, {
        method: "DELETE",
    }),
    // posts
    listPosts: (token, channelId, page = 0, perPage = 60) => request(token, `/channels/${channelId}/posts?page=${page}&per_page=${perPage}`),
    createPost: (token, channelId, message, rootId = "", fileIds = []) => request(token, `/posts`, {
        method: "POST",
        body: { channel_id: channelId, message, root_id: rootId, file_ids: fileIds },
    }),
    updatePost: (token, postId, message, props) => request(token, `/posts/${postId}`, {
        method: "PUT",
        body: { id: postId, message, props: props ?? {} },
    }),
    deletePost: (token, postId) => request(token, `/posts/${postId}`, { method: "DELETE" }),
    pinPost: (token, postId) => request(token, `/posts/${postId}/pin`, { method: "POST" }),
    unpinPost: (token, postId) => request(token, `/posts/${postId}/unpin`, { method: "POST" }),
    listPinned: (token, channelId) => request(token, `/channels/${channelId}/pinned`),
    listThread: (token, postId) => request(token, `/posts/${postId}/thread`),
    // reactions
    addReaction: (token, postId, userId, emoji) => request(token, `/reactions`, {
        method: "POST",
        body: { post_id: postId, user_id: userId, emoji_name: emoji },
    }),
    listReactions: (token, postId) => request(token, `/posts/${postId}/reactions`),
    removeReaction: (token, postId, userId, emoji) => request(token, `/users/${userId}/posts/${postId}/reactions/${encodeURIComponent(emoji)}`, { method: "DELETE" }),
    // slash commands
    executeCommand: (token, teamId, channelId, command) => request(token, "/commands/execute", {
        method: "POST",
        body: { team_id: teamId, channel_id: channelId, command },
    }),
    // files
    uploadFiles: (token, channelId, files) => {
        const fd = new FormData();
        for (const f of files)
            fd.append("files", f, f.name);
        return request(token, `/files?channel_id=${encodeURIComponent(channelId)}`, { method: "POST", body: fd });
    },
    fileInfo: (token, fileId) => request(token, `/files/${fileId}/info`),
    fileDownloadURL: (token, fileId) => 
    // Direct link used in <a href>/<img src>. Token is passed as query arg since
    // the <a href> download flow can't set Authorization headers.
    `${BASE}/files/${fileId}?access_token=${encodeURIComponent(token)}`,
    fileThumbnailURL: (token, fileId) => `${BASE}/files/${fileId}/thumbnail?access_token=${encodeURIComponent(token)}`,
    // Custom emojis. Creation uses multipart (so we can't reuse the JSON
    // helper), List/delete are plain JSON.
    listEmojis: (token, page = 0, perPage = 200) => request(token, `/emoji?page=${page}&per_page=${perPage}`),
    createEmoji: (token, name, image) => {
        const fd = new FormData();
        fd.append("name", name);
        fd.append("image", image, image.name);
        return request(token, "/emoji", { method: "POST", body: fd });
    },
    deleteEmoji: (token, emojiId) => request(token, `/emoji/${emojiId}`, { method: "DELETE" }),
    emojiImageURL: (token, emojiId) => `${BASE}/emoji/${emojiId}/image?access_token=${encodeURIComponent(token)}`,
    // ---- Phase 16: invites (self-signup preview) ----
    //
    // Public endpoint — no token. Rate-limited 5/s per IP on the server. Used
    // by LoginView when it spots an `#invite=<id>` fragment in the URL so we
    // can show "X 팀에 초대되었습니다" before the user even registers.
    getInvite: (inviteId) => request(null, `/invites/${encodeURIComponent(inviteId)}`),
    // ---- Phase 16: self session management ----
    listMySessions: (token) => request(token, "/users/me/sessions"),
    revokeSession: (token, sessionId) => request(token, `/users/me/sessions/${encodeURIComponent(sessionId)}`, { method: "DELETE" }),
    // Server preserves the bearer token's own session and kills all others.
    revokeOtherSessions: (token) => request(token, `/users/me/sessions`, { method: "DELETE" }),
    // ---- Phase 16: channel archive / restore ----
    //
    // Archive soft-deletes by stamping `channels.delete_at`; Restore zeroes it.
    // The server broadcasts `channel_deleted` / `channel_restored` WS events so
    // every connected client can fix up its sidebar without a reload.
    archiveChannel: (token, channelId) => request(token, `/channels/${encodeURIComponent(channelId)}/archive`, { method: "POST" }),
    restoreChannel: (token, channelId) => request(token, `/channels/${encodeURIComponent(channelId)}/restore`, { method: "POST" }),
};
// ---- Phase 12 API extensions ----
//
// Mutating the frozen `api` literal above would force a reorganisation of
// the whole file; instead, extend it as a secondary export and merge at
// the call site. Works cleanly because `api` is a const, not a class.
export const integrationsApi = {
    // bots
    listBots: (token) => request(token, "/bots"),
    createBot: (token, username, display_name, description) => request(token, "/bots", {
        method: "POST",
        body: { username, display_name, description },
    }),
    disableBot: (token, botId) => request(token, `/bots/${botId}`, { method: "DELETE" }),
    // PATs
    listTokens: (token, userId) => request(token, `/users/${userId}/tokens`),
    createToken: (token, userId, description) => request(token, `/users/${userId}/tokens`, {
        method: "POST",
        body: { description },
    }),
    revokeToken: (token, tokenId) => request(token, `/tokens/${tokenId}/revoke`, { method: "POST" }),
    // incoming webhooks
    listIncoming: (token) => request(token, "/hooks/incoming"),
    createIncoming: (token, channel_id, display_name, username = "", icon_url = "", channel_locked = true) => request(token, "/hooks/incoming", {
        method: "POST",
        body: { channel_id, display_name, username, icon_url, channel_locked },
    }),
    deleteIncoming: (token, hookId) => request(token, `/hooks/incoming/${hookId}`, { method: "DELETE" }),
    // outgoing webhooks
    listOutgoing: (token) => request(token, "/hooks/outgoing"),
    createOutgoing: (token, team_id, channel_id, trigger_words, callback_urls, display_name = "", trigger_when = 0, content_type = "application/json") => request(token, "/hooks/outgoing", {
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
    deleteOutgoing: (token, hookId) => request(token, `/hooks/outgoing/${hookId}`, { method: "DELETE" }),
    // ---- Phase 16: team invites (admin) ----
    //
    // `maxUses = 0` means unlimited within the TTL window. `ttlSeconds` is
    // converted server-side to an expires_at; no client-side clock is trusted.
    createInvite: (token, teamId, maxUses, ttlSeconds) => request(token, `/teams/${encodeURIComponent(teamId)}/invites`, {
        method: "POST",
        body: { max_uses: maxUses, ttl_seconds: ttlSeconds },
    }),
    listInvites: (token, teamId) => request(token, `/teams/${encodeURIComponent(teamId)}/invites`),
    revokeInvite: (token, teamId, inviteId) => request(token, `/teams/${encodeURIComponent(teamId)}/invites/${encodeURIComponent(inviteId)}`, { method: "DELETE" }),
    // ---- Phase 16: user deactivate / reactivate ----
    //
    // Deactivate also drops sessions + kicks live WS sockets server-side, so
    // the target's other tabs get a close frame within the read timeout.
    // Reactivate is admin-only and just clears users.delete_at.
    deactivateUser: (token, userId) => request(token, `/users/${encodeURIComponent(userId)}`, { method: "DELETE" }),
    reactivateUser: (token, userId) => request(token, `/users/${encodeURIComponent(userId)}/reactivate`, { method: "POST" }),
    // ---- Phase 16: audit log browse ----
    //
    // `actionPrefix` matches against the leading part of audit.action (e.g.
    // `user.` catches `user.create`/`user.deactivate`/`user.reactivate`).
    // `actor` is a username; the server resolves it to an actor_id and returns
    // an empty list on unknown usernames so typos don't 500.
    listAuditLogs: (token, opts = {}) => {
        const qs = new URLSearchParams();
        if (opts.limit)
            qs.set("limit", String(opts.limit));
        if (opts.actionPrefix)
            qs.set("action_prefix", opts.actionPrefix);
        if (opts.actor)
            qs.set("actor", opts.actor);
        const tail = qs.toString();
        return request(token, `/audit/logs${tail ? `?${tail}` : ""}`);
    },
};
export const adminApi = {
    getConfig: (token) => request(token, "/config"),
    reloadConfig: (token) => request(token, "/config/reload", { method: "POST" }),
    listLogs: (token, limit = 20) => request(token, `/logs?logs_per_page=${encodeURIComponent(String(limit))}`),
    postLog: (token, level, message) => request(token, "/logs", {
        method: "POST",
        body: { level, message },
    }),
    clusterStatus: (token) => request(token, "/cluster/status"),
    getServerBusy: (token) => request(token, "/server_busy"),
    setServerBusy: (token) => request(token, "/server_busy", { method: "POST" }),
    clearServerBusy: (token) => request(token, "/server_busy", { method: "DELETE" }),
    listPlugins: (token) => request(token, "/plugins"),
    listPluginStatuses: (token) => request(token, "/plugins/statuses"),
    enablePlugin: (token, pluginId) => request(token, `/plugins/${encodeURIComponent(pluginId)}/enable`, {
        method: "POST",
    }),
    disablePlugin: (token, pluginId) => request(token, `/plugins/${encodeURIComponent(pluginId)}/disable`, {
        method: "POST",
    }),
    listRoles: (token) => request(token, "/roles"),
    patchRole: (token, roleId, permissions) => request(token, `/roles/${encodeURIComponent(roleId)}/patch`, {
        method: "PUT",
        body: { permissions },
    }),
    listJobs: (token) => request(token, "/jobs"),
    createJob: (token, type) => request(token, "/jobs", { method: "POST", body: { type } }),
    cancelJob: (token, jobId) => request(token, `/jobs/${encodeURIComponent(jobId)}/cancel`, { method: "POST" }),
};
export function openWebSocket(token) {
    const scheme = window.location.protocol === "https:" ? "wss" : "ws";
    const url = `${scheme}://${window.location.host}/api/v4/websocket?access_token=${encodeURIComponent(token)}`;
    return new WebSocket(url);
}
export const prefsApi = {
    list: (token, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/preferences`),
    listCategory: (token, category, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/preferences/${encodeURIComponent(category)}`),
    getOne: (token, category, name, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/preferences/${encodeURIComponent(category)}/name/${encodeURIComponent(name)}`),
    upsert: (token, prefs, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/preferences`, {
        method: "PUT",
        body: prefs,
    }),
    remove: (token, prefs, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/preferences/delete`, { method: "POST", body: prefs }),
};
export const compatApi = {
    // Users
    autocompleteUsers: (token, name, limit = 25) => request(token, `/users/autocomplete?name=${encodeURIComponent(name)}&limit=${limit}`),
    usersByIds: (token, ids) => request(token, `/users/ids`, { method: "POST", body: ids }),
    usersByUsernames: (token, names) => request(token, `/users/usernames`, { method: "POST", body: names }),
    userByEmail: (token, email) => request(token, `/users/email/${encodeURIComponent(email)}`),
    // Teams
    teamByName: (token, name) => request(token, `/teams/name/${encodeURIComponent(name)}`),
    teamStats: (token, teamId) => request(token, `/teams/${encodeURIComponent(teamId)}/stats`),
    teamMembers: (token, teamId, page = 0, perPage = 60) => request(token, `/teams/${encodeURIComponent(teamId)}/members?page=${page}&per_page=${perPage}`),
    // Channels
    channelStats: (token, channelId) => request(token, `/channels/${encodeURIComponent(channelId)}/stats`),
    channelByName: (token, teamId, channelName) => request(token, `/teams/${encodeURIComponent(teamId)}/channels/name/${encodeURIComponent(channelName)}`),
    searchChannels: (token, teamId, term) => request(token, `/teams/${encodeURIComponent(teamId)}/channels/search`, {
        method: "POST",
        body: { term },
    }),
    autocompleteChannels: (token, teamId, name) => request(token, `/teams/${encodeURIComponent(teamId)}/channels/autocomplete?name=${encodeURIComponent(name)}`),
    // Posts
    postsByIds: (token, ids) => request(token, `/posts/ids`, { method: "POST", body: ids }),
    patchPost: (token, postId, patch) => request(token, `/posts/${encodeURIComponent(postId)}/patch`, {
        method: "PUT",
        body: patch,
    }),
    // Phase 22 — Mattermost API v4 compatibility wave 2.
    searchTeams: (token, term, page = 0, perPage = 25) => request(token, `/teams/search`, {
        method: "POST",
        body: { term, page, per_page: perPage },
    }),
    teamNameExists: (token, name) => request(token, `/teams/name/${encodeURIComponent(name)}/exists`),
    listUserChannelMembers: (token, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/channel_members`),
    channelMembersByIds: (token, channelIds, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/channels/members`, { method: "POST", body: { channel_ids: channelIds } }),
    // Cursor-mode listPosts variants. Mattermost uses these for incremental
    // sync; the regular paged path remains available via api.listPosts.
    listPostsSince: (token, channelId, since, perPage = 60) => request(token, `/channels/${encodeURIComponent(channelId)}/posts?since=${since}&per_page=${perPage}`),
    listPostsBefore: (token, channelId, postId, perPage = 60) => request(token, `/channels/${encodeURIComponent(channelId)}/posts?before=${encodeURIComponent(postId)}&per_page=${perPage}`),
    listPostsAfter: (token, channelId, postId, perPage = 60) => request(token, `/channels/${encodeURIComponent(channelId)}/posts?after=${encodeURIComponent(postId)}&per_page=${perPage}`),
};
export const sidebarApi = {
    list: (token, teamId, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories`),
    order: (token, teamId, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/order`),
    updateOrder: (token, teamId, order, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/order`, { method: "PUT", body: order }),
    get: (token, teamId, categoryId, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/${encodeURIComponent(categoryId)}`),
    create: (token, teamId, body, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories`, { method: "POST", body }),
    update: (token, teamId, category, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/${encodeURIComponent(category.id)}`, { method: "PUT", body: category }),
    updateBulk: (token, teamId, categories, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories`, { method: "PUT", body: categories }),
    remove: (token, teamId, categoryId, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/teams/${encodeURIComponent(teamId)}/channels/categories/${encodeURIComponent(categoryId)}`, { method: "DELETE" }),
};
export const notifyApi = {
    get: (token, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/notify_props`),
    put: (token, props, userId = "me") => request(token, `/users/${encodeURIComponent(userId)}/notify_props`, { method: "PUT", body: props }),
};
