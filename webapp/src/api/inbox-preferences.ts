import type { ActivityEvent, ActivityEventType } from "./activity";
import { moyroRequest } from "./transport";

export type InboxBundleMode = "none" | "channel" | "type";

export type InboxPreferences = {
  vip_user_ids: string[];
  priority_event_types: ActivityEventType[];
  bundle_by: InboxBundleMode;
  snooze_presets_minutes: number[];
  work_hours_enabled: boolean;
  work_hours_timezone: string;
  work_hours_weekdays: number[];
  work_hours_start_minute: number;
  work_hours_end_minute: number;
  priority_override: boolean;
  update_at: number;
};

export const DEFAULT_INBOX_PREFERENCES: InboxPreferences = {
  vip_user_ids: [],
  priority_event_types: ["mention", "direct_message", "approval_requested", "system_warning"],
  bundle_by: "channel",
  snooze_presets_minutes: [60, 240, 1440],
  work_hours_enabled: false,
  work_hours_timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  work_hours_weekdays: [1, 2, 3, 4, 5],
  work_hours_start_minute: 9 * 60,
  work_hours_end_minute: 18 * 60,
  priority_override: true,
  update_at: 0,
};

export const inboxPreferencesApi = {
  get: (token: string, signal?: AbortSignal) =>
    moyroRequest<InboxPreferences>(token, "/me/inbox-preferences", { signal }),
  patch: (token: string, patch: Partial<Omit<InboxPreferences, "update_at">>, signal?: AbortSignal) =>
    moyroRequest<InboxPreferences>(token, "/me/inbox-preferences", {
      method: "PATCH",
      body: patch,
      signal,
    }),
};

export function isPriorityActivity(
  preferences: InboxPreferences,
  actorID: string | undefined,
  eventType: ActivityEventType,
): boolean {
  return (!!actorID && preferences.vip_user_ids.includes(actorID))
    || preferences.priority_event_types.includes(eventType);
}

export function inboxNotificationsAllowed(
  preferences: InboxPreferences,
  now: Date,
  priority: boolean,
): boolean {
  if (!preferences.work_hours_enabled || (priority && preferences.priority_override)) return true;
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat("en-US", {
      timeZone: preferences.work_hours_timezone,
      weekday: "short",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }).formatToParts(now);
  } catch {
    return false;
  }
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  const weekday = ({ Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6, Sun: 7 } as Record<string, number>)[values.weekday] ?? 0;
  const minute = Number(values.hour) * 60 + Number(values.minute);
  const { work_hours_start_minute: start, work_hours_end_minute: end } = preferences;
  if (start < end) {
    return preferences.work_hours_weekdays.includes(weekday) && minute >= start && minute < end;
  }
  if (minute >= start) return preferences.work_hours_weekdays.includes(weekday);
  const previous = weekday === 1 ? 7 : weekday - 1;
  return minute < end && preferences.work_hours_weekdays.includes(previous);
}

export type ArrangedActivity = {
  event: ActivityEvent;
  priority: boolean;
  bundleKey: string;
  startsBundle: boolean;
};

export function arrangeActivities(events: ActivityEvent[], preferences: InboxPreferences): ArrangedActivity[] {
  const bundles = new Map<string, Array<{ event: ActivityEvent; priority: boolean }>>();
  for (const event of events) {
    const priority = isPriorityActivity(preferences, event.actor_id, event.type);
    const bundleKey = preferences.bundle_by === "channel"
      ? `channel:${event.channel_id || "none"}`
      : preferences.bundle_by === "type"
        ? `type:${event.type}`
        : `event:${event.id}`;
    const bundle = bundles.get(bundleKey) ?? [];
    bundle.push({ event, priority });
    bundles.set(bundleKey, bundle);
  }
  return [...bundles.entries()]
    .map(([bundleKey, items]) => ({
      bundleKey,
      items: items.sort((left, right) => Number(right.priority) - Number(left.priority) || right.event.create_at - left.event.create_at),
      priority: items.some((item) => item.priority),
      newest: Math.max(...items.map((item) => item.event.create_at)),
    }))
    .sort((left, right) => Number(right.priority) - Number(left.priority) || right.newest - left.newest)
    .flatMap((bundle) => bundle.items.map((item, index) => ({
      ...item,
      bundleKey: bundle.bundleKey,
      startsBundle: index === 0,
    })));
}
