import { Component, Suspense, lazy, type ComponentType, type ErrorInfo, type ReactNode } from "react";
import { Alert, Box, Button, CircularProgress, Stack, Typography } from "@mui/material";
import { Navigate, Outlet, Route, Routes, useLocation } from "react-router-dom";
import { useSelector } from "react-redux";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";

const ProductShell = lazy(() =>
  import("@/features/shell/ProductShell").then((module) => ({ default: module.ProductShell })),
);
const TodayPage = lazy(() =>
  import("@/features/flow/TodayPage").then((module) => ({ default: module.TodayPage })),
);
const UnifiedInboxPage = lazy(() =>
  import("@/features/flow/UnifiedInboxPage").then((module) => ({ default: module.UnifiedInboxPage })),
);
const MyWorkPage = lazy(() =>
  import("@/features/flow/MyWorkPage").then((module) => ({ default: module.MyWorkPage })),
);
const ApprovalCenterPage = lazy(() =>
  import("@/features/flow/ApprovalCenterPage").then((module) => ({ default: module.ApprovalCenterPage })),
);
const AIAssistantPage = lazy(() =>
  import("@/features/flow/AIAssistantPage").then((module) => ({ default: module.AIAssistantPage })),
);
const GlobalSearchPage = lazy(() =>
  import("@/features/flow/GlobalSearchPage").then((module) => ({ default: module.GlobalSearchPage })),
);
const AutomationRulesPage = lazy(() =>
  import("@/features/flow/AutomationRulesPage").then((module) => ({ default: module.AutomationRulesPage })),
);
const KnowledgePage = lazy(() =>
  import("@/features/knowledge/KnowledgePage").then((module) => ({ default: module.KnowledgePage })),
);
const FlowDataLayout = lazy(() =>
  import("@/features/flow/FlowDataProvider").then((module) => ({ default: module.FlowDataLayout })),
);

const ChatView = lazy(() =>
  import("@/components/ChatView").then((module) => ({ default: module.ChatView })),
);
const LoginView = lazy(() =>
  import("@/components/LoginView").then((module) => ({ default: module.LoginView })),
);
const AdminLayout = lazy(() =>
  import("@/layouts/AdminLayout").then((module) => ({ default: module.AdminLayout })),
);
const PersonalSettingsLayout = lazy(() =>
  import("@/layouts/PersonalSettingsLayout").then((module) => ({ default: module.PersonalSettingsLayout })),
);
const AdminOverviewPage = lazy(() =>
  import("@/features/admin/AdminOverviewPage").then((module) => ({ default: module.AdminOverviewPage })),
);
const AIProviderSettingsPage = lazy(() =>
  import("@/features/admin/AIProviderSettingsPage").then((module) => ({ default: module.AIProviderSettingsPage })),
);
const ApprovalWorkflowPage = lazy(() =>
  import("@/features/admin/ApprovalWorkflowPage").then((module) => ({ default: module.ApprovalWorkflowPage })),
);
const KeycloakSettingsPage = lazy(() =>
  import("@/features/admin/KeycloakSettingsPage").then((module) => ({ default: module.KeycloakSettingsPage })),
);
const KeyPolicyPage = lazy(() =>
  import("@/features/admin/KeyPolicyPage").then((module) => ({ default: module.KeyPolicyPage })),
);
const LegacyAdminRoute = lazy(() =>
  import("@/features/admin/LegacyAdminRoute").then((module) => ({ default: module.LegacyAdminRoute })),
);
const MCPSettingsPage = lazy(() =>
  import("@/features/admin/MCPSettingsPage").then((module) => ({ default: module.MCPSettingsPage })),
);
const PluginManagementPage = lazy(() =>
  import("@/features/admin/PluginManagementPage").then((module) => ({ default: module.PluginManagementPage })),
);
const PluginSettingsPage = lazy(() =>
  import("@/features/admin/PluginSettingsPage").then((module) => ({ default: module.PluginSettingsPage })),
);
const SiteSettingsPage = lazy(() =>
  import("@/features/admin/SiteSettingsPage").then((module) => ({ default: module.SiteSettingsPage })),
);

