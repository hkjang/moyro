package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/moyro/server/internal/buildinfo"
)

type compatRole struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name"`
	Description   string   `json:"description"`
	Permissions   []string `json:"permissions"`
	SchemeManaged bool     `json:"scheme_managed"`
	BuiltIn       bool     `json:"built_in"`
	CreateAt      int64    `json:"create_at"`
	UpdateAt      int64    `json:"update_at"`
	DeleteAt      int64    `json:"delete_at"`
}

var compatRoles = []compatRole{
	{
		ID:          "system_admin",
		Name:        "system_admin",
		DisplayName: "System Admin",
		Description: "Full system administration role.",
		Permissions: []string{
			"manage_system", "manage_roles", "manage_jobs", "manage_oauth",
			"manage_system_wide_oauth", "manage_plugins", "read_jobs",
		},
		BuiltIn: true,
	},
	{
		ID:          "system_user",
		Name:        "system_user",
		DisplayName: "System User",
		Description: "Default authenticated user role.",
		Permissions: []string{
			"create_public_channel", "create_private_channel", "create_post",
			"use_channel_mentions", "manage_slash_commands",
		},
		BuiltIn: true,
	},
	{
		ID:          "system_guest",
		Name:        "system_guest",
		DisplayName: "System Guest",
		Description: "Restricted authenticated guest role.",
		Permissions: []string{
			"create_post", "use_channel_mentions",
		},
		BuiltIn: true,
	},
	{
		ID:          "team_admin",
		Name:        "team_admin",
		DisplayName: "Team Admin",
		Description: "Team-scoped administration role.",
		Permissions: []string{
			"manage_team", "manage_public_channel_properties", "manage_private_channel_properties",
		},
		BuiltIn: true,
	},
}

func init() {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	for i := range compatRoles {
		compatRoles[i].CreateAt = now
		compatRoles[i].UpdateAt = now
	}
}

func (h *handlers) adminConfigSnapshot() map[string]any {
	cfg := h.cfg
	site := defaultSiteSettings()
	if h.native != nil {
		site = h.native.currentSiteSettings()
	}
	listen := ""
	publicURL := site.PublicBaseURL
	pluginDir := ""
	fileRoot := ""
	fileBackend := "fs"
	linkPreviews := true
	smtpHost := ""
	smtpPort := ""
	smtpFrom := ""
	smtpTLS := false
	allowedOutgoing := append([]string(nil), site.AllowedOutgoingHosts...)
	tokenTTL := ""
	if cfg != nil {
		listen = cfg.Listen
		if publicURL == "" {
			publicURL = cfg.PublicBaseURL
		}
		pluginDir = cfg.PluginDir
		fileRoot = cfg.FileStorageRoot
		fileBackend = cfg.FileBackend
		linkPreviews = cfg.LinkPreviewsEnabled
		smtpHost = cfg.SMTPHost
		smtpPort = cfg.SMTPPort
		smtpFrom = cfg.SMTPFrom
		smtpTLS = cfg.SMTPTLS
		if len(allowedOutgoing) == 0 {
			allowedOutgoing = append([]string(nil), cfg.AllowedOutgoingHosts...)
		}
		tokenTTL = cfg.TokenTTL.String()
	}
	return map[string]any{
		"ServiceSettings": map[string]any{
			"SiteURL":                 publicURL,
			"ListenAddress":           listen,
			"EnableLinkPreviews":      linkPreviews,
			"AllowedOutgoingHosts":    allowedOutgoing,
			"SessionLengthWebInHours": tokenTTL,
			"TLSStrictTransport":      false,
		},
		"TeamSettings": map[string]any{
			"SiteName":           site.SiteName,
			"EnableOpenServer":   site.LocalSignupEnabled,
			"EnableTeamCreation": true,
		},
		"PluginSettings": map[string]any{
			"Enable":        true,
			"EnableUploads": false,
			"Directory":     pluginDir,
		},
		"FileSettings": map[string]any{
			"DriverName": fileBackend,
			"Directory":  fileRoot,
		},
		"EmailSettings": map[string]any{
			"SMTPServer":    smtpHost,
			"SMTPPort":      smtpPort,
			"FeedbackEmail": smtpFrom,
			"ConnectionSecurity": func() string {
				if smtpTLS {
					return "TLS"
				}
				return ""
			}(),
			"SMTPPassword": "",
		},
		"SupportSettings": map[string]any{
			"SupportEmail": "support@example.invalid",
		},
		"SqlSettings": map[string]any{
			"DriverName": "postgres",
			"DataSource": "********",
		},
		"NativeAppSettings": map[string]any{
			"AppDownloadLink": "",
		},
		"AnnouncementSettings": map[string]any{
			"EnableBanner": false,
			"BannerText":   "",
		},
	}
}

func (h *handlers) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.adminConfigSnapshot())
}

