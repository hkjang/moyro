import { createRegistry } from "./registry";
export function bootstrapPluginRuntime() {
    window.__moddle_plugins__ = window.__moddle_plugins__ ?? {};
    window.registerPlugin = (id, plugin) => {
        if (!id || typeof plugin?.initialize !== "function") {
            throw new Error("invalid plugin registration");
        }
        const previous = window.__moddle_plugins__[id];
        if (previous) {
            previous.registry.unregisterAll();
            void Promise.resolve(previous.plugin.uninitialize?.()).catch((err) => {
                console.error(`failed to uninitialize plugin ${id}`, err);
            });
        }
        const registry = createRegistry(id);
        window.__moddle_plugins__[id] = { plugin, registry };
        void Promise.resolve(plugin.initialize(registry, null)).catch((err) => {
            console.error(`failed to initialize plugin ${id}`, err);
        });
    };
}
export async function loadPluginBundle(id, url) {
    const existing = window.__moddle_plugins__?.[id];
    if (existing)
        return existing;
    const script = document.createElement("script");
    script.src = url;
    script.async = true;
    script.dataset.pluginId = id;
    document.body.appendChild(script);
    await new Promise((resolve, reject) => {
        script.onload = () => resolve();
        script.onerror = () => reject(new Error(`failed to load plugin ${id}`));
    });
    const loaded = window.__moddle_plugins__?.[id];
    if (!loaded) {
        script.remove();
        throw new Error(`plugin ${id} did not register itself`);
    }
    return loaded;
}
