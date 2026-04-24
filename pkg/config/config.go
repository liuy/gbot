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
	APIKey     string `json:"api_key,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`

	// Multi-provider configuration
	Model     string     `json:"model,omitempty"`      // "provider/tier" e.g. "zhipu/pro", empty → providers[0]/pro
	Providers []Provider `json:"providers,omitempty"`  // ordered by priority, providers[0] is primary

	// Permissions
	PermissionMode types.PermissionMode `json:"permission_mode,omitempty"`

	// UI
	Theme string `json:"theme,omitempty"`

	// Debug
	Debug   bool `json:"debug,omitempty"`
	Verbose bool `json:"verbose,omitempty"`

	// API timeout in milliseconds
	APITimeoutMS int `json:"api_timeout_ms,omitempty"`
}

// Tier represents a model capability tier.
type Tier string

const (
	TierLite Tier = "lite"
	TierPro  Tier = "pro"
	TierMax  Tier = "max"
)

// Provider holds configuration for a single LLM provider.
type Provider struct {
	Name   string          `json:"name"`             // display name, e.g. "glm", "claude", "deepseek"
	URL    string          `json:"url"`              // e.g. "https://api.anthropic.com"
	Keys   []string        `json:"keys"`             // env var references like "$ANTHROPIC_API_KEY"
	Models map[Tier]string `json:"models"`           // per-tier model names
	Type   string          `json:"type,omitempty"`   // "auto" (default) | "openai" | "anthropic"

	// ContextWindow is the model's maximum context window in tokens.
	// If 0, DefaultCapabilities provides a fallback based on model name.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxTokens is the model's maximum output tokens per request.
	// If 0, DefaultCapabilities provides a fallback based on model name.
	MaxTokens int `json:"max_tokens,omitempty"`
}

const (
	ProviderTypeAuto     = "auto"
	ProviderTypeOpenAI   = "openai"
	ProviderTypeAnthropic = "anthropic"
)

// ProviderType returns the resolved provider type.
// If Type is "auto" or empty, it is inferred from the URL.
func (p *Provider) ProviderType() string {
	switch p.Type {
	case ProviderTypeOpenAI, ProviderTypeAnthropic:
		return p.Type
	default:
		// auto-detect from URL: hostname or path containing "anthropic"
		if u, err := url.Parse(p.URL); err == nil {
			if strings.HasSuffix(u.Hostname(), "anthropic.com") || strings.Contains(u.Path, "anthropic") {
				return ProviderTypeAnthropic
			}
		}
		return ProviderTypeOpenAI
	}
}

// ResolveKey resolves the first key that yields a non-empty value.
// Entries prefixed with "$" are treated as environment variable references.
// Entries without "$" are used as literal API keys.
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

// ModelFor returns the model name for the given tier.
// Falls back to TierPro if the requested tier has no model.
func (p *Provider) ModelFor(tier Tier) string {
	if m, ok := p.Models[tier]; ok && m != "" {
		return m
	}
	return p.Models[TierPro]
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		BaseURL:        "https://api.anthropic.com",
		PermissionMode: types.PermissionModeDefault,
		APITimeoutMS:   300000,
	}
}

// Load reads configuration from environment variables and config files.
// Priority: env vars > project config > user config > defaults.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// 1. Load user config from ~/.gbot/settings.json
	homeDir, err := os.UserHomeDir()
	if err == nil {
		userCfgPath := filepath.Join(homeDir, ".gbot", "settings.json")
		if err := loadFromFile(cfg, userCfgPath); err != nil {
			return nil, fmt.Errorf("user config: %w", err)
		}
	}

	// 2. Load project config from .gbot/settings.json
	if err := loadFromFile(cfg, ".gbot/settings.json"); err != nil {
		return nil, fmt.Errorf("project config: %w", err)
	}

	// 3. Override with environment variables
	if cfg.Model == "" {
		cfg.Model = "pro"
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

// ValidTiers is the set of recognized tier values.
var ValidTiers = map[Tier]bool{
	TierLite: true,
	TierPro:  true,
	TierMax:  true,
}

// ParseModel parses the Model field into provider name and tier.
// Formats: "provider/tier", "tier", or "" → ("", "pro").
// Returns an error if the tier is not one of lite, pro, max.
func (c *Config) ParseModel() (provider string, tier Tier, err error) {
	if c.Model == "" {
		return "", TierPro, nil
	}
	if before, after, ok := strings.Cut(c.Model, "/"); ok {
		tier = Tier(after)
		if !ValidTiers[tier] {
			return "", "", fmt.Errorf("invalid tier %q in model %q (valid: lite, pro, max)", after, c.Model)
		}
		return before, tier, nil
	}
	tier = Tier(c.Model)
	if !ValidTiers[tier] {
		return "", "", fmt.Errorf("invalid tier %q (valid: lite, pro, max)", c.Model)
	}
	return "", tier, nil
}

// Save writes the configuration back to the user settings file (~/.gbot/settings.json).
// Only updates the "model" field in the existing file to avoid persisting
// env-var-derived secrets or overwriting unrelated settings.
func (c *Config) Save() error {
	configDir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(configDir, "settings.json")

	// Read existing file as raw JSON map
	raw := make(map[string]json.RawMessage)
	if existing, _ := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(existing, &raw); err != nil {
			raw = make(map[string]json.RawMessage) // corrupted file — start fresh
		}
	}

	// Update only the model field
	modelJSON, _ := json.Marshal(c.Model)
	raw["model"] = modelJSON

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to temp file then rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
