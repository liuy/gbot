package config

import (
	"strings"
	"testing"
)

func TestResolvePrimary(t *testing.T) {
	cfg := &Config{
		Model: ModelSpec{"default": "p1/m1"},
		Providers: []Provider{
			{Name: "p1", URL: "http://127.0.0.1:1", Keys: []string{"k1"}, Models: newModelSet("m1")},
			{Name: "p2", URL: "http://127.0.0.1:2", Keys: []string{"k2"}, Models: newModelSet("m2")},
		},
	}
	providerMap, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	prov, model, pcfg, err := cfg.ResolvePrimary(providerMap)
	if err != nil {
		t.Fatalf("ResolvePrimary: %v", err)
	}
	if prov == nil || prov.Name() != "p1" {
		t.Errorf("provider = %v, want p1", prov)
	}
	if model != "m1" {
		t.Errorf("model = %q, want m1", model)
	}
	if pcfg == nil || pcfg.Name != "p1" {
		t.Errorf("provider cfg = %+v, want p1", pcfg)
	}

	// Model resolving to a provider without a live instance in the map is an
	// error, not a silent fallback.
	cfgNoKey := &Config{
		Model:     ModelSpec{"default": "p2/m2"},
		Providers: []Provider{{Name: "p2", URL: "u", Keys: []string{"k"}, Models: newModelSet("m2")}},
	}
	_, _, _, err = cfgNoKey.ResolvePrimary(ProviderMap{"other": prov})
	if err == nil {
		t.Fatal("ResolvePrimary must fail when the resolved provider has no live instance")
	}
	if !strings.Contains(err.Error(), "p2") {
		t.Errorf("error must name the missing provider, got: %v", err)
	}
}

// TestResolvePrimary_ErrorPaths pins the two failure entries: an
// unresolvable model propagates ResolveModel's error verbatim, and a config
// with no providers at all errors instead of returning a nil triple.
func TestResolvePrimary_ErrorPaths(t *testing.T) {
	cfgBadModel := &Config{
		Model:     ModelSpec{"default": "nope/does-not-exist"},
		Providers: []Provider{{Name: "t", URL: "u", Keys: []string{"k"}, Models: newModelSet("m")}},
	}
	_, _, _, err := cfgBadModel.ResolvePrimary(ProviderMap{})
	if err == nil {
		t.Fatal("ResolvePrimary must fail when the configured model resolves to nothing")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error must carry the unresolved model name, got: %v", err)
	}

	cfgNoProviders := &Config{}
	_, _, _, err = cfgNoProviders.ResolvePrimary(ProviderMap{})
	if err == nil {
		t.Fatal("ResolvePrimary must fail with no providers configured")
	}
}

// newModelSet builds a Models set holding the given model ids.
func newModelSet(ids ...string) Models {
	m := Models{}
	for _, id := range ids {
		m.Set(id, ModelConfig{})
	}
	return m
}
