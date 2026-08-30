import type { PaletteColor, PaletteColorOptions } from "@mui/material/styles";
import type { MoyroFlowThemeTokens } from "@/theme/flowTokens";

declare module "@mui/material/styles" {
  interface Palette {
    navigation: PaletteColor;
    ai: PaletteColor;
    automation: PaletteColor;
    approval: PaletteColor;
  }

  interface PaletteOptions {
    navigation?: PaletteColorOptions;
    ai?: PaletteColorOptions;
    automation?: PaletteColorOptions;
    approval?: PaletteColorOptions;
  }

  interface Theme {
    flow: MoyroFlowThemeTokens;
  }

  interface ThemeOptions {
    flow?: MoyroFlowThemeTokens;
  }
}

export {};
