package dream

import (
	"os"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MinHours != 24 {
		t.Errorf("MinHours = %d, want 24", cfg.MinHours)
	}
	if cfg.MinSessions != 5 {
		t.Errorf("MinSessions = %d, want 5", cfg.MinSessions)
	}
}

func TestIsEnabled_DefaultTrue(t *testing.T) {
	_ = os.Unsetenv("GBOT_AUTO_DREAM")
	if !IsEnabled() {
		t.Error("IsEnabled() = false with unset env, want true")
	}
}

func TestIsEnabled_EnvTrue(t *testing.T) {
	tests := []string{"true", "True", "TRUE", "1"}
	for _, v := range tests {
		t.Run(v, func(t *testing.T) {
			_ = os.Setenv("GBOT_AUTO_DREAM", v)
			defer func() { _ = os.Unsetenv("GBOT_AUTO_DREAM") }()
			if !IsEnabled() {
				t.Errorf("IsEnabled() = false with GBOT_AUTO_DREAM=%q, want true", v)
			}
		})
	}
}

func TestIsEnabled_EnvFalse(t *testing.T) {
	tests := []string{"false", "False", "FALSE", "0"}
	for _, v := range tests {
		t.Run(v, func(t *testing.T) {
			_ = os.Setenv("GBOT_AUTO_DREAM", v)
			defer func() { _ = os.Unsetenv("GBOT_AUTO_DREAM") }()
			if IsEnabled() {
				t.Errorf("IsEnabled() = true with GBOT_AUTO_DREAM=%q, want false", v)
			}
		})
	}
}

func TestIsEnabled_Unparseable(t *testing.T) {
	_ = os.Setenv("GBOT_AUTO_DREAM", "maybe")
	defer func() { _ = os.Unsetenv("GBOT_AUTO_DREAM") }()
	// Unparseable values should default to true
	if !IsEnabled() {
		t.Error("IsEnabled() = false with unparseable value, want true (default on)")
	}
}
