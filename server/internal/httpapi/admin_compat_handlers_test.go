package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/config"
)

func requestWithRouteParam(method, path, key, value string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestAdminCompatConfigSnapshot(t *testing.T) {
	h := &handlers{cfg: &config.Config{
		Listen:              ":8065",
		PublicBaseURL:       "http://localhost:8065",
		PluginDir:           "./plugins",
		FileStorageRoot:     "./data/files",
		FileBackend:         "fs",
		LinkPreviewsEnabled: true,
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v4/config", nil)
	rr := httptest.NewRecorder()

	h.getConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["ServiceSettings"]["SiteURL"] != "http://localhost:8065" {
		t.Fatalf("SiteURL = %#v, want configured URL", got["ServiceSettings"]["SiteURL"])
	}
	if got["PluginSettings"]["Enable"] != true {
		t.Fatalf("PluginSettings.Enable = %#v, want true", got["PluginSettings"]["Enable"])
	}
	if got["PluginSettings"]["EnableUploads"] != true {
		t.Fatalf("PluginSettings.EnableUploads = %#v, want true", got["PluginSettings"]["EnableUploads"])
	}
}

func assertAdminCompatNotSupported(t *testing.T, rr *httptest.ResponseRecorder, wantID string) {
	t.Helper()
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", rr.Code, rr.Body.String())
	}
	var got apiError
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode AppError: %v", err)
	}
	if got.ID != wantID || got.StatusCode != http.StatusNotImplemented || got.Message == "" {
		t.Fatalf("AppError = %#v, want id=%q/status=501/non-empty message", got, wantID)
	}
}

func TestAdminCompatConfigMutationsNeverReturnFalseSuccess(t *testing.T) {
	h := &handlers{}
	mutations := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		path    string
		wantID  string
	}{
		{"put", h.putConfig, "/api/v4/config", "api.config.update_config.not_supported.app_error"},
		{"patch", h.patchConfig, "/api/v4/config/patch", "api.config.patch_config.not_supported.app_error"},
	}
	payloads := map[string]string{
		"smtp":         `{"EmailSettings":{"SMTPServer":"smtp.internal","SMTPPassword":"secret"}}`,
		"s3":           `{"FileSettings":{"DriverName":"amazons3","AmazonS3Bucket":"moyro"}}`,
		"redis":        `{"ClusterSettings":{"Enable":true,"InterNodeUrls":["redis://redis.internal"]}}`,
		"link-preview": `{"ServiceSettings":{"EnableLinkPreviews":false}}`,
		"plugin":       `{"PluginSettings":{"EnableUploads":true}}`,
	}
	for _, mutation := range mutations {
		for feature, payload := range payloads {
			t.Run(mutation.name+"/"+feature, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPut, mutation.path, bytes.NewBufferString(payload))
				rr := httptest.NewRecorder()
				mutation.handler(rr, req)
				assertAdminCompatNotSupported(t, rr, mutation.wantID)
			})
		}
	}

	rr := httptest.NewRecorder()
	h.reloadConfig(rr, httptest.NewRequest(http.MethodPost, "/api/v4/config/reload", nil))
	assertAdminCompatNotSupported(t, rr, "api.config.reload.not_supported.app_error")
}

