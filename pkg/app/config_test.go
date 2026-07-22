package app

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

func TestBuildProviderConfigMap(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "openai"},
			{Name: "anthropic"},
			{Name: "zhipu"},
		},
	}
	m := buildProviderConfigMap(cfg)
	if len(m) != 3 {
		t.Fatalf("len(m) = %d, want 3", len(m))
	}
	for _, name := range []string{"openai", "anthropic", "zhipu"} {
		p, ok := m[name]
		if !ok {
			t.Errorf("m[%q] not found", name)
			continue
		}
		if p.Name != name {
			t.Errorf("m[%q].Name = %q, want %q", name, p.Name, name)
		}
	}
}

func TestBuildProviderConfigMap_Empty(t *testing.T) {
	cfg := &config.Config{}
	m := buildProviderConfigMap(cfg)
	if len(m) != 0 {
		t.Errorf("len(m) = %d, want 0", len(m))
	}
}

func TestResolvePrimaryProvider_NoProviderInMap(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelSpec{"default": "unknown/model"},
		Providers: []config.Provider{
			{Name: "unknown", Models: config.NewModelsFromMap(map[string]config.ModelConfig{
				"model": {},
			})},
		},
	}
	_, _, _, err := resolvePrimaryProvider(cfg, config.ProviderMap{})
	if err == nil {
		t.Fatal("expected error for provider not in map, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q does not mention provider name %q", err.Error(), "unknown")
	}
}
