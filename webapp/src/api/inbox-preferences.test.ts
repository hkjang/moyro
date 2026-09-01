import { describe, expect, it } from "vitest";
import type { ActivityEvent } from "./activity";
import {
  DEFAULT_INBOX_PREFERENCES,
  arrangeActivities,
  inboxNotificationsAllowed,
  isPriorityActivity,
} from "./inbox-preferences";

const event = (id: string, type: ActivityEvent["type"], actor: string, channel: string, createAt: number): ActivityEvent => ({
  id, type, actor_id: actor, channel_id: channel, title: id, create_at: createAt,
  update_at: createAt, read_at: 0, completed_at: 0, snoozed_until: 0,
});

describe("inbox preference rules", () => {
  it("treats VIP actors and selected event types as priority", () => {
    const prefs = { ...DEFAULT_INBOX_PREFERENCES, vip_user_ids: ["vip"], priority_event_types: ["task_assigned" as const] };
    expect(isPriorityActivity(prefs, "vip", "plugin_event")).toBe(true);
    expect(isPriorityActivity(prefs, "other", "task_assigned")).toBe(true);
    expect(isPriorityActivity(prefs, "other", "plugin_event")).toBe(false);
  });

  it("handles overnight working hours in the configured IANA timezone", () => {
    const prefs = {
      ...DEFAULT_INBOX_PREFERENCES,
      work_hours_enabled: true,
      work_hours_timezone: "UTC",
      work_hours_weekdays: [1],
      work_hours_start_minute: 22 * 60,
      work_hours_end_minute: 2 * 60,
      priority_override: false,
    };
    expect(inboxNotificationsAllowed(prefs, new Date("2026-08-31T23:00:00Z"), false)).toBe(true);
    expect(inboxNotificationsAllowed(prefs, new Date("2026-09-01T01:00:00Z"), false)).toBe(true);
    expect(inboxNotificationsAllowed(prefs, new Date("2026-09-01T03:00:00Z"), false)).toBe(false);
  });

  it("bundles by channel and raises a bundle containing priority activity", () => {
    const prefs = { ...DEFAULT_INBOX_PREFERENCES, vip_user_ids: ["vip"], priority_event_types: [] };
    const rows = arrangeActivities([
      event("new", "plugin_event", "other", "b", 30),
      event("vip", "plugin_event", "vip", "a", 10),
      event("same", "plugin_event", "other", "a", 20),
    ], prefs);
    expect(rows.map((row) => row.event.id)).toEqual(["vip", "same", "new"]);
    expect(rows.map((row) => row.startsBundle)).toEqual([true, false, true]);
  });
});
