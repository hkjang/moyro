import { useEffect, type Dispatch, type SetStateAction } from "react";

export function useTypingExpiry(
  setTypingUsers: Dispatch<SetStateAction<Record<string, number>>>,
) {
  useEffect(() => {
    const timer = window.setInterval(() => {
      const cutoff = Date.now() - 4_000;
      setTypingUsers((current) => {
        const active: Record<string, number> = {};
        let changed = false;
        for (const [userID, timestamp] of Object.entries(current)) {
          if (timestamp > cutoff) active[userID] = timestamp;
          else changed = true;
        }
        return changed ? active : current;
      });
    }, 1_500);
    return () => window.clearInterval(timer);
  }, [setTypingUsers]);
}
