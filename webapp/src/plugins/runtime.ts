import * as React from "react";
import * as ReactDOM from "react-dom";
import * as ReactRedux from "react-redux";
import * as ReactRouterDom from "react-router-dom";
// RTK re-exports Redux 5's public store helpers (createStore, compose,
// applyMiddleware, bindActionCreators, combineReducers). Exposing the same
// singleton avoids adding a second Redux implementation/context to the page.
import * as Redux from "@reduxjs/toolkit";
import PropTypes from "prop-types";

import { store } from "@/store";
import { createAuthenticatedPluginFetch, resolvePluginBundleURL } from "./authenticatedFetch";
import { createRegistry } from "./registry";
import { createMattermostStoreAdapter } from "./storeAdapter";

export interface PluginClass {
  initialize(
    registry: ReturnType<typeof createRegistry>,
    pluginStore: ReturnType<typeof createMattermostStoreAdapter>,
  ): void | Promise<void>;
  uninitialize?(): void | Promise<void>;
}

type RegisterFn = (id: string, plugin: PluginClass) => void;
export type PluginRecord = {
  plugin: PluginClass;
  registry: ReturnType<typeof createRegistry>;
  initialization?: Promise<void>;
  bundleVersion?: string;
  bundleSource?: string;
  bundleFingerprint?: string;
};

export type LoadedPluginBundle = {
  id: string;
  version?: string;
  source?: string;
};

declare global {
  interface Window {
    React: typeof React;
    ReactDOM: typeof ReactDOM;
    ReactRedux: typeof ReactRedux;
    ReactRouterDom: typeof ReactRouterDom;
    Redux: typeof Redux;
    PropTypes: typeof PropTypes;
    basename?: string;
    registerPlugin?: RegisterFn;
    __moyro_plugins__?: Record<string, PluginRecord>;
    __moyro_plugin_fetch_original__?: typeof fetch;
  }
}

export const mattermostPluginStore = createMattermostStoreAdapter(store);

export function exposeMattermostGlobals(): void {
  window.React = React;
  window.ReactDOM = ReactDOM;
  window.ReactRedux = ReactRedux;
  window.ReactRouterDom = ReactRouterDom;
  window.Redux = Redux;
  window.PropTypes = PropTypes;
  window.basename = window.basename ?? "";
}

function installAuthenticatedPluginFetch(): void {
  if (window.__moyro_plugin_fetch_original__) return;
  const original = window.fetch.bind(window);
  window.__moyro_plugin_fetch_original__ = original;
  window.fetch = createAuthenticatedPluginFetch(
    original,
    () => store.getState().auth.token,
    window.location.origin,
  );
}

export function bootstrapPluginRuntime(): void {
  exposeMattermostGlobals();
  installAuthenticatedPluginFetch();
  window.__moyro_plugins__ = window.__moyro_plugins__ ?? {};
  window.registerPlugin = (id, plugin) => {
    if (!id || typeof plugin?.initialize !== "function") {
      throw new Error("invalid plugin registration");
    }
    const previous = window.__moyro_plugins__![id];
    if (previous) {
      previous.registry.unregisterAll();
      void Promise.resolve().then(() => previous.plugin.uninitialize?.()).catch((err) => {
        console.error(`failed to uninitialize plugin ${id}`, err);
      });
    }
    const registry = createRegistry(id);
    const record: PluginRecord = { plugin, registry };
    window.__moyro_plugins__![id] = record;
    record.initialization = Promise.resolve()
      .then(() => plugin.initialize(registry, mattermostPluginStore))
      .then(() => undefined)
      .catch(async (error: unknown) => {
        registry.unregisterAll();
        try {
          await plugin.uninitialize?.();
        } catch (cleanupError) {
          console.error(`failed to clean up plugin ${id} after initialize failure`, cleanupError);
        }
        if (window.__moyro_plugins__?.[id] === record) delete window.__moyro_plugins__[id];
        throw new Error(`plugin ${id} initialization failed`, { cause: error });
      });
    // executeResolvedPluginBundle awaits this promise after the script load
    // event. Attach a handler immediately as initialization may reject first.
    void record.initialization.catch(() => undefined);
  };
}

type PendingLoad = {
  key: string;
  promise: Promise<PluginRecord>;
};

const pendingLoads = new Map<string, PendingLoad>();

