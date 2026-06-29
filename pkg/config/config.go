// Package config reads gbot configuration from files and environment variables.
//
// Source reference: bootstrap.ts, utils/config.ts
package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lithammer/fuzzysearch/fuzzy"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// Config holds the full application configuration.
type Config struct {
	Model     ModelSpec  `json:"model"`               // string "provider/model" or map of tiers {default,lite,pro,max,...}
	Providers []Provider `json:"providers,omitempty"` // ordered by priority, providers[0] is primary

	PermissionMode types.PermissionMode `json:"permission_mode,omitempty"`
	Permissions    json.RawMessage      `json:"permissions,omitempty"` // parsed by pkg/permission.LoadConfig()

	Theme string `json:"theme,omitempty"`

	Debug   bool `json:"debug,omitempty"`
	Verbose bool `json:"verbose,omitempty"`

	APITimeoutMS int `json:"api_timeout_ms,omitempty"` // milliseconds

	Proxy string `json:"proxy,omitempty"` // HTTP/SOCKS proxy, e.g. "http://localhost:10809"

	// Web search provider API keys. Key = provider name, value = API key.
	// Empty value = anonymous mode. Omitted provider = not registered.
	Web map[string]string `json:"web,omitempty"`

	// Plugins toggles discovered plugins by name. Missing key = enabled
	// (default). Explicit false = disabled. Used by pkg/plugins.LoadAndInitialize.
	Plugins map[string]bool `json:"plugins,omitempty"`

	// PprofAddr is the TCP address for the pprof HTTP server. Empty = use
	// default "localhost:6060". Set GBOT_PPROF_ADDR env to override at
	// runtime; "off" disables the server entirely.
	PprofAddr string `json:"pprof_addr,omitempty"`

	Hooks json.RawMessage `json:"hooks,omitempty"`

	// SessionNotes controls the background session memory agent that
	// extracts SESSION_NOTES.md. Disabled defaults to false (enabled).
	// Model is optional: "provider/model" or "model" (fuzzy search);
	// empty = inherit parent engine's model.
	SessionNotes SessionNotesConfig `json:"session_notes"`
}

// SessionNotesConfig configures the session memory sub-agent.
type SessionNotesConfig struct {
	Disabled bool   `json:"disabled,omitempty"`
	Model    string `json:"model,omitempty"`
}

// ModelConfig holds per-model metadata.
type ModelConfig struct {
	Context   IntOrHuman       `json:"context,omitempty"`    // context window in tokens, e.g. "200k", "1M". Default: 200k.
	Input     []string         `json:"input,omitempty"`      // accepted input types, e.g. ["text", "image"]. Default: ["text"].
	MaxTokens IntOrHuman       `json:"max_tokens,omitempty"` // max output tokens. Default: 32k.
	Thinking  llm.ThinkingMode `json:"thinking,omitempty"`   // Anthropic thinking field. Allowed: "enabled", "disabled", "adaptive". Empty = omit.
}

// Provider holds configuration for a single LLM provider.
type Provider struct {
	Name        string         `json:"name"`                   // display name, e.g. "zhipu", "minimax"
	URL         string         `json:"url"`                    // e.g. "https://api.anthropic.com"
	Keys        []string       `json:"keys"`                   // API keys or "$ENV_VAR" references
	Models      Models         `json:"models"`                 // model name → metadata. Ordered (see Models type).
	Type        string         `json:"type,omitempty"`         // "auto" (default) | "openai" | "anthropic"
	Free        bool           `json:"free,omitempty"`         // if true, fetch free models from /api/v1/models at startup (OpenRouter)
	ExtraParams map[string]any `json:"extra_params,omitempty"` // Provider-specific params merged into request body
}

const (
	ProviderTypeAuto      = "auto"
	ProviderTypeOpenAI    = "openai"
	ProviderTypeAnthropic = "anthropic"
)

// ProviderType returns the resolved provider type.
func (p *Provider) ProviderType() string {
	switch p.Type {
	case ProviderTypeOpenAI, ProviderTypeAnthropic:
		return p.Type
	default:
		if u, err := url.Parse(p.URL); err == nil {
			if strings.HasSuffix(u.Hostname(), "anthropic.com") || strings.Contains(u.Path, "anthropic") {
				return ProviderTypeAnthropic
			}
		}
		return ProviderTypeOpenAI
	}
}

// ResolveKey resolves the first key that yields a non-empty value.
func (p *Provider) ResolveKey() string {
	for _, ref := range p.Keys {
		if after, ok := strings.CutPrefix(ref, "$"); ok {
			if v := os.Getenv(after); v != "" {
				return v
			}
		} else if ref != "" {
			return ref
		}
	}
	return ""
}

