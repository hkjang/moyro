import { createSlice, PayloadAction } from "@reduxjs/toolkit";

// `roles` is a space-separated bag of server-assigned roles
// ("system_user", "system_admin", …). Optional since old tokens in
// localStorage may predate the field; admin checks default to false.
// `picture` + `update_at` (Phase 15) drive avatar rendering for the
// logged-in user's own sidebar status chip and profile menu.
export type AuthUser = {
  id: string;
  username: string;
  email: string;
  roles?: string;
  guest_expires_at?: number;
  guest_file_download?: boolean;
  picture?: string;
  update_at?: number;
};

export type AuthState = { token: string | null; user: AuthUser | null };

export const AUTH_STORAGE_KEY = "moyro.auth.session";

function loadInitialState(): AuthState {
  if (typeof window === "undefined") return { token: null, user: null };
  try {
    const raw = window.sessionStorage.getItem(AUTH_STORAGE_KEY);
    if (!raw) return { token: null, user: null };
    const parsed = JSON.parse(raw) as Partial<AuthState>;
    if (typeof parsed.token !== "string" || !parsed.user || typeof parsed.user.id !== "string") {
      window.sessionStorage.removeItem(AUTH_STORAGE_KEY);
      return { token: null, user: null };
    }
    return { token: parsed.token, user: parsed.user };
  } catch {
    return { token: null, user: null };
  }
}

const slice = createSlice({
  name: "auth",
  initialState: loadInitialState(),
  reducers: {
    setAuth(state, action: PayloadAction<AuthState>) {
      state.token = action.payload.token;
      state.user = action.payload.user;
    },
    clearAuth(state) {
      state.token = null;
      state.user = null;
    },
  },
});

export const { setAuth, clearAuth } = slice.actions;
export const authReducer = slice.reducer;
