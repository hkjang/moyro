// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { createAuthenticatedPluginFetch, resolvePluginBundleURL } from "./authenticatedFetch";
import {
  bootstrapPluginRuntime,
  exposeMattermostGlobals,
  loadPluginBundle,
  loadedPluginBundles,
  unloadPlugin,
  type PluginRecord,
} from "./runtime";
import { createMattermostStoreAdapter } from "./storeAdapter";
import { getRegistryState } from "./registry";

afterEach(() => {
  if (window.__moyro_plugin_fetch_original__) {
    window.fetch = window.__moyro_plugin_fetch_original__;
    delete window.__moyro_plugin_fetch_original__;
  }
  delete window.registerPlugin;
  delete window.__moyro_plugins__;
  document.querySelectorAll("script[data-plugin-id]").forEach((node) => node.remove());
  vi.restoreAllMocks();
});

describe("Mattermost globals", () => {
  it("exposes the official starter-template React and Redux externals", () => {
    exposeMattermostGlobals();
    expect(window.React.createElement).toBeTypeOf("function");
    expect(window.ReactDOM.createPortal).toBeTypeOf("function");
    expect(window.ReactRedux.useSelector).toBeTypeOf("function");
    expect(window.Redux.createStore).toBeTypeOf("function");
    expect(window.Redux.applyMiddleware).toBeTypeOf("function");
    expect(window.Redux.combineReducers).toBeTypeOf("function");
    expect(window.PropTypes.string).toBeTypeOf("function");
  });

  it("rolls back all registrations when plugin initialize rejects", async () => {
    bootstrapPluginRuntime();
    const id = "com.example.initialize-failure";
    const uninitialize = vi.fn();
    window.registerPlugin?.(id, {
      initialize(registry) {
        registry.registerPostTypeComponent("failed_type", () => null);
        throw new Error("broken initialize");
      },
      uninitialize,
    });
    const record = window.__moyro_plugins__?.[id];
    if (!record?.initialization) throw new Error("initialization promise missing");

    await expect(record.initialization).rejects.toThrow(/initialization failed/);
    expect(window.__moyro_plugins__?.[id]).toBeUndefined();
    expect(getRegistryState().postTypeComponents.some((entry) => entry.pluginId === id)).toBe(false);
    expect(uninitialize).toHaveBeenCalledOnce();
  });

  it("removes a stale registration even when plugin cleanup fails", async () => {
    const id = "com.example.cleanup";
    const unregisterAll = vi.fn();
    const uninitialize = vi.fn().mockRejectedValue(new Error("cleanup failed"));
    const errorLog = vi.spyOn(console, "error").mockImplementation(() => undefined);
    window.__moyro_plugins__ = {
      [id]: {
        plugin: { initialize: vi.fn(), uninitialize },
        registry: { unregisterAll },
      } as unknown as PluginRecord,
    };
    const script = document.createElement("script");
    script.dataset.pluginId = id;
    document.body.appendChild(script);

    await expect(unloadPlugin(id)).resolves.toBeUndefined();

    expect(unregisterAll).toHaveBeenCalledOnce();
    expect(uninitialize).toHaveBeenCalledOnce();
    expect(window.__moyro_plugins__?.[id]).toBeUndefined();
    expect(document.querySelector(`script[data-plugin-id="${id}"]`)).toBeNull();
    expect(errorLog).toHaveBeenCalledWith(`failed to uninitialize plugin ${id}`, expect.any(Error));
  });

  it("reports the loaded bundle id, version, and source", () => {
    const id = "com.example.versioned";
    window.__moyro_plugins__ = {
      [id]: {
        plugin: { initialize: vi.fn() },
        registry: { unregisterAll: vi.fn() },
        bundleVersion: "2.0.0",
        bundleSource: "https://chat.example.com/plugins/com.example.versioned/webapp.js",
      } as unknown as PluginRecord,
    };

    expect(loadedPluginBundles()).toEqual([{
      id,
      version: "2.0.0",
      source: "https://chat.example.com/plugins/com.example.versioned/webapp.js",
    }]);
  });

  it("reloads changed bundle content even when id and version are unchanged", async () => {
    const id = "com.example.replaced";
    const source = `/plugins/${id}/webapp.js`;
    const absoluteSource = new URL(source, window.location.origin).href;
    const unregisterAll = vi.fn();
    const uninitialize = vi.fn();
    window.__moyro_plugins__ = {
      [id]: {
        plugin: { initialize: vi.fn(), uninitialize },
        registry: { unregisterAll },
        bundleVersion: "1.0.0",
        bundleSource: absoluteSource,
        bundleFingerprint: "old-content",
      } as unknown as PluginRecord,
    };
    vi.spyOn(window, "fetch").mockResolvedValue(new Response("new plugin javascript"));
    const createObjectURLDescriptor = Object.getOwnPropertyDescriptor(URL, "createObjectURL");
    const revokeObjectURLDescriptor = Object.getOwnPropertyDescriptor(URL, "revokeObjectURL");
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: vi.fn(() => "blob:plugin") });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    const append = document.body.appendChild.bind(document.body);
    vi.spyOn(document.body, "appendChild").mockImplementation((node) => {
      const result = append(node);
      if (node instanceof HTMLScriptElement) {
        queueMicrotask(() => {
          window.__moyro_plugins__![id] = {
            plugin: { initialize: vi.fn() },
            registry: { unregisterAll: vi.fn() },
          } as unknown as PluginRecord;
          node.onload?.(new Event("load"));
        });
      }
      return result;
    });

    try {
      const loaded = await loadPluginBundle(id, source, "1.0.0");
      expect(unregisterAll).toHaveBeenCalledOnce();
      expect(uninitialize).toHaveBeenCalledOnce();
      expect(loaded.bundleVersion).toBe("1.0.0");
      expect(loaded.bundleSource).toBe(absoluteSource);
      expect(loaded.bundleFingerprint).not.toBe("old-content");
    } finally {
      if (createObjectURLDescriptor) Object.defineProperty(URL, "createObjectURL", createObjectURLDescriptor);
      else delete (URL as { createObjectURL?: typeof URL.createObjectURL }).createObjectURL;
      if (revokeObjectURLDescriptor) Object.defineProperty(URL, "revokeObjectURL", revokeObjectURLDescriptor);
      else delete (URL as { revokeObjectURL?: typeof URL.revokeObjectURL }).revokeObjectURL;
    }
  });
});