// ModelNames returns all model names defined for this provider, in
// configuration order (settings.json key order, or API-returned order
// for free providers).
func (p *Provider) ModelNames() []string {
	return p.Models.Ordered()
}

// HasModel returns true if this provider has a model with the exact name.
func (p *Provider) HasModel(name string) bool {
	return p.Models.Has(name)
}

// GetModelConfig returns the ModelConfig for a model, or nil if not found.
func (p *Provider) GetModelConfig(name string) *ModelConfig {
	if mc, ok := p.Models.Get(name); ok {
		return &mc
	}
	return nil
}

// FirstModelName returns the first model name (in config order). Used as
// fallback when Config.Model is empty.
func (p *Provider) FirstModelName() string {
	names := p.Models.Ordered()
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		PermissionMode: types.PermissionModeDefault,
		APITimeoutMS:   300000,
	}
}

// ProxyHTTPClient returns an *http.Client configured with the proxy from settings.
// Returns http.DefaultClient if no proxy is set.
func (c *Config) ProxyHTTPClient() *http.Client {
	if c.Proxy == "" {
		return http.DefaultClient
	}
	proxyURL, err := url.Parse(c.Proxy)
	if err != nil {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
}

// Load reads configuration from environment variables and config files.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	homeDir, err := os.UserHomeDir()
	if err == nil {
		userCfgPath := filepath.Join(homeDir, ".gbot", "settings.json")
		if err := loadFromFile(cfg, userCfgPath); err != nil {
			return nil, fmt.Errorf("user config: %w", err)
		}
	}

	if err := loadFromFile(cfg, ".gbot/settings.json"); err != nil {
		return nil, fmt.Errorf("project config: %w", err)
	}

	if v := os.Getenv("API_TIMEOUT_MS"); v != "" {
		var ms int
		if _, err := fmt.Sscanf(v, "%d", &ms); err == nil && ms > 0 {
			cfg.APITimeoutMS = ms
		}
	}

	return cfg, nil
}

func loadFromFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, cfg)
}

// ConfigDir returns the gbot config directory (~/.gbot).
func ConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gbot"), nil
}

// ParseModel parses the Model field into provider name and model name.
// Formats:
//   - "provider/model" → ("provider", "model")
//   - "model"          → ("", "model") — cross-provider lookup needed
//   - ""               → ("", "") — use first provider's first model
func (c *Config) ParseModel() (providerName, modelName string, err error) {
	m := c.Model.String()
	if m == "" {
		return "", "", nil
	}
	if before, after, ok := strings.Cut(m, "/"); ok {
		return before, after, nil
	}
	return "", m, nil
}

// ResolveTier returns the model string for a given tier name.
// For a single-string ModelSpec, returns that string regardless of tier.
// For a tiers map, looks up the tier; falls back to "default", then "".
// If tier is empty, returns the default.
func (c *Config) ResolveTier(tier string) string {
	return c.Model.ResolveTier(tier)
}

// ResolveModel resolves Config.Model into a concrete Provider and model name.
// Handles fuzzy matching via normalizeModelName + longest prefix.
func (c *Config) ResolveModel() (*Provider, string, error) {
	providerName, modelName, err := c.ParseModel()
	if err != nil {
		return nil, "", err
	}
	return c.resolveProviderModel(providerName, modelName)
}

// ResolveModelByName resolves an arbitrary "provider/model" or "model" string
// into a Provider + model name, reusing the same fuzzy search logic as
// ResolveModel. Empty name returns nil provider (caller inherits parent).
func (c *Config) ResolveModelByName(name string) (*Provider, string, error) {
	if name == "" {
		return nil, "", nil
	}
	var providerName, modelName string
	if before, after, ok := strings.Cut(name, "/"); ok {
		providerName, modelName = before, after
	} else {
		modelName = name
	}
	return c.resolveProviderModel(providerName, modelName)
}

// resolveProviderModel is the shared resolution core for ResolveModel and
// ResolveModelByName.
func (c *Config) resolveProviderModel(providerName, modelName string) (*Provider, string, error) {
	// Empty model → first provider's first model.
	if modelName == "" {
		if len(c.Providers) == 0 {
			return nil, "", fmt.Errorf("no providers configured")
		}
		p := &c.Providers[0]
		first := p.FirstModelName()
		if first == "" {
			return nil, "", fmt.Errorf("provider %q has no models", p.Name)
		}
		return p, first, nil
	}

	// Provider specified → search within that provider only.
	if providerName != "" {
		p := c.findProvider(providerName)
		if p == nil {
			return nil, "", fmt.Errorf("provider %q not found", providerName)
		}
		matched := FindClosestMatch(modelName, p.ModelNames())
		if matched == "" {
			return nil, "", fmt.Errorf("model %q not found in provider %q", modelName, p.Name)
		}
		return p, matched, nil
	}

	// No provider → cross-provider fuzzy search.
	for i := range c.Providers {
		p := &c.Providers[i]
		matched := FindClosestMatch(modelName, p.ModelNames())
		if matched != "" {
			return p, matched, nil
		}
	}
	return nil, "", fmt.Errorf("model %q not found in any provider", modelName)
}

