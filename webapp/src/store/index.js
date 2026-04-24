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
