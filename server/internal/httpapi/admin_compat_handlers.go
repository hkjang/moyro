package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var compatServerBusy atomic.Bool

var compatJobs = struct {
	sync.Mutex
	rows map[string]map[string]any
}{
	rows: map[string]map[string]any{},
}

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
	listen := ""
	publicURL := ""
	pluginDir := ""
	fileRoot := ""
	fileBackend := "fs"
	linkPreviews := true
	smtpHost := ""
	smtpPort := ""
	smtpFrom := ""
	smtpTLS := false
	allowedOutgoing := []string(nil)
	tokenTTL := ""
	if cfg != nil {
		listen = cfg.Listen
		publicURL = cfg.PublicBaseURL
		pluginDir = cfg.PluginDir
		fileRoot = cfg.FileStorageRoot
		fileBackend = cfg.FileBackend
		linkPreviews = cfg.LinkPreviewsEnabled
		smtpHost = cfg.SMTPHost
		smtpPort = cfg.SMTPPort
		smtpFrom = cfg.SMTPFrom
		smtpTLS = cfg.SMTPTLS
		allowedOutgoing = cfg.AllowedOutgoingHosts
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
			"SiteName":           "RelayChat",
			"EnableOpenServer":   true,
			"EnableTeamCreation": true,
		},
		"PluginSettings": map[string]any{
			"Enable":        true,
			"EnableUploads": true,
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

func (h *handlers) putConfig(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, h.adminConfigSnapshot())
}

func (h *handlers) patchConfig(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, h.adminConfigSnapshot())
}

func (h *handlers) reloadConfig(w http.ResponseWriter, r *http.Request) {
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "system.config_reload", "config", map[string]any{})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) getLogs(w http.ResponseWriter, r *http.Request) {
	perPage, _ := strconv.Atoi(r.URL.Query().Get("logs_per_page"))
	if perPage <= 0 || perPage > 100 {
		perPage = 20
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rows := make([]string, 0, perPage)
	rows = append(rows,
		fmt.Sprintf("%s [INFO] RelayChat admin log stream ready", now),
		fmt.Sprintf("%s [INFO] go=%s goroutines=%d", now, runtime.Version(), runtime.NumGoroutine()),
	)
	for len(rows) < perPage {
		rows = append(rows, fmt.Sprintf("%s [DEBUG] compat log placeholder %02d", now, len(rows)+1))
	}
	writeJSON(w, http.StatusOK, rows)
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
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="relaychat.log"`)
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "%s [INFO] RelayChat compatibility log export\n", time.Now().UTC().Format(time.RFC3339))
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
	writeJSON(w, http.StatusOK, []map[string]any{
		{
			"id":               "local",
			"version":          "relaychat-dev",
			"config_hash":      "local",
			"status":           "OK",
			"last_ping_at":     time.Now().UnixMilli(),
			"hostname":         "localhost",
			"ipaddress":        "127.0.0.1",
			"cluster_id":       "local-dev",
			"schema_version":   "",
			"health_score":     1,
			"server_version":   runtime.Version(),
			"uptime_millis":    0,
			"busy":             compatServerBusy.Load(),
			"plugin_directory": h.pluginDirectory(),
		},
	})
}

func (h *handlers) pluginDirectory() string {
	if h.cfg == nil {
		return ""
	}
	return h.cfg.PluginDir
}

func (h *handlers) getServerBusy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"busy": compatServerBusy.Load()})
}

func (h *handlers) setServerBusy(w http.ResponseWriter, r *http.Request) {
	compatServerBusy.Store(true)
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) clearServerBusy(w http.ResponseWriter, r *http.Request) {
	compatServerBusy.Store(false)
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
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
	role, ok := findCompatRole(chi.URLParam(r, "roleID"))
	if !ok {
		writeError(w, http.StatusNotFound, "api.roles.patch_role.app_error", "role not found")
		return
	}
	var patch struct {
		Permissions []string `json:"permissions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&patch)
	if patch.Permissions != nil {
		role.Permissions = patch.Permissions
	}
	role.UpdateAt = time.Now().UnixMilli()
	writeJSON(w, http.StatusOK, role)
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
	writeJSON(w, http.StatusOK, compatJobRows(""))
}

func (h *handlers) listJobsByType(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, compatJobRows(chi.URLParam(r, "jobType")))
}

func (h *handlers) getJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "jobID")
	compatJobs.Lock()
	defer compatJobs.Unlock()
	if row, ok := compatJobs.rows[id]; ok {
		writeJSON(w, http.StatusOK, cloneCompatJob(row))
		return
	}
	writeError(w, http.StatusNotFound, "api.jobs.get_job.app_error", "job not found")
}

