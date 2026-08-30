import { useSyncExternalStore } from "react";
import type { ComponentType, ReactNode } from "react";

// Mattermost accepts components with arbitrary plugin-owned props. Using any
// here mirrors that public boundary; props are constrained at each Moyro
// rendering surface before invocation.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type ReactResolvable = ComponentType<any> | ReactNode;

export type MenuAction = { id: string; pluginId: string; text: string; action: () => void };
export type HeaderButton = {
  id: string;
  pluginId: string;
  icon: ReactResolvable;
  dropdownText: string;
  tooltipText: string;
  action: (...args: unknown[]) => void;
};
export type PostTypeComponent = {
  id: string;
  pluginId: string;
  postType: string;
  component: ComponentType<{ post: unknown }>;
};
export type RhsComponent = {
  id: string;
  pluginId: string;
  title: ReactResolvable;
  component: ReactResolvable;
};
export type AdminConsoleCustomSetting = {
  id: string;
  pluginId: string;
  key: string;
  component: ReactResolvable;
  options: { showTitle?: boolean };
};
export type AdminConsolePlugin = {
  id: string;
  pluginId: string;
  callback: (config: Record<string, unknown>) => void;
};
export type PluginUserSettingSection = { title: string; component: ReactResolvable };
export type PluginUserSettings = {
  id: string;
  pluginId: string;
  settingsId: string;
  uiName: string;
  sections: readonly PluginUserSettingSection[];
};
export type PluginWebSocketMessage = {
  event?: string;
  data?: Record<string, unknown>;
  broadcast?: Record<string, unknown>;
  seq?: number;
};
export type PluginWebSocketHandler = {
  id: string;
  pluginId: string;
  event: string;
  handler: (message: PluginWebSocketMessage) => void;
};

export type RegistryState = Readonly<{
  mainMenuActions: readonly MenuAction[];
  channelHeaderButtons: readonly HeaderButton[];
  postTypeComponents: readonly PostTypeComponent[];
  rhsComponents: readonly RhsComponent[];
  adminConsoleCustomSettings: readonly AdminConsoleCustomSetting[];
  adminConsolePlugins: readonly AdminConsolePlugin[];
  userSettings: readonly PluginUserSettings[];
  webSocketHandlers: readonly PluginWebSocketHandler[];
  activeRhsComponentId: string | null;
}>;

type MutableRegistryState = {
  mainMenuActions: MenuAction[];
  channelHeaderButtons: HeaderButton[];
  postTypeComponents: PostTypeComponent[];
  rhsComponents: RhsComponent[];
  adminConsoleCustomSettings: AdminConsoleCustomSetting[];
  adminConsolePlugins: AdminConsolePlugin[];
  userSettings: PluginUserSettings[];
  webSocketHandlers: PluginWebSocketHandler[];
  activeRhsComponentId: string | null;
};
type RegistryListener = () => void;
type HeaderButtonObject = {
  icon: ReactResolvable;
  action: (...args: unknown[]) => void;
  dropdownText: string;
  tooltipText: string;
};
type AdminSettingObject = {
  key: string;
  component: ReactResolvable;
  options?: { showTitle?: boolean };
};
type RhsObject = { component: ReactResolvable; title: ReactResolvable };
type WebSocketHandlerObject = {
  event: string;
  handler: (message: PluginWebSocketMessage) => void;
};

const emptyState = (): MutableRegistryState => ({
  mainMenuActions: [],
  channelHeaderButtons: [],
  postTypeComponents: [],
  rhsComponents: [],
  adminConsoleCustomSettings: [],
  adminConsolePlugins: [],
  userSettings: [],
  webSocketHandlers: [],
  activeRhsComponentId: null,
});

let nextRegistrationID = 1;
let state: RegistryState = freezeState(emptyState());
const listeners = new Set<RegistryListener>();