// writeAdminCompatNotSupported keeps legacy Mattermost administration
// routes discoverable without claiming that an update was applied.  A
// non-2xx Mattermost AppError is important here: the System Console and SDKs
// otherwise treat any 200 response as a durable configuration change.
func writeAdminCompatNotSupported(w http.ResponseWriter, id, feature string) {
	writeError(
		w,
		http.StatusNotImplemented,
		id,
		fmt.Sprintf("%s is not dynamically supported in moyro %s", feature, buildinfo.Current().Version),
	)
}

func (h *handlers) putConfig(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.config.update_config.not_supported.app_error", "legacy Mattermost configuration updates")
}

func (h *handlers) patchConfig(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.config.patch_config.not_supported.app_error", "legacy Mattermost configuration patches")
}

func (h *handlers) reloadConfig(w http.ResponseWriter, r *http.Request) {
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "system.config_reload.rejected", "config", map[string]any{"reason": "not_supported"})
	}
	writeAdminCompatNotSupported(w, "api.config.reload.not_supported.app_error", "legacy Mattermost configuration reload")
}

func (h *handlers) getLogs(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.logs.list.not_supported.app_error", "legacy runtime log listing")
}

func (h *handlers) postLog(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if h.logger != nil {
		h.logger.Info("client log", "actor", userID(r), "payload", body)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) downloadLogs(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.logs.download.not_supported.app_error", "legacy runtime log download")
}

func (h *handlers) listUserAudits(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "userID")
	if targetID == "me" || targetID == "" {
		targetID = userID(r)
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if h.audit == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	entries, err := h.audit.List(r.Context(), limit, "", targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.audit.list.app_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *handlers) getClusterStatus(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.cluster.status.not_supported.app_error", "legacy cluster status")
}

func (h *handlers) pluginDirectory() string {
	if h.cfg == nil {
		return ""
	}
	return h.cfg.PluginDir
}

func (h *handlers) getServerBusy(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.server_busy.get.not_supported.app_error", "legacy server busy state")
}

func (h *handlers) setServerBusy(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.server_busy.set.not_supported.app_error", "legacy server busy state")
}

func (h *handlers) clearServerBusy(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.server_busy.clear.not_supported.app_error", "legacy server busy state")
}

func (h *handlers) listRoles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, compatRoles)
}

func (h *handlers) getRole(w http.ResponseWriter, r *http.Request) {
	role, ok := findCompatRole(chi.URLParam(r, "roleID"))
	if !ok {
		writeError(w, http.StatusNotFound, "api.roles.get_role_by_id.app_error", "role not found")
		return
	}
	writeJSON(w, http.StatusOK, role)
}

func (h *handlers) getRoleByName(w http.ResponseWriter, r *http.Request) {
	role, ok := findCompatRole(chi.URLParam(r, "name"))
	if !ok {
		writeError(w, http.StatusNotFound, "api.roles.get_role_by_name.app_error", "role not found")
		return
	}
	writeJSON(w, http.StatusOK, role)
}

func (h *handlers) getRolesByNames(w http.ResponseWriter, r *http.Request) {
	var names []string
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &names); err != nil {
			var envelope struct {
				Names []string `json:"names"`
			}
			_ = json.Unmarshal(raw, &envelope)
			names = envelope.Names
		}
	}
	out := make([]compatRole, 0, len(names))
	for _, name := range names {
		if role, ok := findCompatRole(name); ok {
			out = append(out, role)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) patchRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := findCompatRole(chi.URLParam(r, "roleID")); !ok {
		writeError(w, http.StatusNotFound, "api.roles.patch_role.app_error", "role not found")
		return
	}
	writeAdminCompatNotSupported(w, "api.roles.patch_role.not_supported.app_error", "legacy Mattermost role updates")
}

func findCompatRole(idOrName string) (compatRole, bool) {
	for _, role := range compatRoles {
		if role.ID == idOrName || role.Name == idOrName {
			return role, true
		}
	}
	return compatRole{}, false
}

func (h *handlers) listJobs(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.jobs.list.not_supported.app_error", "legacy background jobs")
}

func (h *handlers) listJobsByType(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.jobs.list.not_supported.app_error", "legacy background jobs")
}

func (h *handlers) getJob(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.jobs.get.not_supported.app_error", "legacy background jobs")
}

func (h *handlers) createJob(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.jobs.create.not_supported.app_error", "legacy background jobs")
}

func (h *handlers) cancelJob(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.jobs.cancel.not_supported.app_error", "legacy background jobs")
}

func (h *handlers) patchJobStatus(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.jobs.patch.not_supported.app_error", "legacy background jobs")
}

func (h *handlers) downloadJob(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.jobs.download.not_supported.app_error", "legacy background jobs")
}

func (h *handlers) uploadPlugin(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.plugin.upload.not_supported.app_error", "runtime plugin upload")
}

func (h *handlers) deletePlugin(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.plugin.delete.not_supported.app_error", "runtime plugin deletion")
}

