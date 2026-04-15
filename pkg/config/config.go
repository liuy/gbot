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
	// API
	APIKey     string `json:"api_key,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	Model      string `json:"model,omitempty"`
	SmallModel string `json:"small_model,omitempty"`

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

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		BaseURL:       "https://api.anthropic.com",
		Model:         "claude-sonnet-4-20250514",
		SmallModel:    "claude-haiku-4-5-20251001",
		PermissionMode: types.PermissionModeDefault,
		APITimeoutMS:  300000,
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

// LoadFromSettingsFile reads from a specific settings file path.
// Used for reading ~/.claude/settings.minimax.json format.
func LoadFromSettingsFile(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	// Parse the settings file format (env section)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}

	// Extract env section
	if envRaw, ok := raw["env"]; ok {
		var env map[string]string
		if err := json.Unmarshal(envRaw, &env); err == nil {
			if v, ok := env["ANTHROPIC_BASE_URL"]; ok {
				cfg.BaseURL = strings.TrimRight(v, "/")
			}
			if v, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
				cfg.APIKey = v
			}
			if v, ok := env["ANTHROPIC_MODEL"]; ok {
				cfg.Model = v
			}
			if v, ok := env["ANTHROPIC_SMALL_FAST_MODEL"]; ok {
				cfg.SmallModel = v
			}
			if v, ok := env["API_TIMEOUT_MS"]; ok {
				var ms int
				if _, err := fmt.Sscanf(v, "%d", &ms); err == nil && ms > 0 {
					cfg.APITimeoutMS = ms
				}
			}
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
