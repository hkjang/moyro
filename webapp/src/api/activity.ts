import { moyroRequest } from "./transport";

export type ActivityEventType =
  | "mention"
  | "thread_reply"
  | "direct_message"
  | "approval_requested"
  | "decided"
  | "reminder_fired"
  | "task_assigned"
  | "system_warning"
  | "plugin_event";

export type ActivityEvent = {
  id: string;
  type: ActivityEventType;
  actor_id?: string;
  team_id?: string;
  channel_id?: string;
  post_id?: string;
  resource_type?: string;
  resource_id?: string;
  title: string;
  summary?: string;
  create_at: number;
  update_at: number;
  read_at: number;
  completed_at: number;
  snoozed_until: number;
};

export type ActivityEventPage = {
  events: ActivityEvent[];
  next_cursor: string;
};

export type ActivityStatePatch = {
  read?: boolean;
  completed?: boolean;
  snoozed_until?: number;
};

export const activityApi = {
  list: (token: string, options: {
    cursor?: string;
    limit?: number;
    unread?: boolean;
    types?: ActivityEventType[];
  } = {}, signal?: AbortSignal) => {
    const query = new URLSearchParams();
    if (options.cursor) query.set("cursor", options.cursor);
    if (options.limit) query.set("limit", String(options.limit));
    if (options.unread !== undefined) query.set("unread", String(options.unread));
    for (const type of options.types ?? []) query.append("type", type);
    const suffix = query.toString();
    return moyroRequest<ActivityEventPage>(token, `/me/activity-events${suffix ? `?${suffix}` : ""}`, { signal });
  },
  patch: (token: string, id: string, patch: ActivityStatePatch, signal?: AbortSignal) =>
    moyroRequest<ActivityEvent>(token, `/me/activity-events/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: patch,
      signal,
    }),
  markRead: (token: string, ids: string[], signal?: AbortSignal) =>
    moyroRequest<{ updated: number }>(token, "/me/activity-events/mark-read", {
      method: "POST",
      body: { ids },
      signal,
    }),
};
