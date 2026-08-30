import { useEffect, useState } from "react";
import { useSelector } from "react-redux";

import { pluginApi } from "@/api/client";
import type { RootState } from "@/store";
import { loadPluginBundle, loadedPluginBundles, loadedPluginIDs, unloadPlugin } from "./runtime";

export const PLUGIN_RUNTIME_REFRESH_MS = 60_000;

/** Keeps authenticated, enabled server web bundles aligned with the browser. */
export function PluginLoader() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [revision, setRevision] = useState(0);
  const [loadErrors, setLoadErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    const refresh = () => setRevision((value) => value + 1);
    const refreshWhenVisible = () => {
      if (document.visibilityState === "visible") refresh();
    };
    window.addEventListener("moyro:plugins-changed", refresh);
    document.addEventListener("visibilitychange", refreshWhenVisible);
    const intervalID = window.setInterval(refresh, PLUGIN_RUNTIME_REFRESH_MS);
    return () => {
      window.removeEventListener("moyro:plugins-changed", refresh);
      document.removeEventListener("visibilitychange", refreshWhenVisible);
      window.clearInterval(intervalID);
    };
  }, []);

  useEffect(() => {
    let active = true;
    void (async () => {
      if (!token) {
        await Promise.allSettled(loadedPluginIDs().map((id) => unloadPlugin(id)));
        if (active) setLoadErrors({});
        return;
      }
      try {
        const bundles = await pluginApi.listWebapps(token);
        if (!active) return;
        const desired = new Map(bundles.map((bundle) => [bundle.id, bundle]));
        await Promise.allSettled(
          loadedPluginBundles()
            .filter((loaded) => {
              const bundle = desired.get(loaded.id);
              return !bundle || loaded.version !== bundle.version;
            })
            .map((loaded) => unloadPlugin(loaded.id)),
        );
        if (!active) return;
        const outcomes = await Promise.allSettled(
          bundles.map((bundle) => loadPluginBundle(bundle.id, bundle.url, bundle.version)),
        );
        outcomes.forEach((outcome, index) => {
          const pluginID = bundles[index].id;
          if (outcome.status === "rejected") {
            console.error(`failed to load plugin ${pluginID}`, outcome.reason);
            if (active) setLoadErrors((current) => ({
              ...current,
              [pluginID]: outcome.reason instanceof Error ? outcome.reason.message : String(outcome.reason),
            }));
          } else if (active) {
            setLoadErrors((current) => {
              if (!(pluginID in current)) return current;
              const next = { ...current };
              delete next[pluginID];
              return next;
            });
          }
        });
      } catch (error) {
        if (active) {
          console.error("failed to discover plugin web bundles", error);
          setLoadErrors((current) => ({
            ...current,
            discovery: error instanceof Error ? error.message : String(error),
          }));
        }
      }
    })();
    return () => { active = false; };
  }, [revision, token]);

  const failures = Object.entries(loadErrors);
  if (failures.length === 0) return null;
  return (
    <div
      role="alert"
      aria-live="assertive"
      style={{
        position: "fixed",
        zIndex: 2000,
        right: 16,
        bottom: 16,
        maxWidth: 440,
        border: "1px solid #c2414b",
        borderRadius: 8,
        background: "#fff",
        color: "#7f1d1d",
        padding: "10px 12px",
        boxShadow: "0 12px 32px rgba(24,32,51,.18)",
      }}
    >
      <strong>플러그인 화면을 불러오지 못했습니다.</strong>
      {failures.map(([pluginID, message]) => (
        <div key={pluginID} style={{ marginTop: 4, fontSize: 12 }}>{pluginID}: {message}</div>
      ))}
    </div>
  );
}
