import ArrowBackRounded from "@mui/icons-material/ArrowBackRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import KeyRounded from "@mui/icons-material/KeyRounded";
import MenuRounded from "@mui/icons-material/MenuRounded";
import NotificationsRounded from "@mui/icons-material/NotificationsRounded";
import PaletteRounded from "@mui/icons-material/PaletteRounded";
import PersonRounded from "@mui/icons-material/PersonRounded";
import PsychologyRounded from "@mui/icons-material/PsychologyRounded";
import SecurityRounded from "@mui/icons-material/SecurityRounded";
import ExtensionRounded from "@mui/icons-material/ExtensionRounded";
import {
  Box,
  Divider,
  Drawer,
  IconButton,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Paper,
  Stack,
  Typography,
} from "@mui/material";
import { useState, type ReactNode } from "react";
import { useSelector } from "react-redux";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";
import { BrandMark } from "@/components/brand/BrandMark";
import { usePluginRegistryState } from "@/plugins/registry";
import "@/layouts/settings-layout.css";

type PersonalNavigationItem = {
  to: string;
  label: string;
  icon: ReactNode;
  permission: string;
};

const personalNavigation: readonly PersonalNavigationItem[] = [
  { to: "/settings/profile", label: "프로필", icon: <PersonRounded />, permission: "" },
  { to: "/settings/appearance", label: "화면", icon: <PaletteRounded />, permission: "" },
  { to: "/settings/notifications", label: "알림", icon: <NotificationsRounded />, permission: "" },
  { to: "/settings/security/sessions", label: "보안 · 세션", icon: <SecurityRounded />, permission: "" },
  { to: "/settings/developer/keys", label: "개인 키", icon: <KeyRounded />, permission: "manage_own_api_keys" },
  { to: "/settings/ai", label: "AI 개인화", icon: <PsychologyRounded />, permission: "use_ai" },
];

function routeIsActive(pathname: string, to: string): boolean {
  return pathname === to || pathname.startsWith(`${to}/`);
}

function PersonalNavigation({
  items,
  pathname,
  label,
  onNavigate,
}: {
  items: readonly PersonalNavigationItem[];
  pathname: string;
  label: string;
  onNavigate: (to: string) => void;
}) {
  return (
    <Box component="nav" aria-label={label}>
      <List disablePadding className="personal-navigation-list">
        {items.map((item) => {
          const active = routeIsActive(pathname, item.to);
          return (
            <ListItem key={item.to} disablePadding>
              <ListItemButton
                selected={active}
                aria-current={active ? "page" : undefined}
                onClick={() => onNavigate(item.to)}
                className="personal-navigation-item"
              >
                <ListItemIcon>{item.icon}</ListItemIcon>
                <ListItemText primary={item.label} />
              </ListItemButton>
            </ListItem>
          );
        })}
      </List>
    </Box>
  );
}

export function PersonalSettingsLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const user = useSelector((state: RootState) => state.auth.user);
  const info = useSystemInfo();
  const access = useAdminAccess();
  const pluginRegistry = usePluginRegistryState();
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
  const navigation = [
    ...personalNavigation.filter((item) => !item.permission || access.can(item.permission)),
    ...pluginRegistry.userSettings.map((registration) => ({
      to: `/settings/plugins/${encodeURIComponent(registration.pluginId)}`,
      label: registration.uiName,
      icon: <ExtensionRounded />,
      permission: "",
    })),
  ];
  const activeItem = navigation.find((item) => routeIsActive(location.pathname, item.to));

  const navigateFromMenu = (to: string) => {
    navigate(to);
    setMobileNavigationOpen(false);
  };

  return (
    <Box className="moyro-settings-shell personal-settings-shell">
      <Paper component="aside" square elevation={0} className="moyro-settings-sidebar personal-settings-sidebar">
        <Stack direction="row" className="personal-settings-brand">
          <BrandMark className="moyro-mark" size={40} title="moyro" />
          <Box sx={{ minWidth: 0 }}>
            <Typography variant="subtitle1">내 설정</Typography>
            <Typography variant="caption" color="text.secondary" noWrap>{user?.username ?? "사용자"}</Typography>
          </Box>
        </Stack>
        <Divider />
        <Box className="moyro-scrollbar moyro-settings-nav">
          <PersonalNavigation
            items={navigation}
            pathname={location.pathname}
            label="개인 설정 메뉴"
            onNavigate={navigateFromMenu}
          />
        </Box>
        <Divider />
        <Typography variant="caption" color="text.secondary" className="personal-settings-version">
          moyro {displayVersion(info.version)}
        </Typography>
      </Paper>

      <Box component="main" className="moyro-settings-main">
        <Paper component="header" square elevation={0} className="moyro-settings-header personal-settings-header">
          <IconButton
            className="personal-mobile-menu-button"
            aria-label="개인 설정 메뉴 열기"
            aria-controls="personal-mobile-navigation"
            aria-expanded={mobileNavigationOpen}
            onClick={() => setMobileNavigationOpen(true)}
          >
            <MenuRounded />
          </IconButton>
          <IconButton aria-label="워크스페이스로 돌아가기" onClick={() => navigate("/workspace")}>
            <ArrowBackRounded />
          </IconButton>
          <Box className="personal-settings-current" sx={{ minWidth: 0 }}>
            <Typography variant="subtitle1" noWrap>{activeItem?.label ?? "개인 설정"}</Typography>
            <Typography variant="caption" color="text.secondary" noWrap>내 계정에만 적용되는 설정</Typography>
          </Box>
        </Paper>
        <Box className="moyro-scrollbar moyro-settings-content">
          <Outlet />
        </Box>
      </Box>

      <Drawer
        anchor="left"
        open={mobileNavigationOpen}
        onClose={() => setMobileNavigationOpen(false)}
        slotProps={{ paper: { className: "personal-mobile-drawer" } }}
      >
        <Box
          component="aside"
          id="personal-mobile-navigation"
          aria-labelledby="personal-mobile-navigation-title"
          className="personal-mobile-drawer-content"
        >
          <Box className="personal-mobile-drawer-header">
            <Box sx={{ minWidth: 0 }}>
              <Typography id="personal-mobile-navigation-title" variant="h6">내 설정</Typography>
              <Typography variant="caption" color="text.secondary" noWrap>
                {user?.username ?? "사용자"}
              </Typography>
            </Box>
            <IconButton aria-label="개인 설정 메뉴 닫기" onClick={() => setMobileNavigationOpen(false)}>
              <CloseRounded />
            </IconButton>
          </Box>
          <Divider />
          <Box className="moyro-scrollbar personal-mobile-drawer-navigation">
            <PersonalNavigation
              items={navigation}
              pathname={location.pathname}
              label="모바일 개인 설정 메뉴"
              onNavigate={navigateFromMenu}
            />
          </Box>
          <Divider />
          <Typography variant="caption" color="text.secondary" className="personal-mobile-drawer-version">
            moyro {displayVersion(info.version)}
          </Typography>
        </Box>
      </Drawer>
    </Box>
  );
}
