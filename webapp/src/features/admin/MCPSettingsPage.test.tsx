// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { moyroAdminApi, type MCPOAuthStatus, type MCPSettings } from "@/api/client";
import { MCPSettingsPage, validateMCPOAuth } from "./MCPSettingsPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const base: MCPSettings = {
  enabled: true,
  transport: "streamable-http",
  endpoint_path: "/mcp",
  allowed_tools: ["list_teams"],
  allowed_resources: ["moyro://teams"],
  required_scopes: ["mcp_read"],
  oauth: { enabled: false, resource: "", audience: [], scopes: ["mcp_read"] },
};

const active: MCPOAuthStatus = {
  active: true,
  resource: "https://chat.corp.example/mcp",
  metadata_url: "https://chat.corp.example/.well-known/oauth-protected-resource/mcp",
  issuer_url: "https://sso.corp.example/realms/corp",
  client_id: "moyro-web",
  oidc_configured: true,
};

describe("validateMCPOAuth", () => {
  it("accepts the default (off) card and a half-filled disabled one", () => {
    expect(validateMCPOAuth(base, undefined)).toBe("");
    expect(validateMCPOAuth({ ...base, oauth: { ...base.oauth, scopes: [] } }, { active: false, oidc_configured: false })).toBe("");
  });

  it("refuses what the server would refuse", () => {
    const on = { ...base.oauth, enabled: true };
    expect(validateMCPOAuth({ ...base, oauth: on }, { active: false, oidc_configured: false })).toContain("Keycloak");
    expect(validateMCPOAuth({ ...base, oauth: on }, { active: false, oidc_configured: true })).toContain("리소스 식별자");
    expect(validateMCPOAuth({ ...base, oauth: { ...on, scopes: [] } }, active)).toContain("범위");
    expect(validateMCPOAuth({ ...base, oauth: { ...on, scopes: ["manage_system"] } }, active)).toContain("manage_system");
    expect(validateMCPOAuth({ ...base, oauth: { ...on, resource: "chat.corp.example/mcp" } }, active)).toContain("절대");
    expect(validateMCPOAuth({ ...base, oauth: on }, active)).toBe("");
  });
});

describe("MCPSettingsPage SSO card", () => {
  let container: HTMLDivElement;
  let root: Root;

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

  async function renderPage(settings: MCPSettings) {
    vi.spyOn(moyroAdminApi, "getSettings").mockResolvedValue(settings);
    const store = configureStore({ reducer: { auth: () => ({ token: "admin-token", user: null }) } });
    await act(async () => {
      root.render(
        <Provider store={store}>
          <MCPSettingsPage />
        </Provider>,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  function button(text: string): HTMLButtonElement {
    const found = Array.from(container.querySelectorAll("button"))
      .find((candidate) => candidate.textContent?.trim() === text);
    if (!(found instanceof HTMLButtonElement)) throw new Error(`button ${text} not found`);
    return found;
  }

  it("shows the URLs a client needs once the server accepts SSO tokens", async () => {
    await renderPage({ ...base, oauth: { ...base.oauth, enabled: true, audience: ["claude-mcp"] }, oauth_status: active });

    const inputs = Array.from(container.querySelectorAll<HTMLInputElement>("input")).map((input) => input.value);
    expect(inputs).toContain("https://chat.corp.example/mcp");
    expect(inputs).toContain("https://chat.corp.example/.well-known/oauth-protected-resource/mcp");
    expect(inputs).toContain("https://sso.corp.example/realms/corp");
    expect(inputs).toContain("claude-mcp");
    expect(container.textContent).toContain("SSO 토큰을 받고 있습니다");
  });

  it("explains why an enabled switch is not accepting tokens", async () => {
    await renderPage({
      ...base,
      oauth: { ...base.oauth, enabled: true },
      oauth_status: { active: false, reason: "Keycloak SSO is not enabled or its discovery failed", oidc_configured: false },
    });
    expect(container.textContent).toContain("지금은 토큰을 받지 않습니다: Keycloak SSO is not enabled");
  });

  it("does not send the read-only status back and refuses an unworkable card", async () => {
    const patch = vi.spyOn(moyroAdminApi, "patchSettings").mockImplementation(async (_token, _section, value) => value as MCPSettings);
    await renderPage({ ...base, oauth_status: { active: false, reason: "disabled", oidc_configured: true, resource: "https://chat.corp.example/mcp" } });

    await act(async () => {
      button("설정 저장").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(patch).toHaveBeenCalledTimes(1);
    const [, section, payload] = patch.mock.calls[0] as [string, string, MCPSettings];
    expect(section).toBe("mcp");
    expect(payload.oauth).toEqual(base.oauth);
    expect("oauth_status" in payload).toBe(false);

    patch.mockClear();
    // The SSO switch and scope field are the last of their kind on the page:
    // the endpoint switch and the key-scope field come first.
    const switches = Array.from(container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'));
    await act(async () => {
      switches[switches.length - 1]?.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    const scopes = Array.from(container.querySelectorAll<HTMLInputElement>("input")).filter((input) => input.value === "mcp_read").at(-1);
    await act(async () => {
      if (!scopes) throw new Error("scope field not found");
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
      setter?.call(scopes, "manage_system");
      scopes.dispatchEvent(new Event("input", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await act(async () => {
      button("설정 저장").click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(patch).not.toHaveBeenCalled();
    expect(container.textContent).toContain("manage_system");
  });
});
