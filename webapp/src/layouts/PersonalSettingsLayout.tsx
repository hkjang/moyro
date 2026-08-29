import ArrowBackRounded from "@mui/icons-material/ArrowBackRounded";
import AssignmentTurnedInRounded from "@mui/icons-material/AssignmentTurnedInRounded";
import FactCheckRounded from "@mui/icons-material/FactCheckRounded";
import KeyRounded from "@mui/icons-material/KeyRounded";
import NotificationsRounded from "@mui/icons-material/NotificationsRounded";
import PaletteRounded from "@mui/icons-material/PaletteRounded";
import PersonRounded from "@mui/icons-material/PersonRounded";
import PsychologyRounded from "@mui/icons-material/PsychologyRounded";
import SecurityRounded from "@mui/icons-material/SecurityRounded";
import {
  Box,
  Divider,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Paper,
  Stack,
  Typography,
} from "@mui/material";
import { useSelector } from "react-redux";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";
import { BrandMark } from "@/components/brand/BrandMark";

const personalNavigation = [
  { to: "/settings/profile", label: "프로필", icon: <PersonRounded />, permission: "" },
  { to: "/settings/appearance", label: "화면", icon: <PaletteRounded />, permission: "" },
  { to: "/settings/notifications", label: "알림", icon: <NotificationsRounded />, permission: "" },
  { to: "/settings/security/sessions", label: "보안 · 세션", icon: <SecurityRounded />, permission: "" },
  { to: "/settings/developer/keys", label: "개인 키", icon: <KeyRounded />, permission: "manage_own_api_keys" },
  { to: "/settings/ai", label: "AI 개인화", icon: <PsychologyRounded />, permission: "use_ai" },
];

const approvalNavigation = [
  { to: "/settings/approvals/mine", label: "내 승인 요청", icon: <AssignmentTurnedInRounded />, permission: "request_approval" },
  { to: "/settings/approvals/review", label: "검토 대기", icon: <FactCheckRounded />, permission: "review_approval" },
];

export function PersonalSettingsLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const user = useSelector((state: RootState) => state.auth.user);
  const info = useSystemInfo();
  const access = useAdminAccess();
  const navigation = (info.approval_enabled
    ? [...personalNavigation, ...approvalNavigation]
    : personalNavigation).filter((item) => !item.permission || access.can(item.permission));

  return (
    <Box className="moyro-settings-shell personal-settings-shell">
      <Paper component="aside" square elevation={0} className="moyro-settings-sidebar">
        <Stack direction="row" sx={{ px: 2, py: 2, alignItems: "center", gap: 1.25 }}>
          <BrandMark className="moyro-mark" size={40} title="moyro" />
          <Box sx={{ minWidth: 0 }}>
            <Typography variant="subtitle1">내 설정</Typography>
            <Typography variant="caption" color="text.secondary" noWrap>{user?.username ?? "사용자"}</Typography>
          </Box>
        </Stack>
        <Divider />
        <Box component="nav" aria-label="개인 설정 메뉴" className="moyro-scrollbar moyro-settings-nav">
          <List disablePadding sx={{ p: 1 }}>
            {navigation.map((item) => (
              <ListItemButton
                key={item.to}
                selected={location.pathname === item.to}
                onClick={() => navigate(item.to)}
                sx={{ borderRadius: 1, my: 0.35 }}
              >
                <ListItemIcon sx={{ minWidth: 38 }}>{item.icon}</ListItemIcon>
                <ListItemText primary={item.label} />
              </ListItemButton>
            ))}
          </List>
        </Box>
        <Divider />
        <Typography variant="caption" color="text.secondary" sx={{ p: 2 }}>
          moyro {displayVersion(info.version)}
        </Typography>
      </Paper>

      <Box component="section" className="moyro-settings-main">
        <Paper component="header" square elevation={0} className="moyro-settings-header">
          <IconButton aria-label="워크스페이스로 돌아가기" onClick={() => navigate("/workspace")}>
            <ArrowBackRounded />
          </IconButton>
          <Box>
            <Typography variant="subtitle1">개인화</Typography>
            <Typography variant="caption" color="text.secondary">내 계정에만 적용되는 설정</Typography>
          </Box>
        </Paper>
        <Box className="moyro-scrollbar moyro-settings-content">
          <Outlet />
        </Box>
      </Box>
    </Box>
  );
}
