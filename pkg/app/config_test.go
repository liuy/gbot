package app

import (
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
