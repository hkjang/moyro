import { moyroRequest } from "./transport";
import type { WorkItemPriority, WorkItemRecurrence } from "./work-items";

export type AutomationMatchType = "contains" | "starts_with";
export type AutomationActionType = "task" | "decision" | "reminder";
export type AutomationRunStatus = "pending" | "processing" | "retry" | "succeeded" | "dead" | "cancelled";

export type AutomationActionConfig = {
  title?: string;
  description?: string;
  assignee_id?: string;
  due_offset_minutes?: number;
  priority?: WorkItemPriority;
  recurrence_unit?: WorkItemRecurrence;
  recurrence_interval?: number;
  initial_status?: "proposed" | "recorded";
  reviewer_id?: string;
  remind_offset_minutes?: number;
};

export type AutomationAction = {
  id?: string;
  position?: number;
  type: AutomationActionType;
  config: AutomationActionConfig;
};

export type AutomationRule = {
  id: string;
  name: string;
  created_by: string;
  team_id?: string;
  channel_id: string;
  enabled: boolean;
  match_type: AutomationMatchType;
  match_value: string;
  revision: number;
  actions: AutomationAction[];
  create_at: number;
  update_at: number;
  delete_at: number;
};

export type SaveAutomationRule = Pick<
  AutomationRule,
  "name" | "channel_id" | "enabled" | "match_type" | "match_value" | "actions"
> & { revision?: number };

export type AutomationRun = {
  id: string;
  rule_id: string;
  action_id: string;
  post_id: string;
  actor_id: string;
  action_type: AutomationActionType;
  status: AutomationRunStatus;
  attempt_count: number;
  next_attempt_at: number;
  claimed_at?: number;
  lease_until?: number;
  result_type?: string;
  result_id?: string;
  last_error_code?: string;
  last_error_text?: string;
  create_at: number;
  update_at: number;
  completed_at?: number;
};

export const automationsApi = {
  list: (token: string, signal?: AbortSignal) =>
    moyroRequest<AutomationRule[]>(token, "/me/automation-rules", { signal }),
  create: (token: string, input: SaveAutomationRule, signal?: AbortSignal) =>
    moyroRequest<AutomationRule>(token, "/me/automation-rules", {
      method: "POST",
      body: input,
      signal,
    }),
  update: (token: string, id: string, input: SaveAutomationRule, signal?: AbortSignal) =>
    moyroRequest<AutomationRule>(token, `/me/automation-rules/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: input,
      signal,
    }),
  remove: (token: string, id: string, signal?: AbortSignal) =>
    moyroRequest<void>(token, `/me/automation-rules/${encodeURIComponent(id)}`, {
      method: "DELETE",
      signal,
    }),
  runs: (token: string, id: string, limit = 30, signal?: AbortSignal) =>
    moyroRequest<AutomationRun[]>(token, `/me/automation-rules/${encodeURIComponent(id)}/runs?limit=${Math.min(100, Math.max(1, limit))}`, { signal }),
};
