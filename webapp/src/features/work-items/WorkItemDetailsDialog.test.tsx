// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { workItemsApi, type WorkItem } from "@/api/work-items";
import { WorkItemDetailsDialog } from "./WorkItemDetailsDialog";

const base = (id: string, title: string): WorkItem => ({
  id, kind: "task", title, description: "", status: "open", created_by: "user", channel_id: "channel",
  due_at: 0, decided_at: 0, priority: "normal", completed_at: 0, recurrence_unit: "none",
  recurrence_interval: 0, occurrence_no: 0, dependency_ids: [], impact_task_ids: [],
  create_at: 1, update_at: 1, delete_at: 0,
});

describe("WorkItemDetailsDialog", () => {
  let container: HTMLDivElement;
  let root: Root;
  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("loads history and lets the owner remove a dependency", async () => {
    const dependency = base("dependency", "선행 점검");
    const item = { ...base("task", "배포"), dependency_ids: [dependency.id] };
    vi.spyOn(workItemsApi, "events").mockResolvedValue([{ id: "event", work_item_id: item.id, event_type: "created", details: {}, create_at: 1 }]);
    const remove = vi.spyOn(workItemsApi, "removeDependency").mockResolvedValue({ ...item, dependency_ids: [] });
    await act(async () => root.render(<WorkItemDetailsDialog token="token" item={item} items={[item, dependency]} canManage onClose={() => undefined} onChanged={() => undefined} />));
    await act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
    expect(document.body.textContent).toContain("생성");
    const button = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((candidate) => candidate.textContent?.includes("연결 해제"));
    if (!button) throw new Error("unlink button not found");
    await act(async () => { button.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(remove).toHaveBeenCalledWith("token", "task", "dependency");
  });
});
