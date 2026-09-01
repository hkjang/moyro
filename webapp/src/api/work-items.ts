import { moyroRequest } from "./transport";

export type WorkItemKind = "task" | "decision";
export type WorkItemStatus =
  | "open"
  | "in_progress"
  | "done"
  | "cancelled"
  | "proposed"
  | "under_review"
  | "recorded"
  | "superseded";

export type WorkItemPriority = "low" | "normal" | "high" | "urgent";
export type WorkItemRecurrence = "none" | "daily" | "weekly" | "monthly";

export type WorkItem = {
  id: string;
  kind: WorkItemKind;
  title: string;
  description: string;
  status: WorkItemStatus;
  created_by: string;
  assignee_id?: string;
  team_id?: string;
  channel_id: string;
  source_post_id?: string;
  source_thread_id?: string;
  due_at: number;
  decided_at: number;
  priority: WorkItemPriority;
  completed_at: number;
  reviewer_id?: string;
  recurrence_unit: WorkItemRecurrence;
  recurrence_interval: number;
  series_id?: string;
  occurrence_no: number;
  supersedes_id?: string;
  superseded_by_id?: string;
  dependency_ids: string[];
  impact_task_ids: string[];
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type WorkItemPage = {
  items: WorkItem[];
  next_cursor?: string;
};

export type CreateWorkItem = {
  kind: WorkItemKind;
  title: string;
  description?: string;
  assignee_id?: string;
  source_post_id: string;
  due_at?: number;
  priority?: WorkItemPriority;
  reviewer_id?: string;
  initial_status?: "proposed" | "recorded";
  recurrence_unit?: WorkItemRecurrence;
  recurrence_interval?: number;
  supersedes_id?: string;
  dependency_ids?: string[];
  impact_task_ids?: string[];
  idempotency_key: string;
};

export type PatchWorkItem = Partial<Pick<
  WorkItem,
  "title" | "description" | "status" | "assignee_id" | "due_at" | "priority" |
  "reviewer_id" | "recurrence_unit" | "recurrence_interval"
>>;

export type WorkItemEvent = {
  id: string;
  work_item_id: string;
  actor_id?: string;
  event_type: string;
  from_status?: string;
  to_status?: string;
  details: Record<string, unknown>;
  create_at: number;
};

function listQuery(options: {
  kind?: WorkItemKind;
  status?: WorkItemStatus;
  cursor?: string;
  perPage?: number;
  dueFrom?: number;
  dueTo?: number;
  sort?: "created" | "due";
}): string {
  const query = new URLSearchParams();
  if (options.kind) query.set("kind", options.kind);
  if (options.status) query.set("status", options.status);
  if (options.cursor) query.set("cursor", options.cursor);
  if (options.perPage) query.set("per_page", String(options.perPage));
  if (options.dueFrom) query.set("due_from", String(options.dueFrom));
  if (options.dueTo) query.set("due_to", String(options.dueTo));
  if (options.sort) query.set("sort", options.sort);
  const suffix = query.toString();
  return suffix ? `?${suffix}` : "";
}

export const workItemsApi = {
  list: (token: string, options: Parameters<typeof listQuery>[0] = {}, signal?: AbortSignal) =>
    moyroRequest<WorkItemPage>(token, `/me/work-items${listQuery(options)}`, { signal }),
  create: (token: string, input: CreateWorkItem, signal?: AbortSignal) =>
    moyroRequest<{ item: WorkItem; replayed: boolean }>(token, "/me/work-items", {
      method: "POST",
      headers: { "Idempotency-Key": input.idempotency_key },
      body: input,
      signal,
    }),
  patch: (token: string, id: string, input: PatchWorkItem, signal?: AbortSignal) =>
    moyroRequest<WorkItem>(token, `/me/work-items/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: input,
      signal,
    }),
  events: (token: string, id: string, signal?: AbortSignal) =>
    moyroRequest<WorkItemEvent[]>(token, `/me/work-items/${encodeURIComponent(id)}/events`, { signal }),
  addDependency: (token: string, id: string, dependencyID: string, signal?: AbortSignal) =>
    moyroRequest<WorkItem>(token, `/me/work-items/${encodeURIComponent(id)}/dependencies/${encodeURIComponent(dependencyID)}`, {
      method: "POST",
      signal,
    }),
  removeDependency: (token: string, id: string, dependencyID: string, signal?: AbortSignal) =>
    moyroRequest<WorkItem>(token, `/me/work-items/${encodeURIComponent(id)}/dependencies/${encodeURIComponent(dependencyID)}`, {
      method: "DELETE",
      signal,
    }),
  addImpact: (token: string, id: string, taskID: string, signal?: AbortSignal) =>
    moyroRequest<WorkItem>(token, `/me/work-items/${encodeURIComponent(id)}/impacts/${encodeURIComponent(taskID)}`, {
      method: "POST",
      signal,
    }),
  removeImpact: (token: string, id: string, taskID: string, signal?: AbortSignal) =>
    moyroRequest<WorkItem>(token, `/me/work-items/${encodeURIComponent(id)}/impacts/${encodeURIComponent(taskID)}`, {
      method: "DELETE",
      signal,
    }),
  remove: (token: string, id: string, signal?: AbortSignal) =>
    moyroRequest<void>(token, `/me/work-items/${encodeURIComponent(id)}`, {
      method: "DELETE",
      signal,
    }),
};