describe("plugin store adapter", () => {
  it("maps the live Moyro channel state to Mattermost entities", () => {
    const native = {
      getState: () => ({
        auth: { token: "token", user: null },
        channels: {
          byId: { channel: { id: "channel", team_id: "team", type: "O" as const, display_name: "Town Square", name: "town-square" } },
          currentId: "channel",
        },
        posts: { byChannel: {} },
      }),
      dispatch: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    };
    const adapter = createMattermostStoreAdapter(native, () => "https://chat.example.com");

    expect(adapter.getState()).toMatchObject({
      entities: {
        general: { config: { SiteURL: "https://chat.example.com" } },
        channels: { currentChannelId: "channel", channels: { channel: { id: "channel" } } },
      },
    });
  });

  it("adapts users, teams, posts and executes Mattermost preference thunks", async () => {
    const native = {
      getState: () => ({
        auth: { token: "token", user: { id: "user", username: "alice", email: "alice@example.com" } },
        channels: { byId: {}, currentId: "channel" },
        posts: { byChannel: {} },
      }),
      dispatch: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    };
    const adapter = createMattermostStoreAdapter(native);
    adapter.updateContext({
      currentTeamId: "team",
      teams: [{ id: "team", name: "engineering" }],
      users: { other: { id: "other", username: "bob" } },
      posts: [{ id: "post", channel_id: "channel", message: "before" }],
    });
    const preference = { user_id: "user", category: "pp_echo", name: "delivery_times", value: "09:00" };
    await adapter.dispatch(async (dispatch: (action: unknown) => unknown) => {
      dispatch({ type: "RECEIVED_PREFERENCES", data: [preference] });
      return { data: true };
    });

    expect(adapter.getState()).toMatchObject({
      entities: {
        users: { currentUserId: "user", profiles: { user: { username: "alice" }, other: { username: "bob" } } },
        teams: { currentTeamId: "team", teams: { team: { name: "engineering" } } },
        posts: { posts: { post: { message: "before" } } },
        preferences: { myPreferences: { "pp_echo--delivery_times": preference } },
      },
    });
  });
});

describe("authenticated plugin fetch", () => {
  it("replaces spoofed authorization on same-origin plugin requests", async () => {
    const upstream = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(null, { status: 204 }));
    const pluginFetch = createAuthenticatedPluginFetch(
      upstream as unknown as typeof fetch,
      () => "current-token",
      "https://chat.example.com",
    );

    await pluginFetch("/plugins/com.example/api", {
      headers: { Authorization: "Bearer spoofed" },
    });

    const request = upstream.mock.calls[0][0] as Request;
    expect(request.headers.get("Authorization")).toBe("Bearer current-token");
    expect(request.redirect).toBe("error");
  });

  it("authenticates same-origin /api/v4 calls and strips spoofed user identity", async () => {
    const upstream = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(null, { status: 204 }));
    const pluginFetch = createAuthenticatedPluginFetch(
      upstream as unknown as typeof fetch,
      () => "current-token",
      "https://chat.example.com",
    );

    await pluginFetch("/api/v4/users/me/preferences", {
      headers: { Authorization: "Bearer stale", "Mattermost-User-ID": "spoofed-user" },
    });

    const request = upstream.mock.calls[0][0] as Request;
    expect(request.headers.get("Authorization")).toBe("Bearer current-token");
    expect(request.headers.has("Mattermost-User-ID")).toBe(false);
    expect(request.redirect).toBe("error");
  });

  it("never attaches the token to an external URL", async () => {
    const upstream = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(null, { status: 204 }));
    const pluginFetch = createAuthenticatedPluginFetch(
      upstream as unknown as typeof fetch,
      () => "current-token",
      "https://chat.example.com",
    );
    const init = { headers: { "X-Test": "yes" } };

    await pluginFetch("https://outside.example/plugins/com.example/api", init);

    expect(upstream).toHaveBeenCalledWith("https://outside.example/plugins/com.example/api", init);
  });

  it("allows only matching same-origin bundle paths", () => {
    expect(resolvePluginBundleURL(
      "com.example.plugin",
      "/plugins/com.example.plugin/webapp/main.js",
      "https://chat.example.com",
    )).toBe("https://chat.example.com/plugins/com.example.plugin/webapp/main.js");
    expect(() => resolvePluginBundleURL(
      "com.example.plugin",
      "https://outside.example/plugins/com.example.plugin/main.js",
      "https://chat.example.com",
    )).toThrow(/outside/);
    expect(() => resolvePluginBundleURL(
      "com.example.plugin",
      "/plugins/com.example.other/main.js",
      "https://chat.example.com",
    )).toThrow(/outside/);
  });
});
