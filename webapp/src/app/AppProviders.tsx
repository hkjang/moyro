import "@fontsource-variable/noto-sans-kr/wght.css";
import { CssBaseline, StyledEngineProvider, ThemeProvider } from "@mui/material";
import { BrowserRouter } from "react-router-dom";
import { SystemInfoProvider } from "@/features/system/SystemInfoContext";
import { AdminAccessProvider } from "@/features/admin/AdminAccessContext";
import { moyroTheme } from "@/theme/moyroTheme";

export function AppProviders({ children }: { children: React.ReactNode }) {
  return (
    <StyledEngineProvider injectFirst>
      <ThemeProvider theme={moyroTheme}>
        <CssBaseline />
        <SystemInfoProvider>
          <AdminAccessProvider>
            <BrowserRouter>{children}</BrowserRouter>
          </AdminAccessProvider>
        </SystemInfoProvider>
      </ThemeProvider>
    </StyledEngineProvider>
  );
}
