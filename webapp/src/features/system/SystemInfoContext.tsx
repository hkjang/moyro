import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { api, publicMoyroApi, type SystemInfo } from "@/api/client";

const FALLBACK_INFO: SystemInfo = {
  name: "moyro",
  version: "0.2.14",
  build_hash: "",
  build_date: "",
  oidc_enabled: false,
  oidc_provider_name: "Keycloak",
  approval_enabled: false,
  local_signup_enabled: false,
  capabilities: {
    email_digest: { configured: false, enabled: false },
    drafts: { storage_mode: "local", retention_days: 7, clear_on_logout: true },
  },
};

type SystemInfoContextValue = SystemInfo & { loaded: boolean; refresh: () => Promise<void> };

const SystemInfoContext = createContext<SystemInfoContextValue>({
  ...FALLBACK_INFO,
  loaded: false,
  refresh: async () => undefined,
});

export function SystemInfoProvider({ children }: { children: React.ReactNode }) {
  const [info, setInfo] = useState<SystemInfo & { loaded: boolean }>({ ...FALLBACK_INFO, loaded: false });

  const refresh = useCallback(async () => {
    const [nativeResult, configResult, pingResult] = await Promise.allSettled([
      publicMoyroApi.systemInfo(),
      api.clientConfig(),
      api.ping(),
    ]);
    const native = nativeResult.status === "fulfilled" ? nativeResult.value : undefined;
    const config = configResult.status === "fulfilled" ? configResult.value : {};
    const ping = pingResult.status === "fulfilled" ? pingResult.value : undefined;
    const providers = ping?.oauth_providers ?? [];
    const emailDigestEnabled = native?.capabilities?.email_digest?.enabled ?? config.SendEmailNotifications === "true";
    const emailDigestConfigured = native?.capabilities?.email_digest?.configured ?? emailDigestEnabled;
    setInfo({
      name: "moyro",
      version: native?.version || config.Version || ping?.version || FALLBACK_INFO.version,
      build_hash: native?.build_hash || config.BuildHash || ping?.build_hash || "",
      build_date: native?.build_date || config.BuildDate || ping?.build_date || "",
      oidc_enabled: native?.oidc_enabled ?? (providers.includes("keycloak") || providers.includes("oidc")),
      oidc_provider_name: native?.oidc_provider_name || (providers.includes("keycloak") ? "Keycloak" : "OIDC"),
      approval_enabled: native?.approval_enabled ?? false,
      local_signup_enabled: native?.local_signup_enabled ?? false,
      capabilities: {
        email_digest: {
          configured: emailDigestConfigured,
          enabled: emailDigestEnabled,
        },
        drafts: native?.capabilities?.drafts ?? FALLBACK_INFO.capabilities?.drafts,
      },
      loaded: true,
    });
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  const value = useMemo(() => ({ ...info, refresh }), [info, refresh]);
  return <SystemInfoContext.Provider value={value}>{children}</SystemInfoContext.Provider>;
}

export function useSystemInfo(): SystemInfoContextValue {
  return useContext(SystemInfoContext);
}

export function displayVersion(version: string): string {
  const clean = version.trim() || FALLBACK_INFO.version;
  return clean.startsWith("v") ? clean : `v${clean}`;
}
