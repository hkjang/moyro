import type { ComponentType } from "react";

export interface PluginRegistry {
  registerMainMenuAction(text: string, action: () => void): void;
  registerChannelHeaderButtonAction(
    icon: ComponentType,
    action: (channelId: string) => void,
    tooltip: string,
  ): void;
  registerPostTypeComponent(
    postType: string,
    component: ComponentType<{ post: unknown }>,
  ): void;
  registerRightHandSidebarComponent(title: string, component: ComponentType): void;
  unregisterAll(): void;
}

export interface PluginClass {
  initialize(registry: PluginRegistry, store: unknown): void | Promise<void>;
  uninitialize?(): void | Promise<void>;
}

declare global {
  interface Window {
    registerPlugin?: (id: string, plugin: PluginClass) => void;
  }
}

export function registerPlugin(id: string, plugin: PluginClass): void {
  if (typeof window === "undefined" || !window.registerPlugin) {
    throw new Error("Moddle plugin runtime not available");
  }
  window.registerPlugin(id, plugin);
}
