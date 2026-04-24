import { configureStore } from "@reduxjs/toolkit";
import { authReducer } from "./authSlice";
import { channelsReducer } from "./channelsSlice";
import { postsReducer } from "./postsSlice";

export const store = configureStore({
  reducer: {
    auth: authReducer,
    channels: channelsReducer,
    posts: postsReducer,
  },
});

export type RootState = ReturnType<typeof store.getState>;
export type AppDispatch = typeof store.dispatch;
