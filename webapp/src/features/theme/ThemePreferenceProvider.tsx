import { useColorScheme } from "@mui/material/styles";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useSelector } from "react-redux";
import { prefsApi } from "@/api/client";
import type { RootState } from "@/store";

export type ThemeChoice = "light" | "dark" | "system";

type ThemePreferenceValue = {
  theme: ThemeChoice;
  loaded: boolean;
  setTheme: (theme: ThemeChoice) => Promise<void>;
};

const STORAGE_KEY = "moyro:theme";

function isThemeChoice(value: unknown): value is ThemeChoice {
  return value === "light" || value === "dark" || value === "system";
}

function readCachedTheme(): ThemeChoice {
  if (typeof window === "undefined") return "system";
  try {
    const cached = window.localStorage.getItem(STORAGE_KEY);
    return isThemeChoice(cached) ? cached : "system";
  } catch {
    return "system";
  }
}

const ThemePreferenceContext = createContext<ThemePreferenceValue | null>(null);

/**
 * Owns the user's theme preference for the entire authenticated application.
 * MUI's color-scheme controller is the only writer of the resolved
 * `html[data-theme]` attribute; the same choice is mirrored to Moyro's
 * Mattermost-compatible preference row for cross-device use.
 */
export function ThemePreferenceProvider({ children }: { children: ReactNode }) {
  const token = useSelector((state: RootState) => state.auth.token);
  const userID = useSelector((state: RootState) => state.auth.user?.id ?? null);
  const { setMode } = useColorScheme();
  const [theme, setThemeState] = useState<ThemeChoice>(readCachedTheme);
  const [loaded, setLoaded] = useState(!token || !userID);
  const localRevisionRef = useRef(0);

  const applyLocalTheme = useCallback((next: ThemeChoice) => {
    setThemeState(next);
    setMode(next);
    try {
      window.localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // Storage can be unavailable in hardened/private browser contexts.
    }
  }, [setMode]);

  // Reconcile MUI with the value used by the first-paint bootstrap script.
  useEffect(() => {
    setMode(theme);
  }, [setMode, theme]);

  // The server preference applies regardless of which authenticated route is
  // opened first. A user selection made while this request is in flight wins.
  useEffect(() => {
    if (!token || !userID) {
      setLoaded(true);
      return;
    }
    let active = true;
    const revisionAtStart = localRevisionRef.current;
    setLoaded(false);
    void prefsApi.listCategory(token, "display_settings", userID).then(
      (preferences) => {
        if (!active || revisionAtStart !== localRevisionRef.current) return;
        const saved = preferences.find((preference) => preference.name === "theme")?.value;
        if (isThemeChoice(saved)) applyLocalTheme(saved);
      },
      () => {
        // A missing preference or temporarily unavailable API must not undo
        // the locally cached choice used for first paint.
      },
    ).finally(() => {
      if (active) setLoaded(true);
    });
    return () => {
      active = false;
    };
  }, [applyLocalTheme, token, userID]);

  const setTheme = useCallback(async (next: ThemeChoice) => {
    localRevisionRef.current += 1;
    applyLocalTheme(next);
    if (!token || !userID) return;
    await prefsApi.upsert(token, [{
      user_id: userID,
      category: "display_settings",
      name: "theme",
      value: next,
    }], userID);
  }, [applyLocalTheme, token, userID]);

  const value = useMemo<ThemePreferenceValue>(() => ({ theme, loaded, setTheme }), [loaded, setTheme, theme]);
  return <ThemePreferenceContext.Provider value={value}>{children}</ThemePreferenceContext.Provider>;
}

export function useThemePreference(): ThemePreferenceValue {
  const value = useContext(ThemePreferenceContext);
  if (!value) throw new Error("useThemePreference must be used inside ThemePreferenceProvider");
  return value;
}
