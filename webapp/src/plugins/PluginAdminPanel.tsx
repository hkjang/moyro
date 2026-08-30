import { useCallback, useMemo, useState } from "react";

import {
  adminApi,
  type AdminPlugin,
  type AdminPluginStatus,
} from "@/api/client";
import { useConfirm } from "@/components/shared";
import {
  adminPluginDisplayName,
  adminPluginID,
  adminPluginVersion,
} from "@/features/admin/adminPluginIdentity";
import { notifyPluginRuntimeChanged, unloadPlugin } from "./runtime";

type PluginNotice = {
  tone: "ok" | "danger";
  text: string;
};

type PluginAdminPanelProps = {
  token: string;
  plugins: AdminPlugin[];
  statuses: AdminPluginStatus[];
  runtimeManagementEnabled: boolean;
  onRefresh: () => void | Promise<void>;
  onError: (message: string | null) => void;
  onOpenSettings?: (pluginID: string) => void;
};

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function PluginAdminPanel({
  token,
  plugins,
  statuses,
  runtimeManagementEnabled,
  onRefresh,
  onError,
  onOpenSettings,
}: PluginAdminPanelProps) {
  const confirmer = useConfirm();
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploadInputKey, setUploadInputKey] = useState(0);
  const [replacePlugin, setReplacePlugin] = useState(false);
  const [trustedCodeConfirmed, setTrustedCodeConfirmed] = useState(false);
  const [uploadBusy, setUploadBusy] = useState(false);
  const [operations, setOperations] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState<PluginNotice | null>(null);

  const stateByID = useMemo(() => {
    const next: Record<string, string> = {};
    for (const status of statuses) next[status.plugin_id] = status.state;
    return next;
  }, [statuses]);

  const setOperation = useCallback((pluginID: string, operation: string | null) => {
    setOperations((current) => {
      if (operation) return { ...current, [pluginID]: operation };
      const next = { ...current };
      delete next[pluginID];
      return next;
    });
  }, []);

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
        {plugins.map((plugin) => {
          const pluginID = adminPluginID(plugin);
          if (!pluginID) return null;
          const state = stateByID[pluginID] ?? String(plugin.state ?? "unknown");
          const enabled = typeof plugin.enabled === "boolean"
            ? plugin.enabled
            : state === "running" || state === "enabled";
          const failedButEnabled = enabled && state !== "running" && state !== "enabled";
          const operation = operations[pluginID];
          return (
            <li key={pluginID} className="integrations-row" style={{ display: "block" }}>
              <div className="plugin-management-row-main">
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 600 }}>
                    {adminPluginDisplayName(plugin)}
                    <span className={failedButEnabled ? "admin-pill danger" : enabled ? "admin-pill ok" : "admin-pill"}>
                      {operation ?? (failedButEnabled ? `${state} · 활성화됨` : state)}
                    </span>
                  </div>
                  <div style={{ color: "var(--muted)", fontSize: 13, marginTop: 2 }}>
                    {pluginID} · v{adminPluginVersion(plugin)}
                    {plugin.runtime ? ` · ${String(plugin.runtime)}` : ""}
                  </div>
                  {plugin.error && (
                    <div style={{ color: "var(--danger)", fontSize: 12, marginTop: 4 }}>
                      {String(plugin.error)}
                    </div>
                  )}
                </div>
                <div className="plugin-management-row-actions">
                  {onOpenSettings && (
                    <button
                      type="button"
                      className="btn-ghost"
                      onClick={() => onOpenSettings(pluginID)}
                      aria-label={`${adminPluginDisplayName(plugin)} 플러그인 설정 열기`}
                    >설정</button>
                  )}
                  <button
                    type="button"
                    className="btn-ghost"
                    onClick={() => void togglePlugin(pluginID, enabled)}
                    disabled={!runtimeManagementEnabled || Boolean(operation)}
                  >{enabled ? "비활성화" : "활성화"}</button>
                  <button
                    type="button"
                    className="btn-ghost plugin-delete-button"
                    onClick={() => void deletePlugin(pluginID)}
                    disabled={!runtimeManagementEnabled || Boolean(operation)}
                  >삭제</button>
                </div>
              </div>
            </li>
          );
        })}
      </ul>
      {confirmer.render()}
    </div>
  );
}
