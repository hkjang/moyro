import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { moyroMeApi } from "@/api/client";
import type { RootState } from "@/store";

export const ADMIN_PERMISSIONS = [
  "manage_settings",
  "manage_oidc",
  "manage_ai",
  "manage_key_permissions",
  "manage_roles",
  "manage_api_keys",
  "manage_approval_policies",
] as const;

type AdminAccessValue = {
  loaded: boolean;
  permissions: ReadonlySet<string>;
  can: (permission: string) => boolean;
  canAny: (permissions: readonly string[]) => boolean;
  hasAdminAccess: boolean;
};

const AdminAccessContext = createContext<AdminAccessValue>({
  loaded: false,
  permissions: new Set(),
  can: () => false,
  canAny: () => false,
  hasAdminAccess: false,
});

export function AdminAccessProvider({ children }: { children: React.ReactNode }) {
  const token = useSelector((state: RootState) => state.auth.token);
  const [loaded, setLoaded] = useState(false);
  const [permissions, setPermissions] = useState<ReadonlySet<string>>(new Set());

  useEffect(() => {
    let active = true;
    if (!token) {
      setPermissions(new Set());
      setLoaded(true);
      return () => { active = false; };
    }
    setLoaded(false);
    moyroMeApi.getPermissions(token).then(
      (value) => {
        if (active) setPermissions(new Set(value.permissions));
      },
      () => {
        if (active) setPermissions(new Set());
      },
    ).finally(() => { if (active) setLoaded(true); });
    return () => { active = false; };
  }, [token]);

  const can = useCallback(
    (permission: string) => permissions.has("manage_system") || permissions.has(permission),
    [permissions],
  );
  const canAny = useCallback(
    (requested: readonly string[]) => requested.some((permission) => can(permission)),
    [can],
  );
  const value = useMemo<AdminAccessValue>(() => ({
    loaded,
    permissions,
    can,
    canAny,
    hasAdminAccess: permissions.has("manage_system") || ADMIN_PERMISSIONS.some((permission) => permissions.has(permission)),
  }), [can, canAny, loaded, permissions]);

  return <AdminAccessContext.Provider value={value}>{children}</AdminAccessContext.Provider>;
}

export function useAdminAccess(): AdminAccessValue {
  return useContext(AdminAccessContext);
}
