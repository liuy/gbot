package wui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/config"
)

// settingsHTTPTimeout caps the outbound probe/model-list requests made by
// the test-connection and fetch-models endpoints.
var settingsHTTPTimeout = 10 * time.Second

// RegisterSettingsRoutes mounts the provider-settings HTTP surface:
//
//   - GET  /api/settings/providers  — current providers + resolved default
//   - PUT  /api/settings/providers  — replace the providers array (backup + atomic write)
//   - POST /api/settings/test       — live connection probe against one provider
//   - POST /api/settings/models     — fetch the model id list from an endpoint
//
// The endpoints read/write ~/.gbot/settings.json directly (config.ConfigDir)
// — they are pure functions of the on-disk config and need no running state.
func RegisterSettingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/providers", handleGetProviders)
	mux.HandleFunc("PUT /api/settings/default", handleDefaultPut)
	mux.HandleFunc("PUT /api/settings/providers", handlePutProviders)
	mux.HandleFunc("POST /api/settings/test", handleTestProvider)
	mux.HandleFunc("POST /api/settings/models", handleFetchModels)
}

// settingsPayload is the GET response shape. Providers is coerced to [] so
// the wire shape is always an array, never null (the frontend iterates it).
type settingsPayload struct {
	Providers []config.Provider `json:"providers"`
	Default   settingsDefault   `json:"default"`
}

