// @vitest-environment jsdom
import { act, type ComponentProps, type ComponentType } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { adminApi } from "@/api/client";
import { PluginAdminSettingsPanel } from "./PluginAdminSettingsPanel";
import { createRegistry } from "./registry";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const match = [...container.querySelectorAll("button")].find((node) => node.textContent === label);
  if (!match) throw new Error(`button not found: ${label}`);
  return match;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("PluginAdminSettingsPanel", () => {
  let container: HTMLDivElement;
  let root: Root;
  const registries: Array<ReturnType<typeof createRegistry>> = [];
  const baseProps: ComponentProps<typeof PluginAdminSettingsPanel> = {
    token: "admin-token",
    pluginID: "com.mattermost.botman",
    enabled: true,
  };

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      for (const registry of registries.splice(0)) registry.unregisterAll();
      root.unmount();
    });
    container.remove();
    vi.restoreAllMocks();
  });

  it("renders a Botman custom setting and saves its changed value", async () => {
    const getConfiguration = vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: { Config: "old" },
      schema: { settings: [{ key: "Config", type: "custom", display_name: "Fallback Config" }] },
    });
    const updateConfiguration = vi.spyOn(adminApi, "updatePluginConfiguration").mockResolvedValue({ status: "OK" });
    const registry = createRegistry(baseProps.pluginID);
    registries.push(registry);
    const BotmanConfig: ComponentType<Record<string, unknown>> = (props) => {
      const onChange = props.onChange as (id: string, value: unknown) => void;
      return <button type="button" onClick={() => onChange("Config", "updated")}>Botman Config</button>;
    };
    registry.registerAdminConsoleCustomSetting("Config", BotmanConfig);

    await act(async () => {
      root.render(<PluginAdminSettingsPanel {...baseProps} />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(getConfiguration).toHaveBeenCalledOnce();
    expect(getConfiguration).toHaveBeenCalledWith("admin-token", baseProps.pluginID);
    expect(container.querySelector('[data-plugin-setting-key="Config"]')).toBeNull();
    await act(async () => button(container, "Botman Config").click());
    await act(async () => button(container, "설정 저장").click());

    expect(updateConfiguration).toHaveBeenCalledWith("admin-token", baseProps.pluginID, { Config: "updated" });
    expect(container.textContent).toContain("플러그인 설정을 저장했습니다.");
  });

  it("edits the standard schema types used by Chatdump", async () => {
    const pluginID = "com.hkjang.mattermost-chatdump-plugin";
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
      root.render(<PluginAdminSettingsPanel {...baseProps} pluginID={pluginID} />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

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
    expect(secret?.value).toBe("");
    expect(container.innerHTML).not.toContain("stored");
    await act(async () => enabled?.click());
    if (!weeks) throw new Error("MaxWeeks setting not found");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(weeks, "8");
      weeks.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => button(container, "설정 저장").click());

    expect(update).toHaveBeenCalledWith("admin-token", pluginID, expect.objectContaining({
      EnableExport: false,
      MaxWeeks: "8",
    }));
  });

  it("re-localizes nested EchoSummary sections without exposing a configured secret", async () => {
    const pluginID = "com.mattermost.echosummary";
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
      root.render(<PluginAdminSettingsPanel {...baseProps} pluginID={pluginID} />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(container.textContent).toContain("Echo settings");

    const registry = createRegistry(pluginID);
    registries.push(registry);
    await act(async () => registry.registerAdminConsolePlugin((schema) => {
      schema.header = "한국어 Echo Summary 설정";
      const sections = schema.sections as Array<Record<string, unknown>>;
      sections[0].title = "vLLM 요약 설정";
      const settings = sections[0].settings as Array<Record<string, unknown>>;
      settings[0].display_name = "vLLM API 키";
    }));

    expect(container.textContent).toContain("한국어 Echo Summary 설정");
    expect(container.textContent).toContain("vLLM 요약 설정");
    expect(container.textContent).toContain("vLLM API 키");
    const secret = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="VLLMAPIKey"]');
    expect(secret?.type).toBe("password");
    expect(secret?.value).toBe("");
    expect(secret?.placeholder).toContain("설정됨");
    expect(container.innerHTML).not.toContain("server-secret");
  });

  it("clears a secret draft after saving it", async () => {
    const pluginID = "com.mattermost.echosummary";
    vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: { VLLMAPIKey: "server-secret" },
      schema: { settings: [{ key: "VLLMAPIKey", display_name: "API key", type: "password" }] },
    });
    const update = vi.spyOn(adminApi, "updatePluginConfiguration").mockResolvedValue({ status: "OK" });

    await act(async () => {
      root.render(<PluginAdminSettingsPanel {...baseProps} pluginID={pluginID} />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const secret = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="VLLMAPIKey"]');
    if (!secret) throw new Error("VLLMAPIKey setting not found");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(secret, "replacement-secret");
      secret.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(secret.value).toBe("replacement-secret");
    await act(async () => button(container, "설정 저장").click());

    expect(update).toHaveBeenCalledWith("admin-token", pluginID, { VLLMAPIKey: "replacement-secret" });
    expect(secret.value).toBe("");
    expect(container.innerHTML).not.toContain("replacement-secret");
  });

  it("ignores a stale configuration response after switching plugins", async () => {
    const first = deferred<Awaited<ReturnType<typeof adminApi.getPluginConfiguration>>>();
    const second = deferred<Awaited<ReturnType<typeof adminApi.getPluginConfiguration>>>();
    vi.spyOn(adminApi, "getPluginConfiguration").mockImplementation((_token, pluginID) => (
      pluginID === "plugin-a" ? first.promise : second.promise
    ));

    await act(async () => {
      root.render(<PluginAdminSettingsPanel {...baseProps} pluginID="plugin-a" />);
      await Promise.resolve();
    });
    await act(async () => {
      root.render(<PluginAdminSettingsPanel {...baseProps} pluginID="plugin-b" />);
      await Promise.resolve();
    });
    await act(async () => {
      second.resolve({
        configuration: { Name: "plugin-b-value" },
        schema: { settings: [{ key: "Name", type: "text" }] },
      });
      await second.promise;
    });
    await act(async () => {
      first.resolve({
        configuration: { Name: "stale-plugin-a-value" },
        schema: { settings: [{ key: "Name", type: "text" }] },
      });
      await first.promise;
    });

    const name = container.querySelector<HTMLInputElement>('[data-plugin-setting-key="Name"]');
    expect(name?.value).toBe("plugin-b-value");
    expect(container.innerHTML).not.toContain("stale-plugin-a-value");
  });

  it("explains when a disabled plugin custom setting bundle is unavailable", async () => {
    vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: { Config: "value" },
      schema: { settings: [{ key: "Config", type: "custom" }] },
    });

    await act(async () => {
      root.render(<PluginAdminSettingsPanel {...baseProps} enabled={false} />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.textContent).toContain("활성화하면 웹 번들이 제공하는 사용자 지정 설정을 편집할 수 있습니다.");
  });

  it("reports schema setting types that Moyro cannot edit yet", async () => {
    vi.spyOn(adminApi, "getPluginConfiguration").mockResolvedValue({
      configuration: { Owner: "admin" },
      schema: { settings: [{ key: "Owner", display_name: "Owner account", type: "username" }] },
    });

    await act(async () => {
      root.render(<PluginAdminSettingsPanel {...baseProps} />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.textContent).toContain("편집할 수 없는 설정 형식");
    expect(container.textContent).toContain("Owner account");
    expect(container.textContent).not.toContain("관리자 설정 항목을 제공하지 않습니다.");
  });
});
