// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Provider } from "react-redux";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { moyroMeApi } from "@/api/client";
import { AdminAccessProvider, useAdminAccess } from "./AdminAccessContext";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function AccessProbe() {
  const access = useAdminAccess();
  if (!access.loaded) return <span>loading</span>;
  return (
    <span>
      {access.hasAdminAccess ? "admin" : "user"}
      {access.can("manage_plugins") ? ":plugins" : ":no-plugins"}
    </span>
  );
}

describe("AdminAccessProvider", () => {
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

  it("treats manage_plugins as delegated admin access", async () => {
    vi.spyOn(moyroMeApi, "getPermissions").mockResolvedValue({
      permissions: ["manage_plugins"],
    });
    const store = configureStore({
      reducer: {
        auth: () => ({ token: "plugin-admin-token", user: null }),
      },
    });

    await act(async () => {
      root.render(
        <Provider store={store}>
          <AdminAccessProvider>
            <AccessProbe />
          </AdminAccessProvider>
        </Provider>,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.textContent).toBe("admin:plugins");
  });
});