type settingsDefault struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func handleGetProviders(w http.ResponseWriter, r *http.Request) {
	// Cold start (missing/unreadable settings.json) is a normal state, not
	// an error: the settings page just opens empty.
	cfg, err := config.Load()
	if err != nil {
		writeJSON(w, http.StatusOK, settingsPayload{Providers: []config.Provider{}})
		return
	}
	def := settingsDefault{}
	if p, m, err := cfg.ResolveModel(); err == nil {
		def.Provider, def.Model = p.Name, m
	}
	if cfg.Providers == nil {
		cfg.Providers = []config.Provider{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, settingsPayload{Providers: cfg.Providers, Default: def})
}

func handlePutProviders(w http.ResponseWriter, r *http.Request) {
	var providers []config.Provider
	if err := json.NewDecoder(r.Body).Decode(&providers); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := validateProviders(providers); msg != "" {
		errorJSON(w, http.StatusBadRequest, msg)
		return
	}
	if err := config.SaveProviders(providers); err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// knownProviderTypes is the validation whitelist for Provider.Type.
var knownProviderTypes = map[string]bool{
	"":          true,
	"auto":      true,
	"openai":    true,
	"anthropic": true,
	"responses": true,
}

// validateProviders returns the first rule violation as a user-facing
// message, or "" when the provider set is acceptable. Checked before any
// write happens, so a rejected PUT never touches settings.json.
func validateProviders(providers []config.Provider) string {
	if len(providers) == 0 {
		return "at least one provider is required"
	}
	seen := make(map[string]bool)
	for _, p := range providers {
		if strings.TrimSpace(p.Name) == "" {
			return "provider name is required"
		}
		if seen[p.Name] {
			return fmt.Sprintf("duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true
		if strings.TrimSpace(p.URL) == "" {
			return fmt.Sprintf("provider %s: url is required", p.Name)
		}
		if p.Models.Len() == 0 {
			return fmt.Sprintf("provider %s: at least one model is required", p.Name)
		}
		for _, k := range p.Keys {
			if strings.TrimSpace(k) == "" {
				return fmt.Sprintf("provider %s: keys must be non-empty strings", p.Name)
			}
		}
		if !knownProviderTypes[p.Type] {
			return fmt.Sprintf("provider %s: unknown type %q", p.Name, p.Type)
		}
	}
	return ""
}

// handleTestProvider probes one provider (decoded from the request body)
// against its live endpoint. Outcomes are data: the response is always 200
// with an ok/error envelope — a failing probe is not a transport failure.
func handleTestProvider(w http.ResponseWriter, r *http.Request) {
	var p config.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	latencyMs, err := probeProvider(r.Context(), &p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "latencyMs": latencyMs})
}

// probeProvider makes the single cheapest authenticated call the endpoint
// supports and returns the wall-time latency. Key and type come from the
// provider itself (ResolveKey honors $ENV refs, ProviderType infers from
// the URL); errors are status-code or transport text only — the key lives
// in headers and never reaches an error message.
func probeProvider(ctx context.Context, p *config.Provider) (int, error) {
	key := p.ResolveKey()
	if key == "" {
		return 0, errors.New("no API key configured")
	}
	base := strings.TrimRight(p.URL, "/")
	client := &http.Client{Timeout: settingsHTTPTimeout}

	var req *http.Request
	var err error
	switch p.ProviderType() {
	case config.ProviderTypeAnthropic:
		// The ping needs a model name for the messages body.
		model := p.FirstModelName()
		if model == "" {
			return 0, errors.New("no models configured")
		}
		body, _ := json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
	case config.ProviderTypeResponses:
		// The probe must exercise the PROTOCOL endpoint, not /models: a
		// responses-only gateway (no /models route) would false-negative,
		// and a chat-completions host with a /models route would
		// false-positive. 1-token POST against {base}/responses.
		model := p.FirstModelName()
		if model == "" {
			return 0, errors.New("no models configured")
		}
		body, _ := json.Marshal(map[string]any{
			"model":             model,
			"input":             "ping",
			"max_output_tokens": 1,
		})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/responses", bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
	default:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return int(time.Since(start).Milliseconds()), nil
}

// handleFetchModels lists model ids from an OpenAI-compatible /models
// endpoint. Anthropic has no such endpoint — the client gets mode "manual"
// and adds models by hand.
// humanContext renders a raw token count in the human form the settings UI
// expects ("32k", "1M"); sub-threshold values pass through as plain digits.
// Shared by every fetch branch so the formatting can never diverge.
func humanContext(n int) string {
	h, _ := json.Marshal(config.IntOrHuman(n))
	return strings.Trim(string(h), `"`)
}

func handleFetchModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string `json:"url"`
		Key  string `json:"key"`
		Type string `json:"type"`
		// Free: OpenRouter-style free-models query — max_price=0,
		// popularity-sorted, capped at 10 with context metadata. The UI
		// sets it when the provider URL points at OpenRouter, where the
		// full /models list is too large to be useful.
		Free bool `json:"free"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		errorJSON(w, http.StatusBadRequest, "url is required")
		return
	}
	type fetchedModel struct {
		ID      string   `json:"id"`
		Context string   `json:"context,omitempty"`
		Input   []string `json:"input,omitempty"`
	}
	if req.Free {
		// Reuses the startup path verbatim (same filter, sort, cap) so the
		// manual fetch shows exactly what a free:true provider gets at boot.
		models, err := config.FetchFreeModels(r.Context(), req.URL)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"mode": "error", "error": err.Error()})
			return
		}
		out := make([]fetchedModel, 0, len(models))
		for _, m := range models {
			fm := fetchedModel{ID: m.ID}
			if m.ContextLength > 0 {
				fm.Context = humanContext(m.ContextLength)
			}
			out = append(out, fm)
		}
		writeJSON(w, http.StatusOK, struct {
			Mode   string         `json:"mode"`
			Models []fetchedModel `json:"models"`
		}{Mode: "fetched", Models: out})
		return
	}

	p := config.Provider{URL: req.URL, Type: req.Type}
	if p.ProviderType() == config.ProviderTypeAnthropic {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "manual"})
		return
	}

	base := strings.TrimRight(req.URL, "/")
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/models", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "error", "error": err.Error()})
		return
	}
	if req.Key != "" {
		hreq.Header.Set("Authorization", "Bearer "+req.Key)
	}
	resp, err := (&http.Client{Timeout: settingsHTTPTimeout}).Do(hreq)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "error", "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "error", "error": fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return
	}
	// Two response shapes in the wild: OpenAI {"data":[{id}]} and the
	// codex-style {"models":[{slug, context_window, input_modalities,…}]}
	// (zhipu /api/v1). The codex shape carries real metadata — pass it
	// through so the UI can pre-fill params instead of placeholders.
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Slug            string   `json:"slug"`
			DisplayName     string   `json:"display_name"`
			ContextWindow   int      `json:"context_window"`
			InputModalities []string `json:"input_modalities"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "error", "error": err.Error()})
		return
	}
	// Both shapes may appear in one body (and either may be absent) —
	// merge with dedupe instead of letting one shape shadow the other.
	seen := make(map[string]bool)
	models := make([]fetchedModel, 0, len(list.Data)+len(list.Models))
	add := func(fm fetchedModel) {
		if fm.ID == "" || seen[fm.ID] {
			return
		}
		seen[fm.ID] = true
		models = append(models, fm)
	}
	for _, m := range list.Models {
		fm := fetchedModel{ID: m.Slug}
		if fm.ID == "" {
			fm.ID = m.DisplayName
		}
		if m.ContextWindow > 0 {
			fm.Context = humanContext(m.ContextWindow)
		}
		fm.Input = m.InputModalities
		add(fm)
	}
	for _, m := range list.Data {
		add(fetchedModel{ID: m.ID})
	}
	// An empty list is still a fetched list — the UI toasts "no models returned".
	writeJSON(w, http.StatusOK, struct {
		Mode   string         `json:"mode"`
		Models []fetchedModel `json:"models"`
	}{Mode: "fetched", Models: models})
}

// writeJSON marshals v with the status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encoding error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// errorJSON emits the {"error": msg} failure envelope.
func errorJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleDefaultPut sets the default model tier (settings.json "model.default")
// after validating the provider/model pair exists. Backup + atomic write
// via config.SaveDefaultModel; restart applies.
func handleDefaultPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// Validation lives inside SaveDefaultModel (under the same lock as the
	// write) — a handler-side pre-check raced with concurrent saves.
	if err := config.SaveDefaultModel(req.Provider, req.Model); err != nil {
		switch {
		case errors.Is(err, config.ErrUnknownProvider), errors.Is(err, config.ErrUnknownModel):
			errorJSON(w, http.StatusBadRequest, err.Error())
		default:
			errorJSON(w, http.StatusInternalServerError, "save: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
