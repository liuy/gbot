// Package config reads gbot configuration from files and environment variables.
//
// Source reference: bootstrap.ts, utils/config.ts
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// Config holds the full application configuration.
type Config struct {
	// API (legacy — used until main.go wiring in US-004)
	APIKey     string `json:"api_key,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	Model      string `json:"model,omitempty"`
	SmallModel string `json:"small_model,omitempty"`

	// Multi-provider configuration
	DefaultTier Tier       `json:"default,omitempty"`   // "lite" | "pro" | "max", defaults to "pro"
	Providers   []Provider `json:"providers,omitempty"` // ordered by priority, providers[0] is primary

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
	Name   string          `json:"name"`   // "anthropic" | "openai"
	URL    string          `json:"url"`    // e.g. "https://api.anthropic.com"
	Keys   []string        `json:"keys"`   // env var references like "$ANTHROPIC_API_KEY"
	Models map[Tier]string `json:"models"` // per-tier model names
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
		Model:          "claude-sonnet-4-20250514",
		SmallModel:     "claude-haiku-4-5-20251001",
		DefaultTier:    TierPro,
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
	if cfg.DefaultTier == "" {
		cfg.DefaultTier = TierPro
	}

	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("ANTHROPIC_AUTH_TOKEN"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("ANTHROPIC_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("ANTHROPIC_SMALL_FAST_MODEL"); v != "" {
		cfg.SmallModel = v
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
