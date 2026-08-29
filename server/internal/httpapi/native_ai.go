package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/aiprovider"
	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/jackc/pgx/v5"
)

const (
	aiSettingsSection = "ai"
	aiSettingsKey     = "provider"
	aiSecretKey       = "provider-api-key"
)

type aiProviderView struct {
	ID                  string                `json:"id,omitempty"`
	Name                string                `json:"name"`
	Enabled             bool                  `json:"enabled"`
	APIType             string                `json:"api_type"`
	BaseURL             string                `json:"base_url"`
	Model               string                `json:"model"`
	APIKey              string                `json:"api_key,omitempty"`
	APIKeyState         *secretConfiguredView `json:"api_key_state,omitempty"`
	StreamingDefault    bool                  `json:"streaming_default"`
	ContextWindowTokens int64                 `json:"context_window_tokens"`
	MaxOutputTokens     int64                 `json:"max_output_tokens"`
	TimeoutSeconds      int                   `json:"timeout_seconds"`
	Status              string                `json:"status,omitempty"`
	LastTestedAt        int64                 `json:"last_tested_at,omitempty"`
}

func defaultAIProvider() aiProviderView {
	return aiProviderView{
		ID: "default", Name: "Internal AI", APIType: "openai-compatible",
		StreamingDefault: true, ContextWindowTokens: aiprovider.MaxSupportedTokens,
		MaxOutputTokens: 16 * 1024, TimeoutSeconds: 120, Status: "unknown",
	}
}

func (v aiProviderView) serviceConfig(secret string) aiprovider.Config {
	return aiprovider.Config{
		Enabled: v.Enabled, Name: v.Name, BaseURL: v.BaseURL, APIKey: secret,
		DefaultModel: v.Model, AllowedModels: []string{v.Model},
		ContextWindowTokens:    v.ContextWindowTokens,
		DefaultMaxOutputTokens: v.MaxOutputTokens,
		RequestTimeoutSeconds:  v.TimeoutSeconds,
	}
}

func (n *nativeServices) reloadAI(ctx context.Context) error {
	var view aiProviderView
	if err := n.loadJSON(ctx, aiSettingsSection, aiSettingsKey, &view); err != nil {
		return err
	}
	if !view.Enabled {
		n.ai.Disable()
		return nil
	}
	secret := ""
	if value, _, err := n.settings.RevealSecret(ctx, aiSettingsSection, aiSecretKey); err == nil {
		secret = string(value)
	} else if !errors.Is(err, settings.ErrNotFound) {
		return err
	}
	return n.ai.Configure(view.serviceConfig(secret))
}

func (h *handlers) readAIProvider(ctx context.Context) (aiProviderView, error) {
	view := defaultAIProvider()
	if err := h.native.loadJSON(ctx, aiSettingsSection, aiSettingsKey, &view); err != nil {
		return aiProviderView{}, err
	}
	view.APIKey = ""
	if _, _, err := h.native.settings.RevealSecret(ctx, aiSettingsSection, aiSecretKey); err == nil {
		view.APIKeyState = &secretConfiguredView{Configured: true}
	} else if !errors.Is(err, settings.ErrNotFound) {
		return aiProviderView{}, err
	}
	view.StreamingDefault = true
	return view, nil
}

