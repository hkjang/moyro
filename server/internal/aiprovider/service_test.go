package aiprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func boolPtr(value bool) *bool { return &value }

func TestPrepareDefaultsToStreamingAndSupports256KConfiguration(t *testing.T) {
	if MaxSupportedTokens != 262144 {
		t.Fatalf("MaxSupportedTokens = %d", MaxSupportedTokens)
	}
	service := New(nil)
	if err := service.Configure(Config{
		Enabled: true, BaseURL: "http://ai.internal/v1", DefaultModel: "local-model",
		ContextWindowTokens: MaxSupportedTokens, DefaultMaxOutputTokens: MaxSupportedTokens - 64,
	}); err != nil {
		t.Fatal(err)
	}
	prepared, streaming, err := service.Prepare(ChatRequest{Messages: json.RawMessage(`[{"role":"user","content":"hello"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	if !streaming || prepared.Stream == nil || !*prepared.Stream {
		t.Fatal("streaming was not enabled by default")
	}
	if prepared.MaxCompletionTokens != MaxSupportedTokens-64 {
		t.Fatalf("max_completion_tokens = %d", prepared.MaxCompletionTokens)
	}

	_, _, err = service.Prepare(ChatRequest{
		Messages: json.RawMessage(`[]`), MaxCompletionTokens: MaxSupportedTokens + 1,
	})
	if !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("over-limit error = %v", err)
	}
}

func TestConfigureRejectsAmbiguousBaseURLComponents(t *testing.T) {
	for _, baseURL := range []string{
		"https://user:secret@ai.internal/v1",
		"https://ai.internal/v1?target=metadata",
		"https://ai.internal/v1#fragment",
	} {
		service := New(nil)
		err := service.Configure(Config{
			Enabled: true, BaseURL: baseURL, DefaultModel: "local",
			ContextWindowTokens: 4096, DefaultMaxOutputTokens: 128,
		})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("Configure(%q) error = %v, want ErrInvalidConfig", baseURL, err)
		}
	}
}

func TestProviderRedirectIsNotFollowed(t *testing.T) {
	var targetCalls int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()

	service := New(source.Client())
	if err := service.Configure(Config{
		Enabled: true, BaseURL: source.URL + "/v1", APIKey: "provider-secret",
		DefaultModel: "local", ContextWindowTokens: 4096, DefaultMaxOutputTokens: 128,
	}); err != nil {
		t.Fatal(err)
	}
	err := service.Test(context.Background())
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.StatusCode != http.StatusFound {
		t.Fatalf("Test error = %v, want 302 UpstreamError", err)
	}
	if targetCalls != 0 {
		t.Fatalf("redirect target received %d calls; provider credentials could leak", targetCalls)
	}
}

func TestPrepareAppliesApproximateInputAndOutputContextLimit(t *testing.T) {
	service := New(nil)
	if err := service.Configure(Config{
		Enabled: true, BaseURL: "http://ai.internal/v1", DefaultModel: "local-model",
		ContextWindowTokens: 100, DefaultMaxOutputTokens: 40,
	}); err != nil {
		t.Fatal(err)
	}

	// Four ASCII bytes per estimated token leaves this ordinary prompt under
	// the context window and avoids aggressive byte-for-token blocking.
	ordinary := `[{"role":"user","content":"` + strings.Repeat("a", 180) + `"}]`
	if _, _, err := service.Prepare(ChatRequest{Messages: json.RawMessage(ordinary)}); err != nil {
		t.Fatalf("ordinary ASCII prompt was overblocked: %v", err)
	}

	tooLarge := `[{"role":"user","content":"` + strings.Repeat("가", 70) + `"}]`
	_, _, err := service.Prepare(ChatRequest{Messages: json.RawMessage(tooLarge)})
	var limitErr *ContextLimitError
	if !errors.As(err, &limitErr) || !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("context overflow error = %v", err)
	}
	if limitErr.EstimatedInputTokens+limitErr.RequestedOutputTokens <= limitErr.ContextWindowTokens {
		t.Fatalf("invalid context details: %+v", limitErr)
	}
}

func TestServeChatStreamsUpstreamAndSendsConfiguredSecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer provider-secret" {
			t.Errorf("Authorization = %q", got)
		}
		var body ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Stream == nil || !*body.Stream {
			t.Error("upstream request did not enable streaming")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	service := New(upstream.Client())
	if err := service.Configure(Config{
		Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "provider-secret",
		DefaultModel: "local-model", ContextWindowTokens: MaxSupportedTokens,
		DefaultMaxOutputTokens: 4096,
	}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/moyro/v1/ai/chat/completions", nil)
	result, err := service.ServeChat(recorder, request, ChatRequest{Messages: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Started || !result.Streaming {
		t.Fatalf("result = %+v", result)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	scanner := bufio.NewScanner(strings.NewReader(recorder.Body.String()))
	var dataLines int
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			dataLines++
		}
	}
	if dataLines != 2 {
		t.Fatalf("data lines = %d, body = %q", dataLines, recorder.Body.String())
	}
}

func TestServeChatUsesOneImmutableSnapshotAcrossReconfigure(t *testing.T) {
	type captured struct {
		url, authorization string
		body               ChatRequest
	}
	entered := make(chan captured, 1)
	release := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var got captured
		got.url = req.URL.String()
		got.authorization = req.Header.Get("Authorization")
		if err := json.NewDecoder(req.Body).Decode(&got.body); err != nil {
			return nil, err
		}
		entered <- got
		<-release
		return response(http.StatusOK, "data: [DONE]\n\n"), nil
	})}
	service := New(client)
	if err := service.Configure(Config{
		Enabled: true, BaseURL: "https://old.internal/v1", APIKey: "old-secret",
		DefaultModel: "old-model", ContextWindowTokens: 1000, DefaultMaxOutputTokens: 100,
	}); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result ServeResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.ServeChat(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil), ChatRequest{Messages: json.RawMessage(`[]`)})
		done <- outcome{result: result, err: err}
	}()
	got := <-entered
	if err := service.Configure(Config{
		Enabled: true, BaseURL: "https://new.internal/v1", APIKey: "new-secret",
		DefaultModel: "new-model", ContextWindowTokens: 2000, DefaultMaxOutputTokens: 200,
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	finished := <-done
	if finished.err != nil || !finished.result.Started {
		t.Fatalf("ServeChat result=%+v error=%v", finished.result, finished.err)
	}
	if got.url != "https://old.internal/v1/chat/completions" || got.authorization != "Bearer old-secret" || got.body.Model != "old-model" || got.body.MaxCompletionTokens != 100 {
		t.Fatalf("request combined multiple snapshots: %+v", got)
	}
}

func TestServeChatFlushesEveryChunkAndReportsStartedOnStreamFailure(t *testing.T) {
	streamErr := errors.New("upstream stream broke")
	body := &scriptedBody{steps: []bodyStep{
		{data: "data: one\n\n"},
		{data: "data: two\n\n", err: streamErr},
	}}
	service := configuredService(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}, 0)
	w := newFlushWriter()
	result, err := service.ServeChat(w, httptest.NewRequest(http.MethodPost, "/", nil), ChatRequest{Messages: json.RawMessage(`[]`)})
	if !errors.Is(err, streamErr) {
		t.Fatalf("stream error = %v", err)
	}
	if !result.Started || !result.Streaming {
		t.Fatalf("result = %+v", result)
	}
	if w.flushes != 2 {
		t.Fatalf("flushes = %d", w.flushes)
	}
	if got := w.body.String(); got != "data: one\n\ndata: two\n\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestServeChatPropagatesClientCancellationBeforeResponseStarts(t *testing.T) {
	entered := make(chan struct{})
	service := configuredService(t, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	type outcome struct {
		result ServeResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.ServeChat(httptest.NewRecorder(), req, ChatRequest{Messages: json.RawMessage(`[]`)})
		done <- outcome{result, err}
	}()
	<-entered
	cancel()
	got := <-done
	if got.result.Started || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("result=%+v error=%v", got.result, got.err)
	}
}

func TestServeChatTimeoutAfterChunkKeepsSSEResponseUncorrupted(t *testing.T) {
	service := configuredService(t, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &contextStreamBody{ctx: req.Context()},
		}, nil
	})}, 1)
	w := newFlushWriter()
	startedAt := time.Now()
	result, err := service.ServeChat(w, httptest.NewRequest(http.MethodPost, "/", nil), ChatRequest{Messages: json.RawMessage(`[]`)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if !result.Started || !result.Streaming {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(startedAt); elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("timeout elapsed = %s", elapsed)
	}
	if got := w.body.String(); got != "data: first\n\n" || strings.Contains(got, `"id"`) {
		t.Fatalf("stream was corrupted after timeout: %q", got)
	}
}

func TestPrepareHonorsExplicitNonStreaming(t *testing.T) {
	service := New(nil)
	_ = service.Configure(Config{
		Enabled: true, BaseURL: "http://ai.internal/v1", DefaultModel: "local",
		ContextWindowTokens: MaxSupportedTokens, DefaultMaxOutputTokens: 128,
	})
	_, streaming, err := service.Prepare(ChatRequest{
		Messages: json.RawMessage(`[]`), Stream: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if streaming {
		t.Fatal("explicit stream=false was ignored")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func configuredService(t *testing.T, client *http.Client, timeoutSeconds int) *Service {
	t.Helper()
	service := New(client)
	if err := service.Configure(Config{
		Enabled: true, BaseURL: "http://ai.internal/v1", DefaultModel: "local",
		ContextWindowTokens: MaxSupportedTokens, DefaultMaxOutputTokens: 128,
		RequestTimeoutSeconds: timeoutSeconds,
	}); err != nil {
		t.Fatal(err)
	}
	return service
}

type bodyStep struct {
	data string
	err  error
}

type scriptedBody struct {
	steps []bodyStep
	next  int
}

func (b *scriptedBody) Read(p []byte) (int, error) {
	if b.next >= len(b.steps) {
		return 0, io.EOF
	}
	step := b.steps[b.next]
	b.next++
	return copy(p, step.data), step.err
}

func (*scriptedBody) Close() error { return nil }

type contextStreamBody struct {
	ctx  context.Context
	once sync.Once
}

func (b *contextStreamBody) Read(p []byte) (int, error) {
	wrote := false
	b.once.Do(func() { wrote = true })
	if wrote {
		return copy(p, "data: first\n\n"), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (*contextStreamBody) Close() error { return nil }

type flushWriter struct {
	header  http.Header
	status  int
	body    strings.Builder
	flushes int
}

func newFlushWriter() *flushWriter { return &flushWriter{header: make(http.Header)} }

func (w *flushWriter) Header() http.Header { return w.header }

func (w *flushWriter) WriteHeader(status int) { w.status = status }

func (w *flushWriter) Write(value []byte) (int, error) { return w.body.Write(value) }

func (w *flushWriter) Flush() { w.flushes++ }
