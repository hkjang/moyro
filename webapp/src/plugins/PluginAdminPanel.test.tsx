// @vitest-environment jsdom
import { act, type ComponentProps, type ComponentType } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { adminApi } from "@/api/client";
import { PluginAdminPanel } from "./PluginAdminPanel";
import { createRegistry } from "./registry";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const plugin = {
  id: "com.mattermost.botman",
  name: "Botman",
  version: "1.0.0",
  state: "running",
  enabled: true,
};

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const match = [...container.querySelectorAll("button")].find((node) => node.textContent === label);
  if (!match) throw new Error(`button not found: ${label}`);
  return match;
}

describe("PluginAdminPanel", () => {
  let container: HTMLDivElement;
  let root: Root;
  const registries: Array<ReturnType<typeof createRegistry>> = [];
  const baseProps: ComponentProps<typeof PluginAdminPanel> = {
    token: "admin-token",
    plugins: [plugin],
    statuses: [{ plugin_id: plugin.id, state: "running" }],
    runtimeManagementEnabled: true,
    onRefresh: vi.fn(),
    onError: vi.fn(),
  };

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    for (const registry of registries.splice(0)) registry.unregisterAll();
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("renders a registered Botman custom setting and saves its changed value", async () => {
    const getConfiguration = vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: { Config: "old" },
      schema: { settings: [{ key: "Config", type: "text", display_name: "Fallback Config" }] },
    });
    const updateConfiguration = vi.spyOn(adminApi, "updatePluginConfiguration").mockResolvedValue({ status: "OK" });
    const registry = createRegistry(plugin.id);
    registries.push(registry);
    const BotmanConfig: ComponentType<Record<string, unknown>> = (props) => {
      const onChange = props.onChange as (id: string, value: unknown) => void;
      return <button type="button" onClick={() => onChange("Config", "updated")}>Botman Config</button>;
    };
    registry.registerAdminConsoleCustomSetting("Config", BotmanConfig);

    await act(async () => {
      root.render(<PluginAdminPanel {...baseProps} />);
      await Promise.resolve();
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(getConfiguration).toHaveBeenCalledWith("admin-token", plugin.id);
    expect(container.querySelector('[data-plugin-setting-key="Config"]')).toBeNull();
    await act(async () => button(container, "Botman Config").click());
    await act(async () => button(container, "설정 저장").click());

    expect(updateConfiguration).toHaveBeenCalledWith("admin-token", plugin.id, { Config: "updated" });
  });

  it("uploads a tar.gz with replacement enabled and refreshes the plugin list", async () => {
    const upload = vi.spyOn(adminApi, "uploadPlugin").mockResolvedValue({
      id: plugin.id,
      version: "2.0.0",
      state: "running",
      enabled: true,
      runtime: "mattermost_v1",
      sha256: "abc",
      replaced: true,
    });
    const onRefresh = vi.fn();
    await act(async () => root.render(<PluginAdminPanel {...baseProps} onRefresh={onRefresh} />));

    const input = container.querySelector<HTMLInputElement>('input[type="file"]');
    if (!input) throw new Error("upload input not found");
    const archive = new File(["plugin"], "botman.tar.gz", { type: "application/gzip" });
    Object.defineProperty(input, "files", { configurable: true, value: [archive] });
    await act(async () => input.dispatchEvent(new Event("change", { bubbles: true })));
    expect(button(container, "플러그인 업로드").disabled).toBe(true);
    const replace = container.querySelector<HTMLInputElement>('input[aria-label="동일 ID 플러그인 교체 허용"]');
    if (!replace) throw new Error("replace checkbox not found");
    await act(async () => replace.click());
    expect(button(container, "업로드 및 교체").disabled).toBe(true);
    const trust = container.querySelector<HTMLInputElement>('input[aria-label="신뢰 코드 및 서명 미검증 실행 확인"]');
    if (!trust) throw new Error("trusted-code checkbox not found");
    await act(async () => trust.click());
    await act(async () => button(container, "업로드 및 교체").click());

    expect(upload).toHaveBeenCalledWith("admin-token", archive, true);
    expect(onRefresh).toHaveBeenCalledOnce();
    expect(container.textContent).toContain(`${plugin.id} v2.0.0 플러그인을 교체했습니다.`);
  });

  it("uses plugin.enabled before failed runtime state and edits standard schema settings", async () => {
    const failedPlugin = {
      id: "com.hkjang.mattermost-chatdump-plugin",
      name: "Chatdump",
      version: "1.0.0",
      state: "failed",
      enabled: true,
    };
    vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: {
        EnableExport: true,
        MaxWeeks: "12",
        Limit: 10,
        Format: "json",
        Secret: "stored",
      },
      schema: {
        header: "Export policy",
        settings: [
          { key: "EnableExport", display_name: "Enable Chat Export", type: "bool" },
          { key: "MaxWeeks", display_name: "Maximum Weeks", type: "text" },
          { key: "Limit", display_name: "Limit", type: "number" },
          { key: "Format", display_name: "Format", type: "select", options: [
            { display_name: "JSON", value: "json" },
            { display_name: "CSV", value: "csv" },
          ] },
          { key: "Secret", display_name: "Secret", type: "password" },
        ],
      },
    });
    const update = vi.spyOn(adminApi, "updatePluginConfiguration").mockResolvedValue({ status: "OK" });

    await act(async () => {
      root.render(
        <PluginAdminPanel
          {...baseProps}
          plugins={[failedPlugin]}
          statuses={[{ plugin_id: failedPlugin.id, state: "failed" }]}
        />,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.textContent).toContain("failed · 활성화됨");
    expect(button(container, "비활성화")).toBeTruthy();
    const enabled = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="EnableExport"]');
    const weeks = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="MaxWeeks"]');
    const limit = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="Limit"]');
    const format = container.querySelector<HTMLSelectElement>('[data-plugin-setting-key="Format"]');
    const secret = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="Secret"]');
    expect(enabled?.checked).toBe(true);
    expect(weeks?.type).toBe("text");
    expect(limit?.type).toBe("number");
    expect(format?.value).toBe("json");
    expect(secret?.type).toBe("password");
    await act(async () => enabled?.click());
    if (!weeks) throw new Error("MaxWeeks setting not found");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(weeks, "8");
      weeks.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => button(container, "설정 저장").click());

    expect(update).toHaveBeenCalledWith("admin-token", failedPlugin.id, expect.objectContaining({
      EnableExport: false,
      MaxWeeks: "8",
    }));
  });

  it("renders nested EchoSummary sections, localizes labels, and never exposes a configured secret", async () => {
    const echo = {
      id: "com.mattermost.echosummary",
      name: "Echo Summary",
      version: "0.6.5",
      state: "running",
      enabled: true,
    };
    const registry = createRegistry(echo.id);
    registries.push(registry);
    registry.registerAdminConsolePlugin((schema) => {
      schema.header = "한국어 Echo Summary 설정";
      const sections = schema.sections as Array<Record<string, unknown>>;
      sections[0].title = "vLLM 요약 설정";
      const settings = sections[0].settings as Array<Record<string, unknown>>;
      settings[0].display_name = "vLLM API 키";
    });
    vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: { VLLMAPIKey: "server-secret", VLLMModel: "qwen" },
      schema: {
        header: "Echo settings",
        sections: [{
          key: "vllm",
          title: "vLLM summary settings",
          settings: [
            { key: "VLLMAPIKey", display_name: "API key", type: "text", secret: true },
            { key: "VLLMModel", display_name: "Model", type: "text" },
          ],
        }],
      },
    });

    await act(async () => {
      root.render(
        <PluginAdminPanel
          {...baseProps}
          plugins={[echo]}
          statuses={[{ plugin_id: echo.id, state: "running" }]}
        />,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.textContent).toContain("한국어 Echo Summary 설정");
    expect(container.textContent).toContain("vLLM 요약 설정");
    expect(container.textContent).toContain("vLLM API 키");
    const secret = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="VLLMAPIKey"]');
    expect(secret?.type).toBe("password");
    expect(secret?.value).toBe("");
    expect(secret?.placeholder).toContain("설정됨");
    expect(container.innerHTML).not.toContain("server-secret");
  });
});
