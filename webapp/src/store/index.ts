import { configureStore } from "@reduxjs/toolkit";
import { authReducer } from "./authSlice";
import { AUTH_STORAGE_KEY } from "./authSlice";
import { channelsReducer } from "./channelsSlice";
import { postsReducer } from "./postsSlice";

export const store = configureStore({
  reducer: {
    auth: authReducer,
    channels: channelsReducer,
    posts: postsReducer,
  },
});

let previousAuth = store.getState().auth;
store.subscribe(() => {
  const auth = store.getState().auth;
  if (auth === previousAuth) return;
  previousAuth = auth;
  try {
    if (auth.token && auth.user) {
      window.sessionStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(auth));
    } else {
      window.sessionStorage.removeItem(AUTH_STORAGE_KEY);
    }
  } catch {
    // Private browsing and locked-down enterprise policies may disable storage.
    // The in-memory session still works for the current page in that case.
  }
});

export type RootState = ReturnType<typeof store.getState>;
export type AppDispatch = typeof store.dispatch;
