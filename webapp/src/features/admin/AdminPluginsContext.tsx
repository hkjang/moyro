import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { useSelector } from "react-redux";

import {
  adminApi,
  type AdminPlugin,
  type AdminPluginStatus,
} from "@/api/client";
import type { RootState } from "@/store";

type AdminPluginsContextValue = {
  plugins: readonly AdminPlugin[];
  statuses: readonly AdminPluginStatus[];
  runtimeManagementEnabled: boolean;
  initialized: boolean;
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
};

const AdminPluginsContext = createContext<AdminPluginsContextValue | null>(null);

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "플러그인 목록을 불러오지 못했습니다.";
}

export function AdminPluginsProvider({
  enabled,
  children,
}: {
  enabled: boolean;
  children: React.ReactNode;
}) {
  const token = useSelector((state: RootState) => state.auth.token);
  const [plugins, setPlugins] = useState<AdminPlugin[]>([]);
  const [statuses, setStatuses] = useState<AdminPluginStatus[]>([]);
  const [runtimeManagementEnabled, setRuntimeManagementEnabled] = useState(false);
  const [initialized, setInitialized] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestSequence = useRef(0);

  const refresh = useCallback(async () => {
    const requestID = ++requestSequence.current;
    if (!enabled || !token) {
      setPlugins([]);
      setStatuses([]);
      setRuntimeManagementEnabled(false);
      setError(null);
      setLoading(false);
      setInitialized(true);
      return;
    }

    setLoading(true);
    setError(null);
    try {
      const [pluginRows, statusRows, capabilities] = await Promise.all([
        adminApi.listPlugins(token),
        adminApi.listPluginStatuses(token),
        adminApi.getPluginManagementCapabilities(token),
      ]);
      if (requestSequence.current !== requestID) return;
      setPlugins(pluginRows);
      setStatuses(statusRows);
      setRuntimeManagementEnabled(
        capabilities.management_enabled && capabilities.uploads_enabled,
      );
    } catch (loadError) {
      if (requestSequence.current !== requestID) return;
      setError(errorMessage(loadError));
    } finally {
      if (requestSequence.current === requestID) {
        setLoading(false);
        setInitialized(true);
      }
    }
  }, [enabled, token]);

  useEffect(() => {
    void refresh();
    return () => {
      requestSequence.current += 1;
    };
  }, [refresh]);

  const value = useMemo<AdminPluginsContextValue>(() => ({
    plugins,
    statuses,
    runtimeManagementEnabled,
    initialized,
    loading,
    error,
    refresh,
  }), [error, initialized, loading, plugins, refresh, runtimeManagementEnabled, statuses]);

  return <AdminPluginsContext.Provider value={value}>{children}</AdminPluginsContext.Provider>;
}

export function useAdminPlugins(): AdminPluginsContextValue {
  const value = useContext(AdminPluginsContext);
  if (!value) throw new Error("useAdminPlugins must be used inside AdminPluginsProvider");
  return value;
}
