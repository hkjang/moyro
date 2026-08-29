import ArrowBackRounded from "@mui/icons-material/ArrowBackRounded";
import DashboardRounded from "@mui/icons-material/DashboardRounded";
import HubRounded from "@mui/icons-material/HubRounded";
import KeyRounded from "@mui/icons-material/KeyRounded";
import ManageAccountsRounded from "@mui/icons-material/ManageAccountsRounded";
import LanguageRounded from "@mui/icons-material/LanguageRounded";
import PsychologyRounded from "@mui/icons-material/PsychologyRounded";
import RuleRounded from "@mui/icons-material/RuleRounded";
import SecurityRounded from "@mui/icons-material/SecurityRounded";
import SettingsApplicationsRounded from "@mui/icons-material/SettingsApplicationsRounded";
import {
  Box,
  Chip,
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
import type { RootState } from "@/store";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import { BrandMark } from "@/components/brand/BrandMark";

const adminNavigation = [
  { to: "/admin/overview", label: "관리 개요", icon: <DashboardRounded />, anyOf: [] },
  { to: "/admin/site", label: "사이트 설정", icon: <LanguageRounded />, anyOf: ["manage_settings"] },
  { to: "/admin/auth/keycloak", label: "Keycloak SSO", icon: <SecurityRounded />, anyOf: ["manage_oidc"] },
  { to: "/admin/ai/providers", label: "AI 공급자", icon: <PsychologyRounded />, anyOf: ["manage_ai"] },
  { to: "/admin/security/keys", label: "키 정책", icon: <KeyRounded />, anyOf: ["manage_key_permissions", "manage_roles", "manage_api_keys"] },
  { to: "/admin/integrations/mcp", label: "MCP · API", icon: <HubRounded />, anyOf: ["manage_settings"] },
  { to: "/admin/workflows/review", label: "검토 · 승인", icon: <RuleRounded />, anyOf: ["manage_approval_policies"] },
  { to: "/admin/operations", label: "호환 API", icon: <SettingsApplicationsRounded />, anyOf: ["manage_system"] },
];

export function AdminLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const user = useSelector((state: RootState) => state.auth.user);
  const info = useSystemInfo();
  const access = useAdminAccess();
  const visibleNavigation = adminNavigation.filter((item) => item.anyOf.length === 0 || access.canAny(item.anyOf));

  return (
    <Box className="moyro-settings-shell admin-settings-shell">
      <Paper component="aside" square elevation={0} className="moyro-settings-sidebar">
        <Stack direction="row" sx={{ px: 2, py: 2, alignItems: "center", gap: 1.25 }}>
          <BrandMark className="moyro-mark" size={40} />
          <Box sx={{ minWidth: 0 }}>
            <Typography variant="subtitle1">moyro</Typography>
            <Typography variant="caption" color="text.secondary">서비스 관리</Typography>
          </Box>
        </Stack>
        <Divider />
        <Box component="nav" aria-label="서비스 관리 메뉴" className="moyro-scrollbar moyro-settings-nav admin-user-menu-scroll">
          <Typography variant="caption" color="text.secondary" sx={{ px: 2, pt: 2, display: "block", fontWeight: 700 }}>
            서비스 설정
          </Typography>
          <List disablePadding sx={{ px: 1, py: 1 }}>
            {visibleNavigation.map((item) => (
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
        <Stack spacing={0.75} sx={{ p: 2 }}>
          <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
            <ManageAccountsRounded fontSize="small" color="action" />
            <Typography variant="body2" noWrap>{user?.username ?? "관리자"}</Typography>
          </Stack>
          <Typography variant="caption" color="text.secondary">
            moyro {displayVersion(info.version)}
          </Typography>
        </Stack>
      </Paper>

      <Box component="section" className="moyro-settings-main">
        <Paper component="header" square elevation={0} className="moyro-settings-header">
          <IconButton aria-label="워크스페이스로 돌아가기" onClick={() => navigate("/workspace")}>
            <ArrowBackRounded />
          </IconButton>
          <Box sx={{ minWidth: 0 }}>
            <Typography variant="subtitle1">서비스 관리자</Typography>
            <Typography variant="caption" color="text.secondary">조직 전체에 적용되는 설정</Typography>
          </Box>
          <Chip label={access.can("manage_system") ? "system_admin" : "위임 관리자"} size="small" sx={{ ml: "auto" }} />
        </Paper>
        <Box className="moyro-scrollbar moyro-settings-content">
          <Outlet />
        </Box>
      </Box>
    </Box>
  );
}
