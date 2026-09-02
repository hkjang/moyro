import React from "react";
import ReactDOM from "react-dom/client";
import InitColorSchemeScript from "@mui/material/InitColorSchemeScript";
import { Provider } from "react-redux";
import { store } from "@/store";
import { App } from "@/components/App";
import { AppProviders } from "@/app/AppProviders";
import { bootstrapPluginRuntime } from "@/plugins/runtime";
import "@/index.css";
// Ordered continuations of index.css. The cascade depends on this sequence,
// so keep these three immediately after it.
import "@/styles/admin.css";
import "@/styles/features.css";
import "@/styles/workspace-chrome.css";
import "@/styles/tokens.css";
import "@/styles/base.css";
import "@/styles/accessibility.css";

bootstrapPluginRuntime();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <>
    <InitColorSchemeScript
      attribute="data-theme"
      defaultMode="system"
      modeStorageKey="moyro:theme"
      colorSchemeStorageKey="moyro:color-scheme"
    />
    <React.StrictMode>
      <Provider store={store}>
        <AppProviders>
          <App />
        </AppProviders>
      </Provider>
    </React.StrictMode>
  </>,
);
