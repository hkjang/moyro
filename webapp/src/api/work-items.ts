import { moyroRequest } from "./transport";

export type WorkItemKind = "task" | "decision";
export type WorkItemStatus =
  | "open"
  | "in_progress"
  | "done"
  | "cancelled"
  | "recorded"
  | "superseded";

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
  idempotency_key: string;
};

export type PatchWorkItem = Partial<Pick<
  WorkItem,
  "title" | "description" | "status" | "assignee_id" | "due_at"
>>;

function listQuery(options: {
  kind?: WorkItemKind;
  status?: WorkItemStatus;
  cursor?: string;
  perPage?: number;
}): string {
  const query = new URLSearchParams();
  if (options.kind) query.set("kind", options.kind);
  if (options.status) query.set("status", options.status);
  if (options.cursor) query.set("cursor", options.cursor);
  if (options.perPage) query.set("per_page", String(options.perPage));
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
  remove: (token: string, id: string, signal?: AbortSignal) =>
    moyroRequest<void>(token, `/me/work-items/${encodeURIComponent(id)}`, {
      method: "DELETE",
      signal,
    }),
};