func TestAdminCompatUnsupportedFeatureMutationsReturnAppErrors(t *testing.T) {
	h := &handlers{}
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		path    string
		wantID  string
	}{
		{"plugin URL install", h.installPluginFromURL, http.MethodPost, "/api/v4/plugins/install_from_url", "api.plugin.install_from_url.not_supported.app_error"},
		{"plugin marketplace install", h.installPluginFromMarketplace, http.MethodPost, "/api/v4/plugins/marketplace", "api.plugin.install_marketplace.not_supported.app_error"},
		{"plugin marketplace preference", h.saveMarketplaceFirstAdminVisit, http.MethodPost, "/api/v4/plugins/marketplace/first_admin_visit", "api.plugin.marketplace_preference.not_supported.app_error"},
		{"license upload", h.uploadLicense, http.MethodPost, "/api/v4/license", "api.license.upload.not_supported.app_error"},
		{"license delete", h.deleteLicense, http.MethodDelete, "/api/v4/license", "api.license.delete.not_supported.app_error"},
		{"brand upload", h.uploadBrandImage, http.MethodPost, "/api/v4/brand/image", "api.brand.upload_image.not_supported.app_error"},
		{"brand delete", h.deleteBrandImage, http.MethodDelete, "/api/v4/brand/image", "api.brand.delete_image.not_supported.app_error"},
		{"LDAP group link", h.linkLDAPGroup, http.MethodPost, "/api/v4/ldap/groups/example/link", "api.ldap.group_link.not_supported.app_error"},
		{"LDAP group unlink", h.unlinkLDAPGroup, http.MethodDelete, "/api/v4/ldap/groups/example/link", "api.ldap.group_unlink.not_supported.app_error"},
		{"LDAP certificate upload", h.uploadLDAPCertificate, http.MethodPost, "/api/v4/ldap/certificate/public", "api.ldap.certificate_upload.not_supported.app_error"},
		{"LDAP certificate delete", h.deleteLDAPCertificate, http.MethodDelete, "/api/v4/ldap/certificate/public", "api.ldap.certificate_delete.not_supported.app_error"},
		{"LDAP test", h.ldapDisabledOK, http.MethodPost, "/api/v4/ldap/test", "api.ldap.disabled.app_error"},
		{"SAML metadata", h.uploadSAMLMetadataFromIDP, http.MethodPost, "/api/v4/saml/metadatafromidp", "api.saml.metadata.not_supported.app_error"},
		{"SAML reset", h.resetSAMLAuthData, http.MethodPost, "/api/v4/saml/reset_auth_data", "api.saml.reset.not_supported.app_error"},
		{"SAML certificate upload", h.uploadSAMLCertificate, http.MethodPost, "/api/v4/saml/certificate/idp", "api.saml.certificate_upload.not_supported.app_error"},
		{"SAML certificate delete", h.deleteSAMLCertificate, http.MethodDelete, "/api/v4/saml/certificate/idp", "api.saml.certificate_delete.not_supported.app_error"},
		{"content flagging", h.putContentFlaggingConfig, http.MethodPut, "/api/v4/content_flagging/config", "api.content_flagging.config.not_supported.app_error"},
		{"legacy AI bridge", h.putAIBridge, http.MethodPut, "/api/v4/system/e2e/ai_bridge", "api.ai_bridge.config.not_supported.app_error"},
		{"legacy AI bridge delete", h.deleteAIBridge, http.MethodDelete, "/api/v4/system/e2e/ai_bridge", "api.ai_bridge.delete.not_supported.app_error"},
		{"system notice", h.markSystemNoticeViewed, http.MethodPut, "/api/v4/system/notices/view", "api.system_notice.view.not_supported.app_error"},
		{"runtime logs", h.getLogs, http.MethodGet, "/api/v4/logs", "api.logs.list.not_supported.app_error"},
		{"runtime log download", h.downloadLogs, http.MethodGet, "/api/v4/logs/download", "api.logs.download.not_supported.app_error"},
		{"cluster status", h.getClusterStatus, http.MethodGet, "/api/v4/cluster/status", "api.cluster.status.not_supported.app_error"},
		{"server busy get", h.getServerBusy, http.MethodGet, "/api/v4/server_busy", "api.server_busy.get.not_supported.app_error"},
		{"server busy set", h.setServerBusy, http.MethodPost, "/api/v4/server_busy", "api.server_busy.set.not_supported.app_error"},
		{"server busy clear", h.clearServerBusy, http.MethodDelete, "/api/v4/server_busy", "api.server_busy.clear.not_supported.app_error"},
		{"jobs list", h.listJobs, http.MethodGet, "/api/v4/jobs", "api.jobs.list.not_supported.app_error"},
		{"jobs list by type", h.listJobsByType, http.MethodGet, "/api/v4/jobs/type/example", "api.jobs.list.not_supported.app_error"},
		{"job get", h.getJob, http.MethodGet, "/api/v4/jobs/example", "api.jobs.get.not_supported.app_error"},
		{"job create", h.createJob, http.MethodPost, "/api/v4/jobs", "api.jobs.create.not_supported.app_error"},
		{"job cancel", h.cancelJob, http.MethodPost, "/api/v4/jobs/example/cancel", "api.jobs.cancel.not_supported.app_error"},
		{"job patch", h.patchJobStatus, http.MethodPatch, "/api/v4/jobs/example/status", "api.jobs.patch.not_supported.app_error"},
		{"job download", h.downloadJob, http.MethodGet, "/api/v4/jobs/example/download", "api.jobs.download.not_supported.app_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(`{"enabled":true}`))
			rr := httptest.NewRecorder()
			test.handler(rr, req)
			assertAdminCompatNotSupported(t, rr, test.wantID)
		})
	}
}

func TestAdminCompatRolePatchDoesNotClaimPersistence(t *testing.T) {
	h := &handlers{}
	req := requestWithRouteParam(http.MethodPut, "/api/v4/roles/system_admin/patch", "roleID", "system_admin", []byte(`{"permissions":[]}`))
	rr := httptest.NewRecorder()
	h.patchRole(rr, req)
	assertAdminCompatNotSupported(t, rr, "api.roles.patch_role.not_supported.app_error")
}

func TestAdminCompatRolesByNames(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/api/v4/roles/names", bytes.NewBufferString(`["system_admin","missing"]`))
	rr := httptest.NewRecorder()

	h.getRolesByNames(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []compatRole
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode roles: %v", err)
	}
	if len(got) != 1 || got[0].Name != "system_admin" {
		t.Fatalf("roles = %#v, want system_admin only", got)
	}
}
