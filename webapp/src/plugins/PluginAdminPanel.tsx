import { Fragment, useCallback, useEffect, useMemo, useState } from "react";

import {
  adminApi,
  type AdminPlugin,
  type AdminPluginStatus,
} from "@/api/client";
import { useConfirm } from "@/components/shared";
import { localizePluginAdminSchema, usePluginRegistryState } from "./registry";
import { PluginSurface } from "./PluginSurface";
import { notifyPluginRuntimeChanged, unloadPlugin } from "./runtime";

type PluginSettingProps = {
  id: string;
  value: unknown;
  disabled: boolean;
  setByEnv: boolean;
  onChange: (id: string, value: unknown) => void;
  setSaveNeeded: () => void;
};

type PluginNotice = {
  tone: "ok" | "danger";
  text: string;
};

type PluginSchemaOption = {
  display_name?: string;
  value: unknown;
};

type PluginSchemaSetting = {
  key: string;
  display_name?: string;
  type: string;
  section_key?: string;
  section_title?: string;
  secret?: boolean;
  help_text?: string;
  placeholder?: string;
  default?: unknown;
  options?: PluginSchemaOption[];
};

type PluginSettingsSchema = {
  header?: string;
  footer?: string;
  settings: PluginSchemaSetting[];
};

const SUPPORTED_SCHEMA_SETTING_TYPES = new Set([
  "bool",
  "boolean",
  "text",
  "longtext",
  "textarea",
  "number",
  "select",
  "dropdown",
  "radio",
  "password",
]);

type PluginAdminPanelProps = {
  token: string;
  plugins: AdminPlugin[];
  statuses: AdminPluginStatus[];
  runtimeManagementEnabled: boolean;
  onRefresh: () => void | Promise<void>;
  onError: (message: string | null) => void;
};

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function normalizeSettingsSchema(value: Record<string, unknown> | undefined): PluginSettingsSchema {
  const rawSettings: Array<{ candidate: unknown; sectionKey?: string; sectionTitle?: string }> = [
    ...(Array.isArray(value?.settings) ? value.settings.map((candidate) => ({ candidate })) : []),
  ];
  if (Array.isArray(value?.sections)) {
    for (const candidateSection of value.sections) {
      if (!candidateSection || typeof candidateSection !== "object") continue;
      const section = candidateSection as Record<string, unknown>;
      if (!Array.isArray(section.settings)) continue;
      rawSettings.push(...section.settings.map((candidate) => ({
        candidate,
        sectionKey: typeof section.key === "string" ? section.key : undefined,
        sectionTitle: typeof section.title === "string" ? section.title : undefined,
      })));
    }
  }
  const settings = rawSettings.flatMap(({ candidate, sectionKey, sectionTitle }): PluginSchemaSetting[] => {
    if (!candidate || typeof candidate !== "object") return [];
    const raw = candidate as Record<string, unknown>;
    if (typeof raw.key !== "string" || typeof raw.type !== "string") return [];
    const options = Array.isArray(raw.options)
      ? raw.options.flatMap((option): PluginSchemaOption[] => {
        if (option && typeof option === "object" && "value" in option) {
          const item = option as Record<string, unknown>;
          return [{
            value: item.value,
            display_name: typeof item.display_name === "string" ? item.display_name : undefined,
          }];
        }
        return [{ value: option }];
      })
      : undefined;
    return [{
      key: raw.key,
      type: raw.secret === true ? "password" : raw.type.toLowerCase(),
      section_key: sectionKey,
      section_title: sectionTitle,
      secret: raw.secret === true || raw.type.toLowerCase() === "password",
      display_name: typeof raw.display_name === "string" ? raw.display_name : undefined,
      help_text: typeof raw.help_text === "string" ? raw.help_text : undefined,
      placeholder: typeof raw.placeholder === "string" ? raw.placeholder : undefined,
      default: raw.default,
      options,
    }];
  });
  return {
    header: typeof value?.header === "string" ? value.header : undefined,
    footer: typeof value?.footer === "string" ? value.footer : undefined,
    settings,
  };
}