const loadPersonalBasicsPages = () => import("@/features/settings/PersonalBasicsPages");
const AppearanceSettingsPage = lazy(() =>
  loadPersonalBasicsPages().then((module) => ({ default: module.AppearanceSettingsPage })),
);
const NotificationSettingsPage = lazy(() =>
  loadPersonalBasicsPages().then((module) => ({ default: module.NotificationSettingsPage })),
);
const PersonalProfilePage = lazy(() =>
  loadPersonalBasicsPages().then((module) => ({ default: module.PersonalProfilePage })),
);
const SessionSettingsPage = lazy(() =>
  loadPersonalBasicsPages().then((module) => ({ default: module.SessionSettingsPage })),
);
const PersonalAIPage = lazy(() =>
  import("@/features/settings/PersonalAIPage").then((module) => ({ default: module.PersonalAIPage })),
);
const PersonalKeysPage = lazy(() =>
  import("@/features/settings/PersonalKeysPage").then((module) => ({ default: module.PersonalKeysPage })),
);
const PluginUserSettingsPage = lazy(() =>
  import("@/plugins/PluginUserSettingsPage").then((module) => ({ default: module.PluginUserSettingsPage })),
);

function RouteLoadingFallback() {
  return (
    <Box
      role="status"
      aria-live="polite"
      sx={{ minHeight: "100%", display: "grid", placeItems: "center", p: 4 }}
    >
      <Stack spacing={1.5} sx={{ alignItems: "center" }}>
        <CircularProgress size={28} />
        <Typography variant="body2" color="text.secondary">
          화면을 불러오는 중…
        </Typography>
      </Stack>
    </Box>
  );
}

type RouteBoundaryState = { error: Error | null };

class RouteBoundary extends Component<{ children: ReactNode }, RouteBoundaryState> {
  state: RouteBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): RouteBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("route module failed to load", error, info);
  }

  render() {
    if (this.state.error) {
      return (
        <Box sx={{ minHeight: "100%", display: "grid", placeItems: "center", p: 4 }}>
          <Alert
            severity="error"
            action={(
              <Button color="inherit" size="small" onClick={() => window.location.reload()}>
                다시 시도
              </Button>
            )}
          >
            화면을 불러오지 못했습니다.
          </Alert>
        </Box>
      );
    }
    return <Suspense fallback={<RouteLoadingFallback />}>{this.props.children}</Suspense>;
  }
}

function routeElement(Page: ComponentType) {
  return (
    <RouteBoundary>
      <Page />
    </RouteBoundary>
  );
}

function RequireAdminAccess() {
  const access = useAdminAccess();
  if (!access.loaded) {
    return <div className="chat-empty" role="status">관리 권한을 확인하는 중…</div>;
  }
  return access.hasAdminAccess ? <Outlet /> : <Navigate to="/today" replace />;
}

function RequirePermission({ anyOf, fallback = "/admin/overview" }: { anyOf: readonly string[]; fallback?: string }) {
  const access = useAdminAccess();
  if (!access.loaded) {
    return <div className="chat-empty" role="status">관리 권한을 확인하는 중…</div>;
  }
  return access.canAny(anyOf) ? <Outlet /> : <Navigate to={fallback} replace />;
}

function AuthenticatedFallback() {
  const location = useLocation();
  return <Navigate to="/today" replace state={{ invalidPath: location.pathname }} />;
}

