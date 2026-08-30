import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Alert, CircularProgress, Stack, Typography } from "@mui/material";

import { adminApi } from "@/api/client";
import { localizePluginAdminSchema, usePluginRegistryState } from "./registry";
import { PluginSurface } from "./PluginSurface";

type PluginSettingProps = {
  id: string;
  value: unknown;
  disabled: boolean;
  setByEnv: boolean;
  onChange: (id: string, value: unknown) => void;
  setSaveNeeded: () => void;
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
  secretResetVersion,
  onChange,
}: {
  setting: PluginSchemaSetting;
  inputID: string;
  value: unknown;
  disabled: boolean;
  secretResetVersion: number;
  onChange: (value: unknown) => void;
}) {
  const label = setting.display_name || setting.key;
  const current = value ?? setting.default ?? "";
  const [secretDraft, setSecretDraft] = useState("");
  useEffect(() => {
    setSecretDraft("");
  }, [secretResetVersion]);
  const common = {
    id: inputID,
    "data-plugin-setting-key": setting.key,
    disabled,
    "aria-describedby": setting.help_text ? `${inputID}-help` : undefined,
  };
  let field;
  if (setting.type === "bool" || setting.type === "boolean") {
    field = (
      <label className="plugin-setting-checkbox">
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
      <label htmlFor={common.id} className="plugin-setting-field-label">
        <span>{label}</span>
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
      <label htmlFor={common.id} className="plugin-setting-field-label">
        <span>{label}</span>
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
      <label htmlFor={common.id} className="plugin-setting-field-label">
        <span>{label}</span>
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
    <div className="plugin-setting-field">
      {field}
      {setting.help_text && <small id={`${inputID}-help`}>{setting.help_text}</small>}
    </div>
  );
}

export function PluginAdminSettingsPanel({
  token,
  pluginID,
  enabled,
  onError,
}: {
  token: string;
  pluginID: string;
  enabled: boolean;
  onError?: (message: string | null) => void;
}) {
  const { adminConsoleCustomSettings, adminConsolePlugins } = usePluginRegistryState();
  const [configuration, setConfiguration] = useState<Record<string, unknown> | null>(null);
  const [rawSchema, setRawSchema] = useState<Record<string, unknown> | undefined>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [secretResetVersion, setSecretResetVersion] = useState(0);
  const requestSequence = useRef(0);

  const customSettings = useMemo(
    () => adminConsoleCustomSettings.filter((setting) => setting.pluginId === pluginID),
    [adminConsoleCustomSettings, pluginID],
  );
  const schema = useMemo(
    () => normalizeSettingsSchema(localizePluginAdminSchema(pluginID, rawSchema)),
    [adminConsolePlugins, pluginID, rawSchema],
  );
  const customSettingKeys = useMemo(
    () => new Set(customSettings.map((setting) => setting.key)),
    [customSettings],
  );
  const standardSettings = useMemo(
    () => schema.settings.filter((setting) => (
      SUPPORTED_SCHEMA_SETTING_TYPES.has(setting.type) && !customSettingKeys.has(setting.key)
    )),
    [customSettingKeys, schema.settings],
  );
  const unavailableCustomSettings = useMemo(
    () => schema.settings.filter((setting) => (
      setting.type === "custom" && !customSettingKeys.has(setting.key)
    )),
    [customSettingKeys, schema.settings],
  );
  const unsupportedSettings = useMemo(
    () => schema.settings.filter((setting) => (
      setting.type !== "custom" && !SUPPORTED_SCHEMA_SETTING_TYPES.has(setting.type)
    )),
    [schema.settings],
  );

  const load = useCallback(async () => {
    const requestID = ++requestSequence.current;
    setLoading(true);
    setError("");
    setSaved(false);
    onError?.(null);
    try {
      const result = await adminApi.getPluginConfiguration(token, pluginID);
      if (requestSequence.current !== requestID) return;
      setConfiguration(result.configuration ?? {});
      setRawSchema(result.schema);
      setDirty(false);
      setSecretResetVersion((current) => current + 1);
    } catch (loadError) {
      if (requestSequence.current !== requestID) return;
      const message = errorMessage(loadError);
      setError(message);
      onError?.(message);
    } finally {
      if (requestSequence.current === requestID) setLoading(false);
    }
  }, [onError, pluginID, token]);

  useEffect(() => {
    setConfiguration(null);
    setRawSchema(undefined);
    setDirty(false);
    setSaving(false);
    void load();
    return () => {
      requestSequence.current += 1;
    };
  }, [load]);

  const changeSetting = (fallbackKey: string, reportedKey: string | undefined, value: unknown) => {
    const key = reportedKey?.trim() || fallbackKey;
    setConfiguration((current) => ({ ...(current ?? {}), [key]: value }));
    setDirty(true);
    setSaved(false);
  };

  const save = async () => {
    if (!configuration) return;
    const requestID = ++requestSequence.current;
    setSaving(true);
    setError("");
    setSaved(false);
    onError?.(null);
    try {
      await adminApi.updatePluginConfiguration(token, pluginID, configuration);
      if (requestSequence.current !== requestID) return;
      setDirty(false);
      setSaved(true);
      setSecretResetVersion((current) => current + 1);
    } catch (saveError) {
      if (requestSequence.current !== requestID) return;
      const message = errorMessage(saveError);
      setError(message);
      onError?.(message);
    } finally {
      if (requestSequence.current === requestID) setSaving(false);
    }
  };

  if (loading && !configuration) {
    return (
      <Stack direction="row" sx={{ alignItems: "center", gap: 1.5 }} role="status">
        <CircularProgress size={22} />
        <Typography>플러그인 설정을 불러오는 중입니다.</Typography>
      </Stack>
    );
  }

  return (
    <Stack spacing={2}>
      <Stack direction="row" className="plugin-settings-actions">
        <Typography variant="body2" color="text.secondary" sx={{ flex: 1 }}>
          변경 사항은 이 플러그인에만 적용됩니다.
        </Typography>
        <button type="button" className="btn-ghost" onClick={() => void load()} disabled={loading || saving}>
          다시 불러오기
        </button>
        <button type="button" className="btn-primary" onClick={() => void save()} disabled={loading || saving || !dirty || !configuration}>
          {saving ? "저장 중…" : "설정 저장"}
        </button>
      </Stack>
      {error && <Alert severity="error">{error}</Alert>}
      {saved && <Alert severity="success">플러그인 설정을 저장했습니다.</Alert>}
      {configuration && schema.header && <Typography variant="body2" color="text.secondary">{schema.header}</Typography>}
      {configuration && customSettings.map((setting) => (
        <div key={setting.id} className="plugin-custom-setting">
          <PluginSurface
            component={setting.component}
            label={`${pluginID} admin setting`}
            componentProps={{
              id: setting.key,
              value: configuration[setting.key],
              disabled: loading || saving,
              setByEnv: false,
              onChange: (id: string, value: unknown) => changeSetting(setting.key, id, value),
              setSaveNeeded: () => {
                setDirty(true);
                setSaved(false);
              },
            } satisfies PluginSettingProps}
          />
        </div>
      ))}
      {configuration && standardSettings.map((setting, settingIndex) => {
        const previous = standardSettings[settingIndex - 1];
        const showSection = Boolean(setting.section_title && setting.section_key !== previous?.section_key);
        return (
          <Fragment key={`${setting.section_key ?? "root"}-${setting.key}`}>
            {showSection && <Typography component="h3" variant="subtitle1">{setting.section_title}</Typography>}
            <SchemaSettingField
              setting={setting}
              inputID={`plugin-setting-${pluginID}-${setting.key}`}
              value={configuration[setting.key]}
              disabled={loading || saving}
              secretResetVersion={secretResetVersion}
              onChange={(value) => changeSetting(setting.key, setting.key, value)}
            />
          </Fragment>
        );
      })}
      {configuration && unavailableCustomSettings.length > 0 && (
        <Alert severity="warning">
          {enabled
            ? "플러그인 웹 화면이 제공하는 일부 사용자 지정 설정을 불러오지 못했습니다. 웹 번들 상태를 확인해 주세요."
            : "이 플러그인을 활성화하면 웹 번들이 제공하는 사용자 지정 설정을 편집할 수 있습니다."}
        </Alert>
      )}
      {configuration && unsupportedSettings.length > 0 && (
        <Alert severity="warning">
          이 Moyro 버전에서 편집할 수 없는 설정 형식이 있습니다: {unsupportedSettings
            .map((setting) => setting.display_name || setting.key)
            .join(", ")}
        </Alert>
      )}
      {configuration && customSettings.length === 0 && standardSettings.length === 0
        && unavailableCustomSettings.length === 0 && unsupportedSettings.length === 0 && (
        <Alert severity="info">이 플러그인은 관리자 설정 항목을 제공하지 않습니다.</Alert>
      )}
      {configuration && schema.footer && <Typography variant="body2" color="text.secondary">{schema.footer}</Typography>}
    </Stack>
  );
}
