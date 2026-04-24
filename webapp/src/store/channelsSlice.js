import { createSlice } from "@reduxjs/toolkit";
const slice = createSlice({
    name: "channels",
    initialState: { byId: {}, currentId: null },
    reducers: {
        upsertChannel(state, action) {
            state.byId[action.payload.id] = action.payload;
        },
        setCurrentChannel(state, action) {
            state.currentId = action.payload;
        },
    },
});
export const { upsertChannel, setCurrentChannel } = slice.actions;
export const channelsReducer = slice.reducer;
