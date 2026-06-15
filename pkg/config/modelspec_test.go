package config

import (
	"encoding/json"
	"testing"
)

func TestModelSpec_UnmarshalMap(t *testing.T) {
	t.Parallel()
	var m ModelSpec
	if err := json.Unmarshal([]byte(`{"default":"zhipu/glm-5.2","lite":"deepseek/flash","pro":"zhipu/glm-5.2","max":"minimax/minimax-3"}`), &m); err != nil {
		t.Fatalf("unmarshal map: %v", err)
	}
	if m.String() != "zhipu/glm-5.2" {
		t.Errorf("String() = %q, want default %q", m.String(), "zhipu/glm-5.2")
	}
	if got := m.ResolveTier("max"); got != "minimax/minimax-3" {
		t.Errorf("ResolveTier(max) = %q, want %q", got, "minimax/minimax-3")
	}
	if got := m.ResolveTier("lite"); got != "deepseek/flash" {
		t.Errorf("ResolveTier(lite) = %q, want %q", got, "deepseek/flash")
	}
	// Unknown tier falls back to default.
	if got := m.ResolveTier("ultra"); got != "zhipu/glm-5.2" {
		t.Errorf("ResolveTier(ultra) = %q, want default fallback %q", got, "zhipu/glm-5.2")
	}
	// Empty tier returns default.
	if got := m.ResolveTier(""); got != "zhipu/glm-5.2" {
		t.Errorf("ResolveTier('') = %q, want default %q", got, "zhipu/glm-5.2")
	}
}

func TestModelSpec_MarshalRoundTrip(t *testing.T) {
	t.Parallel()
	original := `{"default":"a","lite":"b","max":"c"}`
	var m ModelSpec
	if err := json.Unmarshal([]byte(original), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if out["default"] != "a" || out["lite"] != "b" || out["max"] != "c" {
		t.Errorf("round-trip lost data: %v", out)
	}
}

func TestModelSpec_IsZero(t *testing.T) {
	t.Parallel()
	var m ModelSpec
	if !m.IsZero() {
		t.Error("empty ModelSpec should be zero")
	}
	m = ModelSpec{"default": "x"}
	if m.IsZero() {
		t.Error("non-empty ModelSpec should not be zero")
	}
}

func TestConfig_ResolveTier(t *testing.T) {
	t.Parallel()
	// Default-only config: unknown tiers return default.
	cfg := &Config{Model: ModelSpec{"default": "zhipu/glm-5.2"}}
	if got := cfg.ResolveTier("max"); got != "zhipu/glm-5.2" {
		t.Errorf("default-only config ResolveTier(max) = %q, want default passthrough %q", got, "zhipu/glm-5.2")
	}

	// Tiers config
	var m ModelSpec
	if err := json.Unmarshal([]byte(`{"default":"a","lite":"b","pro":"c","max":"d"}`), &m); err != nil {
		t.Fatal(err)
	}
	cfg2 := &Config{Model: m}
	if got := cfg2.ResolveTier("lite"); got != "b" {
		t.Errorf("ResolveTier(lite) = %q, want %q", got, "b")
	}
	if got := cfg2.ResolveTier("max"); got != "d" {
		t.Errorf("ResolveTier(max) = %q, want %q", got, "d")
	}
	if got := cfg2.ResolveTier(""); got != "a" {
		t.Errorf("ResolveTier('') = %q, want default %q", got, "a")
	}
}