export function AppRouter() {
  const token = useSelector((state: RootState) => state.auth.token);

  if (!token) {
    return (
      <Routes>
        <Route path="*" element={routeElement(LoginView)} />
      </Routes>
    );
  }

  return (
    <Routes>
      <Route element={routeElement(ProductShell)}>
        <Route path="/" element={<Navigate to="/today" replace />} />
        <Route path="/login" element={<Navigate to="/today" replace />} />
        <Route element={routeElement(FlowDataLayout)}>
          <Route path="/today" element={routeElement(TodayPage)} />
          <Route path="/inbox" element={<Navigate to="/inbox/updates" replace />} />
          <Route path="/inbox/:tab" element={routeElement(UnifiedInboxPage)} />
          <Route path="/my-work" element={<Navigate to="/my-work/tasks" replace />} />
          <Route path="/my-work/:tab" element={routeElement(MyWorkPage)} />
          <Route path="/approvals" element={<Navigate to="/approvals/mine" replace />} />
          <Route path="/approvals/:tab" element={routeElement(ApprovalCenterPage)} />
          <Route path="/automations" element={routeElement(AutomationRulesPage)} />
          <Route path="/knowledge" element={routeElement(KnowledgePage)} />
          <Route path="/search" element={routeElement(GlobalSearchPage)} />
        </Route>
        <Route element={<RequirePermission anyOf={["use_ai"]} fallback="/today" />}>
          <Route path="/assistant" element={routeElement(AIAssistantPage)} />
        </Route>

        <Route path="/workspace" element={routeElement(ChatView)} />
        <Route path="/workspace/:teamId" element={routeElement(ChatView)} />
        <Route path="/workspace/:teamId/channel/:channelId" element={routeElement(ChatView)} />
        <Route path="/workspace/:teamId/saved" element={<Navigate to="/my-work/saved" replace />} />
        <Route path="/workspace/:teamId/scheduled" element={<Navigate to="/my-work/scheduled" replace />} />

        <Route path="/settings/approvals/mine" element={<Navigate to="/approvals/mine" replace />} />
        <Route path="/settings/approvals/review" element={<Navigate to="/approvals/review" replace />} />
        <Route path="/settings" element={routeElement(PersonalSettingsLayout)}>
          <Route index element={<Navigate to="profile" replace />} />
          <Route path="profile" element={routeElement(PersonalProfilePage)} />
          <Route path="appearance" element={routeElement(AppearanceSettingsPage)} />
          <Route path="notifications" element={routeElement(NotificationSettingsPage)} />
          <Route path="security/sessions" element={routeElement(SessionSettingsPage)} />
          <Route element={<RequirePermission anyOf={["manage_own_api_keys"]} fallback="/settings/profile" />}>
            <Route path="developer/keys" element={routeElement(PersonalKeysPage)} />
          </Route>
          <Route element={<RequirePermission anyOf={["use_ai"]} fallback="/settings/profile" />}>
            <Route path="ai" element={routeElement(PersonalAIPage)} />
          </Route>
          <Route path="plugins/:pluginId" element={routeElement(PluginUserSettingsPage)} />
        </Route>

        <Route element={<RequireAdminAccess />}>
          <Route element={<RequirePermission anyOf={["manage_system"]} />}>
            <Route path="/admin/operations" element={routeElement(LegacyAdminRoute)} />
          </Route>
          <Route path="/admin" element={routeElement(AdminLayout)}>
            <Route index element={<Navigate to="overview" replace />} />
            <Route path="overview" element={routeElement(AdminOverviewPage)} />
            <Route element={<RequirePermission anyOf={["manage_settings"]} />}>
              <Route path="site" element={routeElement(SiteSettingsPage)} />
              <Route path="integrations/mcp" element={routeElement(MCPSettingsPage)} />
            </Route>
            <Route element={<RequirePermission anyOf={["manage_plugins"]} />}>
              <Route path="integrations/plugins" element={routeElement(PluginManagementPage)} />
              <Route path="integrations/plugins/:pluginId" element={routeElement(PluginSettingsPage)} />
            </Route>
            <Route element={<RequirePermission anyOf={["manage_oidc"]} />}>
              <Route path="auth/keycloak" element={routeElement(KeycloakSettingsPage)} />
            </Route>
            <Route element={<RequirePermission anyOf={["manage_ai"]} />}>
              <Route path="ai/providers" element={routeElement(AIProviderSettingsPage)} />
            </Route>
            <Route element={<RequirePermission anyOf={["manage_key_permissions", "manage_roles", "manage_api_keys"]} />}>
              <Route path="security/keys" element={routeElement(KeyPolicyPage)} />
            </Route>
            <Route element={<RequirePermission anyOf={["manage_approval_policies"]} />}>
              <Route path="workflows/review" element={routeElement(ApprovalWorkflowPage)} />
            </Route>
          </Route>
        </Route>

        <Route path="*" element={<AuthenticatedFallback />} />
      </Route>
    </Routes>
  );
}
