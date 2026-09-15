package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/hkjang/moyro/server/internal/activityevents"
	eventmail "github.com/hkjang/moyro/server/internal/mail"
	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/hkjang/moyro/server/internal/store"
)

// Event notification mail: the administrator-managed relay configuration,
// the delivery log, and the test send.
//
// The configuration lives in the settings store under section "mail" — the
// JSON row holds everything but the password, which is an encrypted secret
// row that the read API reports only as "configured". The live copy, with
// the password, is held by the mail service in memory.

const (
	mailSettingsSection = "mail"
	mailPasswordKey     = "smtp-password"
)

// mailSettingsView is the request and response shape of the mail section.
// Password is accepted on write and blanked before the row is stored or the
// answer is written; PasswordConfigured says whether a secret exists.
type mailSettingsView struct {
	eventmail.Config
	Password           string `json:"password,omitempty"`
	ClearPassword      bool   `json:"clear_password,omitempty"`
	PasswordConfigured bool   `json:"password_configured"`
}

func (n *nativeServices) loadMailConfig(ctx context.Context) (eventmail.Config, bool, error) {
	value := eventmail.Default()
	err := n.loadJSON(ctx, mailSettingsSection, nativeSettingsKey, &value)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return eventmail.Config{}, false, err
	}
	value.Normalize()
	password, err := n.revealOptionalSecret(ctx, mailSettingsSection, mailPasswordKey)
	if err != nil {
		return eventmail.Config{}, false, err
	}
	value.Password = password
	return value, password != "", nil
}

// reloadMail hands the stored configuration to the mail service. A stored
// configuration this build no longer accepts leaves mail off rather than
// taking the management services down.
func (n *nativeServices) reloadMail(ctx context.Context, service *eventmail.Service) error {
	if service == nil {
		return nil
	}
	value, _, err := n.loadMailConfig(ctx)
	if err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	service.Configure(value)
	return nil
}

func (h *handlers) getMailSettings(w http.ResponseWriter, r *http.Request) {
	value, configured, err := h.native.loadMailConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.settings.read", err.Error())
		return
	}
	value.Password = ""
	writeJSON(w, http.StatusOK, mailSettingsView{Config: value, PasswordConfigured: configured})
}

// patchMailSettings stores the section and applies it immediately. An empty
// password keeps the stored one; clear_password removes it.
func (h *handlers) patchMailSettings(w http.ResponseWriter, r *http.Request, decoder *json.Decoder, actor string) {
	view := mailSettingsView{Config: eventmail.Default()}
	if err := decoder.Decode(&view); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.settings.body", err.Error())
		return
	}
	view.Config.Normalize()
	if err := view.Config.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.settings.mail", err.Error())
		return
	}
	if strings.ContainsAny(view.Password, "\r\n\x00") {
		writeError(w, http.StatusBadRequest, "api.moyro.settings.mail", "password contains a line break")
		return
	}
	unlock := h.native.beginSettingsUpdate()
	defer unlock()
	ctx := r.Context()
	secret := view.Password
	if view.ClearPassword {
		secret = ""
		if err := h.native.settings.Delete(ctx, mailSettingsSection, mailPasswordKey, nil); err != nil && !errors.Is(err, settings.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "api.moyro.settings.save", err.Error())
			return
		}
	} else if strings.TrimSpace(secret) == "" {
		stored, err := h.native.revealOptionalSecret(ctx, mailSettingsSection, mailPasswordKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.settings.read", err.Error())
			return
		}
		secret = stored
	}
	stored := view.Config
	stored.Password = ""
	newSecret := []byte(nil)
	if !view.ClearPassword && strings.TrimSpace(view.Password) != "" {
		newSecret = []byte(view.Password)
	}
	if _, err := h.native.settings.PutJSONAndOptionalSecret(ctx, mailSettingsSection, nativeSettingsKey, stored, mailPasswordKey, newSecret, actor); err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.settings.save", err.Error())
		return
	}
	live := view.Config
	live.Password = secret
	if h.mail != nil {
		h.mail.Configure(live)
	}
	if h.audit != nil {
		h.audit.LogAsync(actor, "settings.mail.update", mailSettingsSection, map[string]any{"enabled": live.Enabled, "smtp_host": live.SMTPHost})
	}
	writeJSON(w, http.StatusOK, mailSettingsView{Config: stored, PasswordConfigured: secret != ""})
}

// listMailDeliveries shows what was attempted, so "it never arrived" has an
// answer.
func (h *handlers) listMailDeliveries(w http.ResponseWriter, r *http.Request) {
	if h.mail == nil {
		writeJSON(w, http.StatusOK, eventmail.Page{Items: []eventmail.Delivery{}, Summary: eventmail.Summary{Status: map[string]int{}}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, err := h.mail.Deliveries(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.mail.deliveries", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// sendTestMail proves the relay works with the saved settings before anybody
// depends on it. It is synchronous on purpose: the administrator is waiting
// for the answer.
func (h *handlers) sendTestMail(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Recipient string `json:"recipient"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.mail.body", err.Error())
		return
	}
	if h.mail == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.mail.unavailable", "mail service is unavailable")
		return
	}
	recipient := strings.TrimSpace(input.Recipient)
	if recipient == "" {
		// The administrator's own address is the natural default.
		if actor, err := h.auth.UserByID(r.Context(), userID(r)); err == nil && actor != nil {
			recipient = strings.TrimSpace(actor.Email)
		}
	}
	if _, err := mail.ParseAddress(recipient); err != nil || strings.ContainsAny(recipient, "<>\r\n") {
		writeError(w, http.StatusBadRequest, "api.moyro.mail.recipient", "recipient must be an email address")
		return
	}
	if err := h.mail.SendTest(r.Context(), recipient, userID(r)); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, eventmail.ErrDisabled) || errors.Is(err, eventmail.ErrInvalid) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"sent": false, "recipient": recipient, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "recipient": recipient})
}

// mailDirectory is the one lookup mail borrows from the account table: id to
// address, minus accounts that have no address or turned event mail off.
type mailDirectory struct{ db *store.DB }

func (d mailDirectory) LookupEmails(ctx context.Context, userIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if d.db == nil || len(userIDs) == 0 {
		return out, nil
	}
	rows, err := d.db.Pool.Query(ctx, `
		SELECT id, email FROM users
		WHERE id = ANY($1) AND delete_at = 0 AND email <> ''
		  AND COALESCE(email_prefs ->> 'events_enabled', 'true') <> 'false'
	`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, address string
		if err := rows.Scan(&id, &address); err != nil {
			return nil, err
		}
		if strings.Contains(address, "@") {
			out[id] = address
		}
	}
	return out, rows.Err()
}

// notifyMailForActivity hands a committed inbox event to the mail service,
// which decides whether the kind is one people wait on. It never blocks: the
// service queues and returns.
func notifyMailForActivity(ctx context.Context, service *eventmail.Service, event *activityevents.Event) {
	if service == nil || event == nil {
		return
	}
	if notification, ok := eventmail.FromActivity(*event); ok {
		service.Notify(ctx, notification)
	}
}
