import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

export type DraftController = {
  hasSaved: boolean;
  stage: (value: string) => void;
  flush: () => void;
  clear: () => void;
  clearSaved: () => void;
};

/**
 * Debounced local draft persistence for a controlled text value. A null key
 * disables both storage reads and writes, which keeps unauthenticated or
 * channel-less composer state out of shared browser storage.
 */
export function useDraft(
  key: string | null,
  value: string,
  setValue: (value: string) => void,
  defaultValue = "",
): DraftController {
  const [hasSaved, setHasSaved] = useState(false);
  const defaultValueRef = useRef(defaultValue);
  const hydrationRef = useRef<{
    key: string | null;
    value: string;
    pending: boolean;
  }>({ key: null, value: "", pending: false });
  const persistenceTimerRef = useRef<number | null>(null);
  const suppressedFlushRef = useRef<{ key: string; value: string } | null>(null);
  const activeDraftRef = useRef<{ key: string | null; value: string }>({ key, value });
  defaultValueRef.current = defaultValue;
  // A destination change renders once with the new key and the previous
  // controlled value. Keep that value associated with the last committed key
  // until the layout effect below has had a chance to persist it.
  if (activeDraftRef.current.key === key) {
    const suppressed = suppressedFlushRef.current;
    if (!suppressed || suppressed.key !== key || suppressed.value !== value) {
      if (suppressed?.key === key) suppressedFlushRef.current = null;
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
    if (!key) {
      hydrationRef.current = { key: null, value: "", pending: false };
      activeDraftRef.current = { key: null, value: "" };
      suppressedFlushRef.current = null;
      setHasSaved(false);
      return;
    }
    let nextValue = defaultValueRef.current;
    let savedExists = false;
    try {
      const saved = localStorage.getItem(key);
      if (saved !== null) {
        nextValue = saved;
        savedExists = saved.trim().length > 0;
      }
    } catch {
      // Storage is best-effort; the per-surface default remains authoritative.
    }
    hydrationRef.current = { key, value: nextValue, pending: true };
    activeDraftRef.current = { key, value: nextValue };
    suppressedFlushRef.current = null;
    setValue(nextValue);
    setHasSaved(savedExists);

    return () => {
      if (persistenceTimerRef.current !== null) {
        window.clearTimeout(persistenceTimerRef.current);
        persistenceTimerRef.current = null;
      }
      const activeDraft = activeDraftRef.current;
      if (activeDraft.key !== key) return;
      try {
        if (activeDraft.value.trim()) {
          localStorage.setItem(key, activeDraft.value);
        } else {
          localStorage.removeItem(key);
        }
      } catch {
        // Storage is best-effort; unmounts and destination changes continue.
      }
    };
  }, [key, setValue]);

  useEffect(() => {
    if (!key) return;
    const hydration = hydrationRef.current;
    if (hydration.key !== key) return;
    if (hydration.pending) {
      if (value !== hydration.value) return;
      hydration.pending = false;
      return;
    }
    const timer = window.setTimeout(() => {
      persistenceTimerRef.current = null;
      try {
        if (value.trim()) {
          localStorage.setItem(key, value);
          setHasSaved(true);
        } else {
          localStorage.removeItem(key);
          setHasSaved(false);
        }
      } catch {
        // Storage may be blocked or full; composing remains available.
      }
    }, 500);
    persistenceTimerRef.current = timer;
    return () => {
      window.clearTimeout(timer);
      if (persistenceTimerRef.current === timer) persistenceTimerRef.current = null;
    };
  }, [key, value]);

  const clearSaved = useCallback(() => {
    if (persistenceTimerRef.current !== null) {
      window.clearTimeout(persistenceTimerRef.current);
      persistenceTimerRef.current = null;
    }
    if (key) {
      const activeDraft = activeDraftRef.current;
      const activeValue = activeDraft.key === key ? activeDraft.value : "";
      suppressedFlushRef.current = { key, value: activeValue };
      if (activeDraft.key === key) activeDraft.value = "";
      try { localStorage.removeItem(key); } catch { /* storage is best-effort */ }
    }
    setHasSaved(false);
  }, [key]);

  // Controlled state normally reaches activeDraftRef on the next render.
  // A navigation click can unmount the composer in the same browser task as
  // the final input event, though, so let the input handler stage that value
  // synchronously while keeping the actual storage write debounced.
  const stage = useCallback((nextValue: string) => {
    const activeDraft = activeDraftRef.current;
    if (!key || activeDraft.key !== key) return;
    if (suppressedFlushRef.current?.key === key) suppressedFlushRef.current = null;
    activeDraft.value = nextValue;
  }, [key]);

  const flush = useCallback(() => {
    const activeDraft = activeDraftRef.current;
    if (!key || activeDraft.key !== key) return;
    try {
      if (activeDraft.value.trim()) {
        localStorage.setItem(key, activeDraft.value);
      } else {
        localStorage.removeItem(key);
      }
    } catch {
      // Storage is best-effort; losing focus must never block navigation.
    }
  }, [key]);

  const clear = useCallback(() => {
    clearSaved();
    setValue("");
  }, [clearSaved, setValue]);

  return useMemo(
    () => ({ hasSaved, stage, flush, clear, clearSaved }),
    [clear, clearSaved, flush, hasSaved, stage],
  );
}
