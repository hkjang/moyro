package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/settings"
)

type secretReadRepository struct{ err error }

func (r secretReadRepository) Get(context.Context, string, string) (settings.Record, error) {
	return settings.Record{}, r.err
}
func (secretReadRepository) List(context.Context, string) ([]settings.Record, error) {
	return nil, nil
}
func (secretReadRepository) Put(context.Context, settings.Record, *int64) (settings.Record, error) {
	return settings.Record{}, nil
}
func (secretReadRepository) Delete(context.Context, string, string, *int64) error { return nil }

type unusedSettingsCipher struct{}

func (unusedSettingsCipher) Encrypt(string, []byte) (string, []byte, []byte, error) {
	return "", nil, nil, nil
}
func (unusedSettingsCipher) Decrypt(string, string, []byte, []byte) ([]byte, error) {
	return nil, nil
}

func TestNativeSettingsPermissionIsSectionSpecific(t *testing.T) {
	if got := nativeSettingsPermission("key-policy"); got != rbac.PermissionManageKeyPermissions {
		t.Fatalf("key-policy permission = %q", got)
	}
	for _, section := range []string{"site", "mcp", "unknown"} {
		if got := nativeSettingsPermission(section); got != rbac.PermissionManageSettings {
			t.Fatalf("%s permission = %q", section, got)
		}
	}
}

func TestSettingsUpdatesAreSerializedThroughActivation(t *testing.T) {
	t.Parallel()
	native := &nativeServices{}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var completed atomic.Int32

	go func() {
		unlock := native.beginSettingsUpdate()
		close(firstEntered)
		<-releaseFirst
		completed.Add(1)
		unlock()
	}()
	<-firstEntered
	go func() {
		unlock := native.beginSettingsUpdate()
		close(secondEntered)
		completed.Add(1)
		unlock()
	}()

	select {
	case <-secondEntered:
		t.Fatal("second settings activation entered before the first committed and activated")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second settings activation did not resume")
	}
	if completed.Load() != 2 {
		t.Fatalf("completed updates = %d, want 2", completed.Load())
	}
}

func TestRevealOptionalSecretOnlyTreatsNotFoundAsOmitted(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		repoErr error
	}{
		{name: "not found", repoErr: settings.ErrNotFound},
		{name: "database failure", repoErr: errors.New("database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := settings.New(secretReadRepository{err: test.repoErr}, unusedSettingsCipher{})
			if err != nil {
				t.Fatal(err)
			}
			native := &nativeServices{settings: service}
			value, gotErr := native.revealOptionalSecret(context.Background(), "ai", "provider-api-key")
			if value != "" {
				t.Fatalf("value = %q, want redacted omission", value)
			}
			if errors.Is(test.repoErr, settings.ErrNotFound) {
				if gotErr != nil {
					t.Fatalf("missing optional secret error = %v", gotErr)
				}
			} else if !errors.Is(gotErr, test.repoErr) {
				t.Fatalf("secret read error = %v, want %v", gotErr, test.repoErr)
			}
		})
	}
}

func TestNativeBearerOnlyRejectsQueryCredentials(t *testing.T) {
	nextCalled := false
	handler := nativeBearerOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true }))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://moyro.local/api/moyro/v1/system/info?access_token=secret", nil))
	if recorder.Code != http.StatusBadRequest || nextCalled {
		t.Fatalf("query credential response = %d, next=%v", recorder.Code, nextCalled)
	}
}

func TestValidateSiteSettingsCanonicalizesValues(t *testing.T) {
	value := siteSettingsView{
		SiteName:      "  Moyro Intranet  ",
		PublicBaseURL: "https://chat.internal/",
		AllowedOutgoingHosts: []string{
			"Keycloak.Internal.", "10.20.30.40", "keycloak.internal", "[fd00::1]",
		},
	}
	if err := validateSiteSettings(&value); err != nil {
		t.Fatalf("validate site settings: %v", err)
	}
	if value.SiteName != "Moyro Intranet" || value.PublicBaseURL != "https://chat.internal" {
		t.Fatalf("unexpected canonical site settings: %#v", value)
	}
	wantHosts := []string{"10.20.30.40", "fd00::1", "keycloak.internal"}
	if !reflect.DeepEqual(value.AllowedOutgoingHosts, wantHosts) {
		t.Fatalf("allowed hosts = %#v, want %#v", value.AllowedOutgoingHosts, wantHosts)
	}
}

func TestValidateSiteSettingsRejectsUnsafeValues(t *testing.T) {
	tests := []siteSettingsView{
		{SiteName: "moyro", PublicBaseURL: "javascript:alert(1)"},
		{SiteName: "moyro", PublicBaseURL: "https://user:pass@chat.internal"},
		{SiteName: "moyro", PublicBaseURL: "https://chat.internal/subpath"},
		{SiteName: "moyro", AllowedOutgoingHosts: []string{"https://keycloak.internal"}},
		{SiteName: "moyro", AllowedOutgoingHosts: []string{"keycloak.internal:8080"}},
	}
	for _, value := range tests {
		if err := validateSiteSettings(&value); err == nil {
			t.Fatalf("unsafe settings unexpectedly accepted: %#v", value)
		}
	}
}

func TestEffectivePublicBaseURLUsesDirectRequestInsteadOfLocalhostDefault(t *testing.T) {
	h := &handlers{cfg: &config.Config{PublicBaseURL: config.DefaultPublicBaseURL}}
	req := httptest.NewRequest(http.MethodGet, "http://moyro.internal/api/v4/config/client", nil)
	if got := h.effectivePublicBaseURL(req); got != "http://moyro.internal" {
		t.Fatalf("effective base URL = %q, want direct request origin", got)
	}
}
