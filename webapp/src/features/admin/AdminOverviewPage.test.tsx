// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { AdminOperationsSnapshot } from "@/api/admin-operations";
import { OperationsStatusCards } from "./AdminOverviewPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function operationsFixture(): AdminOperationsSnapshot {
  return {
    checked_at: 1,
    database: {
      state: "ready",
      message: "PostgreSQL 응답과 Migration 목표 일치를 확인했습니다.",
      migration: {
        applied_version: 10,
        applied_name: "flow_membership_index",
        applied_at: 1,
        target_version: 10,
        target_name: "flow_membership_index",
      },
      pool: { acquired: 2, idle: 3, total: 5, max: 20 },
    },
    workers: {
      state: "unknown",
      message: "큐 수치는 조회됐지만 Worker heartbeat가 없어 실행 상태는 미확인입니다.",
      runtime_observable: false,
      scheduled: { pending: 2, processing: 0, retry: 1, dead: 0, due: 1, expired_leases: 0 },
      reminders: { pending: 3, processing: 0, due: 1 },
      approvals: { pending: 1, processing: 0, failed: 0 },
    },
    webhooks: {
      state: "warning",
      message: "재시도, DLQ 또는 만료된 Lease가 있습니다.",
      runtime_observable: false,
      pending: 4,
      processing: 1,
      retry: 2,
      succeeded: 20,
      dead: 1,
      expired_leases: 0,
      last_succeeded_at: 1,
      last_dead_at: 1,
    },
    storage: {
      state: "ready",
      message: "로컬 파일 저장 경로와 메타데이터 조회를 확인했습니다.",
      configured_backend: "fs",
      active_backend: "fs",
      fallback: false,
      file_count: 7,
      bytes: 1536,
    },
  };
}

describe("OperationsStatusCards", () => {
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
  });

  it("renders evidence-backed DB, queue, DLQ, and storage details without claiming worker health", async () => {
    await act(async () => {
      root.render(<OperationsStatusCards query={{ state: "loaded", value: operationsFixture() }} />);
    });

    const cards = Array.from(container.querySelectorAll<HTMLElement>(".admin-status-card"));
    const card = (title: string) => cards.find((item) => item.querySelector("h2")?.textContent === title);
    expect(card("PostgreSQL")?.textContent).toContain("Migration 10/10");
    expect(card("PostgreSQL")?.textContent).toContain("Pool 2/20");
    expect(card("Worker")?.dataset.operationalState).toBe("unknown");
    expect(card("Worker")?.textContent).toContain("heartbeat가 없어 실행 상태는 미확인");
    expect(card("Webhook 전달")?.dataset.operationalState).toBe("warning");
    expect(card("Webhook 전달")?.textContent).toContain("DLQ 1");
    expect(card("파일 저장소")?.textContent).toContain("파일 7개");
    expect(card("파일 저장소")?.textContent).toContain("1.5 KiB");
  });

  it("keeps diagnostics unknown while loading and explicit when authority is absent", async () => {
    await act(async () => root.render(<OperationsStatusCards query={{ state: "loading" }} />));
    expect(container.querySelectorAll('[data-operational-state="loading"]')).toHaveLength(4);
    expect(container.textContent).toContain("운영 지표를 확인하고 있습니다.");

    await act(async () => root.render(<OperationsStatusCards query={{ state: "not_authorized" }} />));
    expect(container.querySelectorAll('[data-operational-state="not_authorized"]')).toHaveLength(4);
    expect(container.textContent).toContain("manage_system 권한이 있어야 상세 운영 지표를 볼 수 있습니다.");
  });
});
