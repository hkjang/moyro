// Package aiprovider provides an OpenAI-compatible AI gateway with streaming
// enabled by default. Provider credentials are supplied by the encrypted
// settings layer; this package never persists or logs them.
package aiprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxSupportedTokens int64 = 256 * 1024

var (
	ErrDisabled       = errors.New("ai provider is disabled")
	ErrInvalidConfig  = errors.New("invalid ai provider configuration")
	ErrInvalidRequest = errors.New("invalid ai request")
	ErrTokenLimit     = errors.New("requested token limit is outside the allowed range")
	ErrModelDenied    = errors.New("requested model is not allowed")
)

type Config struct {
	Enabled                bool     `json:"enabled"`
	Name                   string   `json:"name"`
	BaseURL                string   `json:"base_url"`
	APIKey                 string   `json:"-"`
	DefaultModel           string   `json:"default_model"`
	AllowedModels          []string `json:"allowed_models"`
	ContextWindowTokens    int64    `json:"context_window_tokens"`
	DefaultMaxOutputTokens int64    `json:"default_max_output_tokens"`
	RequestTimeoutSeconds  int      `json:"request_timeout_seconds"`
}

type ChatRequest struct {
	Model               string          `json:"model,omitempty"`
	Messages            json.RawMessage `json:"messages"`
	Stream              *bool           `json:"stream,omitempty"`
	MaxTokens           int64           `json:"max_tokens,omitempty"`
	MaxCompletionTokens int64           `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	Tools               json.RawMessage `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
	User                string          `json:"user,omitempty"`
}

// ServeResult describes how far ServeChat got before returning. Once Started
// is true, callers MUST NOT try to write a JSON error envelope: response
// headers or body bytes have already been committed. This is especially
// important for SSE, where appending JSON would corrupt the event stream.
type ServeResult struct {
	Started   bool
	Streaming bool
}

type snapshot struct {
	config         Config
	endpoint       string
	modelsEndpoint string
}

type Service struct {
	mu      sync.RWMutex
	current *snapshot
	client  *http.Client
}

func New(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	// Provider endpoints are administrator-configured and may intentionally be
	// private/offline. Never follow redirects, however: doing so could forward
	// the provider Authorization header to an unrelated or metadata endpoint.
	copy := *client
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Service{client: &copy}
}

func (s *Service) Configure(cfg Config) error {
	if !cfg.Enabled {
		s.Disable()
		return nil
	}
	normalized, endpoint, modelsEndpoint, err := normalizeConfig(cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.current = &snapshot{config: normalized, endpoint: endpoint, modelsEndpoint: modelsEndpoint}
	s.mu.Unlock()
	return nil
}

// Test performs a bounded, read-only provider probe. OpenAI-compatible
// deployments conventionally expose GET /models; using that endpoint avoids
// consuming tokens while still validating the base URL and credential.
func (s *Service) Test(ctx context.Context) error {
	snap, err := s.load()
	if err != nil {
		return err
	}
	if snap.config.RequestTimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(snap.config.RequestTimeoutSeconds)*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, snap.modelsEndpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if snap.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+snap.config.APIKey)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("ai provider probe: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		return &UpstreamError{StatusCode: response.StatusCode, Body: string(limited)}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return nil
}

func (s *Service) Disable() {
	s.mu.Lock()
	s.current = nil
	s.mu.Unlock()
}

func (s *Service) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current != nil
}

func (s *Service) PublicConfig() (Config, bool) {
	snap, err := s.load()
	if err != nil {
		return Config{}, false
	}
	cfg := snap.config
	// Config contains a slice. Return a detached copy so a caller cannot mutate
	// the immutable snapshot used by in-flight requests.
	cfg.AllowedModels = slices.Clone(cfg.AllowedModels)
	cfg.APIKey = ""
	return cfg, true
}

// Prepare applies provider defaults and validates the complete request. A nil
// stream field is deliberately converted to true.
func (s *Service) Prepare(input ChatRequest) (ChatRequest, bool, error) {
	snap, err := s.load()
	if err != nil {
		return ChatRequest{}, false, err
	}
	return prepareWithSnapshot(snap, input)
}

