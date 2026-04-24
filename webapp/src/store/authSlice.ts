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
  picture?: string;
  update_at?: number;
};

type State = { token: string | null; user: AuthUser | null };

const slice = createSlice({
  name: "auth",
  initialState: { token: null, user: null } as State,
  reducers: {
    setAuth(state, action: PayloadAction<State>) {
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
