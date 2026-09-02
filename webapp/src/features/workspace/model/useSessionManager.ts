import { useCallback, useState } from "react";
import { useDispatch } from "react-redux";
import { api, type SessionRow } from "@/api/client";
import { clearAuth } from "@/store/authSlice";
import { clearMoyroDraftsForUser } from "@/features/workspace/composer/useDraft";

/** Confirmation prompt the workspace already owns; passed in so this hook
 *  stays free of any dialog implementation. */
type Confirmer = {
  confirm: (options: {
    title: string;
    message: string;
    confirmLabel: string;
    destructive?: boolean;
  }) => Promise<boolean>;
};

export type SessionManager = {
  visible: boolean;
  sessions: SessionRow[];
  loading: boolean;
  open: () => void;
  close: () => void;
  revoke: (sessionId: string) => Promise<void>;
  revokeOthers: () => Promise<void>;
};

export type SessionManagerOptions = {
  token: string | null;
  userId: string | undefined;
  /** False when site policy keeps drafts after logout. */
  clearDraftsOnLogout: boolean;
  confirmer: Confirmer;
  onError: (message: string) => void;
};

/**
 * Owns the session-management modal: its visibility, the lazily fetched list,
 * and the two revoke actions.
 *
 * The list is fetched when the modal opens rather than kept in sync. It is
 * short-lived, and a stale row simply 404s on revoke, which is handled.
 */
export function useSessionManager({
  token,
  userId,
  clearDraftsOnLogout,
  confirmer,
  onError,
}: SessionManagerOptions): SessionManager {
  const dispatch = useDispatch();
  const [visible, setVisible] = useState(false);
  const [sessions, setSessions] = useState<SessionRow[]>([]);
  const [loading, setLoading] = useState(false);

  const open = useCallback(() => {
    if (!token) return;
    setVisible(true);
    setLoading(true);
    api
      .listMySessions(token)
      .then((list) => {
        // Newest first so the current-device row typically sits up top.
        setSessions([...list].sort((a, b) => b.create_at - a.create_at));
      })
      .catch((e: unknown) => onError(e instanceof Error ? e.message : "세션 조회 실패"))
      .finally(() => setLoading(false));
  }, [token, onError]);

  const close = useCallback(() => setVisible(false), []);

  const revoke = useCallback(
    async (sessionId: string) => {
      if (!token) return;
      const ok = await confirmer.confirm({
        title: "세션 종료",
        message: "이 세션을 종료할까요?",
        confirmLabel: "종료",
        destructive: true,
      });
      if (!ok) return;
      // Read the row before the request so the local list can be trimmed
      // without racing the state update below.
      const killedCurrent = sessions.find((s) => s.id === sessionId)?.is_current;
      try {
        await api.revokeSession(token, sessionId);
        setSessions((prev) => prev.filter((s) => s.id !== sessionId));
        // Killing the current session means signing out locally, otherwise
        // the app would keep rendering against a credential the server has
        // already dropped.
        if (killedCurrent) {
          if (userId && clearDraftsOnLogout) clearMoyroDraftsForUser(userId);
          dispatch(clearAuth());
        }
      } catch (e) {
        onError(e instanceof Error ? e.message : "세션 종료 실패");
      }
    },
    [token, sessions, confirmer, userId, clearDraftsOnLogout, dispatch, onError],
  );

  const revokeOthers = useCallback(async () => {
    if (!token) return;
    const ok = await confirmer.confirm({
      title: "다른 기기 로그아웃",
      message: "다른 모든 기기에서 로그아웃할까요? 이 기기의 세션은 유지됩니다.",
      confirmLabel: "로그아웃",
      destructive: true,
    });
    if (!ok) return;
    try {
      await api.revokeOtherSessions(token);
      setSessions((prev) => prev.filter((s) => s.is_current));
    } catch (e) {
      onError(e instanceof Error ? e.message : "다른 세션 종료 실패");
    }
  }, [token, confirmer, onError]);

  return { visible, sessions, loading, open, close, revoke, revokeOthers };
}
