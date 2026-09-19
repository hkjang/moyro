// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { moyroAdminApi, type MailDeliveryPage, type MailSettings } from "@/api/client";
import { DEFAULT_MAIL, MailSettingsPage, validateMail } from "./MailSettingsPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const relayFixture: MailSettings = {
  ...DEFAULT_MAIL,
  enabled: true,
  smtp_host: "postra.corp.example",
  from_address: "moyro@corp.example",
  password_configured: true,
};

const deliveries: MailDeliveryPage = {
  items: [
    { id: "d1", event: "approval_requested", recipient: "reviewer@corp.example", subject: "[moyro] 검토할 승인 요청: 운영 공지", status: "sent", attempts: 1, create_at: 1_700_000_000_000, update_at: 1_700_000_000_500 },
    { id: "d2", event: "task_assigned", recipient: "dev@corp.example", subject: "[moyro] 새 작업이 할당되었습니다: 배포 점검", status: "failed", attempts: 2, error_message: "smtp connect: connection refused", create_at: 1_700_000_100_000, update_at: 1_700_000_104_000 },
  ],
  summary: { total: 2, status: { sent: 1, failed: 1 } },
};

describe("validateMail", () => {
  it("accepts the default (off) configuration and a disabled half-filled form", () => {
    expect(validateMail(DEFAULT_MAIL)).toBe("");
    expect(validateMail({ ...DEFAULT_MAIL, smtp_host: "postra.corp.example" })).toBe("");
  });

  it("refuses what the server would refuse", () => {
    expect(validateMail({ ...DEFAULT_MAIL, enabled: true })).toContain("릴레이 주소");
    expect(validateMail({ ...DEFAULT_MAIL, enabled: true, smtp_host: "relay" })).toContain("보내는 주소");
    expect(validateMail({ ...DEFAULT_MAIL, smtp_port: 70000 })).toContain("포트");
    expect(validateMail({ ...DEFAULT_MAIL, from_address: "Ops <moyro@corp.example>" })).toContain("이름 없이");
    expect(validateMail({ ...DEFAULT_MAIL, base_url: "moyro.corp.example" })).toContain("http(s)://");
  });
});

describe("MailSettingsPage", () => {
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

  async function renderPage(settings: MailSettings, page: MailDeliveryPage) {
    vi.spyOn(moyroAdminApi, "getSettings").mockResolvedValue(settings);
    vi.spyOn(moyroAdminApi, "listMailDeliveries").mockResolvedValue(page);
    const store = configureStore({ reducer: { auth: () => ({ token: "admin-token", user: null }) } });
    await act(async () => {
      root.render(
        <Provider store={store}>
          <MailSettingsPage />
        </Provider>,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  function button(text: string): HTMLButtonElement {
    const found = Array.from(container.querySelectorAll("button"))
      .find((candidate) => candidate.textContent?.trim() === text);
    if (!(found instanceof HTMLButtonElement)) throw new Error(`button ${text} not found`);
    return found;
  }

  it("shows the password as configured without ever holding its value, and keeps it on save", async () => {
    await renderPage(relayFixture, deliveries);
    const passwordField = container.querySelector<HTMLInputElement>('input[type="password"]');
    expect(passwordField?.value).toBe("");
    expect(passwordField?.placeholder).toContain("설정됨");
    expect(container.textContent).toContain("설정됨");

    const patch = vi.spyOn(moyroAdminApi, "patchSettings").mockResolvedValue(relayFixture);
    await act(async () => {
      button("설정 저장").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(patch).toHaveBeenCalledTimes(1);
    const body = patch.mock.calls[0][2] as MailSettings;
    expect(body.password).toBeUndefined();
    expect(body.clear_password).toBeUndefined();
    expect(body.smtp_host).toBe("postra.corp.example");
  });

  it("lists every attempt with its outcome and sends a test message in place", async () => {
    await renderPage(relayFixture, deliveries);
    const rows = container.querySelectorAll("tr.delivery-row");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("승인 요청");
    expect(rows[0].textContent).toContain("보냄");
    expect(rows[1].textContent).toContain("실패");
    expect(rows[1].textContent).toContain("connection refused");
    expect(container.textContent).toContain("보냄 1");
    expect(container.textContent).toContain("실패 1");

    const test = vi.spyOn(moyroAdminApi, "sendTestMail").mockResolvedValue({ sent: false, recipient: "me@corp.example", message: "smtp connect: dial tcp: connection refused" });
    const recipient = Array.from(container.querySelectorAll<HTMLInputElement>("input"))
      .find((input) => input.placeholder === "me@corp.example");
    if (!recipient) throw new Error("recipient field not found");
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
      setter?.call(recipient, "me@corp.example");
      recipient.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      button("시험 발송").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(test).toHaveBeenCalledWith("admin-token", "me@corp.example");
    expect(container.textContent).toContain("connection refused");
  });

  it("keeps the test button off while mail is disabled", async () => {
    await renderPage(DEFAULT_MAIL, { items: [], summary: { total: 0, status: {} } });
    expect(button("시험 발송").disabled).toBe(true);
    expect(container.textContent).toContain("메일 알림을 켜면 보낸 기록이 여기에 쌓입니다.");
  });
});
