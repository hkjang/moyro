// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { adminApi, moyroMeApi } from "@/api/client";
import { AdminAccessProvider } from "@/features/admin/AdminAccessContext";
import { AdminLayout } from "./AdminLayout";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function testStore() {
  return configureStore({
    reducer: {
      auth: () => ({ token: "plugin-admin-token", user: { username: "admin" } }),
    },
  });
}

function renderAdmin(root: Root, initialEntry: string) {
  root.render(
    <Provider store={testStore()}>
      <AdminAccessProvider>
        <MemoryRouter initialEntries={[initialEntry]}>
          <Routes>
            <Route path="/admin" element={<AdminLayout />}>
              <Route path="integrations/plugins" element={<div>plugin list</div>} />
              <Route path="integrations/plugins/:pluginId" element={<div>plugin settings</div>} />
              <Route path="ai/providers" element={<div>AI settings</div>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </AdminAccessProvider>
    </Provider>,
  );
}

describe("AdminLayout plugin navigation", () => {
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

  it("shows every installed plugin as a sorted nested item and selects only the detail item", async () => {
    vi.spyOn(moyroMeApi, "getPermissions").mockResolvedValue({ permissions: ["manage_plugins"] });
    vi.spyOn(adminApi, "listPlugins").mockResolvedValue([
      {
        id: "com.example.zulu",
        version: "2.0.0",
        enabled: false,
        state: "disabled",
        manifest: { name: "Zulu Plugin" },
      },
      {
        id: "com.example.alpha",
        version: "1.0.0",
        enabled: true,
        state: "running",
        manifest: { name: "Alpha Plugin" },
      },
      {
        id: "com.example.beta",
        version: "1.1.0",
        enabled: false,
        state: "disabled",
        manifest: { name: "Alpha Plugin" },
      },
    ]);
    vi.spyOn(adminApi, "listPluginStatuses").mockResolvedValue([
      { plugin_id: "com.example.zulu", state: "disabled" },
      { plugin_id: "com.example.alpha", state: "running" },
      { plugin_id: "com.example.beta", state: "disabled" },
    ]);
    vi.spyOn(adminApi, "getPluginManagementCapabilities").mockResolvedValue({
      management_enabled: true,
      uploads_enabled: true,
    });

    await act(async () => {
      renderAdmin(root, "/admin/integrations/plugins/com.example.alpha");
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const pluginMenu = container.querySelector('[aria-label="설치된 플러그인"]');
    expect(pluginMenu).not.toBeNull();
    expect([...pluginMenu?.children ?? []].every((child) => child.tagName === "LI")).toBe(true);
    const alpha = container.querySelector('[aria-label="Alpha Plugin 플러그인 설정 · com.example.alpha · running · v1.0.0"]');
    const beta = container.querySelector('[aria-label="Alpha Plugin 플러그인 설정 · com.example.beta · disabled · v1.1.0"]');
    const zulu = container.querySelector('[aria-label="Zulu Plugin 플러그인 설정 · com.example.zulu · disabled · v2.0.0"]');
    expect(alpha).not.toBeNull();
    expect(beta).not.toBeNull();
    expect(zulu).not.toBeNull();
    expect(alpha?.textContent).toContain("com.example.alpha");
    expect(beta?.textContent).toContain("com.example.beta");
    expect((alpha?.compareDocumentPosition(zulu as Node) ?? 0) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(alpha?.getAttribute("aria-current")).toBe("page");
    expect(container.querySelector('[aria-label="서비스 관리 메뉴"] [aria-current="page"]')?.textContent).toContain("Alpha Plugin");
    const parent = [...container.querySelectorAll(".admin-navigation-item")].find((item) => item.textContent?.trim() === "플러그인");
    expect(parent?.getAttribute("aria-current")).toBeNull();
    expect(container.textContent).toContain("plugin settings");
  });

  it("does not fetch or expose plugin submenus without manage_plugins", async () => {
    vi.spyOn(moyroMeApi, "getPermissions").mockResolvedValue({ permissions: ["manage_ai"] });
    const listPlugins = vi.spyOn(adminApi, "listPlugins");

    await act(async () => {
      renderAdmin(root, "/admin/ai/providers");
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(listPlugins).not.toHaveBeenCalled();
    expect(container.querySelector('[aria-label="설치된 플러그인"]')).toBeNull();
    expect(container.textContent).not.toContain("플러그인");
  });
});
