import { createSlice, PayloadAction } from "@reduxjs/toolkit";

export type Post = {
  id: string;
  channel_id: string;
  user_id: string;
  message: string;
  root_id: string;
  create_at: number;
  update_at: number;
  props: Record<string, unknown>;
};

type State = { byChannel: Record<string, Post[]> };

const slice = createSlice({
  name: "posts",
  initialState: { byChannel: {} } as State,
  reducers: {
    appendPost(state, action: PayloadAction<Post>) {
      const arr = state.byChannel[action.payload.channel_id] ?? [];
      arr.push(action.payload);
      state.byChannel[action.payload.channel_id] = arr;
    },
    setChannelPosts(
      state,
      action: PayloadAction<{ channelId: string; posts: Post[] }>,
    ) {
      state.byChannel[action.payload.channelId] = action.payload.posts;
    },
  },
});

export const { appendPost, setChannelPosts } = slice.actions;
export const postsReducer = slice.reducer;
