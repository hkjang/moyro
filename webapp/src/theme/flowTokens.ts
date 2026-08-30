export const moyroFlowColors = {
  brand: "#3157D5",
  brandDark: "#203E9F",
  navigation: "#15213D",
  ai: "#6D55C5",
  automation: "#0F766E",
  approvalAccent: "#B76E00",
  approvalAccessible: "#A46000",
  success: "#218358",
  danger: "#C2414B",
  pageBackground: "#F6F7F9",
  surface: "#FFFFFF",
  border: "#DFE3EA",
  text: "#182033",
  textSecondary: "#667085",
} as const;

export const moyroFlowDarkColors = {
  brand: "#8FA6EE",
  brandDark: "#6F89E1",
  navigation: "#10182D",
  ai: "#A18FEC",
  automation: "#52B8AD",
  approval: "#F0B95A",
  success: "#62BD90",
  danger: "#EF858C",
  pageBackground: "#16181D",
  surface: "#202228",
  border: "#363A43",
  text: "#F2F4F7",
  textSecondary: "#B7C0CE",
} as const;

export const moyroFlowThemeTokens = {
  colors: {
    approvalAccent: moyroFlowColors.approvalAccent,
  },
  radii: {
    input: "8px",
    card: "10px",
    popover: "12px",
    pill: "999px",
  },
  focus: {
    width: "2px",
    offset: "2px",
    ringWidth: "3px",
  },
  motion: {
    fast: "120ms",
    standard: "160ms",
    slow: "180ms",
  },
} as const;

export type MoyroFlowThemeTokens = typeof moyroFlowThemeTokens;
