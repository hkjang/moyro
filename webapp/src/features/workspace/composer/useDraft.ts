import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useSystemInfo } from "@/features/system/SystemInfoContext";

export const DEFAULT_DRAFT_TTL_MS = 7 * 24 * 60 * 60 * 1_000;
export const DRAFT_CLEANUP_INTERVAL_MS = 60 * 60 * 1_000;

const DRAFT_STORAGE_VERSION = 1;

type StoredDraftEnvelope = {
  version: typeof DRAFT_STORAGE_VERSION;
  value: string;
  updated_at: number;
};

type LoadedDraft = {
  value: string;
  updatedAt: number;
  hasSaved: boolean;
};

function removeStoredDraft(key: string, storage: Storage): boolean {
  try {
    storage.removeItem(key);
    return true;
  } catch {
    return false;
  }
}

function persistStoredDraft(key: string, value: string, updatedAt: number, storage: Storage): boolean {
  if (!value.trim()) return removeStoredDraft(key, storage);
  const envelope: StoredDraftEnvelope = {
    version: DRAFT_STORAGE_VERSION,
    value,
    updated_at: updatedAt,
  };
  try {
    storage.setItem(key, JSON.stringify(envelope));
    return true;
  } catch {
    return false;
  }
}

function loadStoredDraft(key: string, storage: Storage, ttlMS: number, now = Date.now()): LoadedDraft {
  const empty = { value: "", updatedAt: now, hasSaved: false };
  let raw: string | null;
  try {
    raw = storage.getItem(key);
  } catch {
    return empty;
  }
  if (raw === null) return empty;

  let decoded: unknown;
  try {
    decoded = JSON.parse(raw);
  } catch {
    decoded = null;
  }

  if (decoded && typeof decoded === "object" && !Array.isArray(decoded)) {
    const candidate = decoded as Partial<StoredDraftEnvelope>;
    const looksLikeEnvelope = "version" in candidate || "updated_at" in candidate;
    if (looksLikeEnvelope) {
      const storedValue = candidate.value;
      const storedUpdatedAt = candidate.updated_at;
      if (
        candidate.version !== DRAFT_STORAGE_VERSION
        || typeof storedValue !== "string"
        || typeof storedUpdatedAt !== "number"
        || !Number.isFinite(storedUpdatedAt)
        || storedUpdatedAt <= 0
        || !storedValue.trim()
      ) {
        removeStoredDraft(key, storage);
        return empty;
      }
      if (now - storedUpdatedAt >= ttlMS) {
        removeStoredDraft(key, storage);
        return empty;
      }
      return {
        value: storedValue,
        updatedAt: storedUpdatedAt,
        hasSaved: true,
      };
    }
  }

  // Releases before the TTL envelope stored the draft as a plain string.
  // Preserve it and migrate in place so the configured retention policy
  // applies from this first v1 read.
  if (!raw.trim()) {
    removeStoredDraft(key, storage);
    return empty;
  }
  persistStoredDraft(key, raw, now, storage);
  return { value: raw, updatedAt: now, hasSaved: true };
}

function browserDraftStorages(): Storage[] {
  if (typeof window === "undefined") return [];
  const storages: Storage[] = [];
  for (const name of ["localStorage", "sessionStorage"] as const) {
    try {
      storages.push(window[name]);
    } catch {
      // Some privacy modes make the Storage getter itself throw.
    }
  }
  return storages;
}

function draftPrefixesForKey(key: string): string[] {
  const editMatch = /^moyro:draft:edit:([^:]+):/.exec(key);
  const messageMatch = /^moyro:draft:([^:]+):/.exec(key);
  const userID = editMatch?.[1] ?? messageMatch?.[1] ?? "";
  if (!userID) return [];
  return [`moyro:draft:${userID}:`, `moyro:draft:edit:${userID}:`];
}

function matchingDraftKeys(storage: Storage, prefixes: string[]): string[] {
  const keys: string[] = [];
  try {
    for (let index = 0; index < storage.length; index += 1) {
      const candidate = storage.key(index);
      if (candidate && prefixes.some((prefix) => candidate.startsWith(prefix))) keys.push(candidate);
    }
  } catch {
    return [];
  }
  return keys;
}

function cleanupExpiredDraftsForKey(key: string, ttlMS: number, now = Date.now()): void {
  const prefixes = draftPrefixesForKey(key);
  if (prefixes.length === 0) return;
  for (const storage of browserDraftStorages()) {
    for (const candidate of matchingDraftKeys(storage, prefixes)) {
      // Loading validates/removes an expired or malformed envelope and
      // migrates a legacy plain-string draft into the bounded format.
      loadStoredDraft(candidate, storage, ttlMS, now);
    }
  }
}

