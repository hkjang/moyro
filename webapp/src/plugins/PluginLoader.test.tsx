// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const runtime = vi.hoisted(() => ({
  loadPluginBundle: vi.fn(async () => ({})),
  loadedPluginBundles: vi.fn(() => [] as Array<{ id: string; version?: string; source?: string }>),
  loadedPluginIDs: vi.fn(() => [] as string[]),
  unloadPlugin: vi.fn(async () => undefined),
}));

vi.mock("./runtime", () => runtime);

import { pluginApi } from "@/api/client";
import { PLUGIN_RUNTIME_REFRESH_MS, PluginLoader } from "./PluginLoader";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("PluginLoader reconciliation", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    runtime.loadPluginBundle.mockClear();
    runtime.loadedPluginBundles.mockReset();
    runtime.loadedPluginBundles.mockReturnValue([]);
    runtime.loadedPluginIDs.mockReset();
    runtime.loadedPluginIDs.mockReturnValue([]);
    runtime.unloadPlugin.mockClear();
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  function renderLoader() {
    const store = configureStore({ reducer: { auth: () => ({ token: "token", user: null }) } });
    root.render(<Provider store={store}><PluginLoader /></Provider>);
  }

  it("unloads removed and older-version contributions before loading the desired bundle", async () => {
    runtime.loadedPluginBundles.mockReturnValue([
      { id: "removed", version: "1.0.0" },
      { id: "versioned", version: "1.0.0" },
    ]);
    vi.spyOn(pluginApi, "listWebapps").mockResolvedValue([
      { id: "versioned", version: "2.0.0", url: "/plugins/versioned/webapp.js" },
    ]);

    await act(async () => {
      renderLoader();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(runtime.unloadPlugin).toHaveBeenCalledWith("removed");
    expect(runtime.unloadPlugin).toHaveBeenCalledWith("versioned");
    expect(runtime.loadPluginBundle).toHaveBeenCalledWith(
      "versioned",
      "/plugins/versioned/webapp.js",
      "2.0.0",
    );
  });

  it("refreshes discovery periodically and when the page becomes visible", async () => {
    vi.useFakeTimers();
    const list = vi.spyOn(pluginApi, "listWebapps").mockResolvedValue([]);
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    await act(async () => {
      renderLoader();
      await Promise.resolve();
    });
    expect(list).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PLUGIN_RUNTIME_REFRESH_MS);
    });
    expect(list).toHaveBeenCalledTimes(2);

    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await Promise.resolve();
    });
    expect(list).toHaveBeenCalledTimes(3);
  });
});