export function loadPluginBundle(id: string, source: string, version = ""): Promise<PluginRecord> {
  const url = resolvePluginBundleURL(id, source, window.location.origin);
  const pending = pendingLoads.get(id);
  const key = `${version}\n${url}`;
  if (pending?.key === key) return pending.promise;
  const predecessor = pending?.promise;
  const load = (async () => {
    if (predecessor) await predecessor.catch(() => undefined);
    const fetched = await fetchResolvedPluginBundle(id, url);
    const existing = window.__moyro_plugins__?.[id];
    if (
      existing &&
      existing.bundleVersion === version &&
      existing.bundleSource === url &&
      existing.bundleFingerprint === fetched.fingerprint
    ) {
      return existing;
    }
    if (existing) await unloadPlugin(id);
    return executeResolvedPluginBundle(id, url, version, fetched);
  })().finally(() => {
    if (pendingLoads.get(id)?.promise === load) pendingLoads.delete(id);
  });
  pendingLoads.set(id, { key, promise: load });
  return load;
}

async function bundleFingerprint(javascript: string): Promise<string> {
  if (globalThis.crypto?.subtle) {
    const bytes = new TextEncoder().encode(javascript);
    const digest = await globalThis.crypto.subtle.digest("SHA-256", bytes);
    return [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, "0")).join("");
  }
  // Only used by older browsers/test DOMs lacking Web Crypto. This is a
  // change detector, not a trust or signature decision.
  let hash = 2166136261;
  for (let index = 0; index < javascript.length; index += 1) {
    hash ^= javascript.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return `${javascript.length}:${hash >>> 0}`;
}

async function fetchResolvedPluginBundle(id: string, url: string) {
  // A forced replacement may keep the same plugin id and version. Bypass the
  // browser cache so the newly installed bundle takes effect immediately.
  const response = await window.fetch(url, { redirect: "error", cache: "no-store" });
  if (!response.ok) {
    throw new Error(`failed to fetch plugin ${id}: HTTP ${response.status}`);
  }
  const javascript = await response.text();
  return { javascript, fingerprint: await bundleFingerprint(javascript) };
}

async function executeResolvedPluginBundle(
  id: string,
  url: string,
  version: string,
  fetched: { javascript: string; fingerprint: string },
): Promise<PluginRecord> {
  const { javascript, fingerprint } = fetched;
  const objectURL = URL.createObjectURL(new Blob([javascript], { type: "text/javascript" }));
  const script = document.createElement("script");
  script.src = objectURL;
  script.async = true;
  script.dataset.pluginId = id;
  script.dataset.pluginVersion = version;
  try {
    const loadedScript = new Promise<void>((resolve, reject) => {
      script.onload = () => resolve();
      script.onerror = () => reject(new Error(`failed to load plugin ${id}`));
    });
    document.body.appendChild(script);
    await loadedScript;
  } finally {
    URL.revokeObjectURL(objectURL);
  }
  const loaded = window.__moyro_plugins__?.[id];
  if (!loaded) {
    script.remove();
    throw new Error(`plugin ${id} did not register itself`);
  }
  try {
    await loaded.initialization;
  } catch (error) {
    script.remove();
    throw error;
  }
  loaded.bundleVersion = version;
  loaded.bundleSource = url;
  loaded.bundleFingerprint = fingerprint;
  return loaded;
}

export async function unloadPlugin(id: string): Promise<void> {
  const loaded = window.__moyro_plugins__?.[id];
  if (!loaded) return;
  loaded.registry.unregisterAll();
  try {
    await loaded.plugin.uninitialize?.();
  } catch (error) {
    // A plugin cleanup failure must not leave a stale registration behind or
    // prevent its replacement bundle from loading.
    console.error(`failed to uninitialize plugin ${id}`, error);
  } finally {
    delete window.__moyro_plugins__?.[id];
    document.querySelectorAll<HTMLScriptElement>("script[data-plugin-id]").forEach((node) => {
      if (node.dataset.pluginId === id) node.remove();
    });
  }
}

export function loadedPluginIDs(): string[] {
  return Object.keys(window.__moyro_plugins__ ?? {});
}

export function loadedPluginBundles(): LoadedPluginBundle[] {
  return Object.entries(window.__moyro_plugins__ ?? {}).map(([id, record]) => ({
    id,
    version: record.bundleVersion,
    source: record.bundleSource,
  }));
}

export function notifyPluginRuntimeChanged(): void {
  window.dispatchEvent(new Event("moyro:plugins-changed"));
}
