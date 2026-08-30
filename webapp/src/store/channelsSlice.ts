import { createSlice, PayloadAction } from "@reduxjs/toolkit";

export type Channel = {
  id: string;
  team_id: string;
  type: "O" | "P" | "D" | "G";
  display_name: string;
  name: string;
};

type State = { byId: Record<string, Channel>; currentId: string | null };

const slice = createSlice({
  name: "channels",
  initialState: { byId: {}, currentId: null } as State,
  reducers: {
    upsertChannel(state, action: PayloadAction<Channel>) {
      state.byId[action.payload.id] = action.payload;
    },
    setCurrentChannel(state, action: PayloadAction<string | null>) {
      state.currentId = action.payload;
    },
  },
});

export const { upsertChannel, setCurrentChannel } = slice.actions;
export const channelsReducer = slice.reducer;
