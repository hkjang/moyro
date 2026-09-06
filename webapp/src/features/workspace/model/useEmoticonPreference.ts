import { useCallback, useEffect, useState } from "react";
import { prefsApi } from "@/api/client";

const CATEGORY = "display_settings";
const NAME = "emoticons";

/**
 * Per-user emoticon switch, stored as a Mattermost-shaped preference so it
 * follows the account across devices. Enabled by default: a missing
 * preference means the picker shows and received emoticons draw as images.
 * Disabled means neither — the caption text stands in for the image.
 */
export function useEmoticonPreference(token: string | null, userId: string | undefined) {
  const [enabled, setEnabled] = useState(true);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    if (!token || !userId) return;
    let active = true;
    prefsApi.listCategory(token, CATEGORY, userId).then(
      (preferences) => {
        if (!active) return;
        const saved = preferences.find((preference) => preference.name === NAME)?.value;
        setEnabled(saved !== "off");
      },
      () => { /* keep the default; the API being briefly away must not hide emoticons */ },
    ).finally(() => { if (active) setLoaded(true); });
    return () => { active = false; };
  }, [token, userId]);

  const update = useCallback(async (next: boolean) => {
    setEnabled(next);
    if (!token || !userId) return;
    await prefsApi.upsert(token, [{ user_id: userId, category: CATEGORY, name: NAME, value: next ? "on" : "off" }], userId);
  }, [token, userId]);

  return { enabled, loaded, setEnabled: update };
}
