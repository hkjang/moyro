import type { ComponentType } from "react";

type MenuAction = {
  pluginId: string;
  text: string;
  action: () => void;
};

type HeaderButton = {
  pluginId: string;
  icon: ComponentType;
  tooltip: string;
  action: (channelId: string) => void;
};

type PostTypeComponent = {
  pluginId: string;
  postType: string;
  component: ComponentType<{ post: unknown }>;
};

type RhsComponent = {
  pluginId: string;
  title: string;
  component: ComponentType;
};

type RegistryState = {
  mainMenuActions: MenuAction[];
  channelHeaderButtons: HeaderButton[];
  postTypeComponents: PostTypeComponent[];
  rhsComponents: RhsComponent[];
};

const state: RegistryState = {
  mainMenuActions: [],
  channelHeaderButtons: [],
  postTypeComponents: [],
  rhsComponents: [],
};

export function createRegistry(pluginId: string) {
  return {
    registerMainMenuAction(text: string, action: () => void) {
      state.mainMenuActions.push({ pluginId, text, action });
    },
    registerChannelHeaderButtonAction(
      icon: ComponentType,
      action: (channelId: string) => void,
      tooltip: string,
    ) {
      state.channelHeaderButtons.push({ pluginId, icon, tooltip, action });
    },
    registerPostTypeComponent(
      postType: string,
      component: ComponentType<{ post: unknown }>,
    ) {
      state.postTypeComponents.push({ pluginId, postType, component });
    },
    registerRightHandSidebarComponent(
      title: string,
      component: ComponentType,
    ) {
      state.rhsComponents.push({ pluginId, title, component });
    },
    unregisterAll() {
      for (const key of Object.keys(state) as (keyof RegistryState)[]) {
        state[key] = state[key].filter((e) => e.pluginId !== pluginId) as never;
      }
    },
  };
}

export function getRegistryState(): Readonly<RegistryState> {
  return state;
}
