// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { moyroAdminApi, type TrackingSettings, type TrackingViolation } from "@/api/client";
import { DEFAULT_TRACKING, MAX_SNIPPET_BYTES, TrackingSettingsPage, validateTracking } from "./TrackingSettingsPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const momentoFixture: TrackingSettings = {
  ...DEFAULT_TRACKING,
  enabled: true,
  provider: "momento",
  momento_url: "https://momento.corp.example",
  momento_site_id: "moyro-prd",
};

const blocked: TrackingViolation = {
  origin: "https://pixel.corp.example",
  directive: "img-src",
  page: "https://moyro.example/today",
  count: 4,
  first_seen: 1_700_000_000_000,
  last_seen: 1_700_000_400_000,
  allowed: false,
};

describe("validateTracking", () => {
  it("accepts the default (off) configuration and a disabled half-filled form", () => {
    expect(validateTracking(DEFAULT_TRACKING)).toBe("");
    expect(validateTracking({ ...DEFAULT_TRACKING, provider: "momento" })).toBe("");
  });

  it("refuses what the server would refuse", () => {
    expect(validateTracking({ ...DEFAULT_TRACKING, enabled: true })).toContain("공급자");
    expect(validateTracking({ ...momentoFixture, momento_site_id: "" })).toContain("사이트 ID");
    expect(validateTracking({ ...DEFAULT_TRACKING, enabled: true, provider: "ga4" })).toContain("측정 ID");
    expect(validateTracking({ ...DEFAULT_TRACKING, custom_snippet: "가".repeat(MAX_SNIPPET_BYTES / 3 + 1) })).toContain("바이트");
    expect(validateTracking({ ...DEFAULT_TRACKING, momento_url: "momento.corp.example" })).toContain("절대 URL");
  });
});

describe("TrackingSettingsPage", () => {
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

  async function renderPage(settings: TrackingSettings, violations: TrackingViolation[]) {
    vi.spyOn(moyroAdminApi, "getSettings").mockResolvedValue(settings);
    vi.spyOn(moyroAdminApi, "listTrackingViolations").mockResolvedValue({ items: violations });
    const store = configureStore({ reducer: { auth: () => ({ token: "admin-token", user: null }) } });
    await act(async () => {
      root.render(
        <Provider store={store}>
          <TrackingSettingsPage />
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

  it("renders the stored configuration and the blocked origins", async () => {
    await renderPage(momentoFixture, [blocked]);

    expect(container.querySelector<HTMLInputElement>('input[placeholder="https://momento.corp.example"]')?.value)
      .toBe("https://momento.corp.example");
    expect(container.textContent).toContain("/momento/*");
    expect(container.textContent).toContain("https://pixel.corp.example");
    expect(container.textContent).toContain("img-src");
    expect(button("허용")).toBeTruthy();
  });

  it("puts a blocked origin on the allow list with one click and saves", async () => {
    const patch = vi.spyOn(moyroAdminApi, "patchSettings").mockImplementation(
      async (_token, _section, value) => value as TrackingSettings,
    );
    await renderPage(momentoFixture, [blocked]);

    await act(async () => {
      button("허용").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(patch).toHaveBeenCalledTimes(1);
    const [, section, payload] = patch.mock.calls[0] as [string, string, TrackingSettings];
    expect(section).toBe("tracking");
    expect(payload.allowed_hosts).toEqual(["https://pixel.corp.example"]);
    expect(payload.enabled).toBe(true);
    expect(container.textContent).toContain("허용 목록에 넣었습니다");
    expect(container.querySelector<HTMLTextAreaElement>("textarea")?.value).toContain("https://pixel.corp.example");
  });

  it("does not save a configuration the server would refuse", async () => {
    const patch = vi.spyOn(moyroAdminApi, "patchSettings").mockResolvedValue(DEFAULT_TRACKING);
    await renderPage({ ...DEFAULT_TRACKING, enabled: true }, []);

    await act(async () => {
      button("설정 저장").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(patch).not.toHaveBeenCalled();
    expect(container.textContent).toContain("켜기 전에 공급자를 고르세요.");
  });
});
