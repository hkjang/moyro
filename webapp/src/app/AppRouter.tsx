import { Navigate, Outlet, Route, Routes, useLocation } from "react-router-dom";
import { useSelector } from "react-redux";
import { ChatView } from "@/components/ChatView";
import { LoginView } from "@/components/LoginView";
import { AdminLayout } from "@/layouts/AdminLayout";
import { PersonalSettingsLayout } from "@/layouts/PersonalSettingsLayout";
import { AdminOverviewPage } from "@/features/admin/AdminOverviewPage";
import { AIProviderSettingsPage } from "@/features/admin/AIProviderSettingsPage";
import { ApprovalWorkflowPage } from "@/features/admin/ApprovalWorkflowPage";
import { KeycloakSettingsPage } from "@/features/admin/KeycloakSettingsPage";
import { KeyPolicyPage } from "@/features/admin/KeyPolicyPage";
import { LegacyAdminRoute } from "@/features/admin/LegacyAdminRoute";
import { MCPSettingsPage } from "@/features/admin/MCPSettingsPage";
import { SiteSettingsPage } from "@/features/admin/SiteSettingsPage";
import {
  AppearanceSettingsPage,
  NotificationSettingsPage,
  PersonalProfilePage,
  SessionSettingsPage,
} from "@/features/settings/PersonalBasicsPages";
import { PersonalAIPage } from "@/features/settings/PersonalAIPage";
import { PersonalKeysPage } from "@/features/settings/PersonalKeysPage";
import {
  MyApprovalRequestsPage,
  ReviewApprovalRequestsPage,
} from "@/features/settings/ApprovalRequestsPages";
import { useSystemInfo } from "@/features/system/SystemInfoContext";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";

function RequireAdminAccess() {
  const access = useAdminAccess();
  if (!access.loaded) {
    return <div className="chat-empty" role="status">관리 권한을 확인하는 중…</div>;
  }
  return access.hasAdminAccess ? <Outlet /> : <Navigate to="/workspace" replace />;
}

function RequirePermission({ anyOf, fallback = "/admin/overview" }: { anyOf: readonly string[]; fallback?: string }) {
  const access = useAdminAccess();
  if (!access.loaded) {
    return <div className="chat-empty" role="status">관리 권한을 확인하는 중…</div>;
  }
  return access.canAny(anyOf) ? <Outlet /> : <Navigate to={fallback} replace />;
}

function RequireApprovalEnabled() {
  const info = useSystemInfo();
  if (!info.loaded) {
    return <div className="chat-empty" role="status">서비스 설정을 확인하는 중…</div>;
  }
  return info.approval_enabled ? <Outlet /> : <Navigate to="/settings/profile" replace />;
}

function WorkspaceFallback() {
  const location = useLocation();
  return <Navigate to="/workspace" replace state={{ invalidPath: location.pathname }} />;
}

export function AppRouter() {
  const token = useSelector((state: RootState) => state.auth.token);

  if (!token) {
    return (
      <Routes>
        <Route path="*" element={<LoginView />} />
      </Routes>
    );
  }

  return (
    <Routes>
      <Route path="/" element={<Navigate to="/workspace" replace />} />
      <Route path="/login" element={<Navigate to="/workspace" replace />} />
      <Route path="/workspace" element={<ChatView />} />
      <Route path="/workspace/:teamId" element={<ChatView />} />
      <Route path="/workspace/:teamId/channel/:channelId" element={<ChatView />} />
      <Route path="/workspace/:teamId/:view" element={<ChatView />} />

      <Route path="/settings" element={<PersonalSettingsLayout />}>
        <Route index element={<Navigate to="profile" replace />} />
        <Route path="profile" element={<PersonalProfilePage />} />
        <Route path="appearance" element={<AppearanceSettingsPage />} />
        <Route path="notifications" element={<NotificationSettingsPage />} />
        <Route path="security/sessions" element={<SessionSettingsPage />} />
		<Route element={<RequirePermission anyOf={["manage_own_api_keys"]} fallback="/settings/profile" />}>
		  <Route path="developer/keys" element={<PersonalKeysPage />} />
		</Route>
		<Route element={<RequirePermission anyOf={["use_ai"]} fallback="/settings/profile" />}>
		  <Route path="ai" element={<PersonalAIPage />} />
		</Route>
        <Route element={<RequireApprovalEnabled />}>
		  <Route element={<RequirePermission anyOf={["request_approval"]} fallback="/settings/profile" />}>
		    <Route path="approvals/mine" element={<MyApprovalRequestsPage />} />
		  </Route>
		  <Route element={<RequirePermission anyOf={["review_approval"]} fallback="/settings/profile" />}>
		    <Route path="approvals/review" element={<ReviewApprovalRequestsPage />} />
		  </Route>
        </Route>
      </Route>

      <Route element={<RequireAdminAccess />}>
        <Route element={<RequirePermission anyOf={["manage_system"]} />}>
          <Route path="/admin/operations" element={<LegacyAdminRoute />} />
        </Route>
        <Route path="/admin" element={<AdminLayout />}>
          <Route index element={<Navigate to="overview" replace />} />
          <Route path="overview" element={<AdminOverviewPage />} />
          <Route element={<RequirePermission anyOf={["manage_settings"]} />}>
            <Route path="site" element={<SiteSettingsPage />} />
            <Route path="integrations/mcp" element={<MCPSettingsPage />} />
          </Route>
          <Route element={<RequirePermission anyOf={["manage_oidc"]} />}>
            <Route path="auth/keycloak" element={<KeycloakSettingsPage />} />
          </Route>
          <Route element={<RequirePermission anyOf={["manage_ai"]} />}>
            <Route path="ai/providers" element={<AIProviderSettingsPage />} />
          </Route>
          <Route element={<RequirePermission anyOf={["manage_key_permissions", "manage_roles", "manage_api_keys"]} />}>
            <Route path="security/keys" element={<KeyPolicyPage />} />
          </Route>
          <Route element={<RequirePermission anyOf={["manage_approval_policies"]} />}>
            <Route path="workflows/review" element={<ApprovalWorkflowPage />} />
          </Route>
        </Route>
      </Route>

      <Route path="*" element={<WorkspaceFallback />} />
    </Routes>
  );
}
