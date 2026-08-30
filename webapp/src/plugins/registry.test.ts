// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  applyPluginRegistryAction,
  createRegistry,
  dispatchPluginWebSocketEvent,
  getRegistryState,
  localizePluginAdminSchema,
  subscribeRegistry,
} from "./registry";

const registries: Array<ReturnType<typeof createRegistry>> = [];

afterEach(() => {
  for (const registry of registries.splice(0)) registry.unregisterAll();
});

describe("plugin registry", () => {
  it("publishes Mattermost channel-header registrations reactively", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeRegistry(listener);
    const registry = createRegistry("com.example.header");
    registries.push(registry);
    const action = vi.fn();

    const id = registry.registerChannelHeaderButtonAction("icon", action, "Open", "Open plugin");

    expect(listener).toHaveBeenCalledTimes(1);
    expect(getRegistryState().channelHeaderButtons).toContainEqual({
      id,
      pluginId: "com.example.header",
      icon: "icon",
      action,
      dropdownText: "Open",
      tooltipText: "Open plugin",
    });
    unsubscribe();
  });

  it("registers and removes admin custom settings", () => {
    const registry = createRegistry("com.example.admin");
    registries.push(registry);
    const Component = () => null;

    registry.registerAdminConsoleCustomSetting("Config", Component, { showTitle: true });
    expect(getRegistryState().adminConsoleCustomSettings).toMatchObject([
      { pluginId: "com.example.admin", key: "Config", component: Component, options: { showTitle: true } },
    ]);

    registry.unregisterAll();
    expect(getRegistryState().adminConsoleCustomSettings).toEqual([]);
  });

  it("implements the official component-first RHS action contract", () => {
    const registry = createRegistry("com.mattermost.langflow");
    registries.push(registry);
    const Pane = () => null;
    const Title = () => null;

    const rhs = registry.registerRightHandSidebarComponent(Pane, Title);

    expect(getRegistryState().rhsComponents).toMatchObject([{
      id: rhs.id,
      pluginId: "com.mattermost.langflow",
      component: Pane,
      title: Title,
    }]);
    expect(applyPluginRegistryAction(rhs.showRHSPlugin)).toBe(true);
    expect(getRegistryState().activeRhsComponentId).toBe(rhs.id);
    applyPluginRegistryAction(rhs.toggleRHSPlugin);
    expect(getRegistryState().activeRhsComponentId).toBeNull();
  });

  it("dispatches custom websocket events and unregisters the handler", () => {
    const registry = createRegistry("com.mattermost.langflow");
    registries.push(registry);
    const handler = vi.fn();
    const event = "custom_com.mattermost.langflow_postupdate";
    registry.registerWebSocketEventHandler(event, handler);

    dispatchPluginWebSocketEvent({ event, data: { post_id: "post-1", next: "stream" } });
    expect(handler).toHaveBeenCalledWith(expect.objectContaining({ event, data: { post_id: "post-1", next: "stream" } }));

    registry.unregisterWebSocketEventHandler(event, handler);
    dispatchPluginWebSocketEvent({ event, data: {} });
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("registers user settings and applies a plugin-owned admin localization callback", () => {
    const registry = createRegistry("com.mattermost.echosummary");
    registries.push(registry);
    const Delivery = () => null;
    registry.registerUserSettings({
      id: "com.mattermost.echosummary",
      uiName: "Echo Summary",
      sections: [{ title: "Delivery", component: Delivery }],
    });
    registry.registerAdminConsolePlugin((schema) => {
      schema.header = "localized";
    });

    expect(getRegistryState().userSettings).toMatchObject([{
      pluginId: "com.mattermost.echosummary",
      uiName: "Echo Summary",
      sections: [{ title: "Delivery", component: Delivery }],
    }]);
    const source = { header: "original", sections: [] };
    expect(localizePluginAdminSchema("com.mattermost.echosummary", source)?.header).toBe("localized");
    expect(source.header).toBe("original");
  });
});