function SchemaSettingField({
  setting,
  inputID,
  value,
  disabled,
  onChange,
}: {
  setting: PluginSchemaSetting;
  inputID: string;
  value: unknown;
  disabled: boolean;
  onChange: (value: unknown) => void;
}) {
  const label = setting.display_name || setting.key;
  const current = value ?? setting.default ?? "";
  const [secretDraft, setSecretDraft] = useState("");
  const common = {
    id: inputID,
    "data-plugin-setting-key": setting.key,
    disabled,
    "aria-describedby": setting.help_text ? `${inputID}-help` : undefined,
  };
  let field;
  if (setting.type === "bool" || setting.type === "boolean") {
    field = (
      <label style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
        <input
          {...common}
          type="checkbox"
          checked={current === true || current === "true"}
          onChange={(event) => onChange(event.target.checked)}
        />
        {label}
      </label>
    );
  } else if (["select", "dropdown", "radio"].includes(setting.type) && setting.options?.length) {
    field = (
      <label htmlFor={common.id} style={{ display: "grid", gap: 5 }}>
        <span style={{ fontWeight: 600 }}>{label}</span>
        <select
          {...common}
          className="field-input"
          value={String(current)}
          onChange={(event) => {
            const selected = setting.options?.find((option) => String(option.value) === event.target.value);
            onChange(selected?.value ?? event.target.value);
          }}
        >
          {setting.options.map((option) => (
            <option key={String(option.value)} value={String(option.value)}>
              {option.display_name || String(option.value)}
            </option>
          ))}
        </select>
      </label>
    );
  } else if (setting.type === "longtext" || setting.type === "textarea") {
    field = (
      <label htmlFor={common.id} style={{ display: "grid", gap: 5 }}>
        <span style={{ fontWeight: 600 }}>{label}</span>
        <textarea
          {...common}
          className="field-input"
          rows={4}
          value={String(current)}
          placeholder={setting.placeholder}
          onChange={(event) => onChange(event.target.value)}
        />
      </label>
    );
  } else {
    const inputType = setting.type === "password" ? "password" : setting.type === "number" ? "number" : "text";
    field = (
      <label htmlFor={common.id} style={{ display: "grid", gap: 5 }}>
        <span style={{ fontWeight: 600 }}>{label}</span>
        <input
          {...common}
          className="field-input"
          type={inputType}
          value={setting.secret ? secretDraft : String(current)}
          placeholder={setting.secret && current ? "설정됨 — 변경하려면 입력" : setting.placeholder}
          autoComplete={setting.secret ? "new-password" : undefined}
          onChange={(event) => {
            if (setting.secret) {
              setSecretDraft(event.target.value);
              onChange(event.target.value);
            } else if (inputType !== "number") onChange(event.target.value);
            else onChange(event.target.value === "" ? "" : event.target.valueAsNumber);
          }}
        />
      </label>
    );
  }
  return (
    <div style={{ display: "grid", gap: 4, marginTop: 10 }}>
      {field}
      {setting.help_text && (
        <small id={`${inputID}-help`} style={{ color: "var(--muted)" }}>
          {setting.help_text}
        </small>
      )}
    </div>
  );
}

