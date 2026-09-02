import { useEffect } from "react";

const BASE_TITLE = "moyro";

/**
 * Reflects unread state in the browser tab title so a reader working in
 * another tab notices activity without a system notification.
 *
 * Mentions take precedence — "(3) moyro" — over merely unread channels, which
 * show as "• moyro". The base title is restored when everything is read and
 * when the owning surface unmounts, so a stale badge never outlives the view
 * that knew about it.
 */
export function useUnreadTitle(mentionTotal: number, unreadChannels: number): void {
  useEffect(() => {
    document.title = mentionTotal > 0
      ? `(${mentionTotal}) ${BASE_TITLE}`
      : unreadChannels > 0
        ? `• ${BASE_TITLE}`
        : BASE_TITLE;
    return () => { document.title = BASE_TITLE; };
  }, [mentionTotal, unreadChannels]);
}
