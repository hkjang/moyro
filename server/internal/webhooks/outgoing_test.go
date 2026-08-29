package webhooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/posts"
)

func TestDispatcherConfigureAllowedHosts(t *testing.T) {
	fresh := NewDispatcher(nil, nil, nil, nil, 1, nil)
	if fresh.policyReady() {
		t.Fatal("dispatcher policy became ready before durable settings loaded")
	}
	if fresh.hostAllowed("https://example.com/hook") {
		t.Fatal("dispatcher must deny callbacks before durable settings load")
	}
	fresh.ConfigureAllowedHosts([]string{})
	if !fresh.policyReady() {
		t.Fatal("explicit deny-all policy did not unblock durable workers")
	}

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

func TestPersistedOutgoingPayloadMasksCredentialsAndLimitsSize(t *testing.T) {
	job := dispatchJob{
		hook: OutgoingHook{
			ID:           "hook-1",
			Token:        "super-secret-webhook-token",
			TeamID:       "team-1",
			TriggerWords: []string{"deploy"},
		},
		post: &posts.Post{
			ID:        "post-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			Message:   "deploy now",
			CreateAt:  123,
			Props:     map[string]any{"webhook_depth": float64(2)},
		},
		user: "alice",
	}
	raw, err := persistedOutgoingPayload(job)
	if err != nil {
		t.Fatalf("persist payload: %v", err)
	}
	if strings.Contains(string(raw), job.hook.Token) {
		t.Fatal("persisted payload contains the usable webhook token")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode persisted payload: %v", err)
	}
	if payload["token"] != redactedPayloadValue {
		t.Fatalf("persisted token = %#v, want redaction marker", payload["token"])
	}
	if payload["text"] != job.post.Message {
		t.Fatalf("persisted text = %#v", payload["text"])
	}
	if payload["moyro_webhook_depth"] != float64(2) {
		t.Fatalf("persisted webhook depth = %#v", payload["moyro_webhook_depth"])
	}

	job.post.Message = strings.Repeat("x", maxPersistedPayloadSize+1)
	if _, err := persistedOutgoingPayload(job); err == nil {
		t.Fatal("oversized outgoing payload was accepted")
	}
}

func TestMaskPayloadRecursesThroughSensitiveFields(t *testing.T) {
	masked := maskPayload(map[string]any{
		"authorization": "Bearer secret",
		"nested": []any{map[string]any{
			"api-key": "private",
			"safe":    "visible",
		}},
	}).(map[string]any)
	if masked["authorization"] != redactedPayloadValue {
		t.Fatalf("authorization was not redacted: %#v", masked)
	}
	nested := masked["nested"].([]any)[0].(map[string]any)
	if nested["api-key"] != redactedPayloadValue || nested["safe"] != "visible" {
		t.Fatalf("nested masking = %#v", nested)
	}
}

func TestStableWebhookIDsAndBoundedRetry(t *testing.T) {
	eventA := stableWebhookID("event", "hook-1", "post-1")
	eventB := stableWebhookID("event", "hook-1", "post-1")
	if eventA != eventB || eventA == stableWebhookID("event", "hook-1", "post-2") {
		t.Fatalf("stable event ids = %q, %q", eventA, eventB)
	}
	deliveryA := stableWebhookID("delivery", eventA, "https://example.test/a")
	if deliveryA == stableWebhookID("delivery", eventA, "https://example.test/b") {
		t.Fatal("different callback URLs produced the same delivery id")
	}
	if got := deliveryRetryDelay(1); got != deliveryInitialBackoff {
		t.Fatalf("first retry = %s", got)
	}
	if got := deliveryRetryDelay(100); got != deliveryMaximumBackoff {
		t.Fatalf("bounded retry = %s", got)
	}
	if !retryableHTTPStatus(http.StatusTooManyRequests) || !retryableHTTPStatus(http.StatusBadGateway) {
		t.Fatal("transient HTTP response classified as permanent")
	}
	if retryableHTTPStatus(http.StatusBadRequest) || retryableHTTPStatus(http.StatusTemporaryRedirect) {
		t.Fatal("permanent HTTP response classified as retryable")
	}
	if deliveryLeaseDuration <= deliveryRequestTimeout-time.Second {
		t.Fatal("delivery lease does not safely exceed request timeout")
	}
}

func TestDeliveryUsesOnlyCurrentlyConfiguredCallback(t *testing.T) {
	configured := []string{" https://current.example/hook ", "https://backup.example/hook"}
	if !callbackStillConfigured("https://current.example/hook", configured) {
		t.Fatal("current callback was treated as removed")
	}
	if callbackStillConfigured("https://retired.example/hook", configured) {
		t.Fatal("retired callback would receive the hook's current token")
	}
}
