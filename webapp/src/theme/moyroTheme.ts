import { createTheme } from "@mui/material/styles";
import {
  moyroFlowColors,
  moyroFlowDarkColors,
  moyroFlowThemeTokens,
} from "@/theme/flowTokens";

export const moyroFontFamily = [
  '"Noto Sans KR Variable"',
  '"Noto Sans KR"',
  '"Pretendard"',
  '"Segoe UI"',
  '"Apple SD Gothic Neo"',
  "system-ui",
  "sans-serif",
].join(",");

export const moyroTheme = createTheme({
  cssVariables: {
    cssVarPrefix: "moyro",
    colorSchemeSelector: '[data-theme="%s"]',
  },
  defaultColorScheme: "light",
  colorSchemes: {
    light: {
      palette: {
        mode: "light",
        primary: {
          main: moyroFlowColors.brand,
          dark: moyroFlowColors.brandDark,
          light: "#5F78DF",
          contrastText: "#FFFFFF",
        },
        secondary: {
          main: moyroFlowColors.automation,
          dark: "#0B5D57",
          light: "#4E9F97",
          contrastText: "#FFFFFF",
        },
        navigation: {
          main: moyroFlowColors.navigation,
          dark: "#10182D",
          light: "#31405F",
          contrastText: "#FFFFFF",
        },
        ai: {
          main: moyroFlowColors.ai,
          dark: "#513B9E",
          light: "#8B76D7",
          contrastText: "#FFFFFF",
        },
        automation: {
          main: moyroFlowColors.automation,
          dark: "#0B5D57",
          light: "#4E9F97",
          contrastText: "#FFFFFF",
        },
        approval: {
          main: moyroFlowColors.approvalAccessible,
          dark: "#8A4F00",
          light: moyroFlowColors.approvalAccent,
          contrastText: "#FFFFFF",
        },
        info: { main: moyroFlowColors.brand, contrastText: "#FFFFFF" },
        warning: {
          main: moyroFlowColors.approvalAccessible,
          dark: "#8A4F00",
          light: moyroFlowColors.approvalAccent,
          contrastText: "#FFFFFF",
        },
        success: { main: moyroFlowColors.success, contrastText: "#FFFFFF" },
        error: { main: moyroFlowColors.danger, contrastText: "#FFFFFF" },
        background: {
          default: moyroFlowColors.pageBackground,
          paper: moyroFlowColors.surface,
        },
        divider: moyroFlowColors.border,
        text: {
          primary: moyroFlowColors.text,
          secondary: moyroFlowColors.textSecondary,
        },
      },
    },
    dark: {
      palette: {
        mode: "dark",
        primary: {
          main: moyroFlowDarkColors.brand,
          dark: moyroFlowDarkColors.brandDark,
          light: "#B8C7F5",
          contrastText: "#10182D",
        },
        secondary: {
          main: moyroFlowDarkColors.automation,
          dark: "#2D8F85",
          light: "#82D2C9",
          contrastText: "#101F25",
        },
        navigation: {
          main: moyroFlowDarkColors.navigation,
          dark: "#0A1020",
          light: "#31405F",
          contrastText: "#F2F4F7",
        },
        ai: {
          main: moyroFlowDarkColors.ai,
          dark: moyroFlowColors.ai,
          light: "#C3B8F4",
          contrastText: "#171229",
        },
        automation: {
          main: moyroFlowDarkColors.automation,
          dark: "#2D8F85",
          light: "#82D2C9",
          contrastText: "#101F25",
        },
        approval: {
          main: moyroFlowDarkColors.approval,
          dark: "#D18C20",
          light: "#FFD997",
          contrastText: moyroFlowColors.text,
        },
        info: { main: moyroFlowDarkColors.brand, contrastText: "#10182D" },
        warning: {
          main: moyroFlowDarkColors.approval,
          dark: "#D18C20",
          light: "#FFD997",
          contrastText: moyroFlowColors.text,
        },
        success: { main: moyroFlowDarkColors.success, contrastText: "#10221A" },
        error: { main: moyroFlowDarkColors.danger, contrastText: "#2A1013" },
        background: {
          default: moyroFlowDarkColors.pageBackground,
          paper: moyroFlowDarkColors.surface,
        },
        divider: moyroFlowDarkColors.border,
        text: {
          primary: moyroFlowDarkColors.text,
          secondary: moyroFlowDarkColors.textSecondary,
        },
      },
    },
  },
  flow: moyroFlowThemeTokens,
  shape: { borderRadius: 8 },
  transitions: {
    duration: {
      shortest: 120,
      shorter: 140,
      short: 160,
      standard: 180,
    },
  },
  typography: {
    fontFamily: moyroFontFamily,
    fontSize: 16,
    htmlFontSize: 16,
    body1: { fontSize: "1rem", lineHeight: 1.6 },
    body2: { fontSize: "0.9375rem", lineHeight: 1.55 },
    caption: { fontSize: "0.8125rem", lineHeight: 1.45 },
    button: { fontSize: "0.875rem", lineHeight: 1.4, fontWeight: 650, textTransform: "none" },
    subtitle1: { fontSize: "1rem", lineHeight: 1.5, fontWeight: 700 },
    subtitle2: { fontSize: "0.9375rem", lineHeight: 1.5, fontWeight: 700 },
    h1: { fontSize: "2rem", lineHeight: 1.25, fontWeight: 750 },
    h2: { fontSize: "1.625rem", lineHeight: 1.3, fontWeight: 750 },
    h3: { fontSize: "1.375rem", lineHeight: 1.35, fontWeight: 750 },
    h4: { fontSize: "1.1875rem", lineHeight: 1.4, fontWeight: 750 },
    h5: { fontSize: "1.0625rem", lineHeight: 1.45, fontWeight: 700 },
    h6: { fontSize: "1rem", lineHeight: 1.5, fontWeight: 700 },
  },
  components: {
    MuiCssBaseline: {
      styleOverrides: {
        html: { fontSize: "16px" },
        body: {
          minWidth: 320,
          fontFamily: moyroFontFamily,
          fontSize: "1rem",
          lineHeight: 1.6,
        },
      },
    },
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: {
        root: {
          minHeight: 40,
          borderRadius: moyroFlowThemeTokens.radii.input,
          transitionDuration: moyroFlowThemeTokens.motion.standard,
        },
      },
    },
    MuiIconButton: {
      styleOverrides: {
        root: {
          minWidth: 40,
          minHeight: 40,
          borderRadius: moyroFlowThemeTokens.radii.input,
        },
      },
    },
    MuiOutlinedInput: {
      styleOverrides: {
        root: { borderRadius: moyroFlowThemeTokens.radii.input },
      },
    },
    MuiInputBase: {
      styleOverrides: { root: { minHeight: 42, fontSize: "0.9375rem" } },
    },
    MuiInputLabel: {
      styleOverrides: { root: { fontSize: "0.9375rem" } },
    },
    MuiFormHelperText: {
      styleOverrides: { root: { fontSize: "0.8125rem", lineHeight: 1.45 } },
    },
    MuiMenuItem: {
      styleOverrides: { root: { minHeight: 42, fontSize: "0.875rem" } },
    },
    MuiListItemText: {
      styleOverrides: {
        primary: { fontSize: "0.875rem" },
        secondary: { fontSize: "0.8125rem" },
      },
    },
    MuiTableCell: {
      styleOverrides: { root: { fontSize: "0.875rem", lineHeight: 1.5 } },
    },
    MuiTooltip: {
      styleOverrides: {
        tooltip: {
          borderRadius: moyroFlowThemeTokens.radii.input,
          fontSize: "0.8125rem",
          lineHeight: 1.45,
        },
      },
    },
    MuiCard: {
      styleOverrides: { root: { borderRadius: moyroFlowThemeTokens.radii.card } },
    },
    MuiDialog: {
      styleOverrides: { paper: { borderRadius: moyroFlowThemeTokens.radii.popover } },
    },
    MuiPopover: {
      styleOverrides: { paper: { borderRadius: moyroFlowThemeTokens.radii.popover } },
    },
    MuiPaper: {
      styleOverrides: { root: { backgroundImage: "none" } },
    },
  },
});
