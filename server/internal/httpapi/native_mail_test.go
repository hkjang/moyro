package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	eventmail "github.com/hkjang/moyro/server/internal/mail"
	"github.com/hkjang/moyro/server/internal/settings"
)

// mailSettingsRepository is the memory store plus what the mail section
// needs: atomic batch writes for the JSON row and its secret, and a Delete
// that actually forgets the row.
type mailSettingsRepository struct {
	memorySettingsRepository
}

func (m *mailSettingsRepository) PutBatch(ctx context.Context, records []settings.Record) ([]settings.Record, error) {
	out := make([]settings.Record, 0, len(records))
	for _, record := range records {
		stored, err := m.Put(ctx, record, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, stored)
	}
	return out, nil
}

func (m *mailSettingsRepository) Delete(_ context.Context, section, key string, _ *int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, section+"/"+key)
	return nil
}

// plainCipher keeps secrets readable in memory; the test only cares that the
// API never hands them back.
type plainCipher struct{}

func (plainCipher) Encrypt(_ string, plaintext []byte) (string, []byte, []byte, error) {
	return "test", []byte("nonce"), append([]byte(nil), plaintext...), nil
}
func (plainCipher) Decrypt(_, _ string, _, ciphertext []byte) ([]byte, error) {
	return append([]byte(nil), ciphertext...), nil
}

type mailSendRecorder struct {
	mu   sync.Mutex
	sent []eventmail.Message
	fail error
}

func (r *mailSendRecorder) send(_ context.Context, _ eventmail.Config, message eventmail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, message)
	return r.fail
}