function freezeState(next: MutableRegistryState): RegistryState {
  return Object.freeze({
    mainMenuActions: Object.freeze(next.mainMenuActions),
    channelHeaderButtons: Object.freeze(next.channelHeaderButtons),
    postTypeComponents: Object.freeze(next.postTypeComponents),
    rhsComponents: Object.freeze(next.rhsComponents),
    adminConsoleCustomSettings: Object.freeze(next.adminConsoleCustomSettings),
    adminConsolePlugins: Object.freeze(next.adminConsolePlugins),
    userSettings: Object.freeze(next.userSettings),
    webSocketHandlers: Object.freeze(next.webSocketHandlers),
    activeRhsComponentId: next.activeRhsComponentId,
  });
}

function mutableState(): MutableRegistryState {
  return {
    mainMenuActions: [...state.mainMenuActions],
    channelHeaderButtons: [...state.channelHeaderButtons],
    postTypeComponents: [...state.postTypeComponents],
    rhsComponents: [...state.rhsComponents],
    adminConsoleCustomSettings: [...state.adminConsoleCustomSettings],
    adminConsolePlugins: [...state.adminConsolePlugins],
    userSettings: [...state.userSettings],
    webSocketHandlers: [...state.webSocketHandlers],
    activeRhsComponentId: state.activeRhsComponentId,
  };
}

function registrationID(pluginId: string, kind: string): string {
  const id = `${pluginId}_${kind}_${nextRegistrationID}`;
  nextRegistrationID += 1;
  return id;
}

function publish(next: MutableRegistryState): void {
  state = freezeState(next);
  for (const listener of [...listeners]) listener();
}

type RegistryArrayKey = Exclude<keyof MutableRegistryState, "activeRhsComponentId">;

function append<K extends RegistryArrayKey>(key: K, value: MutableRegistryState[K][number]): void {
  const next = mutableState();
  (next[key] as Array<MutableRegistryState[K][number]>).push(value);
  publish(next);
}

function normalizeHeaderButton(
  iconOrDefinition: ReactResolvable | HeaderButtonObject,
  action?: (...args: unknown[]) => void,
  dropdownText = "",
  tooltipText?: string,
): HeaderButtonObject {
  if (
    typeof iconOrDefinition === "object" && iconOrDefinition !== null &&
    "icon" in iconOrDefinition && "action" in iconOrDefinition
  ) return iconOrDefinition as HeaderButtonObject;
  if (typeof action !== "function") throw new Error("channel header action is required");
  return {
    icon: iconOrDefinition as ReactResolvable,
    action,
    dropdownText,
    tooltipText: tooltipText ?? dropdownText,
  };
}

function normalizeAdminSetting(
  keyOrDefinition: string | AdminSettingObject,
  component?: ReactResolvable,
  options: { showTitle?: boolean } = {},
): AdminSettingObject {
  if (typeof keyOrDefinition === "object") return keyOrDefinition;
  if (!component) throw new Error("admin console custom setting component is required");
  return { key: keyOrDefinition, component, options };
}

function normalizeRhs(
  componentOrDefinition: ReactResolvable | RhsObject,
  title?: ReactResolvable,
): RhsObject {
  if (
    typeof componentOrDefinition === "object" && componentOrDefinition !== null &&
    !Array.isArray(componentOrDefinition) && "component" in componentOrDefinition && "title" in componentOrDefinition
  ) return componentOrDefinition as RhsObject;
  // The official contract is component-first. Preserve the legacy Moyro
  // string-first form for already-supported bundles.
  if (typeof componentOrDefinition === "string" && title) {
    return { component: title, title: componentOrDefinition };
  }
  if (!title) throw new Error("right-hand sidebar title is required");
  return { component: componentOrDefinition as ReactResolvable, title };
}

function normalizeWebSocketHandler(
  eventOrDefinition: string | WebSocketHandlerObject,
  handler?: (message: PluginWebSocketMessage) => void,
): WebSocketHandlerObject {
  if (typeof eventOrDefinition === "object") return eventOrDefinition;
  if (typeof handler !== "function") throw new Error("websocket event handler is required");
  return { event: eventOrDefinition, handler };
}

function rhsAction(type: "show" | "hide" | "toggle", id: string) {
  return Object.freeze({ type: `moyro/plugin-rhs/${type}`, payload: { id } });
}

