// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { adminApi } from "@/api/client";
import { AdminPluginsProvider } from "./AdminPluginsContext";
import { PluginManagementPage } from "./PluginManagementPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("PluginManagementPage", () => {
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

  it("loads installed plugins with the Redux auth token and enables runtime controls", async () => {
    const listPlugins = vi.spyOn(adminApi, "listPlugins").mockResolvedValue([{
      id: "com.mattermost.botman",
      version: "1.0.0",
      state: "running",
      manifest: { name: "Botman" },
    }]);
    vi.spyOn(adminApi, "listPluginStatuses").mockResolvedValue([
      { plugin_id: "com.mattermost.botman", state: "running" },
    ]);
    const getCapabilities = vi.spyOn(adminApi, "getPluginManagementCapabilities").mockResolvedValue({
      management_enabled: true,
      uploads_enabled: true,
    });
    const store = configureStore({
      reducer: {
        auth: () => ({ token: "admin-token", user: null }),
      },
    });

    await act(async () => {
      root.render(
        <Provider store={store}>
          <MemoryRouter>
            <AdminPluginsProvider enabled>
              <PluginManagementPage />
            </AdminPluginsProvider>
          </MemoryRouter>
        </Provider>,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(listPlugins).toHaveBeenCalledWith("admin-token");
    expect(getCapabilities).toHaveBeenCalledWith("admin-token");
    expect(container.textContent).toContain("Botman");
    expect(container.textContent).toContain("Trusted Native");
    expect(container.textContent).toContain("서명 미검증");
    expect(container.textContent).toContain("서버 프로세스 권한");
    expect(container.querySelector<HTMLInputElement>('input[type="file"]')?.disabled).toBe(false);
  });
});
