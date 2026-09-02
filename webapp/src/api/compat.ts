// Mattermost-compatible preferences, sidebar, notification, and profile APIs.
//
// Split out of the former single `client.ts`. `client.ts` re-exports every
// symbol here, so callers keep importing from `@/api/client`.

// ---- Phase 21: Mattermost-shaped preferences ----
//
// Each preference is a (user_id, category, name) -> value triple. Value is
// always a string in the official contract, even when it carries JSON —
// callers stringify before save, parse on read. Common categories the UI
// relies on: "display_settings" (theme, message_display), "favorite_channel"
// (name=channel_id, value="true").
import {
  compatRequest as request,
} from "./transport";
import type { Channel, ChannelMember, Post, Team, User } from "./chat";

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
