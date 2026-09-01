import { describe, expect, it } from "vitest";
import type { WorkItem } from "@/api/work-items";
import { groupTasksByDueDate } from "./WorkManagementView";

function task(id: string, dueAt: number): WorkItem {
  return {
    id, kind: "task", title: id, description: "", status: "open", created_by: "user",
    channel_id: "channel", due_at: dueAt, decided_at: 0, priority: "normal", completed_at: 0,
    recurrence_unit: "none", recurrence_interval: 0, occurrence_no: 0,
    dependency_ids: [], impact_task_ids: [], create_at: 1, update_at: 1, delete_at: 0,
  };
}

describe("groupTasksByDueDate", () => {
  it("sorts dated tasks first and keeps undated work visible", () => {
    const groups = groupTasksByDueDate([task("none", 0), task("later", 2_000), task("first", 1_000)]);
    expect(groups.flatMap((group) => group.items.map((item) => item.id))).toEqual(["first", "later", "none"]);
    expect(groups.at(-1)?.label).toBe("마감 없음");
  });
});