func (h *handlers) listNativeAIProviders(w http.ResponseWriter, r *http.Request) {
	view, err := h.readAIProvider(r.Context())
	if errors.Is(err, settings.ErrNotFound) {
		writeJSON(w, http.StatusOK, []aiProviderView{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.ai.read", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, []aiProviderView{view})
}

func (h *handlers) saveNativeAIProvider(w http.ResponseWriter, r *http.Request) {
	view := defaultAIProvider()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&view); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.ai.body", err.Error())
		return
	}
	if view.ID == "" {
		view.ID = "default"
	}
	if view.APIType == "" {
		view.APIType = "openai-compatible"
	}
	if view.APIType != "openai-compatible" && view.APIType != "openai" {
		writeError(w, http.StatusBadRequest, "api.moyro.ai.type", "only OpenAI-compatible providers are supported")
		return
	}
	unlock := h.native.beginSettingsUpdate()
	defer unlock()

	view.StreamingDefault = true
	secret := strings.TrimSpace(view.APIKey)
	if secret == "" {
		var err error
		secret, err = h.native.revealOptionalSecret(r.Context(), aiSettingsSection, aiSecretKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.ai.secret_read", err.Error())
			return
		}
	}
	// Validate against an isolated service before changing live state.
	probe := aiprovider.New(nil)
	if err := probe.Configure(view.serviceConfig(secret)); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.ai.config", err.Error())
		return
	}
	actor := userID(r)
	view.APIKey = ""
	view.Status = "unknown"
	if _, err := h.native.settings.PutJSONAndOptionalSecret(
		r.Context(), aiSettingsSection, aiSettingsKey, view,
		aiSecretKey, []byte(secret), actor,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.ai.save", err.Error())
		return
	}
	if view.Enabled {
		if err := h.native.ai.Configure(view.serviceConfig(secret)); err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.ai.activate", err.Error())
			return
		}
	} else {
		h.native.ai.Disable()
	}
	view.APIKeyState = &secretConfiguredView{Configured: secret != ""}
	if h.audit != nil {
		h.audit.LogAsync(actor, "settings.ai.update", view.ID, map[string]any{"enabled": view.Enabled, "base_url": view.BaseURL, "model": view.Model})
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *handlers) testNativeAIProvider(w http.ResponseWriter, r *http.Request) {
	view := defaultAIProvider()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&view); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.ai.body", err.Error())
		return
	}
	secret := strings.TrimSpace(view.APIKey)
	if secret == "" {
		var err error
		secret, err = h.native.revealOptionalSecret(r.Context(), aiSettingsSection, aiSecretKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api.moyro.ai.secret_read", err.Error())
			return
		}
	}
	view.Enabled = true
	view.StreamingDefault = true
	probe := aiprovider.New(nil)
	if err := probe.Configure(view.serviceConfig(secret)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	if err := probe.Test(r.Context()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": view.Model})
}

type personalAIPreferences struct {
	Enabled         bool    `json:"enabled"`
	ProviderID      string  `json:"provider_id,omitempty"`
	Model           string  `json:"model,omitempty"`
	Streaming       bool    `json:"streaming"`
	MaxOutputTokens int64   `json:"max_output_tokens"`
	Temperature     float64 `json:"temperature"`
}

func defaultPersonalAI() personalAIPreferences {
	return personalAIPreferences{Enabled: true, Streaming: true, MaxOutputTokens: 8192, Temperature: .7}
}

func (h *handlers) getPersonalAIPreferences(w http.ResponseWriter, r *http.Request) {
	view, err := h.readPersonalAIPreferences(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.ai.preferences", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *handlers) readPersonalAIPreferences(ctx context.Context, actor string) (personalAIPreferences, error) {
	view := defaultPersonalAI()
	err := h.auth.DB().Pool.QueryRow(ctx, `
		SELECT enabled, COALESCE(provider_id,''), COALESCE(model,''), max_output_tokens, temperature
		FROM user_ai_preferences WHERE user_id=$1
	`, actor).Scan(&view.Enabled, &view.ProviderID, &view.Model, &view.MaxOutputTokens, &view.Temperature)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, nil
	}
	view.Streaming = true
	return view, err
}

func (h *handlers) patchPersonalAIPreferences(w http.ResponseWriter, r *http.Request) {
	view := defaultPersonalAI()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&view); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.ai.preferences_body", err.Error())
		return
	}
	if view.MaxOutputTokens < 1 || view.MaxOutputTokens > aiprovider.MaxSupportedTokens || view.Temperature < 0 || view.Temperature > 2 {
		writeError(w, http.StatusBadRequest, "api.moyro.ai.preferences_invalid", "max_output_tokens or temperature is outside the supported range")
		return
	}
	view.Streaming = true
	actor := userID(r)
	_, err := h.auth.DB().Pool.Exec(r.Context(), `
		INSERT INTO user_ai_preferences (user_id, enabled, provider_id, model, max_output_tokens, temperature, update_at)
		VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7)
		ON CONFLICT (user_id) DO UPDATE SET enabled=EXCLUDED.enabled, provider_id=EXCLUDED.provider_id,
		model=EXCLUDED.model, max_output_tokens=EXCLUDED.max_output_tokens,
		temperature=EXCLUDED.temperature, update_at=EXCLUDED.update_at
	`, actor, view.Enabled, strings.TrimSpace(view.ProviderID), strings.TrimSpace(view.Model), view.MaxOutputTokens, view.Temperature, time.Now().UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.ai.preferences_save", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type nativeAICompletionRequest struct {
	Model           string          `json:"model,omitempty"`
	Messages        json.RawMessage `json:"messages"`
	MaxOutputTokens int64           `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	Stream          *bool           `json:"stream,omitempty"`
}

func (h *handlers) nativeAICompletion(w http.ResponseWriter, r *http.Request) {
	var input nativeAICompletionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "api.moyro.ai.body", err.Error())
		return
	}
	prefs, err := h.readPersonalAIPreferences(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api.moyro.ai.preferences", err.Error())
		return
	}
	if !prefs.Enabled {
		writeError(w, http.StatusForbidden, "api.moyro.ai.user_disabled", "AI is disabled in personal preferences")
		return
	}
	if input.Model == "" {
		input.Model = prefs.Model
	}
	if input.MaxOutputTokens == 0 {
		input.MaxOutputTokens = prefs.MaxOutputTokens
	}
	if input.Temperature == nil {
		input.Temperature = &prefs.Temperature
	}
	streaming := true
	if input.Stream != nil {
		streaming = *input.Stream
	}
	request := aiprovider.ChatRequest{
		Model: input.Model, Messages: input.Messages, Stream: &streaming,
		MaxCompletionTokens: input.MaxOutputTokens, Temperature: input.Temperature,
		User: userID(r),
	}
	if h.audit != nil {
		h.audit.LogAsync(userID(r), "ai.completion.start", input.Model, map[string]any{"stream": streaming, "max_output_tokens": input.MaxOutputTokens})
	}
	result, err := h.native.ai.ServeChat(w, r, request)
	if err == nil || result.Started {
		// Once any response (especially SSE) has started, close it as-is. A
		// JSON envelope here would corrupt the response already on the wire.
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		// The client is gone; there is no response recipient.
		return
	}
	status := http.StatusBadGateway
	code := "api.moyro.ai.upstream"
	switch {
	case errors.Is(err, aiprovider.ErrTokenLimit), errors.Is(err, aiprovider.ErrModelDenied), errors.Is(err, aiprovider.ErrInvalidConfig), errors.Is(err, aiprovider.ErrInvalidRequest):
		status, code = http.StatusBadRequest, "api.moyro.ai.request"
	case errors.Is(err, aiprovider.ErrDisabled):
		status, code = http.StatusServiceUnavailable, "api.moyro.ai.disabled"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "api.moyro.ai.timeout"
	}
	writeError(w, status, code, err.Error())
}
