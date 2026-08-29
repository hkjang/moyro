import React from "react";
import ReactDOM from "react-dom/client";
import { Provider } from "react-redux";
import { store } from "@/store";
import { App } from "@/components/App";
import { AppProviders } from "@/app/AppProviders";
import { bootstrapPluginRuntime } from "@/plugins/runtime";
import "@/index.css";

bootstrapPluginRuntime();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Provider store={store}>
      <AppProviders>
        <App />
      </AppProviders>
    </Provider>
  </React.StrictMode>,
);
