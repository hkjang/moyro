import { useEffect, useState } from "react";
import {
  DEFAULT_INBOX_PREFERENCES,
  inboxPreferencesApi,
  type InboxPreferences,
} from "@/api/inbox-preferences";

export function useInboxPreferences(token: string | null): InboxPreferences {
  const [preferences, setPreferences] = useState<InboxPreferences>(DEFAULT_INBOX_PREFERENCES);

  useEffect(() => {
    if (!token) {
      setPreferences(DEFAULT_INBOX_PREFERENCES);
      return undefined;
    }
    const controller = new AbortController();
    void inboxPreferencesApi.get(token, controller.signal).then(setPreferences).catch(() => undefined);
    return () => controller.abort();
  }, [token]);

  return preferences;
}