/** Remove every persisted copy of one draft, independent of active policy. */
export function clearMoyroDraft(key: string): void {
  if (!key) return;
  for (const storage of browserDraftStorages()) removeStoredDraft(key, storage);
}

export type DraftController = {
  hasSaved: boolean;
  stage: (value: string) => void;
  flush: () => void;
  clear: () => void;
  clearSaved: () => void;
};

export function clearMoyroDraftsForUser(userID: string): void {
  const trimmedUserID = userID.trim();
  if (!trimmedUserID || typeof window === "undefined") return;
  const prefixes = [
    `moyro:draft:${trimmedUserID}:`,
    `moyro:draft:edit:${trimmedUserID}:`,
  ];
  for (const storage of browserDraftStorages()) {
    try {
      matchingDraftKeys(storage, prefixes).forEach((candidate) => storage.removeItem(candidate));
    } catch {
      // Browser storage is a best-effort convenience; logout must continue.
    }
  }
}

function clearAllMoyroDrafts(): void {
  if (typeof window === "undefined") return;
  for (const storage of browserDraftStorages()) {
    const keys: string[] = [];
    try {
      for (let index = 0; index < storage.length; index += 1) {
        const candidate = storage.key(index);
        if (candidate?.startsWith("moyro:draft:")) keys.push(candidate);
      }
      keys.forEach((candidate) => storage.removeItem(candidate));
    } catch {
      // Fail closed for future writes even if a browser blocks removal.
    }
  }
}

/**
 * Debounced, policy-selected browser draft persistence for a controlled text
 * value. A null key disables reads and writes, which keeps unauthenticated or
 * channel-less composer state out of browser storage.
 */
