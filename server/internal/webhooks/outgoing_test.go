package webhooks

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDispatcherConfigureAllowedHosts(t *testing.T) {
	d := &Dispatcher{allowedHosts: map[string]struct{}{}}
	d.ConfigureAllowedHosts(nil)
	if d.hostAllowed("https://example.com/hook") {
		t.Fatal("an explicitly configured empty allow-list must deny all callbacks")
	}
	d.ConfigureAllowedHosts([]string{" Keycloak.Internal ", "AI.INTERNAL", "keycloak.internal"})

	if !d.hostAllowed("https://keycloak.internal/realms/moyro") {
		t.Fatal("configured Keycloak host should be allowed")
	}
	if !d.hostAllowed("http://ai.internal:8080/v1/chat/completions") {
		t.Fatal("configured AI host with a port should be allowed by hostname")
	}
	if d.hostAllowed("https://attacker.example/callback") {
		t.Fatal("host outside the configured allow-list should be blocked")
	}
	if d.hostAllowed("file:///etc/passwd") {
		t.Fatal("non-HTTP schemes must always be blocked")
	}
}

func TestDispatcherAllowedHostsConcurrentReload(t *testing.T) {
	d := &Dispatcher{allowedHosts: map[string]struct{}{}}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				host := fmt.Sprintf("service-%d.internal", (worker+iteration)%4)
				d.ConfigureAllowedHosts([]string{host})
				_ = d.hostAllowed("https://" + host + "/hook")
			}
		}(worker)
	}
	wg.Wait()
}

func TestOutboundHTTPClientRejectsRedirectBeforeFollowing(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/internal", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	req, err := http.NewRequest(http.MethodPost, source.URL+"/callback", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newOutboundHTTPClient().Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, errOutgoingRedirect) {
		t.Fatalf("redirect error = %v, want %v", err, errOutgoingRedirect)
	}
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests; want none", got)
	}
}