export function createRegistry(pluginId: string) {
  return {
    registerMainMenuAction(text: string, action: () => void) {
      const id = registrationID(pluginId, "main_menu");
      append("mainMenuActions", { id, pluginId, text, action });
      return id;
    },
    registerChannelHeaderButtonAction(
      iconOrDefinition: ReactResolvable | HeaderButtonObject,
      action?: (...args: unknown[]) => void,
      dropdownText = "",
      tooltipText?: string,
    ) {
      const definition = normalizeHeaderButton(iconOrDefinition, action, dropdownText, tooltipText);
      const id = registrationID(pluginId, "channel_header");
      append("channelHeaderButtons", { id, pluginId, ...definition });
      return id;
    },
    registerAdminConsoleCustomSetting(
      keyOrDefinition: string | AdminSettingObject,
      component?: ReactResolvable,
      options: { showTitle?: boolean } = {},
    ) {
      const definition = normalizeAdminSetting(keyOrDefinition, component, options);
      const id = registrationID(pluginId, "admin_setting");
      append("adminConsoleCustomSettings", {
        id, pluginId, key: definition.key, component: definition.component, options: definition.options ?? {},
      });
      return id;
    },
    registerAdminConsolePlugin(callbackOrDefinition:
      | ((config: Record<string, unknown>) => void)
      | { component: (config: Record<string, unknown>) => void }) {
      const callback = typeof callbackOrDefinition === "function"
        ? callbackOrDefinition
        : callbackOrDefinition.component;
      if (typeof callback !== "function") throw new Error("admin console plugin callback is required");
      const id = registrationID(pluginId, "admin_console");
      append("adminConsolePlugins", { id, pluginId, callback });
      return id;
    },
    registerUserSettings(definitionOrWrapper: Record<string, unknown>) {
      const definition = ("setting" in definitionOrWrapper
        ? definitionOrWrapper.setting
        : definitionOrWrapper) as Record<string, unknown>;
      const sections = Array.isArray(definition?.sections)
        ? definition.sections.flatMap((candidate): PluginUserSettingSection[] => {
          if (!candidate || typeof candidate !== "object") return [];
          const section = candidate as Record<string, unknown>;
          if (typeof section.title !== "string" || !section.component) return [];
          return [{ title: section.title, component: section.component as ReactResolvable }];
        })
        : [];
      if (sections.length === 0) throw new Error("user settings sections are required");
      const id = registrationID(pluginId, "user_settings");
      append("userSettings", {
        id,
        pluginId,
        settingsId: typeof definition.id === "string" ? definition.id : pluginId,
        uiName: typeof definition.uiName === "string" ? definition.uiName : pluginId,
        sections,
      });
      return id;
    },
    registerPostTypeComponent(postType: string, component: ComponentType<{ post: unknown }>) {
      const id = registrationID(pluginId, "post_type");
      append("postTypeComponents", { id, pluginId, postType, component });
      return id;
    },
    registerRightHandSidebarComponent(componentOrDefinition: ReactResolvable | RhsObject, title?: ReactResolvable) {
      const definition = normalizeRhs(componentOrDefinition, title);
      const id = registrationID(pluginId, "rhs");
      append("rhsComponents", { id, pluginId, ...definition });
      return {
        id,
        showRHSPlugin: rhsAction("show", id),
        hideRHSPlugin: rhsAction("hide", id),
        toggleRHSPlugin: rhsAction("toggle", id),
      };
    },
    registerWebSocketEventHandler(
      eventOrDefinition: string | WebSocketHandlerObject,
      handler?: (message: PluginWebSocketMessage) => void,
    ) {
      const definition = normalizeWebSocketHandler(eventOrDefinition, handler);
      const id = registrationID(pluginId, "websocket");
      append("webSocketHandlers", { id, pluginId, ...definition });
      return id;
    },
    unregisterWebSocketEventHandler(event: string, handler?: (message: PluginWebSocketMessage) => void) {
      const next = mutableState();
      next.webSocketHandlers = next.webSocketHandlers.filter((entry) => (
        entry.pluginId !== pluginId || entry.event !== event || (handler && entry.handler !== handler)
      ));
      publish(next);
    },
    unregisterAll() {
      const removedRhsIDs = new Set(
        state.rhsComponents.filter((entry) => entry.pluginId === pluginId).map((entry) => entry.id),
      );
      const next = mutableState();
      next.mainMenuActions = next.mainMenuActions.filter((entry) => entry.pluginId !== pluginId);
      next.channelHeaderButtons = next.channelHeaderButtons.filter((entry) => entry.pluginId !== pluginId);
      next.postTypeComponents = next.postTypeComponents.filter((entry) => entry.pluginId !== pluginId);
      next.rhsComponents = next.rhsComponents.filter((entry) => entry.pluginId !== pluginId);
      next.adminConsoleCustomSettings = next.adminConsoleCustomSettings.filter((entry) => entry.pluginId !== pluginId);
      next.adminConsolePlugins = next.adminConsolePlugins.filter((entry) => entry.pluginId !== pluginId);
      next.userSettings = next.userSettings.filter((entry) => entry.pluginId !== pluginId);
      next.webSocketHandlers = next.webSocketHandlers.filter((entry) => entry.pluginId !== pluginId);
      if (next.activeRhsComponentId && removedRhsIDs.has(next.activeRhsComponentId)) next.activeRhsComponentId = null;
      const changed = (Object.keys(next) as (keyof MutableRegistryState)[]).some((key) => (
        key === "activeRhsComponentId" ? next[key] !== state[key] : next[key].length !== state[key].length
      ));
      if (changed) publish(next);
    },
  };
}

