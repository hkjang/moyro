import { createSlice } from "@reduxjs/toolkit";
const slice = createSlice({
    name: "posts",
    initialState: { byChannel: {} },
    reducers: {
        appendPost(state, action) {
            const arr = state.byChannel[action.payload.channel_id] ?? [];
            arr.push(action.payload);
            state.byChannel[action.payload.channel_id] = arr;
        },
        setChannelPosts(state, action) {
            state.byChannel[action.payload.channelId] = action.payload.posts;
        },
    },
});
export const { appendPost, setChannelPosts } = slice.actions;
export const postsReducer = slice.reducer;
