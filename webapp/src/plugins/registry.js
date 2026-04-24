const state = {
    mainMenuActions: [],
    channelHeaderButtons: [],
    postTypeComponents: [],
    rhsComponents: [],
};
export function createRegistry(pluginId) {
    return {
        registerMainMenuAction(text, action) {
            state.mainMenuActions.push({ pluginId, text, action });
        },
        registerChannelHeaderButtonAction(icon, action, tooltip) {
            state.channelHeaderButtons.push({ pluginId, icon, tooltip, action });
        },
        registerPostTypeComponent(postType, component) {
            state.postTypeComponents.push({ pluginId, postType, component });
        },
        registerRightHandSidebarComponent(title, component) {
            state.rhsComponents.push({ pluginId, title, component });
        },
        unregisterAll() {
            for (const key of Object.keys(state)) {
                state[key] = state[key].filter((e) => e.pluginId !== pluginId);
            }
        },
    };
}
export function getRegistryState() {
    return state;
}