export function applyPluginRegistryAction(action: unknown): boolean {
  if (!action || typeof action !== "object") return false;
  const candidate = action as { type?: unknown; payload?: { id?: unknown } };
  if (typeof candidate.type !== "string" || !candidate.type.startsWith("moyro/plugin-rhs/")) return false;
  const id = typeof candidate.payload?.id === "string" ? candidate.payload.id : "";
  if (!id || !state.rhsComponents.some((entry) => entry.id === id)) return true;
  const next = mutableState();
  if (candidate.type.endsWith("/show")) next.activeRhsComponentId = id;
  else if (candidate.type.endsWith("/hide")) {
    if (next.activeRhsComponentId === id) next.activeRhsComponentId = null;
  } else if (candidate.type.endsWith("/toggle")) {
    next.activeRhsComponentId = next.activeRhsComponentId === id ? null : id;
  }
  if (next.activeRhsComponentId !== state.activeRhsComponentId) publish(next);
  return true;
}

export function hideActivePluginRHS(): void {
  if (!state.activeRhsComponentId) return;
  const next = mutableState();
  next.activeRhsComponentId = null;
  publish(next);
}

export function dispatchPluginWebSocketEvent(message: PluginWebSocketMessage): void {
  if (!message.event) return;
  for (const registration of state.webSocketHandlers) {
    if (registration.event !== message.event) continue;
    try {
      registration.handler(message);
    } catch (error) {
      console.error(`plugin websocket handler failed for ${registration.pluginId}`, error);
    }
  }
}

export function localizePluginAdminSchema(
  pluginId: string,
  schema: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  if (!schema) return schema;
  let clone: Record<string, unknown>;
  try {
    clone = structuredClone(schema);
  } catch {
    clone = JSON.parse(JSON.stringify(schema)) as Record<string, unknown>;
  }
  for (const registration of state.adminConsolePlugins) {
    if (registration.pluginId !== pluginId) continue;
    try {
      registration.callback(clone);
    } catch (error) {
      console.error(`plugin admin localization failed for ${pluginId}`, error);
    }
  }
  return clone;
}

export function getRegistryState(): RegistryState { return state; }
export function subscribeRegistry(listener: RegistryListener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
export function usePluginRegistryState(): RegistryState {
  return useSyncExternalStore(subscribeRegistry, getRegistryState, getRegistryState);
}
