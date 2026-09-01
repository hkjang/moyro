// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { moyroAdminApi, type OIDCProviderSettings } from "@/api/client";
import { KeycloakSettingsPage } from "./KeycloakSettingsPage";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const providerFixture: OIDCProviderSettings = {
  id: "keycloak",
  kind: "keycloak",
  name: "Keycloak",
  enabled: true,
  issuer_url: "https://keycloak.internal/realms/moyro",
  client_id: "moyro",
  client_secret_state: { configured: true },
  scopes: ["openid", "profile", "email"],
  username_claim: "preferred_username",
  email_claim: "email",
  allow_signup: true,
  require_verified_email: true,
  allow_insecure_backchannel: false,
  discovery_status: "ready",
};

describe("KeycloakSettingsPage", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.spyOn(moyroAdminApi, "listOIDCProviders").mockResolvedValue([providerFixture]);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function renderPage() {
    const store = configureStore({ reducer: { auth: () => ({ token: "admin-token", user: null }) } });
    await act(async () => {
      root.render(
        <Provider store={store}>
          <KeycloakSettingsPage />
        </Provider>,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  function connectionButton(): HTMLButtonElement {
    const button = Array.from(container.querySelectorAll("button"))
      .find((candidate) => candidate.textContent?.includes("연동 확인"));
    if (!(button instanceof HTMLButtonElement)) throw new Error("connection button not found");
    return button;
  }

  async function setInputValue(input: HTMLInputElement, value: string) {
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
      setter?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it("hydrates a legacy provider without group mappings as an empty list", async () => {
    await renderPage();

    expect(container.textContent).toContain("매핑이 없으면 신규 SSO 사용자는 기존 기본 공간에 가입합니다.");
  });

  it("clears a stale ready state when a connection field changes", async () => {
    await renderPage();
    expect(container.textContent).toContain("OIDC discovery와 JWKS 서명 키를 확인했습니다.");

    const issuer = container.querySelector<HTMLInputElement>('input[placeholder*="keycloak.internal"]');
    if (!issuer) throw new Error("issuer input not found");
    await setInputValue(issuer, "https://new-keycloak.internal/realms/moyro");

    expect(container.textContent).not.toContain("OIDC discovery와 JWKS 서명 키를 확인했습니다.");
  });

  it("marks discovery as failed when the API rejects the probe", async () => {
    vi.spyOn(moyroAdminApi, "testOIDCProvider").mockRejectedValue(new Error("JWKS endpoint timed out"));
    await renderPage();

    await act(async () => {
      connectionButton().click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.textContent).not.toContain("OIDC discovery와 JWKS 서명 키를 확인했습니다.");
    expect(container.textContent).toContain("JWKS endpoint timed out");
  });

  it("adopts the canonical issuer returned by a successful probe", async () => {
    vi.spyOn(moyroAdminApi, "testOIDCProvider").mockResolvedValue({
      ok: true,
      issuer: "https://keycloak.internal/realms/moyro/",
    });
    await renderPage();

    await act(async () => {
      connectionButton().click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const issuer = container.querySelector<HTMLInputElement>('input[placeholder*="keycloak.internal"]');
    expect(issuer?.value).toBe("https://keycloak.internal/realms/moyro/");
    expect(container.textContent).toContain("OIDC discovery와 JWKS 서명 키를 확인했습니다.");
  });

  it("ignores a probe result after the tested settings change", async () => {
    let resolveProbe!: (result: { ok: boolean; issuer?: string }) => void;
    vi.spyOn(moyroAdminApi, "testOIDCProvider").mockReturnValue(new Promise((resolve) => {
      resolveProbe = resolve;
    }));
    await renderPage();

    await act(async () => { connectionButton().click(); });
    const issuer = container.querySelector<HTMLInputElement>('input[placeholder*="keycloak.internal"]');
    if (!issuer) throw new Error("issuer input not found");
    await setInputValue(issuer, "https://edited.internal/realms/moyro");
    await act(async () => {
      resolveProbe({ ok: true, issuer: "https://keycloak.internal/realms/moyro/" });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(issuer.value).toBe("https://edited.internal/realms/moyro");
    expect(container.textContent).not.toContain("OIDC discovery와 JWKS 서명 키를 확인했습니다.");
  });

  it("sends the explicit HTTP back-channel opt-in with the probe", async () => {
    const probe = vi.spyOn(moyroAdminApi, "testOIDCProvider").mockResolvedValue({
      ok: true,
      issuer: providerFixture.issuer_url,
    });
    await renderPage();

    const label = Array.from(container.querySelectorAll("label"))
      .find((candidate) => candidate.textContent?.includes("HTTP back-channel"));
    const toggle = label?.querySelector<HTMLInputElement>('input[type="checkbox"]');
    if (!toggle) throw new Error("HTTP back-channel toggle not found");
    await act(async () => { toggle.click(); });

    expect(container.textContent).toContain("authorization code가 평문 HTTP로 전송될 수 있으므로");
    await act(async () => {
      connectionButton().click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(probe).toHaveBeenCalledWith(
      "admin-token",
      expect.objectContaining({ allow_insecure_backchannel: true }),
    );
  });
});