func mailTestHandlers(t *testing.T) (*handlers, *mailSettingsRepository, *mailSendRecorder) {
	t.Helper()
	repo := &mailSettingsRepository{}
	service, err := settings.New(repo, plainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &mailSendRecorder{}
	mailService := eventmail.New(nil, nil, nil)
	mailService.SetSender(recorder.send)
	native := &nativeServices{settings: service}
	h := &handlers{native: native, mail: mailService}
	if err := native.reloadMail(context.Background(), mailService); err != nil {
		t.Fatal(err)
	}
	return h, repo, recorder
}

func patchMail(t *testing.T, h *handlers, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := requestWithRouteParam(http.MethodPatch, "/api/moyro/v1/admin/settings/mail", "section", "mail", []byte(body))
	req = req.WithContext(context.WithValue(req.Context(), userIDKey, "admin"))
	h.patchNativeSettings(rec, req)
	return rec
}

func getMail(t *testing.T, h *handlers) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.getNativeSettings(rec, requestWithRouteParam(http.MethodGet, "/api/moyro/v1/admin/settings/mail", "section", "mail", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET mail = %d %s", rec.Code, rec.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

// A fresh installation has mail off, and reading the section never returns
// the password — only whether one is set.
func TestMailSettingsDefaultOffAndPasswordNeverReturned(t *testing.T) {
	h, repo, _ := mailTestHandlers(t)
	view := getMail(t, h)
	if view["enabled"] != false || view["smtp_port"] != float64(25) || view["security"] != "auto" || view["password_configured"] != false {
		t.Fatalf("fresh mail settings = %v", view)
	}
	if h.mail.Enabled() {
		t.Fatal("fresh mail service reports enabled")
	}

	rec := patchMail(t, h, `{"enabled":true,"smtp_host":"relay.corp.example","from_address":"moyro@corp.example","username":"relay-user","password":"s3cret","security":"starttls"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH mail = %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "s3cret") || !strings.Contains(body, `"password_configured":true`) {
		t.Fatalf("PATCH response leaks or misreports the password: %s", body)
	}
	view = getMail(t, h)
	if _, present := view["password"]; present || view["password_configured"] != true || view["username"] != "relay-user" {
		t.Fatalf("GET after save = %v", view)
	}
	// The stored JSON row carries no password either; only the secret row does.
	if row := repo.rows["mail/config"]; strings.Contains(string(row.ValueJSON), "s3cret") {
		t.Fatalf("settings row stores the password: %s", row.ValueJSON)
	}
	if live := h.mail.Config(); live.Password != "s3cret" || !live.Enabled || live.Security != "starttls" {
		t.Fatalf("live configuration = %+v", live)
	}

	// Saving again without a password keeps the stored one.
	rec = patchMail(t, h, `{"enabled":true,"smtp_host":"relay.corp.example","from_address":"moyro@corp.example","username":"relay-user","security":"starttls","from_name":"moyro 알림"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH without password = %d %s", rec.Code, rec.Body.String())
	}
	if live := h.mail.Config(); live.Password != "s3cret" || live.FromName != "moyro 알림" {
		t.Fatalf("live configuration after password-less save = %+v", live)
	}
	// clear_password forgets it.
	rec = patchMail(t, h, `{"enabled":true,"smtp_host":"relay.corp.example","from_address":"moyro@corp.example","clear_password":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"password_configured":false`) {
		t.Fatalf("PATCH clear_password = %d %s", rec.Code, rec.Body.String())
	}
	if live := h.mail.Config(); live.Password != "" {
		t.Fatal("password survived clear_password")
	}
	// A restart reloads the same truth.
	fresh := eventmail.New(nil, nil, nil)
	if err := h.native.reloadMail(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	if reloaded := fresh.Config(); !reloaded.Enabled || reloaded.SMTPHost != "relay.corp.example" || reloaded.Password != "" {
		t.Fatalf("reloaded configuration = %+v", reloaded)
	}
}

func TestMailSettingsRejectsAnUnreachableEnabledConfiguration(t *testing.T) {
	h, _, _ := mailTestHandlers(t)
	if rec := patchMail(t, h, `{"enabled":true,"from_address":"moyro@corp.example"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "smtp_host") {
		t.Fatalf("enabled without host = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patchMail(t, h, `{"enabled":false,"smtp_host":"relay.corp.example","security":"ssl"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown security word = %d %s", rec.Code, rec.Body.String())
	}
	// Disabled but half-filled is fine: the administrator can come back.
	if rec := patchMail(t, h, `{"enabled":false,"smtp_host":"relay.corp.example"}`); rec.Code != http.StatusOK {
		t.Fatalf("disabled half-filled = %d %s", rec.Code, rec.Body.String())
	}
	if h.mail.Enabled() {
		t.Fatal("disabled configuration reports enabled")
	}
}

func TestSendTestMailReportsOutcomeInPlace(t *testing.T) {
	h, _, recorder := mailTestHandlers(t)
	send := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/moyro/v1/admin/mail/test", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userIDKey, "admin"))
		h.sendTestMail(rec, req)
		return rec
	}
	if rec := send(`{"recipient":"admin@corp.example"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("test send while disabled = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patchMail(t, h, `{"enabled":true,"smtp_host":"relay.corp.example","from_address":"moyro@corp.example"}`); rec.Code != http.StatusOK {
		t.Fatalf("enable = %d %s", rec.Code, rec.Body.String())
	}
	if rec := send(`{"recipient":"not-an-address"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad recipient = %d %s", rec.Code, rec.Body.String())
	}
	if rec := send(`{"recipient":"admin@corp.example"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sent":true`) {
		t.Fatalf("test send = %d %s", rec.Code, rec.Body.String())
	}
	recorder.fail = errors.New("smtp connect: dial tcp: connection refused")
	rec := send(`{"recipient":"admin@corp.example"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "connection refused") || !strings.Contains(rec.Body.String(), `"sent":false`) {
		t.Fatalf("test send against dead relay = %d %s", rec.Code, rec.Body.String())
	}
	if len(recorder.sent) != 2 || recorder.sent[0].To != "admin@corp.example" {
		t.Fatalf("relay saw %#v", recorder.sent)
	}
}
