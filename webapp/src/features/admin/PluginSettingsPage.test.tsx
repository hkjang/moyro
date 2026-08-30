// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { adminApi } from "@/api/client";
import { AdminPluginsProvider } from "./AdminPluginsContext";
import { PluginSettingsPage } from "./PluginSettingsPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("PluginSettingsPage", () => {
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

  function renderPage(pluginID: string) {
    const store = configureStore({
      reducer: {
        auth: () => ({ token: "admin-token", user: null }),
      },
    });
    root.render(
      <Provider store={store}>
        <MemoryRouter initialEntries={[`/admin/integrations/plugins/${pluginID}`]}>
          <AdminPluginsProvider enabled>
            <Routes>
              <Route path="/admin/integrations/plugins/:pluginId" element={<PluginSettingsPage />} />
              <Route path="/admin/integrations/plugins" element={<div>plugin list</div>} />
            </Routes>
          </AdminPluginsProvider>
        </MemoryRouter>
      </Provider>,
    );
  }

  it("loads configuration only for the selected plugin from a large inventory", async () => {
    const plugins = Array.from({ length: 20 }, (_, index) => ({
      id: `com.example.plugin-${index}`,
      version: "1.0.0",
      enabled: true,
      state: "running",
      manifest: { name: `Plugin ${String(index).padStart(2, "0")}` },
    }));
    vi.spyOn(adminApi, "listPlugins").mockResolvedValue(plugins);
    vi.spyOn(adminApi, "listPluginStatuses").mockResolvedValue(plugins.map((plugin) => ({
      plugin_id: plugin.id,
      state: plugin.state,
    })));
    vi.spyOn(adminApi, "getPluginManagementCapabilities").mockResolvedValue({
      management_enabled: true,
      uploads_enabled: true,
    });
    const getConfiguration = vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: {},
      schema: { settings: [] },
    });

    await act(async () => {
      renderPage("com.example.plugin-13");
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.querySelector("h1")?.textContent).toBe("Plugin 13");
    expect(getConfiguration).toHaveBeenCalledOnce();
    expect(getConfiguration).toHaveBeenCalledWith("admin-token", "com.example.plugin-13");
    expect(container.textContent).toContain("관리자 설정 항목을 제공하지 않습니다.");
  });

  it("shows a safe return path for a removed or unknown plugin", async () => {
    vi.spyOn(adminApi, "listPlugins").mockResolvedValue([]);
    vi.spyOn(adminApi, "listPluginStatuses").mockResolvedValue([]);
    vi.spyOn(adminApi, "getPluginManagementCapabilities").mockResolvedValue({
      management_enabled: true,
      uploads_enabled: true,
    });
    const getConfiguration = vi.spyOn(adminApi, "getPluginConfiguration");

    await act(async () => {
      renderPage("com.example.removed");
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.querySelector("h1")?.textContent).toBe("플러그인을 찾을 수 없습니다");
    expect(container.textContent).toContain("플러그인 관리로");
    expect(getConfiguration).not.toHaveBeenCalled();
  });
});