// IsPluginEnabled reports whether a discovered plugin should be loaded.
// Plugins are enabled by default; only an explicit false in settings.json
// disables them.
func (c *Config) IsPluginEnabled(name string) bool {
	if c.Plugins == nil {
		return true
	}
	enabled, ok := c.Plugins[name]
	if !ok {
		return true
	}
	return enabled
}

// findProvider finds a provider by name (exact match).
func (c *Config) findProvider(name string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}

// normalizeModelName strips -_. separators and lowercases for fuzzy matching.
func normalizeModelName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == '-' || c == '_' || c == '.' {
			continue
		}
		b.WriteRune(c)
	}
	return strings.ToLower(b.String())
}

// FindClosestMatch finds the closest string from candidates using fuzzy
// search (character-order subsequence matching, case-insensitive,
// separator-agnostic). Used by /model (model name fuzzy match) and /engine
// (engine name fuzzy match). Returns empty string if no candidate matches.
func FindClosestMatch(input string, candidates []string) string {
	if input == "" || len(candidates) == 0 {
		return ""
	}
	ni := normalizeModelName(input)

	// Normalize candidates once, building a lookup slice.
	type entry struct {
		original string
		norm     string
	}
	entries := make([]entry, len(candidates))
	normCandidates := make([]string, len(candidates))
	for i, c := range candidates {
		e := entry{original: c, norm: normalizeModelName(c)}
		entries[i] = e
		normCandidates[i] = e.norm
	}

	ranks := fuzzy.RankFind(ni, normCandidates)
	if len(ranks) == 0 {
		return ""
	}
	// fuzzy.RankFind returns matches in candidate-list order, not sorted
	// by distance. Sort ascending so the closest match wins.
	sort.Slice(ranks, func(i, j int) bool {
		return ranks[i].Distance < ranks[j].Distance
	})
	return entries[ranks[0].OriginalIndex].original
}

// FindClosestMatchRank finds the best fuzzy match and returns its distance.
// Returns -1 if no candidate matches. Used for cross-provider selection
// where the globally closest match (across providers) wins.
func FindClosestMatchRank(input string, candidates []string) (model string, distance int) {
	if input == "" || len(candidates) == 0 {
		return "", -1
	}
	ni := normalizeModelName(input)

	type entry struct {
		original string
		norm     string
	}
	entries := make([]entry, len(candidates))
	normCandidates := make([]string, len(candidates))
	for i, c := range candidates {
		e := entry{original: c, norm: normalizeModelName(c)}
		entries[i] = e
		normCandidates[i] = e.norm
	}

	ranks := fuzzy.RankFind(ni, normCandidates)
	if len(ranks) == 0 {
		return "", -1
	}
	sort.Slice(ranks, func(i, j int) bool {
		return ranks[i].Distance < ranks[j].Distance
	})
	return entries[ranks[0].OriginalIndex].original, ranks[0].Distance
}

// Save writes the configuration back to the user settings file.
func (c *Config) Save() error {
	configDir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(configDir, "settings.json")

	raw := make(map[string]json.RawMessage)
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if err := json.Unmarshal(existing, &raw); err != nil {
			raw = make(map[string]json.RawMessage)
		}
	}

	modelJSON, _ := json.Marshal(c.Model)
	raw["model"] = modelJSON

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// ModelSpec is a map of named tiers to model strings
// (e.g. {"default":"zhipu/glm-5.2","lite":"minimax/minimax-3","pro":"zhipu/glm-5.1","max":"zhipu/glm-5.2"}).
// "default" is the main conversation model; other keys are tier names
// referenced by agents via model: <tier> in their frontmatter.
type ModelSpec map[string]string

// String returns the default model (the "default" key value, or "" if unset).
func (m ModelSpec) String() string {
	return m["default"]
}

// IsZero reports whether the spec is empty.
func (m ModelSpec) IsZero() bool {
	return len(m) == 0
}

// ResolveTier returns the model for a given tier name.
// Falls back to "default" if the tier is not found or tier is empty.
func (m ModelSpec) ResolveTier(tier string) string {
	if tier == "" {
		return m["default"]
	}
	if v, ok := m[tier]; ok {
		return v
	}
	return m["default"]
}
