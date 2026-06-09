// Package config reads gbot configuration from files and environment variables.
//
// Source reference: bootstrap.ts, utils/config.ts
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// Config holds the full application configuration.
type Config struct {
	Model     string     `json:"model,omitempty"`      // "provider/model" or "model", empty → providers[0] first model
	Providers []Provider `json:"providers,omitempty"`  // ordered by priority, providers[0] is primary

	PermissionMode types.PermissionMode `json:"permission_mode,omitempty"`
	Permissions   json.RawMessage      `json:"permissions,omitempty"` // parsed by pkg/permission.LoadConfig()

	Theme string `json:"theme,omitempty"`

	Debug   bool `json:"debug,omitempty"`
	Verbose bool `json:"verbose,omitempty"`

	APITimeoutMS int `json:"api_timeout_ms,omitempty"` // milliseconds

	Hooks json.RawMessage `json:"hooks,omitempty"`
}

// ModelConfig holds per-model metadata.
type ModelConfig struct {
	Context   IntOrHuman `json:"context,omitempty"`    // context window in tokens, e.g. "200k", "1M". Default: 200k.
	Input     []string   `json:"input,omitempty"`      // accepted input types, e.g. ["text", "image"]. Default: ["text"].
	MaxTokens IntOrHuman `json:"max_tokens,omitempty"` // max output tokens. Default: 32k.
}

// Provider holds configuration for a single LLM provider.
type Provider struct {
	Name   string                 `json:"name"`              // display name, e.g. "zhipu", "minimax"
	URL    string                 `json:"url"`               // e.g. "https://api.anthropic.com"
	Keys   []string               `json:"keys"`              // API keys or "$ENV_VAR" references
	Models map[string]ModelConfig `json:"models"`            // model name → metadata, e.g. {"glm-5": {context: "32k"}}
	Type   string                 `json:"type,omitempty"`    // "auto" (default) | "openai" | "anthropic"
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

// ModelNames returns all model names defined for this provider.
func (p *Provider) ModelNames() []string {
	names := make([]string, 0, len(p.Models))
	for name := range p.Models {
		names = append(names, name)
	}
	return names
}

// HasModel returns true if this provider has a model with the exact name.
func (p *Provider) HasModel(name string) bool {
	_, ok := p.Models[name]
	return ok
}

// GetModelConfig returns the ModelConfig for a model, or nil if not found.
func (p *Provider) GetModelConfig(name string) *ModelConfig {
	if mc, ok := p.Models[name]; ok {
		return &mc
	}
	return nil
}

// FirstModelName returns the first model name. Used as fallback when Config.Model is empty.
func (p *Provider) FirstModelName() string {
	for name := range p.Models {
		return name
	}
	return ""
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		PermissionMode: types.PermissionModeDefault,
		APITimeoutMS:   300000,
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
	if c.Model == "" {
		return "", "", nil
	}
	if before, after, ok := strings.Cut(c.Model, "/"); ok {
		return before, after, nil
	}
	return "", c.Model, nil
}

// ResolveModel resolves Config.Model into a concrete Provider and model name.
// Handles fuzzy matching via normalizeModelName + longest prefix.
func (c *Config) ResolveModel() (*Provider, string, error) {
	providerName, modelName, err := c.ParseModel()
	if err != nil {
		return nil, "", err
	}

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
		matched := FindModelByLongestPrefix(modelName, p.ModelNames())
		if matched == "" {
			return nil, "", fmt.Errorf("model %q not found in provider %q", modelName, p.Name)
		}
		return p, matched, nil
	}

	// No provider → cross-provider fuzzy search.
	for i := range c.Providers {
		p := &c.Providers[i]
		matched := FindModelByLongestPrefix(modelName, p.ModelNames())
		if matched != "" {
			return p, matched, nil
		}
	}
	return nil, "", fmt.Errorf("model %q not found in any provider", modelName)
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

// normalizeModelName strips -_. separators for fuzzy matching.
func normalizeModelName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == '-' || c == '_' || c == '.' {
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// FindModelByLongestPrefix finds the model name whose normalized form
// starts with the normalized input. Returns the longest match.
// Returns empty string if no match. Exported for use by TUI model commands.
func FindModelByLongestPrefix(input string, candidates []string) string {
	ni := normalizeModelName(input)

	var best string
	var bestNorm string
	for _, c := range candidates {
		// Exact match wins immediately.
		if c == input {
			return c
		}
		nc := normalizeModelName(c)
		if !strings.HasPrefix(nc, ni) {
			continue
		}
		// Pick longest normalized match.
		if best == "" || len(nc) > len(bestNorm) {
			best = c
			bestNorm = nc
		}
	}
	return best
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