// prepareWithSnapshot applies all request defaults and limits from exactly
// the snapshot that also supplies the upstream endpoint and credential. Never
// reload service state while preparing an in-flight request.
func prepareWithSnapshot(snap *snapshot, input ChatRequest) (ChatRequest, bool, error) {
	if len(bytes.TrimSpace(input.Messages)) == 0 || string(bytes.TrimSpace(input.Messages)) == "null" {
		return ChatRequest{}, false, fmt.Errorf("%w: messages are required", ErrInvalidRequest)
	}
	if !json.Valid(input.Messages) {
		return ChatRequest{}, false, fmt.Errorf("%w: messages must be valid JSON", ErrInvalidRequest)
	}
	if input.Model == "" {
		input.Model = snap.config.DefaultModel
	}
	if len(snap.config.AllowedModels) > 0 && !slices.Contains(snap.config.AllowedModels, input.Model) {
		return ChatRequest{}, false, ErrModelDenied
	}
	requested := input.MaxCompletionTokens
	if requested == 0 {
		requested = input.MaxTokens
	}
	if requested == 0 {
		requested = snap.config.DefaultMaxOutputTokens
	}
	if requested < 1 || requested > MaxSupportedTokens || requested > snap.config.ContextWindowTokens {
		return ChatRequest{}, false, ErrTokenLimit
	}
	// There is no provider-independent tokenizer. We therefore use a documented
	// UTF-8 estimate: about four ASCII bytes per token and one token per
	// non-ASCII rune. It is intentionally an estimate (not a billing count), but
	// catches requests that plainly cannot fit without aggressively rejecting
	// normal ASCII prompts. The upstream remains authoritative.
	estimatedInput := estimateUTF8Tokens(input.Messages) +
		estimateUTF8Tokens(input.Tools) +
		estimateUTF8Tokens(input.ToolChoice) +
		estimateUTF8Tokens(input.ResponseFormat)
	if estimatedInput > snap.config.ContextWindowTokens-requested {
		return ChatRequest{}, false, &ContextLimitError{
			EstimatedInputTokens:  estimatedInput,
			RequestedOutputTokens: requested,
			ContextWindowTokens:   snap.config.ContextWindowTokens,
		}
	}
	input.MaxCompletionTokens = requested
	input.MaxTokens = 0
	stream := true
	if input.Stream != nil {
		stream = *input.Stream
	}
	input.Stream = &stream
	return input, stream, nil
}

// ServeChat proxies one validated request to the configured OpenAI-compatible
// endpoint. SSE bytes are forwarded as they arrive and flushed per chunk. The
// returned Started flag is the caller's authoritative signal for whether an
// HTTP error envelope can still be written.
func (s *Service) ServeChat(w http.ResponseWriter, r *http.Request, input ChatRequest) (ServeResult, error) {
	snap, err := s.load()
	if err != nil {
		return ServeResult{}, err
	}
	// Load once. Reconfiguration swaps the snapshot pointer, so this request's
	// defaults, limits, endpoint, timeout, and API key remain one atomic view.
	prepared, streaming, err := prepareWithSnapshot(snap, input)
	result := ServeResult{Streaming: streaming}
	if err != nil {
		return result, err
	}
	body, err := json.Marshal(prepared)
	if err != nil {
		return result, err
	}

	ctx := r.Context()
	var cancel context.CancelFunc
	if snap.config.RequestTimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(snap.config.RequestTimeoutSeconds)*time.Second)
		defer cancel()
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, snap.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "application/json")
	if streaming {
		upstream.Header.Set("Accept", "text/event-stream")
	}
	if snap.config.APIKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+snap.config.APIKey)
	}
	response, err := s.client.Do(upstream)
	if err != nil {
		return result, fmt.Errorf("ai upstream request: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		return result, &UpstreamError{StatusCode: response.StatusCode, Body: string(limited)}
	}

	if streaming {
		flusher, ok := w.(http.Flusher)
		if !ok {
			return result, errors.New("streaming is not supported by this response writer")
		}
		buffer := make([]byte, 16<<10)
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if !result.Started {
					startSSE(w)
					result.Started = true
				}
				if _, err := w.Write(buffer[:n]); err != nil {
					return result, err
				}
				flusher.Flush()
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					if !result.Started {
						startSSE(w)
						result.Started = true
					}
					return result, nil
				}
				return result, readErr
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	result.Started = true
	_, err = io.Copy(w, response.Body)
	return result, err
}

func startSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// ContextLimitError reports an approximate input/output context overflow.
// EstimatedInputTokens is deliberately named as an estimate because exact
// tokenization belongs to the configured model/provider.
type ContextLimitError struct {
	EstimatedInputTokens  int64
	RequestedOutputTokens int64
	ContextWindowTokens   int64
}

func (e *ContextLimitError) Error() string {
	return fmt.Sprintf(
		"%v: estimated input (%d) plus requested output (%d) exceeds context window (%d); exact tokenization is provider-specific",
		ErrTokenLimit, e.EstimatedInputTokens, e.RequestedOutputTokens, e.ContextWindowTokens,
	)
}

func (e *ContextLimitError) Unwrap() error { return ErrTokenLimit }

func estimateUTF8Tokens(value []byte) int64 {
	var asciiBytes, nonASCII int64
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 {
			// Messages are valid JSON, hence valid UTF-8 in practice. Treat a
			// malformed byte conservatively if this helper is reused directly.
			asciiBytes++
			value = value[1:]
			continue
		}
		if r < utf8.RuneSelf {
			asciiBytes++
		} else {
			nonASCII++
		}
		value = value[size:]
	}
	return (asciiBytes+3)/4 + nonASCII
}

type UpstreamError struct {
	StatusCode int
	Body       string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("ai upstream returned HTTP %d", e.StatusCode)
}

func (s *Service) load() (*snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil, ErrDisabled
	}
	return s.current, nil
}

func normalizeConfig(cfg Config) (Config, string, string, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return Config{}, "", "", fmt.Errorf("%w: base_url must be an absolute HTTP(S) URL", ErrInvalidConfig)
	}
	if base.User != nil || base.Fragment != "" || base.RawQuery != "" || base.ForceQuery {
		return Config{}, "", "", fmt.Errorf("%w: base_url must not contain credentials, query parameters, or a fragment", ErrInvalidConfig)
	}
	cfg.BaseURL = strings.TrimSuffix(base.String(), "/")
	cfg.DefaultModel = strings.TrimSpace(cfg.DefaultModel)
	if cfg.DefaultModel == "" {
		return Config{}, "", "", fmt.Errorf("%w: default_model is required", ErrInvalidConfig)
	}
	if cfg.ContextWindowTokens == 0 {
		cfg.ContextWindowTokens = MaxSupportedTokens
	}
	if cfg.ContextWindowTokens < 1 || cfg.ContextWindowTokens > MaxSupportedTokens {
		return Config{}, "", "", ErrTokenLimit
	}
	if cfg.DefaultMaxOutputTokens == 0 {
		cfg.DefaultMaxOutputTokens = min(int64(4096), cfg.ContextWindowTokens)
	}
	if cfg.DefaultMaxOutputTokens < 1 || cfg.DefaultMaxOutputTokens > cfg.ContextWindowTokens || cfg.DefaultMaxOutputTokens > MaxSupportedTokens {
		return Config{}, "", "", ErrTokenLimit
	}
	if cfg.RequestTimeoutSeconds < 0 || cfg.RequestTimeoutSeconds > 24*60*60 {
		return Config{}, "", "", fmt.Errorf("%w: request timeout is outside 0..86400 seconds", ErrInvalidConfig)
	}
	models := make([]string, 0, len(cfg.AllowedModels)+1)
	seen := map[string]struct{}{}
	for _, model := range append([]string{cfg.DefaultModel}, cfg.AllowedModels...) {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	cfg.AllowedModels = models
	if cfg.Name == "" {
		cfg.Name = "OpenAI compatible"
	}

	endpointURL := *base
	cleaned := strings.TrimSuffix(endpointURL.Path, "/")
	if strings.HasSuffix(cleaned, "/chat/completions") {
		endpointURL.Path = cleaned
	} else {
		endpointURL.Path = path.Join(cleaned, "chat/completions")
	}
	modelsURL := *base
	modelsPath := strings.TrimSuffix(modelsURL.Path, "/")
	if strings.HasSuffix(modelsPath, "/chat/completions") {
		modelsPath = strings.TrimSuffix(modelsPath, "/chat/completions")
	}
	modelsURL.Path = path.Join(modelsPath, "models")
	return cfg, endpointURL.String(), modelsURL.String(), nil
}
