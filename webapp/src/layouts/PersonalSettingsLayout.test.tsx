// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { createRegistry } from "@/plugins/registry";
import { PersonalSettingsLayout } from "./PersonalSettingsLayout";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("PersonalSettingsLayout navigation semantics", () => {
  let container: HTMLDivElement;
  let root: Root;
  const pluginRegistry = createRegistry("com.example.settings");

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    pluginRegistry.registerUserSettings({
      id: "preferences",
      uiName: "예제 플러그인",
      sections: [{ title: "일반", component: () => null }],
    });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    pluginRegistry.unregisterAll();
    container.remove();
  });

  it("renders built-in and plugin navigation actions inside list items", async () => {
    const store = configureStore({
      reducer: {
        auth: () => ({ token: "user-token", user: { username: "user" } }),
      },
    });

    await act(async () => {
      root.render(
        <Provider store={store}>
          <MemoryRouter initialEntries={["/settings/profile"]}>
            <Routes>
              <Route path="/settings" element={<PersonalSettingsLayout />}>
                <Route path="profile" element={<div>profile</div>} />
              </Route>
            </Routes>
          </MemoryRouter>
        </Provider>,
      );
    });

    const list = container.querySelector('nav[aria-label="개인 설정 메뉴"] > ul');
    expect(list).not.toBeNull();
    expect([...list?.children ?? []].every((child) => child.tagName === "LI")).toBe(true);
    expect(list?.querySelector(":scope > [role=button]")).toBeNull();
    expect(list?.textContent).toContain("예제 플러그인");
    expect(list?.querySelectorAll("li > .personal-navigation-item").length).toBe(list?.children.length);
  });
});