func (h *handlers) createJob(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	jobType, _ := body["type"].(string)
	if strings.TrimSpace(jobType) == "" {
		jobType = "compatibility"
	}
	id := uuid.NewString()
	now := time.Now().UnixMilli()
	row := map[string]any{
		"id":               id,
		"type":             jobType,
		"priority":         0,
		"create_at":        now,
		"start_at":         int64(0),
		"last_activity_at": now,
		"status":           "pending",
		"progress":         0,
		"data":             body,
	}
	compatJobs.Lock()
	compatJobs.rows[id] = row
	compatJobs.Unlock()
	writeJSON(w, http.StatusCreated, cloneCompatJob(row))
}

func (h *handlers) cancelJob(w http.ResponseWriter, r *http.Request) {
	h.setCompatJobStatus(w, r, "canceled")
}

func (h *handlers) patchJobStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Status == "" {
		body.Status = "pending"
	}
	h.setCompatJobStatus(w, r, body.Status)
}

func (h *handlers) setCompatJobStatus(w http.ResponseWriter, r *http.Request, status string) {
	id := chi.URLParam(r, "jobID")
	compatJobs.Lock()
	defer compatJobs.Unlock()
	row, ok := compatJobs.rows[id]
	if !ok {
		writeError(w, http.StatusNotFound, "api.jobs.update_job.app_error", "job not found")
		return
	}
	row["status"] = status
	row["last_activity_at"] = time.Now().UnixMilli()
	writeJSON(w, http.StatusOK, cloneCompatJob(row))
}

func (h *handlers) downloadJob(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="job.json"`)
	id := chi.URLParam(r, "jobID")
	compatJobs.Lock()
	row, ok := compatJobs.rows[id]
	compatJobs.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "api.jobs.download_job.app_error", "job not found")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func compatJobRows(jobType string) []map[string]any {
	compatJobs.Lock()
	defer compatJobs.Unlock()
	out := make([]map[string]any, 0, len(compatJobs.rows))
	for _, row := range compatJobs.rows {
		if jobType != "" && row["type"] != jobType {
			continue
		}
		out = append(out, cloneCompatJob(row))
	}
	return out
}

func cloneCompatJob(row map[string]any) map[string]any {
	cp := make(map[string]any, len(row))
	for k, v := range row {
		cp[k] = v
	}
	return cp
}

func (h *handlers) uploadPlugin(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(64 << 20)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "OK", "id": ""})
}

func (h *handlers) deletePlugin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) enablePlugin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) disablePlugin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) installPluginFromURL(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusCreated, map[string]any{"status": "OK", "plugin": body})
}

func (h *handlers) installPluginFromMarketplace(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusCreated, map[string]any{"status": "OK", "plugin": body})
}

func (h *handlers) getMarketplaceFirstAdminVisit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"first_admin_visit_marketplace_status": false})
}

func (h *handlers) saveMarketplaceFirstAdminVisit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
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
	_ = r.ParseMultipartForm(16 << 20)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "OK"})
}

func (h *handlers) deleteLicense(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
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
	_ = r.ParseMultipartForm(8 << 20)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "OK"})
}

func (h *handlers) deleteBrandImage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) listLDAPGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handlers) linkLDAPGroup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) unlinkLDAPGroup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) uploadLDAPCertificate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(8 << 20)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "OK"})
}

func (h *handlers) deleteLDAPCertificate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) ldapDisabledOK(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "OK", "enabled": false})
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
	_, _ = w.Write([]byte(`<EntityDescriptor entityID="relaychat-dev"></EntityDescriptor>`))
}

func (h *handlers) uploadSAMLMetadataFromIDP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "OK", "idp_descriptor_url": ""})
}

func (h *handlers) resetSAMLAuthData(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) uploadSAMLCertificate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(8 << 20)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "OK"})
}

func (h *handlers) deleteSAMLCertificate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) getContentFlaggingConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "review_statuses": []string{}})
}

func (h *handlers) putContentFlaggingConfig(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	body["enabled"] = false
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) getContentFlaggingFlagConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}

func (h *handlers) getAIBridge(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "service_url": ""})
}

func (h *handlers) putAIBridge(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	body["enabled"] = false
	writeJSON(w, http.StatusOK, body)
}

func (h *handlers) deleteAIBridge(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) getSystemNotice(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      chi.URLParam(r, "noticeID"),
		"visible": false,
	})
}

func (h *handlers) markSystemNoticeViewed(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *handlers) getSupportPacket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="support-packet.zip"`)
	w.WriteHeader(http.StatusOK)
}

func (h *handlers) migrateAuthLDAP(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "api.ldap.migrate_not_available.app_error", "LDAP auth migration is not available in this build")
}
