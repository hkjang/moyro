import { createRegistry } from "./registry";

export interface PluginClass {
  initialize(registry: ReturnType<typeof createRegistry>, store: unknown): void | Promise<void>;
  uninitialize?(): void | Promise<void>;
}

type RegisterFn = (id: string, plugin: PluginClass) => void;
type PluginRecord = {
  plugin: PluginClass;
  registry: ReturnType<typeof createRegistry>;
};

declare global {
  interface Window {
    registerPlugin?: RegisterFn;
    __moyro_plugins__?: Record<string, PluginRecord>;
  }
}

export function bootstrapPluginRuntime() {
  window.__moyro_plugins__ = window.__moyro_plugins__ ?? {};
  window.registerPlugin = (id, plugin) => {
    if (!id || typeof plugin?.initialize !== "function") {
      throw new Error("invalid plugin registration");
    }
    const previous = window.__moyro_plugins__![id];
    if (previous) {
      previous.registry.unregisterAll();
      void Promise.resolve(previous.plugin.uninitialize?.()).catch((err) => {
        console.error(`failed to uninitialize plugin ${id}`, err);
      });
    }
    const registry = createRegistry(id);
    window.__moyro_plugins__![id] = { plugin, registry };
    void Promise.resolve(plugin.initialize(registry, null)).catch((err) => {
      console.error(`failed to initialize plugin ${id}`, err);
    });
  };
}

export async function loadPluginBundle(id: string, url: string): Promise<PluginRecord> {
  const existing = window.__moyro_plugins__?.[id];
  if (existing) return existing;
  const script = document.createElement("script");
  script.src = url;
  script.async = true;
  script.dataset.pluginId = id;
  document.body.appendChild(script);
  await new Promise<void>((resolve, reject) => {
    script.onload = () => resolve();
    script.onerror = () => reject(new Error(`failed to load plugin ${id}`));
  });
  const loaded = window.__moyro_plugins__?.[id];
  if (!loaded) {
    script.remove();
    throw new Error(`plugin ${id} did not register itself`);
  }
  return loaded;
}
