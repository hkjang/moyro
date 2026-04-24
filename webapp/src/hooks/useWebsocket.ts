// useWebsocket: opens a WebSocket to the server and, on unexpected drop,
// reopens it with exponential backoff + jitter. Returns a stable `send`
// closure (dispatches through the current socket) and a status string
// ("connected" | "reconnecting" | "offline") so the UI can render a
// banner. `reconnectSeq` bumps on every *successful* reopen (not the
// initial connect) so callers can reconcile state — refetch posts,
// channels, unread counters — after coming back.
//
// The caller passes `onMessage` once; we attach it to every fresh
// socket automatically. `onClose` with code 4001 is treated as a
// user-initiated logout and suppresses the reconnect attempt.

import { useEffect, useRef, useState } from "react";
import { openWebSocket } from "../api/client";

export type WSStatus = "connected" | "reconnecting" | "offline";

export interface UseWebsocketResult {
  status: WSStatus;
  attempts: number;
  reconnectSeq: number;
  send: (data: string) => void;
}

/** Close code used when the React component unmounts (logout / token
 *  change). The reconnect logic checks this code and skips reopening. */
export const WS_CLOSE_LOGOUT = 4001;

export function useWebsocket(
  token: string | null,
  onMessage: (ev: MessageEvent) => void,
): UseWebsocketResult {
  const [status, setStatus] = useState<WSStatus>("offline");
  const [attempts, setAttempts] = useState(0);
  const [reconnectSeq, setReconnectSeq] = useState(0);

  const wsRef = useRef<WebSocket | null>(null);
  const retryTimerRef = useRef<number | null>(null);
  const attemptsRef = useRef(0);
  const tokenRef = useRef<string | null>(null);
  const onMessageRef = useRef(onMessage);
  const openedOnceRef = useRef(false); // tracks "have we ever connected?"
  const closedByUserRef = useRef(false);

  // Keep latest onMessage without re-running the effect — the user passes
  // a new handler on every render but the socket itself is stable.
  useEffect(() => { onMessageRef.current = onMessage; }, [onMessage]);

  useEffect(() => {
    tokenRef.current = token;
    closedByUserRef.current = false;
    if (!token) {
      setStatus("offline");
      return;
    }

    function clearRetry() {
      if (retryTimerRef.current != null) {
        window.clearTimeout(retryTimerRef.current);
        retryTimerRef.current = null;
      }
    }

    function open() {
      if (!tokenRef.current) return;
      const ws = openWebSocket(tokenRef.current);
      wsRef.current = ws;
      ws.addEventListener("open", () => {
        if (wsRef.current !== ws) return;
        attemptsRef.current = 0;
        setAttempts(0);
        setStatus("connected");
        if (openedOnceRef.current) {
          // Only bump on *re*opens so ChatView's reconciler doesn't fire
          // on the initial boot (where nothing needs reconciling).
          setReconnectSeq((n) => n + 1);
        }
        openedOnceRef.current = true;
      });
      ws.addEventListener("message", (ev) => {
        if (wsRef.current !== ws) return;
        onMessageRef.current(ev);
      });
      ws.addEventListener("close", (ev) => {
        if (wsRef.current !== ws) return;
        wsRef.current = null;
        if (closedByUserRef.current || ev.code === WS_CLOSE_LOGOUT) {
          setStatus("offline");
          return;
        }
        // Backoff: 1s, 2s, 4s, 8s, 16s, then capped at 30s. Jitter ±500ms
        // prevents thundering-herd when many clients reconnect together.
        const base = Math.min(1000 * 2 ** attemptsRef.current, 30000);
        const jitter = Math.random() * 500;
        attemptsRef.current += 1;
        setAttempts(attemptsRef.current);
        setStatus("reconnecting");
        clearRetry();
        retryTimerRef.current = window.setTimeout(open, base + jitter) as unknown as number;
      });
      ws.addEventListener("error", () => {
        // onerror always fires before onclose; let onclose drive the
        // backoff so we don't double-schedule.
      });
    }

    open();

    return () => {
      closedByUserRef.current = true;
      clearRetry();
      const ws = wsRef.current;
      wsRef.current = null;
      if (ws) {
        try { ws.close(WS_CLOSE_LOGOUT, "logout"); } catch { /* ignore */ }
      }
      openedOnceRef.current = false;
      setStatus("offline");
    };
  }, [token]);

  function send(data: string) {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    try { ws.send(data); } catch { /* socket just closed, drop */ }
  }

  return { status, attempts, reconnectSeq, send };
}