export function PluginAdminPanel({
  token,
  plugins,
  statuses,
  runtimeManagementEnabled,
  onRefresh,
  onError,
}: PluginAdminPanelProps) {
  const confirmer = useConfirm();
  const { adminConsoleCustomSettings, adminConsolePlugins } = usePluginRegistryState();
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploadInputKey, setUploadInputKey] = useState(0);
  const [replacePlugin, setReplacePlugin] = useState(false);
  const [trustedCodeConfirmed, setTrustedCodeConfirmed] = useState(false);
  const [uploadBusy, setUploadBusy] = useState(false);
  const [operations, setOperations] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState<PluginNotice | null>(null);
  const [configurations, setConfigurations] = useState<Record<string, Record<string, unknown>>>({});
  const [configurationSchemas, setConfigurationSchemas] = useState<Record<string, PluginSettingsSchema>>({});
  const [configurationBusy, setConfigurationBusy] = useState<Record<string, boolean>>({});
  const [configurationErrors, setConfigurationErrors] = useState<Record<string, string>>({});
  const [configurationDirty, setConfigurationDirty] = useState<Record<string, boolean>>({});

  const stateByID = useMemo(() => {
    const next: Record<string, string> = {};
    for (const status of statuses) next[status.plugin_id] = status.state;
    return next;
  }, [statuses]);

  const customSettingsByID = useMemo(() => {
    return adminConsoleCustomSettings.reduce<Record<string, typeof adminConsoleCustomSettings>>(
      (result, setting) => {
        result[setting.pluginId] = [...(result[setting.pluginId] ?? []), setting];
        return result;
      },
      {},
    );
  }, [adminConsoleCustomSettings]);

  const installedPluginIDs = useMemo(
    () => new Set(plugins.map((plugin, index) => String(plugin.id ?? plugin.plugin_id ?? `plugin-${index}`))),
    [plugins],
  );

  const setOperation = useCallback((pluginID: string, operation: string | null) => {
    setOperations((current) => {
      if (operation) return { ...current, [pluginID]: operation };
      const next = { ...current };
      delete next[pluginID];
      return next;
    });
  }, []);

  const clearConfiguration = useCallback((pluginID: string) => {
    setConfigurations((current) => {
      const next = { ...current };
      delete next[pluginID];
      return next;
    });
    setConfigurationErrors((current) => {
      const next = { ...current };
      delete next[pluginID];
      return next;
    });
    setConfigurationSchemas((current) => {
      const next = { ...current };
      delete next[pluginID];
      return next;
    });
    setConfigurationDirty((current) => ({ ...current, [pluginID]: false }));
  }, []);

  const loadPluginConfiguration = useCallback(async (pluginID: string) => {
    setConfigurationBusy((current) => ({ ...current, [pluginID]: true }));
    setConfigurationErrors((current) => {
      const next = { ...current };
      delete next[pluginID];
      return next;
    });
    try {
      const result = await adminApi.getPluginConfiguration(token, pluginID);
      setConfigurations((current) => ({ ...current, [pluginID]: result.configuration ?? {} }));
      setConfigurationSchemas((current) => ({
        ...current,
        [pluginID]: normalizeSettingsSchema(localizePluginAdminSchema(pluginID, result.schema)),
      }));
      setConfigurationDirty((current) => ({ ...current, [pluginID]: false }));
    } catch (error) {
      setConfigurationErrors((current) => ({ ...current, [pluginID]: errorMessage(error) }));
    } finally {
      setConfigurationBusy((current) => ({ ...current, [pluginID]: false }));
    }
  }, [adminConsolePlugins, token]);

  useEffect(() => {
    for (const pluginID of installedPluginIDs) {
      if (
        installedPluginIDs.has(pluginID) &&
        configurations[pluginID] === undefined &&
        !configurationBusy[pluginID] &&
        !configurationErrors[pluginID]
      ) {
        void loadPluginConfiguration(pluginID);
      }
    }
  }, [
    configurationBusy,
    configurationErrors,
    configurations,
    customSettingsByID,
    installedPluginIDs,
    loadPluginConfiguration,
  ]);

  const uploadPlugin = async () => {
    if (!uploadFile) return;
    if (!uploadFile.name.toLowerCase().endsWith(".tar.gz")) {
      setNotice({ tone: "danger", text: "Mattermost 플러그인 .tar.gz 파일을 선택해 주세요." });
      return;
    }
    if (!trustedCodeConfirmed) {
      setNotice({ tone: "danger", text: "업로드 전에 신뢰하는 코드이며 서명 미검증 상태로 실행됨을 확인해 주세요." });
      return;
    }
    setTrustedCodeConfirmed(false);
    setUploadBusy(true);
    setNotice(null);
    onError(null);
    try {
      const result = await adminApi.uploadPlugin(token, uploadFile, replacePlugin);
      // A replacement keeps the same ID. Explicitly unload the old instance so
      // PluginLoader does not mistake it for an already-current bundle.
      await unloadPlugin(result.id);
      clearConfiguration(result.id);
      notifyPluginRuntimeChanged();
      setUploadFile(null);
      setUploadInputKey((current) => current + 1);
      setNotice({
        tone: "ok",
        text: `${result.id} v${result.version} 플러그인을 ${result.replaced ? "교체" : "설치"}했습니다.`,
      });
      await onRefresh();
    } catch (error) {
      const message = errorMessage(error);
      setNotice({ tone: "danger", text: message });
      onError(message);
    } finally {
      setUploadBusy(false);
    }
  };

  const togglePlugin = async (pluginID: string, enabled: boolean) => {
    setOperation(pluginID, enabled ? "비활성화 중…" : "활성화 중…");
    setNotice(null);
    onError(null);
    try {
      if (enabled) {
        await adminApi.disablePlugin(token, pluginID);
        await unloadPlugin(pluginID);
      } else {
        await adminApi.enablePlugin(token, pluginID);
      }
      notifyPluginRuntimeChanged();
      setNotice({ tone: "ok", text: `${pluginID} 플러그인을 ${enabled ? "비활성화" : "활성화"}했습니다.` });
      await onRefresh();
    } catch (error) {
      const message = errorMessage(error);
      setNotice({ tone: "danger", text: message });
      onError(message);
    } finally {
      setOperation(pluginID, null);
    }
  };

  const deletePlugin = async (pluginID: string) => {
    const confirmed = await confirmer.confirm({
      title: "플러그인 삭제",
      message: `${pluginID} 플러그인을 서버에서 삭제합니다. 계속할까요?`,
      confirmLabel: "삭제",
      destructive: true,
    });
    if (!confirmed) return;
    setOperation(pluginID, "삭제 중…");
    setNotice(null);
    onError(null);
    try {
      await adminApi.deletePlugin(token, pluginID);
      await unloadPlugin(pluginID);
      clearConfiguration(pluginID);
      notifyPluginRuntimeChanged();
      setNotice({ tone: "ok", text: `${pluginID} 플러그인을 삭제했습니다.` });
      await onRefresh();
    } catch (error) {
      const message = errorMessage(error);
      setNotice({ tone: "danger", text: message });
      onError(message);
    } finally {
      setOperation(pluginID, null);
    }
  };

  const changePluginSetting = (
    pluginID: string,
    fallbackKey: string,
    reportedKey: string | undefined,
    value: unknown,
  ) => {
    const key = reportedKey?.trim() || fallbackKey;
    setConfigurations((current) => ({
      ...current,
      [pluginID]: { ...(current[pluginID] ?? {}), [key]: value },
    }));
    setConfigurationDirty((current) => ({ ...current, [pluginID]: true }));
  };

  const savePluginConfiguration = async (pluginID: string) => {
    const configuration = configurations[pluginID];
    if (!configuration) return;
    setConfigurationBusy((current) => ({ ...current, [pluginID]: true }));
    setConfigurationErrors((current) => {
      const next = { ...current };
      delete next[pluginID];
      return next;
    });
    setNotice(null);
    onError(null);
    try {
      await adminApi.updatePluginConfiguration(token, pluginID, configuration);
      setConfigurationDirty((current) => ({ ...current, [pluginID]: false }));
      setNotice({ tone: "ok", text: `${pluginID} 플러그인 설정을 저장했습니다.` });
    } catch (error) {
      const message = errorMessage(error);
      setConfigurationErrors((current) => ({ ...current, [pluginID]: message }));
      onError(message);
    } finally {
      setConfigurationBusy((current) => ({ ...current, [pluginID]: false }));
    }
  };

  return (
    <div className="integrations-body">
      <div className="integrations-create admin-toolbar" style={{ alignItems: "center", flexWrap: "wrap" }}>
        <input
          key={uploadInputKey}
          className="field-input"
          type="file"
          accept=".tar.gz,application/gzip"
          aria-label="플러그인 tar.gz 선택"
          onChange={(event) => {
            setUploadFile(event.target.files?.[0] ?? null);
            setTrustedCodeConfirmed(false);
          }}
          disabled={uploadBusy || !runtimeManagementEnabled}
          style={{ flex: "1 1 260px" }}
        />
        <label style={{ display: "inline-flex", alignItems: "center", gap: 6, fontSize: 13 }}>
          <input
            type="checkbox"
            aria-label="동일 ID 플러그인 교체 허용"
            checked={replacePlugin}
            onChange={(event) => setReplacePlugin(event.target.checked)}
            disabled={uploadBusy || !runtimeManagementEnabled}
          />
          같은 ID 교체 허용
        </label>
        <label style={{ display: "inline-flex", alignItems: "flex-start", gap: 6, flex: "1 0 100%", fontSize: 13 }}>
          <input
            type="checkbox"
            aria-label="신뢰 코드 및 서명 미검증 실행 확인"
            checked={trustedCodeConfirmed}
            onChange={(event) => setTrustedCodeConfirmed(event.target.checked)}
            disabled={uploadBusy || !runtimeManagementEnabled || !uploadFile}
          />
          이 아카이브가 신뢰하는 코드이며, 서명 검증 없이 서버 프로세스 권한으로 실행될 수 있음을 확인했습니다.
        </label>
        <button
          type="button"
          className="btn-primary"
          style={{ width: "auto", padding: "0 14px", height: 34 }}
          onClick={() => void uploadPlugin()}
          disabled={!uploadFile || !trustedCodeConfirmed || uploadBusy || !runtimeManagementEnabled}
        >{uploadBusy ? "업로드 중…" : replacePlugin ? "업로드 및 교체" : "플러그인 업로드"}</button>
        <button
          type="button"
          className="btn-ghost"
          style={{ width: "auto", padding: "0 12px", height: 34 }}
          onClick={() => void onRefresh()}
          disabled={uploadBusy}
        >상태 새로고침</button>
      </div>
      {!runtimeManagementEnabled && (
        <div className="chat-empty" style={{ padding: "8px 12px" }}>
          서버에서 플러그인 업로드/런타임 관리가 비활성화되어 있습니다.
        </div>
      )}
      {notice && (
        <div
          role="status"
          className={notice.tone === "danger" ? "login-error" : "reveal-card"}
          style={{ margin: "0 0 12px" }}
        >{notice.text}</div>
      )}
      <ul className="integrations-list">
        {plugins.length === 0 && (
          <li className="chat-empty" style={{ padding: 12 }}>로드된 플러그인이 없습니다.</li>
        )}
        {plugins.map((plugin, index) => {
          const pluginID = String(plugin.id ?? plugin.plugin_id ?? `plugin-${index}`);
          const state = stateByID[pluginID] ?? String(plugin.state ?? "unknown");
          const enabled = typeof plugin.enabled === "boolean"
            ? plugin.enabled
            : state === "running" || state === "enabled";
          const failedButEnabled = enabled && state !== "running" && state !== "enabled";
          const operation = operations[pluginID];
          const customSettings = customSettingsByID[pluginID] ?? [];
          const configuration = configurations[pluginID];
          const schema = configurationSchemas[pluginID] ?? { settings: [] };
          const customSettingKeys = new Set(customSettings.map((setting) => setting.key));
          const standardSettings = schema.settings.filter((setting) => (
            SUPPORTED_SCHEMA_SETTING_TYPES.has(setting.type) && !customSettingKeys.has(setting.key)
          ));
          const configBusy = configurationBusy[pluginID] === true;
          const configError = configurationErrors[pluginID];
          return (
            <li key={pluginID} className="integrations-row" style={{ display: "block" }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                    {String(plugin.name ?? pluginID)}
                    <span className={failedButEnabled ? "admin-pill danger" : enabled ? "admin-pill ok" : "admin-pill"}>
                      {operation ?? (failedButEnabled ? `${state} · 활성화됨` : state)}
                    </span>
                  </div>
                  <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                    {pluginID} · v{String(plugin.version ?? "dev")}
                    {plugin.runtime ? ` · ${String(plugin.runtime)}` : ""}
                  </div>
                  {plugin.error && (
                    <div style={{ color: "var(--danger)", fontSize: 12, marginTop: 4 }}>
                      {String(plugin.error)}
                    </div>
                  )}
                </div>
                <button
                  type="button"
                  className="btn-ghost"
                  style={{ width: "auto", padding: "0 10px", height: 30 }}
                  onClick={() => void togglePlugin(pluginID, enabled)}
                  disabled={!runtimeManagementEnabled || Boolean(operation)}
                >{enabled ? "비활성화" : "활성화"}</button>
                <button
                  type="button"
                  className="btn-ghost"
                  style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                  onClick={() => void deletePlugin(pluginID)}
                  disabled={!runtimeManagementEnabled || Boolean(operation)}
                >삭제</button>
              </div>
              {(configBusy || Boolean(configError) || customSettings.length > 0 || standardSettings.length > 0) && (
                <section
                  aria-label={`${pluginID} 설정`}
                  style={{ borderTop: "1px solid var(--border)", marginTop: 12, paddingTop: 12 }}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 10 }}>
                    <strong style={{ flex: 1 }}>플러그인 설정</strong>
                    <button
                      type="button"
                      className="btn-ghost"
                      style={{ width: "auto", padding: "0 10px", height: 30 }}
                      onClick={() => void loadPluginConfiguration(pluginID)}
                      disabled={configBusy}
                    >다시 불러오기</button>
                    <button
                      type="button"
                      className="btn-primary"
                      style={{ width: "auto", padding: "0 12px", height: 30 }}
                      onClick={() => void savePluginConfiguration(pluginID)}
                      disabled={configBusy || !configurationDirty[pluginID] || !configuration}
                    >{configBusy ? "처리 중…" : "설정 저장"}</button>
                  </div>
                  {configError && <div className="login-error" style={{ marginBottom: 10 }}>{configError}</div>}
                  {!configuration && !configError && (
                    <div className="chat-empty" style={{ padding: 8 }}>설정을 불러오는 중…</div>
                  )}
                  {configuration && schema.header && (
                    <div style={{ color: "var(--muted)", fontSize: 13, marginBottom: 10 }}>{schema.header}</div>
                  )}
                  {configuration && customSettings.map((setting) => (
                    <div key={setting.id} style={{ marginTop: 8 }}>
                      <PluginSurface
                        component={setting.component}
                        label={`${pluginID} admin setting`}
                        componentProps={{
                          id: setting.key,
                          value: configuration[setting.key],
                          disabled: configBusy,
                          setByEnv: false,
                          onChange: (id: string, value: unknown) => changePluginSetting(pluginID, setting.key, id, value),
                          setSaveNeeded: () => setConfigurationDirty((current) => ({ ...current, [pluginID]: true })),
                        } satisfies PluginSettingProps}
                      />
                    </div>
                  ))}
                  {configuration && standardSettings.map((setting, settingIndex) => {
                    const previous = standardSettings[settingIndex - 1];
                    const showSection = Boolean(
                      setting.section_title && setting.section_key !== previous?.section_key,
                    );
                    return (
                      <Fragment key={`${setting.section_key ?? "root"}-${setting.key}`}>
                        {showSection && (
                          <h4 style={{ margin: "18px 0 4px", fontSize: 15 }}>{setting.section_title}</h4>
                        )}
                        <SchemaSettingField
                          setting={setting}
                          inputID={`plugin-setting-${pluginID}-${setting.key}`}
                          value={configuration[setting.key]}
                          disabled={configBusy}
                          onChange={(value) => changePluginSetting(pluginID, setting.key, setting.key, value)}
                        />
                      </Fragment>
                    );
                  })}
                  {configuration && schema.footer && (
                    <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 10 }}>{schema.footer}</div>
                  )}
                </section>
              )}
            </li>
          );
        })}
      </ul>
      {confirmer.render()}
    </div>
  );
}
