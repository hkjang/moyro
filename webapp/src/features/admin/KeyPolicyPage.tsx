import {
  Alert,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  FormControlLabel,
  FormGroup,
  Grid,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import {
  moyroAdminApi,
  type AdminAPIKey,
  type KeyPolicySettings,
  type RBACPermission,
  type RBACRole,
} from "@/api/client";
import { LoadState, SaveBar, SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";

const DEFAULT_POLICY: KeyPolicySettings = {
  enabled: true,
  allowed_scopes: [
    "manage_own_api_keys",
    "use_ai",
    "mcp_read",
    "mcp_write",
    "request_approval",
    "review_approval",
  ],
  default_scopes: ["mcp_read"],
  default_ttl_days: 90,
  max_ttl_days: 365,
  rotation_days: 90,
  rotation_grace_hours: 24,
  allow_personal_keys: true,
  allow_scope_self_service: false,
};

const parseList = (value: string) => value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);

export function KeyPolicyPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const access = useAdminAccess();
  const canPolicy = access.can("manage_key_permissions");
  const canRoles = access.can("manage_roles");
  const canKeys = access.can("manage_api_keys");
  const [policy, setPolicy] = useState<KeyPolicySettings>(DEFAULT_POLICY);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const [permissions, setPermissions] = useState<RBACPermission[]>([]);
  const [roles, setRoles] = useState<RBACRole[]>([]);
  const [selectedRoleId, setSelectedRoleId] = useState("");
  const [rolesLoading, setRolesLoading] = useState(true);
  const [roleSaving, setRoleSaving] = useState(false);
  const [roleError, setRoleError] = useState("");
  const [roleSaved, setRoleSaved] = useState("");
  const [keys, setKeys] = useState<AdminAPIKey[]>([]);
  const [keysLoading, setKeysLoading] = useState(true);
  const [keysError, setKeysError] = useState("");
  const [revokingKey, setRevokingKey] = useState("");

  useEffect(() => {
    if (!token || !canPolicy) { setLoading(false); return; }
    let cancelled = false;
    moyroAdminApi.getSettings<KeyPolicySettings>(token, "key-policy").then(
      (value) => { if (!cancelled) { setPolicy({ ...DEFAULT_POLICY, ...value }); setError(""); } },
      (err: unknown) => { if (!cancelled) setError(err instanceof Error ? err.message : "키 정책 API에 연결하지 못했습니다."); },
    ).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [canPolicy, token]);

  useEffect(() => {
    if (!token || !canKeys) { setKeysLoading(false); return; }
    let active = true;
    setKeysLoading(true);
    moyroAdminApi.listAPIKeys(token).then(
      (rows) => {
        if (!active) return;
        setKeys(rows);
        setKeysError("");
      },
      (err: unknown) => {
        if (active) setKeysError(err instanceof Error ? err.message : "API 키 목록을 불러오지 못했습니다.");
      },
    ).finally(() => { if (active) setKeysLoading(false); });
    return () => { active = false; };
  }, [canKeys, token]);

  useEffect(() => {
    if (!token || !canRoles) { setRolesLoading(false); return; }
    let cancelled = false;
    setRolesLoading(true);
    Promise.all([moyroAdminApi.listPermissions(token), moyroAdminApi.listRoles(token)]).then(
      ([permissionRows, roleRows]) => {
        if (cancelled) return;
        setPermissions(permissionRows);
        setRoles(roleRows);
        setSelectedRoleId((current) => roleRows.some((role) => role.id === current)
          ? current
          : roleRows[0]?.id ?? "");
        setRoleError("");
      },
      (err: unknown) => {
        if (!cancelled) setRoleError(err instanceof Error ? err.message : "RBAC 역할 정보를 불러오지 못했습니다.");
      },
    ).finally(() => { if (!cancelled) setRolesLoading(false); });
    return () => { cancelled = true; };
  }, [canRoles, token]);

  const selectedRole = useMemo(
    () => roles.find((role) => role.id === selectedRoleId),
    [roles, selectedRoleId],
  );
  const permissionGroups = useMemo(() => {
    const grouped = new Map<string, RBACPermission[]>();
    for (const permission of permissions) {
      const group = permission.resource_type || "general";
      grouped.set(group, [...(grouped.get(group) ?? []), permission]);
    }
    return Array.from(grouped.entries());
  }, [permissions]);

  const update = <K extends keyof KeyPolicySettings>(key: K, value: KeyPolicySettings[K]) => {
    setPolicy((prev) => ({ ...prev, [key]: value }));
    setSaved("");
  };

  async function save() {
    if (!token) return;
    setSaving(true);
    try {
      const normalized = {
        ...policy,
        default_scopes: policy.default_scopes.filter((scope) => policy.allowed_scopes.includes(scope)),
        max_ttl_days: Math.max(policy.default_ttl_days, policy.max_ttl_days),
      };
      const result = await moyroAdminApi.patchSettings(token, "key-policy", normalized);
      setPolicy(result);
      setError("");
      setSaved("키 정책을 저장했습니다.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "키 정책을 저장하지 못했습니다.");
    } finally {
      setSaving(false);
    }
  }

  function toggleRolePermission(permission: string, enabled: boolean) {
    if (!selectedRole) return;
    setRoles((current) => current.map((role) => {
      if (role.id !== selectedRole.id) return role;
      const next = new Set(role.permissions);
      if (enabled) next.add(permission); else next.delete(permission);
      return { ...role, permissions: Array.from(next).sort() };
    }));
    setRoleSaved("");
  }

  async function saveRole() {
    if (!token || !selectedRole) return;
    setRoleSaving(true);
    setRoleSaved("");
    try {
      const result = await moyroAdminApi.patchRole(token, selectedRole.id, {
        permissions: selectedRole.permissions,
        revision: selectedRole.revision,
      });
      setRoles((current) => current.map((role) => role.id === result.id ? result : role));
      setRoleError("");
      setRoleSaved(`${result.display_name || result.name} 권한을 저장했습니다.`);
    } catch (err) {
      setRoleError(err instanceof Error ? err.message : "역할 권한을 저장하지 못했습니다.");
    } finally {
      setRoleSaving(false);
    }
  }

  async function revokeKey(key: AdminAPIKey) {
    if (!token || key.status === "revoked") return;
    if (!window.confirm(`${key.username}의 “${key.name}” 키를 즉시 폐기할까요? 이 작업은 되돌릴 수 없습니다.`)) return;
    setRevokingKey(key.id);
    try {
      await moyroAdminApi.revokeAPIKey(token, key.id);
      setKeys((current) => current.map((item) => item.id === key.id ? { ...item, status: "revoked" } : item));
      setKeysError("");
    } catch (err) {
      setKeysError(err instanceof Error ? err.message : "API 키를 폐기하지 못했습니다.");
    } finally {
      setRevokingKey("");
    }
  }

  return (
    <SettingsPage title="키 정책" description="개인별 API·MCP 키의 권한, 수명, 회전과 폐기 기준을 관리합니다.">
        {canPolicy && (
        <LoadState loading={loading} error={error}>
        <SettingsCard title="발급 권한" description="사용자는 허용된 scope 범위 안에서만 개인 키를 만들 수 있습니다.">
          <Stack spacing={2}>
            <FormControlLabel control={<Switch checked={policy.enabled} onChange={(event) => update("enabled", event.target.checked)} />} label="키 관리 체계를 사용합니다" />
            <FormControlLabel control={<Switch checked={policy.allow_personal_keys} onChange={(event) => update("allow_personal_keys", event.target.checked)} />} label="사용자 개인 키 발급 허용" />
            <FormControlLabel control={<Switch checked={policy.allow_scope_self_service} onChange={(event) => update("allow_scope_self_service", event.target.checked)} />} label="사용자가 허용 범위 안에서 scope를 직접 변경할 수 있음" />
            <TextField fullWidth label="허용 scope" value={policy.allowed_scopes.join(", ")} onChange={(event) => update("allowed_scopes", parseList(event.target.value))} helperText="쉼표로 구분합니다. 예: manage_own_api_keys, mcp_read, mcp_write" />
            <TextField fullWidth label="기본 scope" value={policy.default_scopes.join(", ")} onChange={(event) => update("default_scopes", parseList(event.target.value))} helperText="허용 scope에 포함된 값만 저장됩니다." />
          </Stack>
        </SettingsCard>

        <SettingsCard title="수명과 회전" description="회전하면 새 secret을 한 번만 공개하고 기존 키는 유예시간 뒤 폐기합니다.">
          <Grid container spacing={2}>
            <Grid size={{ xs: 12, sm: 6 }}><TextField fullWidth type="number" label="기본 TTL (일)" value={policy.default_ttl_days} onChange={(event) => update("default_ttl_days", Math.max(1, Number(event.target.value) || 1))} /></Grid>
            <Grid size={{ xs: 12, sm: 6 }}><TextField fullWidth type="number" label="최대 TTL (일)" value={policy.max_ttl_days} onChange={(event) => update("max_ttl_days", Math.max(1, Number(event.target.value) || 1))} /></Grid>
            <Grid size={{ xs: 12, sm: 6 }}><TextField fullWidth type="number" label="권장 회전 주기 (일)" value={policy.rotation_days} onChange={(event) => update("rotation_days", Math.max(1, Number(event.target.value) || 1))} /></Grid>
            <Grid size={{ xs: 12, sm: 6 }}><TextField fullWidth type="number" label="기존 키 유예 (시간)" value={policy.rotation_grace_hours} onChange={(event) => update("rotation_grace_hours", Math.max(0, Number(event.target.value) || 0))} /></Grid>
          </Grid>
        </SettingsCard>
        {!policy.allow_personal_keys && <Alert severity="info">개인 키 메뉴는 보이지만 새 키 발급은 비활성 상태로 안내됩니다.</Alert>}
        <SaveBar saving={saving} saved={saved} onSave={save} />
        </LoadState>
        )}
        {canKeys && (
        <SettingsCard title="발급된 키" description="모든 사용자의 키 메타데이터를 확인하고 유출되거나 불필요한 키를 즉시 폐기합니다. secret과 digest는 표시하지 않습니다.">
          {keysError && <Alert severity="warning" sx={{ mb: 2 }}>{keysError}</Alert>}
          {keysLoading ? (
            <Stack direction="row" sx={{ alignItems: "center", gap: 1.5 }} role="status">
              <CircularProgress size={22} />
              <Typography>발급된 키를 불러오는 중입니다.</Typography>
            </Stack>
          ) : keys.length === 0 ? (
            <Alert severity="info">아직 발급된 API·MCP 키가 없습니다.</Alert>
          ) : (
            <Stack spacing={1.25}>
              {keys.map((key) => (
                <Stack
                  key={key.id}
                  direction={{ xs: "column", sm: "row" }}
                  sx={{ alignItems: { sm: "center" }, gap: 1.25, p: 1.5, border: 1, borderColor: "divider", borderRadius: 2 }}
                >
                  <Stack sx={{ flex: 1, minWidth: 0 }}>
                    <Stack direction="row" sx={{ alignItems: "center", flexWrap: "wrap", gap: 0.75 }}>
                      <Typography variant="subtitle2">{key.name}</Typography>
                      <Chip size="small" label={key.kind.toUpperCase()} variant="outlined" />
                      <Chip size="small" label={key.status} color={key.status === "active" ? "success" : "default"} />
                    </Stack>
                    <Typography variant="body2" color="text.secondary" sx={{ overflowWrap: "anywhere" }}>
                      {key.username} · {key.email} · {key.prefix}
                    </Typography>
                    <Typography variant="caption" color="text.secondary">
                      {key.scopes.join(", ")} · 만료 {new Date(key.expires_at).toLocaleDateString("ko-KR")}
                    </Typography>
                  </Stack>
                  <Button
                    color="error"
                    variant="outlined"
                    disabled={key.status === "revoked" || key.status === "expired" || revokingKey === key.id}
                    onClick={() => void revokeKey(key)}
                  >
                    {revokingKey === key.id ? "폐기 중…" : "폐기"}
                  </Button>
                </Stack>
              ))}
            </Stack>
          )}
        </SettingsCard>
        )}
        {canRoles && (
        <SettingsCard title="역할별 권한" description="역할에 부여할 실제 RBAC permission을 편집합니다. 저장 시 revision을 비교해 다른 관리자의 변경을 덮어쓰지 않습니다.">
          {roleError && <Alert severity="warning" sx={{ mb: 2 }}>{roleError}</Alert>}
          {rolesLoading ? (
            <Stack direction="row" sx={{ alignItems: "center", gap: 1.5 }} role="status">
              <CircularProgress size={22} />
              <Typography>역할과 permission을 불러오는 중입니다.</Typography>
            </Stack>
          ) : roles.length === 0 ? (
            <Alert severity="info">편집할 역할이 없습니다.</Alert>
          ) : (
            <Stack spacing={2.5}>
              <TextField
                select
                fullWidth
                label="역할"
                value={selectedRoleId}
                onChange={(event) => { setSelectedRoleId(event.target.value); setRoleSaved(""); setRoleError(""); }}
              >
                {roles.map((role) => (
                  <MenuItem key={role.id} value={role.id}>
                    {role.display_name || role.name} · {role.scope_type}
                  </MenuItem>
                ))}
              </TextField>

              {selectedRole && (
                <>
                  <Stack direction="row" sx={{ alignItems: "center", flexWrap: "wrap", gap: 1 }}>
                    <Typography variant="subtitle1">{selectedRole.display_name || selectedRole.name}</Typography>
                    <Chip size="small" label={`revision ${selectedRole.revision}`} />
                    {selectedRole.built_in && <Chip size="small" color="primary" variant="outlined" label="기본 역할" />}
                  </Stack>
                  {selectedRole.description && <Typography color="text.secondary">{selectedRole.description}</Typography>}
                  {permissionGroups.map(([resourceType, rows]) => (
                    <Stack key={resourceType} spacing={1}>
                      <Typography variant="subtitle2" color="text.secondary">{resourceType}</Typography>
                      <FormGroup>
                        <Grid container spacing={1}>
                          {rows.map((permission) => {
                            const requiredSystemPermission = selectedRole.name === "system_admin" && permission.name === "manage_system";
                            return (
                              <Grid key={permission.name} size={{ xs: 12, md: 6 }}>
                                <FormControlLabel
                                  sx={{ alignItems: "flex-start", m: 0 }}
                                  control={(
                                    <Checkbox
                                      checked={selectedRole.permissions.includes(permission.name)}
                                      disabled={requiredSystemPermission}
                                      onChange={(event) => toggleRolePermission(permission.name, event.target.checked)}
                                    />
                                  )}
                                  label={(
                                    <Stack sx={{ py: 0.75 }}>
                                      <Typography variant="body2" component="span">{permission.name}</Typography>
                                      <Typography variant="caption" color="text.secondary" component="span">
                                        {permission.description || "설명 없음"}
                                      </Typography>
                                    </Stack>
                                  )}
                                />
                              </Grid>
                            );
                          })}
                        </Grid>
                      </FormGroup>
                    </Stack>
                  ))}
                  <SaveBar saving={roleSaving} saved={roleSaved} onSave={saveRole} label="역할 권한 저장" />
                </>
              )}
            </Stack>
          )}
        </SettingsCard>
        )}
    </SettingsPage>
  );
}