export function useDraft(
  key: string | null,
  value: string,
  setValue: (value: string) => void,
  defaultValue = "",
): DraftController {
  const systemInfo = useSystemInfo();
  const draftPolicy = systemInfo.capabilities?.drafts;
  const storageMode = draftPolicy?.storage_mode ?? "local";
  const retentionDays = Math.min(30, Math.max(1, draftPolicy?.retention_days ?? 7));
  const ttlMS = retentionDays * 24 * 60 * 60 * 1_000;
  const storage = typeof window === "undefined" || storageMode === "disabled"
    ? null
    : storageMode === "session" ? window.sessionStorage : window.localStorage;
  const storageKey = storage ? key : null;
  const [hasSaved, setHasSaved] = useState(false);
  const defaultValueRef = useRef(defaultValue);
  const hydrationRef = useRef<{
    key: string | null;
    value: string;
    pending: boolean;
  }>({ key: null, value: "", pending: false });
  const persistenceTimerRef = useRef<number | null>(null);
  const suppressedFlushRef = useRef<{ key: string; value: string } | null>(null);
  const activeDraftRef = useRef<{ key: string | null; value: string; updatedAt: number }>({
    key: storageKey,
    value,
    updatedAt: Date.now(),
  });
  defaultValueRef.current = defaultValue;
  // A destination change renders once with the new key and the previous
  // controlled value. Keep that value associated with the last committed key
  // until the layout effect below has had a chance to persist it.
  if (activeDraftRef.current.key === storageKey) {
    const suppressed = suppressedFlushRef.current;
    if (!suppressed || suppressed.key !== storageKey || suppressed.value !== value) {
      if (suppressed?.key === storageKey) suppressedFlushRef.current = null;
      if (activeDraftRef.current.value !== value) activeDraftRef.current.updatedAt = Date.now();
      activeDraftRef.current.value = value;
    }
  }

  // Hydrate before paint so a destination change cannot briefly expose the
  // previous channel's draft. The persistence effect below is gated until
  // this exact value has reached the controlled textarea.
  useLayoutEffect(() => {
    if (persistenceTimerRef.current !== null) {
      window.clearTimeout(persistenceTimerRef.current);
      persistenceTimerRef.current = null;
    }
    if (!storageKey || !storage) {
      if (storageMode === "disabled") clearAllMoyroDrafts();
      hydrationRef.current = { key: null, value: "", pending: false };
      activeDraftRef.current = { key: null, value: "", updatedAt: Date.now() };
      suppressedFlushRef.current = null;
      setHasSaved(false);
      return;
    }
    cleanupExpiredDraftsForKey(storageKey, ttlMS);
    const otherStorage = storage === window.localStorage ? window.sessionStorage : window.localStorage;
    removeStoredDraft(storageKey, otherStorage);
    const loaded = loadStoredDraft(storageKey, storage, ttlMS);
    const nextValue = loaded.hasSaved ? loaded.value : defaultValueRef.current;
    hydrationRef.current = { key: storageKey, value: nextValue, pending: true };
    activeDraftRef.current = {
      key: storageKey,
      value: nextValue,
      updatedAt: loaded.hasSaved ? loaded.updatedAt : Date.now(),
    };
    suppressedFlushRef.current = null;
    setValue(nextValue);
    setHasSaved(loaded.hasSaved);

    return () => {
      if (persistenceTimerRef.current !== null) {
        window.clearTimeout(persistenceTimerRef.current);
        persistenceTimerRef.current = null;
      }
      const activeDraft = activeDraftRef.current;
      if (activeDraft.key !== storageKey) return;
      persistStoredDraft(storageKey, activeDraft.value, activeDraft.updatedAt, storage);
    };
  }, [setValue, storage, storageKey, storageMode, ttlMS]);

  useEffect(() => {
    if (!storageKey || !storage) return;
    const timer = window.setInterval(() => {
      cleanupExpiredDraftsForKey(storageKey, ttlMS);
      setHasSaved(loadStoredDraft(storageKey, storage, ttlMS).hasSaved);
    }, DRAFT_CLEANUP_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [storage, storageKey, ttlMS]);

  useEffect(() => {
    if (!storageKey || !storage) return;
    const hydration = hydrationRef.current;
    if (hydration.key !== storageKey) return;
    if (hydration.pending) {
      if (value !== hydration.value) return;
      hydration.pending = false;
      return;
    }
    const timer = window.setTimeout(() => {
      persistenceTimerRef.current = null;
      const activeDraft = activeDraftRef.current;
      const updatedAt = activeDraft.key === storageKey && activeDraft.value === value
        ? activeDraft.updatedAt
        : Date.now();
      if (value.trim()) {
        setHasSaved(persistStoredDraft(storageKey, value, updatedAt, storage));
      } else {
        removeStoredDraft(storageKey, storage);
        setHasSaved(false);
      }
    }, 500);
    persistenceTimerRef.current = timer;
    return () => {
      window.clearTimeout(timer);
      if (persistenceTimerRef.current === timer) persistenceTimerRef.current = null;
    };
  }, [storage, storageKey, value]);

  const clearSaved = useCallback(() => {
    if (persistenceTimerRef.current !== null) {
      window.clearTimeout(persistenceTimerRef.current);
      persistenceTimerRef.current = null;
    }
    if (storageKey && storage) {
      const activeDraft = activeDraftRef.current;
      const activeValue = activeDraft.key === storageKey ? activeDraft.value : "";
      suppressedFlushRef.current = { key: storageKey, value: activeValue };
      if (activeDraft.key === storageKey) {
        activeDraft.value = "";
        activeDraft.updatedAt = Date.now();
      }
      clearMoyroDraft(storageKey);
    }
    setHasSaved(false);
  }, [storage, storageKey]);

  // Controlled state normally reaches activeDraftRef on the next render.
  // A navigation click can unmount the composer in the same browser task as
  // the final input event, though, so let the input handler stage that value
  // synchronously while keeping the actual storage write debounced.
  const stage = useCallback((nextValue: string) => {
    const activeDraft = activeDraftRef.current;
    if (!storageKey || activeDraft.key !== storageKey) return;
    if (suppressedFlushRef.current?.key === storageKey) suppressedFlushRef.current = null;
    activeDraft.value = nextValue;
    activeDraft.updatedAt = Date.now();
  }, [storageKey]);

  const flush = useCallback(() => {
    const activeDraft = activeDraftRef.current;
    if (!storageKey || !storage || activeDraft.key !== storageKey) return;
    if (activeDraft.value.trim()) {
      // Persist synchronously so a navigation or unmount cannot lose the last
      // keystroke, but leave the visual status to the existing debounce. A
      // synchronous setHasSaved here can insert the draft badge during blur,
      // shifting an adjacent button between pointer-down and pointer-up and
      // swallowing the user's click (for example, an AI rewrite action).
      persistStoredDraft(storageKey, activeDraft.value, activeDraft.updatedAt, storage);
    } else {
      removeStoredDraft(storageKey, storage);
    }
  }, [storage, storageKey]);

  const clear = useCallback(() => {
    clearSaved();
    setValue("");
  }, [clearSaved, setValue]);

  return useMemo(
    () => ({ hasSaved, stage, flush, clear, clearSaved }),
    [clear, clearSaved, flush, hasSaved, stage],
  );
}
