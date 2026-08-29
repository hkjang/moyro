import { createTheme } from "@mui/material/styles";

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
  cssVariables: { cssVarPrefix: "moyro" },
  palette: {
    primary: { main: "#2457C5", dark: "#1B439A", light: "#5D82DC" },
    secondary: { main: "#227C70" },
    error: { main: "#C63F4B" },
    warning: { main: "#A96400" },
    success: { main: "#1F7A50" },
    background: { default: "#F4F6F8", paper: "#FFFFFF" },
    text: { primary: "#172033", secondary: "#596579" },
  },
  shape: { borderRadius: 8 },
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
      styleOverrides: { root: { minHeight: 40, borderRadius: 7 } },
    },
    MuiIconButton: {
      styleOverrides: { root: { minWidth: 40, minHeight: 40 } },
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
      styleOverrides: { tooltip: { fontSize: "0.8125rem", lineHeight: 1.45 } },
    },
    MuiPaper: {
      styleOverrides: { root: { backgroundImage: "none" } },
    },
  },
});
