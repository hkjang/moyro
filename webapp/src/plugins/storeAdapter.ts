import type { RootState } from "@/store";
import { applyPluginRegistryAction, getRegistryState, subscribeRegistry } from "./registry";

type NativeStore = {
  getState(): RootState;
  dispatch(action: unknown): unknown;
  subscribe(listener: () => void): () => void;
};

type MattermostRecord = Record<string, unknown>;
export type MattermostPreference = {
  user_id: string;
  category: string;
  name: string;
  value: string;
};

export type MattermostPluginContext = {
  teams?: readonly MattermostRecord[];
  currentTeamId?: string | null;
  users?: Record<string, MattermostRecord>;
  posts?: readonly MattermostRecord[];
  preferences?: readonly MattermostPreference[];
  selectedPostId?: string | null;
};

export type MattermostPluginState = {
  entities: {
    general: { config: Record<string, string>; license: Record<string, string> };
    users: { currentUserId: string; profiles: Record<string, MattermostRecord> };
    teams: { currentTeamId: string; teams: Record<string, MattermostRecord> };
    channels: {
      channels: RootState["channels"]["byId"];
      currentChannelId: string;
      membersInChannel: Record<string, MattermostRecord>;
    };
    posts: { posts: Record<string, MattermostRecord> };
    preferences: {
      myPreferences: Record<string, MattermostPreference>;
      userPreferences: Record<string, Record<string, MattermostPreference>>;
    };
  };
  views: {
    rhs: {
      isSidebarOpen: boolean;
      pluginId: string;
      selectedPostId?: string;
    };
  };
};

export type MattermostStoreAdapter = {
  getState(): MattermostPluginState;
  dispatch(action: unknown): unknown;
  subscribe(listener: () => void): () => void;
  updateContext(context: MattermostPluginContext): void;
};

function identifier(record: MattermostRecord): string {
  return typeof record.id === "string" ? record.id : "";
}

function preferenceKey(preference: Pick<MattermostPreference, "category" | "name">): string {
  return `${preference.category}--${preference.name}`;
}

function recordsByID(records: readonly MattermostRecord[]): Record<string, MattermostRecord> {
  const result: Record<string, MattermostRecord> = {};
  for (const record of records) {
    const id = identifier(record);
    if (id) result[id] = record;
  }
  return result;
}

function preferenceList(value: unknown): MattermostPreference[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((candidate): MattermostPreference[] => {
    if (!candidate || typeof candidate !== "object") return [];
    const record = candidate as Record<string, unknown>;
    if (
      typeof record.user_id !== "string" || typeof record.category !== "string" ||
      typeof record.name !== "string" || typeof record.value !== "string"
    ) return [];
    return [record as MattermostPreference];
  });
}

/**
 * Presents the Mattermost GlobalState subset consumed by starter-template
 * web bundles. It also executes Redux thunks from bundled mattermost-redux,
 * while the authenticated fetch facade keeps their /api/v4 calls scoped to
 * Moyro's current same-origin session.
 */
export function createMattermostStoreAdapter(
  nativeStore: NativeStore,
  siteURL: () => string = () => window.location.origin,
): MattermostStoreAdapter {
  const listeners = new Set<() => void>();
  let context: MattermostPluginContext = {};
  let contextRevision = 0;
  let cachedNativeState: RootState | null = null;
  let cachedRevision = -1;
  let cachedRegistryRevision = "";
  let cachedState: MattermostPluginState | null = null;
  let shadowPosts: Record<string, MattermostRecord> = {};
  let preferences: Record<string, MattermostPreference> = {};

  const notify = () => {
    cachedState = null;
    for (const listener of [...listeners]) listener();
  };
  nativeStore.subscribe(notify);
  subscribeRegistry(notify);

  function getState(): MattermostPluginState {
    const native = nativeStore.getState();
    const registry = getRegistryState();
    const registryRevision = `${registry.activeRhsComponentId ?? ""}:${registry.rhsComponents.length}`;
    if (
      cachedState && cachedNativeState === native && cachedRevision === contextRevision &&
      cachedRegistryRevision === registryRevision
    ) return cachedState;

    const authUser = native.auth.user;
    const currentUserId = authUser?.id ?? "";
    const locale = document.documentElement.lang || navigator.language || "en";
    const profiles: Record<string, MattermostRecord> = { ...(context.users ?? {}) };
    if (authUser) {
      profiles[authUser.id] = {
        ...authUser,
        locale: (authUser as unknown as MattermostRecord).locale ?? locale,
      };
    }
    const teams = recordsByID(context.teams ?? []);
    const activeRhs = registry.rhsComponents.find((entry) => entry.id === registry.activeRhsComponentId);
    cachedNativeState = native;
    cachedRevision = contextRevision;
    cachedRegistryRevision = registryRevision;
    cachedState = {
      entities: {
        general: {
          config: {
            SiteURL: siteURL(),
            SiteName: "Moyro",
            EnableUserStatuses: "true",
          },
          license: {},
        },
        users: { currentUserId, profiles },
        teams: { currentTeamId: context.currentTeamId ?? "", teams },
        channels: {
          channels: native.channels.byId,
          currentChannelId: native.channels.currentId ?? "",
          membersInChannel: {},
        },
        posts: { posts: shadowPosts },
        preferences: { myPreferences: preferences, userPreferences: {} },
      },
      views: {
        rhs: {
          isSidebarOpen: Boolean(activeRhs),
          pluginId: activeRhs?.pluginId ?? "",
          selectedPostId: context.selectedPostId ?? undefined,
        },
      },
    };
    return cachedState;
  }

  function dispatch(action: unknown): unknown {
    if (typeof action === "function") {
      return (action as (
        dispatchAction: (next: unknown) => unknown,
        getPluginState: () => MattermostPluginState,
      ) => unknown)(dispatch, getState);
    }
    if (applyPluginRegistryAction(action)) return action;
    if (!action || typeof action !== "object") return action;
    const candidate = action as { type?: unknown; data?: unknown };
    const type = typeof candidate.type === "string" ? candidate.type : "";

    if (type === "RECEIVED_POST" || type === "RECEIVED_NEW_POST") {
      if (candidate.data && typeof candidate.data === "object") {
        const post = candidate.data as MattermostRecord;
        const id = identifier(post);
        if (id) {
          shadowPosts = { ...shadowPosts, [id]: post };
          contextRevision += 1;
          notify();
          if (typeof window !== "undefined") {
            window.dispatchEvent(new CustomEvent("moyro:plugin-post-updated", { detail: post }));
          }
        }
      }
      return action;
    }

    if (type === "RECEIVED_PREFERENCES" || type === "RECEIVED_ALL_PREFERENCES") {
      const next = type === "RECEIVED_ALL_PREFERENCES" ? {} : { ...preferences };
      for (const preference of preferenceList(candidate.data)) next[preferenceKey(preference)] = preference;
      preferences = next;
      contextRevision += 1;
      notify();
      return action;
    }
    if (type === "DELETED_PREFERENCES") {
      const next = { ...preferences };
      for (const preference of preferenceList(candidate.data)) delete next[preferenceKey(preference)];
      preferences = next;
      contextRevision += 1;
      notify();
      return action;
    }

    return nativeStore.dispatch(action);
  }

  return {
    getState,
    dispatch,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    updateContext(next) {
      context = { ...context, ...next };
      if (next.posts) shadowPosts = recordsByID(next.posts);
      if (next.preferences) {
        preferences = Object.fromEntries(next.preferences.map((preference) => [preferenceKey(preference), preference]));
      }
      contextRevision += 1;
      notify();
    },
  };
}
