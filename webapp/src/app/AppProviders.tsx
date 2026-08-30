import "@fontsource-variable/noto-sans-kr/wght.css";
import { CssBaseline, StyledEngineProvider, ThemeProvider } from "@mui/material";
import { BrowserRouter } from "react-router-dom";
import { SystemInfoProvider } from "@/features/system/SystemInfoContext";
import { AdminAccessProvider } from "@/features/admin/AdminAccessContext";
import { ThemePreferenceProvider } from "@/features/theme/ThemePreferenceProvider";
import { moyroTheme } from "@/theme/moyroTheme";
import { PluginLoader } from "@/plugins/PluginLoader";

export function AppProviders({ children }: { children: React.ReactNode }) {
  return (
    <StyledEngineProvider injectFirst>
      <ThemeProvider
        theme={moyroTheme}
        defaultMode="system"
        modeStorageKey="moyro:theme"
        colorSchemeStorageKey="moyro:color-scheme"
        disableTransitionOnChange
      >
        <ThemePreferenceProvider>
          <CssBaseline />
          <PluginLoader />
          <SystemInfoProvider>
            <AdminAccessProvider>
              <BrowserRouter>{children}</BrowserRouter>
            </AdminAccessProvider>
          </SystemInfoProvider>
        </ThemePreferenceProvider>
      </ThemeProvider>
    </StyledEngineProvider>
  );
}
