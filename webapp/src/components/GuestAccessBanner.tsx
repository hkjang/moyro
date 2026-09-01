import { Alert, Box, Chip, Stack, Typography } from "@mui/material";
import { useEffect, useState } from "react";
import { useSelector } from "react-redux";
import type { RootState } from "@/store";

function hasGuestRole(roles = ""): boolean {
  return roles.split(/\s+/).includes("system_guest");
}

export function GuestAccessBanner() {
  const user = useSelector((state: RootState) => state.auth.user);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!hasGuestRole(user?.roles)) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, [user?.roles]);

  if (!hasGuestRole(user?.roles)) return null;
  const expiresAt = Number(user?.guest_expires_at ?? 0);
  const expired = expiresAt <= now;

  return (
    <>
      <Box
        aria-hidden
        sx={{
          position: "fixed",
          inset: 0,
          zIndex: 19,
          pointerEvents: "none",
          display: "grid",
          placeItems: "center",
          overflow: "hidden",
          color: "text.primary",
          opacity: 0.055,
          fontSize: { xs: "1.8rem", md: "3rem" },
          fontWeight: 800,
          letterSpacing: "0.12em",
          transform: "rotate(-24deg)",
          whiteSpace: "nowrap",
          userSelect: "none",
        }}
      >
        외부 게스트 · {user?.username || "guest"}
      </Box>
      <Alert
        severity={expired ? "error" : "warning"}
        variant="filled"
        icon={false}
        sx={{ borderRadius: 0, py: 0.5, position: "sticky", top: 0, zIndex: 20 }}
        role="status"
      >
        <Stack direction={{ xs: "column", sm: "row" }} sx={{ alignItems: { sm: "center" }, gap: 1 }}>
          <Chip size="small" label="외부 게스트" color="default" />
          <Typography variant="body2" sx={{ flex: 1 }}>
            초대된 채널에서만 협업할 수 있습니다. {expired
              ? "게스트 접근 시간이 만료되었습니다."
              : `${new Date(expiresAt).toLocaleString("ko-KR")}까지 접근할 수 있습니다.`}
          </Typography>
          {user?.guest_file_download === false && <Chip size="small" label="원본 다운로드 제한" />}
        </Stack>
      </Alert>
    </>
  );
}
