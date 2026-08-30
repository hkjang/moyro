import { moyroRequest } from "./transport";

export type OperationalState = "ready" | "warning" | "unknown";

export type AdminOperationsSnapshot = {
  checked_at: number;
  database: {
    state: OperationalState;
    message: string;
    migration: {
      applied_version: number;
      applied_name: string;
      applied_at: number;
      target_version: number;
      target_name: string;
    };
    pool: { acquired: number; idle: number; total: number; max: number };
  };
  workers: {
    state: OperationalState;
    message: string;
    runtime_observable: boolean;
    scheduled: {
      pending: number;
      processing: number;
      retry: number;
      dead: number;
      due: number;
      expired_leases: number;
    };
    reminders: { pending: number; processing: number; due: number };
    approvals: { pending: number; processing: number; failed: number };
  };
  webhooks: {
    state: OperationalState;
    message: string;
    runtime_observable: boolean;
    pending: number;
    processing: number;
    retry: number;
    succeeded: number;
    dead: number;
    expired_leases: number;
    last_succeeded_at: number;
    last_dead_at: number;
  };
  storage: {
    state: OperationalState;
    message: string;
    configured_backend: string;
    active_backend: string;
    fallback: boolean;
    file_count: number;
    bytes: number;
  };
};

export const adminOperationsApi = {
  getSnapshot: (token: string) =>
    moyroRequest<AdminOperationsSnapshot>(token, "/admin/operations"),
};
