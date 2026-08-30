// @vitest-environment jsdom
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { adminApi } from "@/api/client";
import { PluginAdminPanel } from "./PluginAdminPanel";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const plugin = {
  id: "com.mattermost.botman",
  version: "1.0.0",
  state: "running",
  enabled: true,
  manifest: { name: "Botman" },
};

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const match = [...container.querySelectorAll("button")].find((node) => node.textContent === label);
  if (!match) throw new Error(`button not found: ${label}`);
  return match;
}

describe("PluginAdminPanel", () => {
  let container: HTMLDivElement;
  let root: Root;
  const baseProps: ComponentProps<typeof PluginAdminPanel> = {
    token: "admin-token",
    plugins: [plugin],
    statuses: [{ plugin_id: plugin.id, state: "running" }],
    runtimeManagementEnabled: true,
    onRefresh: vi.fn(),
    onError: vi.fn(),
    onOpenSettings: vi.fn(),
  };

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("keeps the management list compact and opens the selected plugin settings", async () => {
    const getConfiguration = vi.spyOn(adminApi, "getPluginConfiguration");
    const onOpenSettings = vi.fn();

    await act(async () => root.render(<PluginAdminPanel {...baseProps} onOpenSettings={onOpenSettings} />));
    await act(async () => button(container, "설정").click());

    expect(container.textContent).toContain("Botman");
    expect(onOpenSettings).toHaveBeenCalledWith(plugin.id);
    expect(getConfiguration).not.toHaveBeenCalled();
  });

  it("uploads a tar.gz with replacement enabled and refreshes the shared plugin list", async () => {
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
    const trust = container.querySelector<HTMLInputElement>('input[aria-label="신뢰 코드 및 서명 미검증 실행 확인"]');
    if (!trust) throw new Error("trusted-code checkbox not found");
    await act(async () => trust.click());
    await act(async () => button(container, "업로드 및 교체").click());

    expect(upload).toHaveBeenCalledWith("admin-token", archive, true);
    expect(onRefresh).toHaveBeenCalledOnce();
    expect(container.textContent).toContain(`${plugin.id} v2.0.0 플러그인을 교체했습니다.`);
  });

  it("uses plugin.enabled when an enabled plugin runtime has failed", async () => {
    const failedPlugin = {
      id: "com.hkjang.mattermost-chatdump-plugin",
      version: "1.0.0",
      state: "failed",
      enabled: true,
      manifest: { name: "Chatdump" },
    };

    await act(async () => root.render(
      <PluginAdminPanel
        {...baseProps}
        plugins={[failedPlugin]}
        statuses={[{ plugin_id: failedPlugin.id, state: "failed" }]}
      />,
    ));

    expect(container.textContent).toContain("failed · 활성화됨");
    expect(button(container, "비활성화")).toBeTruthy();
    expect(button(container, "설정")).toBeTruthy();
  });
});
