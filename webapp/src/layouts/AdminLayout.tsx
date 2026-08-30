import ArrowBackRounded from "@mui/icons-material/ArrowBackRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import DashboardRounded from "@mui/icons-material/DashboardRounded";
import ExtensionRounded from "@mui/icons-material/ExtensionRounded";
import HubRounded from "@mui/icons-material/HubRounded";
import KeyRounded from "@mui/icons-material/KeyRounded";
import LanguageRounded from "@mui/icons-material/LanguageRounded";
import ManageAccountsRounded from "@mui/icons-material/ManageAccountsRounded";
import MenuRounded from "@mui/icons-material/MenuRounded";
import PsychologyRounded from "@mui/icons-material/PsychologyRounded";
import RuleRounded from "@mui/icons-material/RuleRounded";
import SecurityRounded from "@mui/icons-material/SecurityRounded";
import SettingsApplicationsRounded from "@mui/icons-material/SettingsApplicationsRounded";
import {
  Box,
  Chip,
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
import { useMemo, useState, type ReactNode } from "react";
import { useSelector } from "react-redux";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import type { RootState } from "@/store";
import { displayVersion, useSystemInfo } from "@/features/system/SystemInfoContext";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import { AdminPluginsProvider, useAdminPlugins } from "@/features/admin/AdminPluginsContext";
import {
  adminPluginDisplayName,
  adminPluginID,
  adminPluginVersion,
  sortAdminPlugins,
} from "@/features/admin/adminPluginIdentity";
import { BrandMark } from "@/components/brand/BrandMark";
import "@/layouts/settings-layout.css";

type AdminNavigationItem = {
  to: string;
  label: string;
  icon: ReactNode;
  anyOf: readonly string[];
  exact?: boolean;
  nested?: boolean;
  ariaLabel?: string;
  children?: readonly AdminNavigationItem[];
};

type AdminNavigationGroup = {
  id: string;
  label: string;
  description: string;
  items: readonly AdminNavigationItem[];
};

const adminNavigationGroups: readonly AdminNavigationGroup[] = [
  {
    id: "overview",
    label: "개요",
    description: "운영 상태와 주요 경고",
    items: [
      { to: "/admin/overview", label: "운영 현황", icon: <DashboardRounded />, anyOf: [] },
    ],
  },
  {
    id: "auth-security",
    label: "인증과 보안",
    description: "로그인, 역할과 키 정책",
    items: [
      { to: "/admin/auth/keycloak", label: "Keycloak SSO", icon: <SecurityRounded />, anyOf: ["manage_oidc"] },
      { to: "/admin/security/keys", label: "키 정책", icon: <KeyRounded />, anyOf: ["manage_key_permissions", "manage_roles", "manage_api_keys"] },
    ],
  },
  {
    id: "ai-automation",
    label: "AI와 자동화",
    description: "모델과 AI 실행 정책",
    items: [
      { to: "/admin/ai/providers", label: "AI 공급자", icon: <PsychologyRounded />, anyOf: ["manage_ai"] },
    ],
  },
  {
    id: "integrations",
    label: "연동",
    description: "MCP와 외부 API",
    items: [
      { to: "/admin/integrations/mcp", label: "MCP · API", icon: <HubRounded />, anyOf: ["manage_settings"] },
      { to: "/admin/integrations/plugins", label: "플러그인", icon: <ExtensionRounded />, anyOf: ["manage_plugins"], exact: true },
    ],
  },
  {
    id: "workflows",
    label: "워크플로",
    description: "검토와 승인 정책",
    items: [
      { to: "/admin/workflows/review", label: "검토 · 승인", icon: <RuleRounded />, anyOf: ["manage_approval_policies"] },
    ],
  },
  {
    id: "system",
    label: "시스템",
    description: "사이트와 호환 API",
    items: [
      { to: "/admin/site", label: "사이트 설정", icon: <LanguageRounded />, anyOf: ["manage_settings"] },
      { to: "/admin/operations", label: "호환 API", icon: <SettingsApplicationsRounded />, anyOf: ["manage_system"] },
    ],
  },
];

function routeIsActive(pathname: string, item: AdminNavigationItem): boolean {
  return pathname === item.to || (!item.exact && pathname.startsWith(`${item.to}/`));
}

function childRouteIsActive(pathname: string, item: AdminNavigationItem): boolean {
  return item.children?.some((child) => routeIsActive(pathname, child)) ?? false;
}

function AdminNavigation({
  groups,
  pathname,
  idPrefix,
  onNavigate,
}: {
  groups: readonly AdminNavigationGroup[];
  pathname: string;
  idPrefix: string;
  onNavigate: (to: string) => void;
}) {
  return (
    <Box component="nav" aria-label="서비스 관리 메뉴" className="admin-navigation-groups">
      {groups.map((group) => {
        const headingID = `${idPrefix}-${group.id}`;
        return (
          <Box component="section" className="admin-navigation-group" aria-labelledby={headingID} key={group.id}>
            <Box className="admin-navigation-group-heading">
              <Typography id={headingID} variant="caption" component="h2">{group.label}</Typography>
              <Typography variant="caption" color="text.secondary">{group.description}</Typography>
            </Box>
            <List disablePadding aria-labelledby={headingID}>
              {group.items.map((item) => {
                const active = routeIsActive(pathname, item);
                const activeDescendant = childRouteIsActive(pathname, item);
                const showChildren = Boolean(item.children?.length && (active || activeDescendant));
                return (
                  <Box component="li" className="admin-navigation-tree-item" key={item.to}>
                    <ListItemButton
                      selected={active}
                      aria-current={active ? "page" : undefined}
                      aria-expanded={item.children?.length ? showChildren : undefined}
                      aria-label={item.ariaLabel}
                      onClick={() => onNavigate(item.to)}
                      className={`admin-navigation-item${activeDescendant ? " admin-navigation-item-ancestor" : ""}`}
                    >
                      <ListItemIcon>{item.icon}</ListItemIcon>
                      <ListItemText primary={item.label} />
                    </ListItemButton>
                    {showChildren && (
                      <List disablePadding className="admin-plugin-navigation-list" aria-label="설치된 플러그인">
                        {item.children?.map((child) => {
                          const childActive = routeIsActive(pathname, child);
                          return (
                            <ListItem key={child.to} disablePadding>
                              <ListItemButton
                                selected={childActive}
                                aria-current={childActive ? "page" : undefined}
                                aria-label={child.ariaLabel}
                                title={child.ariaLabel ?? child.label}
                                onClick={() => onNavigate(child.to)}
                                className="admin-navigation-item admin-navigation-item-nested"
                              >
                                <ListItemIcon>{child.icon}</ListItemIcon>
                                <ListItemText primary={child.label} />
                              </ListItemButton>
                            </ListItem>
                          );
                        })}
                      </List>
                    )}
                  </Box>
                );
              })}
            </List>
          </Box>
        );
      })}
    </Box>
  );
}

function AdminLayoutContent() {
  const navigate = useNavigate();
  const location = useLocation();
  const user = useSelector((state: RootState) => state.auth.user);
  const info = useSystemInfo();
  const access = useAdminAccess();
  const { plugins, statuses } = useAdminPlugins();
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
  const pluginStateByID = useMemo(() => new Map(
    statuses.map((status) => [status.plugin_id, status.state]),
  ), [statuses]);
  const pluginNavigationItems = useMemo<AdminNavigationItem[]>(() => {
    const seen = new Set<string>();
    const uniquePlugins = sortAdminPlugins(plugins).filter((plugin) => {
      const pluginID = adminPluginID(plugin);
      if (!pluginID || seen.has(pluginID)) return false;
      seen.add(pluginID);
      return true;
    });
    const displayNameCounts = new Map<string, number>();
    for (const plugin of uniquePlugins) {
      const displayName = adminPluginDisplayName(plugin);
      displayNameCounts.set(displayName, (displayNameCounts.get(displayName) ?? 0) + 1);
    }
    return uniquePlugins.map((plugin) => {
      const pluginID = adminPluginID(plugin);
      const state = pluginStateByID.get(pluginID) ?? String(plugin.state ?? "unknown");
      const enabled = typeof plugin.enabled === "boolean"
        ? plugin.enabled
        : state === "running" || state === "enabled";
      const stateTone = enabled && state !== "running" && state !== "enabled"
        ? "danger"
        : enabled
          ? "ok"
          : "disabled";
      const displayName = adminPluginDisplayName(plugin);
      const label = (displayNameCounts.get(displayName) ?? 0) > 1
        ? `${displayName} · ${pluginID}`
        : displayName;
      return {
        to: `/admin/integrations/plugins/${encodeURIComponent(pluginID)}`,
        label,
        icon: <Box component="span" className={`admin-plugin-state-dot ${stateTone}`} />,
        anyOf: ["manage_plugins"],
        exact: true,
        nested: true,
        ariaLabel: `${displayName} 플러그인 설정 · ${pluginID} · ${state} · v${adminPluginVersion(plugin)}`,
      };
    });
  }, [pluginStateByID, plugins]);
  const navigationGroups = useMemo<AdminNavigationGroup[]>(() => adminNavigationGroups.map((group) => ({
    ...group,
    items: group.items.map((item) => (
      item.to === "/admin/integrations/plugins"
        ? { ...item, children: pluginNavigationItems }
        : item
    )),
  })), [pluginNavigationItems]);
  const visibleNavigationGroups = navigationGroups
    .map((group) => ({
      ...group,
      items: group.items
        .filter((item) => item.anyOf.length === 0 || access.canAny(item.anyOf))
        .map((item) => ({
          ...item,
          children: item.children?.filter((child) => child.anyOf.length === 0 || access.canAny(child.anyOf)),
        })),
    }))
    .filter((group) => group.items.length > 0);
  const activeGroup = visibleNavigationGroups.find((group) =>
    group.items.some((item) => routeIsActive(location.pathname, item) || childRouteIsActive(location.pathname, item)),
  );
  const activeParentItem = activeGroup?.items.find((item) => (
    routeIsActive(location.pathname, item) || childRouteIsActive(location.pathname, item)
  ));
  const activeItem = activeParentItem?.children?.find((item) => routeIsActive(location.pathname, item))
    ?? (activeParentItem && routeIsActive(location.pathname, activeParentItem) ? activeParentItem : undefined);

  const navigateFromMenu = (to: string) => {
    navigate(to);
    setMobileNavigationOpen(false);
  };

  return (
    <Box className="moyro-settings-shell admin-settings-shell">
      <Paper component="aside" square elevation={0} className="moyro-settings-sidebar admin-settings-sidebar">
        <Stack direction="row" className="admin-settings-brand">
          <BrandMark className="moyro-mark" size={40} />
          <Box sx={{ minWidth: 0 }}>
            <Typography variant="subtitle1">moyro</Typography>
            <Typography variant="caption" color="text.secondary">서비스 관리</Typography>
          </Box>
        </Stack>
        <Divider />
        <Box className="moyro-scrollbar moyro-settings-nav admin-user-menu-scroll">
          <AdminNavigation
            groups={visibleNavigationGroups}
            pathname={location.pathname}
            idPrefix="admin-desktop-group"
            onNavigate={navigateFromMenu}
          />
        </Box>
        <Divider />
        <Stack spacing={0.75} className="admin-settings-account">
          <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
            <ManageAccountsRounded fontSize="small" color="action" />
            <Typography variant="body2" noWrap>{user?.username ?? "관리자"}</Typography>
          </Stack>
          <Typography variant="caption" color="text.secondary">
            moyro {displayVersion(info.version)}
          </Typography>
        </Stack>
      </Paper>

      <Box component="main" className="moyro-settings-main">
        <Paper component="header" square elevation={0} className="moyro-settings-header admin-settings-header">
          <IconButton
            className="admin-mobile-menu-button"
            aria-label="서비스 관리 메뉴 열기"
            aria-controls="admin-mobile-navigation"
            aria-expanded={mobileNavigationOpen}
            onClick={() => setMobileNavigationOpen(true)}
          >
            <MenuRounded />
          </IconButton>
          <IconButton aria-label="워크스페이스로 돌아가기" onClick={() => navigate("/workspace")}>
            <ArrowBackRounded />
          </IconButton>
          <Box className="admin-settings-current" sx={{ minWidth: 0 }}>
            <Typography variant="subtitle1" noWrap>{activeItem?.label ?? "서비스 관리자"}</Typography>
            <Typography variant="caption" color="text.secondary" noWrap>
              {activeGroup ? `${activeGroup.label} · ${activeGroup.description}` : "조직 전체에 적용되는 설정"}
            </Typography>
          </Box>
          <Chip
            className="admin-settings-role"
            label={access.can("manage_system") ? "system_admin" : "위임 관리자"}
            size="small"
            sx={{ ml: "auto" }}
          />
        </Paper>
        <Box className="moyro-scrollbar moyro-settings-content">
          <Outlet />
        </Box>
      </Box>

      <Drawer
        anchor="left"
        open={mobileNavigationOpen}
        onClose={() => setMobileNavigationOpen(false)}
        slotProps={{ paper: { className: "admin-mobile-drawer" } }}
      >
        <Box component="aside" id="admin-mobile-navigation" aria-labelledby="admin-mobile-navigation-title" className="admin-mobile-drawer-content">
          <Box className="admin-mobile-drawer-header">
            <Box sx={{ minWidth: 0 }}>
              <Typography id="admin-mobile-navigation-title" variant="h6">서비스 관리</Typography>
              <Typography variant="caption" color="text.secondary">운영 영역을 선택하세요</Typography>
            </Box>
            <IconButton aria-label="서비스 관리 메뉴 닫기" onClick={() => setMobileNavigationOpen(false)}>
              <CloseRounded />
            </IconButton>
          </Box>
          <Divider />
          <Box className="moyro-scrollbar admin-mobile-drawer-navigation">
            <AdminNavigation
              groups={visibleNavigationGroups}
              pathname={location.pathname}
              idPrefix="admin-mobile-group"
              onNavigate={navigateFromMenu}
            />
          </Box>
          <Divider />
          <Typography variant="caption" color="text.secondary" className="admin-mobile-drawer-version">
            {user?.username ?? "관리자"} · moyro {displayVersion(info.version)}
          </Typography>
        </Box>
      </Drawer>
    </Box>
  );
}

export function AdminLayout() {
  const access = useAdminAccess();
  return (
    <AdminPluginsProvider enabled={access.loaded && access.can("manage_plugins")}>
      <AdminLayoutContent />
    </AdminPluginsProvider>
  );
}