func (h *handlers) enablePlugin(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.plugin.enable.not_supported.app_error", "runtime plugin enablement")
}

func (h *handlers) disablePlugin(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.plugin.disable.not_supported.app_error", "runtime plugin disablement")
}

func (h *handlers) installPluginFromURL(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.plugin.install_from_url.not_supported.app_error", "plugin installation from a URL")
}

func (h *handlers) installPluginFromMarketplace(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.plugin.install_marketplace.not_supported.app_error", "plugin Marketplace installation")
}

func (h *handlers) getMarketplaceFirstAdminVisit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"first_admin_visit_marketplace_status": false})
}

func (h *handlers) saveMarketplaceFirstAdminVisit(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.plugin.marketplace_preference.not_supported.app_error", "plugin Marketplace preferences")
}

func (h *handlers) getLicenseLoadMetric(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"active_users":     0,
		"registered_users": 0,
		"teams":            0,
		"posts":            0,
		"channels":         0,
	})
}

func (h *handlers) getLicenseRenewal(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"is_licensed": false,
		"renewal":     nil,
	})
}

func (h *handlers) uploadLicense(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.license.upload.not_supported.app_error", "license upload")
}

func (h *handlers) deleteLicense(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.license.delete.not_supported.app_error", "license deletion")
}

func (h *handlers) getPreviousTrialLicense(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"is_trial": false,
		"license":  nil,
	})
}

func (h *handlers) requestTrialLicense(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "api.license.trial_not_available.app_error", "trial license is not available in this build")
}

func (h *handlers) getUpgradeToEnterpriseAllowed(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"allowed": false})
}

func (h *handlers) getUpgradeToEnterpriseStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "not_available"})
}

func (h *handlers) upgradeToEnterprise(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "api.enterprise.upgrade_not_available.app_error", "enterprise upgrade is not available in this build")
}

func (h *handlers) getBrandImage(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "api.brand.get_image.app_error", "brand image not configured")
}

func (h *handlers) uploadBrandImage(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.brand.upload_image.not_supported.app_error", "brand image upload")
}

func (h *handlers) deleteBrandImage(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.brand.delete_image.not_supported.app_error", "brand image deletion")
}

func (h *handlers) listLDAPGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) linkLDAPGroup(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.ldap.group_link.not_supported.app_error", "LDAP group linking")
}

func (h *handlers) unlinkLDAPGroup(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.ldap.group_unlink.not_supported.app_error", "LDAP group unlinking")
}

func (h *handlers) uploadLDAPCertificate(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.ldap.certificate_upload.not_supported.app_error", "LDAP certificate upload")
}

func (h *handlers) deleteLDAPCertificate(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.ldap.certificate_delete.not_supported.app_error", "LDAP certificate deletion")
}

func (h *handlers) ldapDisabledOK(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.ldap.disabled.app_error", "LDAP administration")
}

func (h *handlers) getSAMLCertificateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"idp_certificate_file":     false,
		"public_certificate_file":  false,
		"private_key_file":         false,
		"can_login_with_saml":      false,
		"can_login_with_saml_test": false,
	})
}

func (h *handlers) getSAMLMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<EntityDescriptor entityID="moyro-dev"></EntityDescriptor>`))
}

func (h *handlers) uploadSAMLMetadataFromIDP(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.saml.metadata.not_supported.app_error", "SAML metadata import")
}

func (h *handlers) resetSAMLAuthData(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.saml.reset.not_supported.app_error", "SAML authentication reset")
}

func (h *handlers) uploadSAMLCertificate(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.saml.certificate_upload.not_supported.app_error", "SAML certificate upload")
}

func (h *handlers) deleteSAMLCertificate(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.saml.certificate_delete.not_supported.app_error", "SAML certificate deletion")
}

func (h *handlers) getContentFlaggingConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "review_statuses": []string{}})
}

func (h *handlers) putContentFlaggingConfig(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.content_flagging.config.not_supported.app_error", "content flagging configuration")
}

func (h *handlers) getContentFlaggingFlagConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}

func (h *handlers) getAIBridge(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "service_url": ""})
}

func (h *handlers) putAIBridge(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.ai_bridge.config.not_supported.app_error", "legacy AI bridge configuration")
}

func (h *handlers) deleteAIBridge(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.ai_bridge.delete.not_supported.app_error", "legacy AI bridge deletion")
}

func (h *handlers) getSystemNotice(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      chi.URLParam(r, "noticeID"),
		"visible": false,
	})
}

func (h *handlers) markSystemNoticeViewed(w http.ResponseWriter, r *http.Request) {
	writeAdminCompatNotSupported(w, "api.system_notice.view.not_supported.app_error", "system notice acknowledgement")
}

func (h *handlers) getSupportPacket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="support-packet.zip"`)
	w.WriteHeader(http.StatusOK)
}

func (h *handlers) migrateAuthLDAP(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "api.ldap.migrate_not_available.app_error", "LDAP auth migration is not available in this build")
}
