package config

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestFindClosestMatch_ExactMatch(t *testing.T) {
	t.Parallel()

	candidates := []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1"}
	got := FindClosestMatch("gpt-4o", candidates)
	if got != "gpt-4o" {
		t.Errorf("exact match = %q, want %q", got, "gpt-4o")
	}
}

func TestFindClosestMatch_FuzzyMatch(t *testing.T) {
	t.Parallel()

	// "max3" fuzzy-matches "MiniMax-M3" via character-order subsequence.
	got := FindClosestMatch("max3", []string{"MiniMax-M3", "glm-5.2"})
	if got != "MiniMax-M3" {
		t.Errorf("fuzzy match = %q, want %q", got, "MiniMax-M3")
	}
}

func TestFindClosestMatch_NoMatch(t *testing.T) {
	t.Parallel()

	got := FindClosestMatch("zzzzz", []string{"MiniMax-M3", "glm-5.2"})
	if got != "" {
		t.Errorf("no-match = %q, want empty", got)
	}
}

func TestFindClosestMatch_EmptyInput(t *testing.T) {
	t.Parallel()

	got := FindClosestMatch("", []string{"glm-5"})
	if got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
}

func TestFindClosestMatch_EmptyCandidates(t *testing.T) {
	t.Parallel()

	got := FindClosestMatch("glm-5", nil)
	if got != "" {
		t.Errorf("empty candidates = %q, want empty", got)
	}
}

func TestFindClosestMatch_ClosestNotFirst(t *testing.T) {
	t.Parallel()

	// When the closest match is NOT the first candidate, FindClosestMatch must
	// still return it. "mimo2.5" → "mimo-v2.5" (distance 1) beats
	// "mimo-v2.5-pro" (distance 4) even though pro appears first.
	candidates := []string{"mimo-v2.5-pro", "mimo-v2.5"}
	got := FindClosestMatch("mimo2.5", candidates)
	if got != "mimo-v2.5" {
		t.Errorf("FindClosestMatch(\"mimo2.5\") = %q, want %q", got, "mimo-v2.5")
	}
}

func TestFindClosestMatch_ClosestNotFirst_Rank(t *testing.T) {
	t.Parallel()

	candidates := []string{"mimo-v2.5-pro", "mimo-v2.5"}
	got, dist := FindClosestMatchRank("mimo2.5", candidates)
	if got != "mimo-v2.5" {
		t.Errorf("FindClosestMatchRank = %q, want %q", got, "mimo-v2.5")
	}
	if dist != 1 {
		t.Errorf("distance = %d, want 1", dist)
	}
}

func TestFindClosestMatch_PrefixCompromised(t *testing.T) {
	t.Parallel()

	// "glm5" still matches "glm-5.2" (normalized "glm52" starts with "glm5").
	got := FindClosestMatch("glm5", []string{"glm-5.2"})
	if got != "glm-5.2" {
		t.Errorf("prefix-like match = %q, want %q", got, "glm-5.2")
	}
}

func TestFindClosestMatch_CrossProviderPrefix(t *testing.T) {
	t.Parallel()

	// "gpt5" fuzzy-matches "gpt-4o" (via g-p-t-5 characters... actually
	// "gpt5" doesn't match "gpt4o" because 5 ≠ 4o. Let's use a valid case.)
	got := FindClosestMatch("gpt4", []string{"gpt-4o", "gpt-4o-mini"})
	if got == "" {
		t.Error("gpt4 should match at least one gpt-4 model")
	}
}

func TestFindClosestMatch_CaseInsensitive(t *testing.T) {
	t.Parallel()

	got := FindClosestMatch("MINIMAX", []string{"MiniMax-M3"})
	if got != "MiniMax-M3" {
		t.Errorf("case-insensitive = %q, want %q", got, "MiniMax-M3")
	}
}

func TestFindClosestMatch_SeparatorAgnostic(t *testing.T) {
	t.Parallel()

	got := FindClosestMatch("glm_5", []string{"glm-5.2"})
	if got != "glm-5.2" {
		t.Errorf("separator agnostic = %q, want %q", got, "glm-5.2")
	}
}

func TestNormalizeModelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"glm-5", "glm5"},
		{"glm_5", "glm5"},
		{"glm.5", "glm5"},
		{"MiniMax-M3", "minimaxm3"},
		{"gpt-4o-mini", "gpt4omini"},
		{"plain", "plain"},
		{"", ""},
		{"-_.", ""},
		{"a-b.c_d", "abcd"},
	}
	for _, tc := range tests {
		if got := normalizeModelName(tc.input); got != tc.want {
			t.Errorf("normalizeModelName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ResolveModel error paths
// ---------------------------------------------------------------------------

func TestResolveModel_ProviderHasNoModels(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Providers: []Provider{
			{Name: "empty", Models: NewModelsFromMap(map[string]ModelConfig{})},
		},
	}
	_, _, err := cfg.ResolveModel()
	if err == nil {
		t.Fatal("expected error when first provider has no models")
	}
	if !strings.Contains(err.Error(), "has no models") {
		t.Errorf("error should mention 'has no models', got: %v", err)
	}
}

func TestResolveModel_ProviderNotFound(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Model: ModelSpec{"default": "unknown/glm-5"},
		Providers: []Provider{
			{Name: "zhipu", Models: NewModelsFromMap(map[string]ModelConfig{"glm-5": {}})},
		},
	}
	_, _, err := cfg.ResolveModel()
	if err == nil {
		t.Fatal("expected error when provider name does not exist")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention the missing provider name, got: %v", err)
	}
}

func TestResolveModel_ModelNotFoundInProvider(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Model: ModelSpec{"default": "zhipu/nonexistent"},
		Providers: []Provider{
			{Name: "zhipu", Models: NewModelsFromMap(map[string]ModelConfig{"glm-5": {}})},
		},
	}
	_, _, err := cfg.ResolveModel()
	if err == nil {
		t.Fatal("expected error when model is not found within a named provider")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
	if !strings.Contains(err.Error(), "zhipu") {
		t.Errorf("error should mention the provider name, got: %v", err)
	}
}

func TestResolveModel_NoProvidersConfigured(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	_, _, err := cfg.ResolveModel()
	if err == nil {
		t.Fatal("expected error when no providers configured")
	}
	if !strings.Contains(err.Error(), "no providers") {
		t.Errorf("error should mention 'no providers', got: %v", err)
	}
}

func TestResolveModel_CrossProviderMatch(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Model: ModelSpec{"default": "glm-5"},
		Providers: []Provider{
			{Name: "zhipu", Models: NewModelsFromMap(map[string]ModelConfig{"glm-5": {}})},
			{Name: "other", Models: NewModelsFromMap(map[string]ModelConfig{"other-model": {}})},
		},
	}
	p, model, err := cfg.ResolveModel()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "zhipu" {
		t.Errorf("provider = %q, want %q", p.Name, "zhipu")
	}
	if model != "glm-5" {
		t.Errorf("model = %q, want %q", model, "glm-5")
	}
}

// ---------------------------------------------------------------------------
// findProvider tests
// ---------------------------------------------------------------------------

func TestFindProvider_NotFound(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Providers: []Provider{
			{Name: "zhipu"},
			{Name: "minimax"},
		},
	}
	if got := cfg.findProvider("nonexistent"); got != nil {
		t.Errorf("findProvider(nonexistent) = %v, want nil", got)
	}
}

func TestFindProvider_Found(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Providers: []Provider{
			{Name: "zhipu", URL: "https://zhipu.ai"},
			{Name: "minimax", URL: "https://minimax.com"},
		},
	}
	for _, name := range []string{"zhipu", "minimax"} {
		got := cfg.findProvider(name)
		if got == nil {
			t.Errorf("findProvider(%q) = nil, want non-nil", name)
			continue
		}
		if got.Name != name {
			t.Errorf("findProvider(%q).Name = %q, want %q", name, got.Name, name)
		}
	}
}

// ---------------------------------------------------------------------------
// ProxyHTTPClient tests
// ---------------------------------------------------------------------------

func TestProxyHTTPClient_NoProxy(t *testing.T) {
	t.Parallel()

	cfg := &Config{Proxy: ""}
	client := cfg.ProxyHTTPClient()
	if client == nil {
		t.Fatal("ProxyHTTPClient() returned nil, want http.DefaultClient")
	}
}

func TestProxyHTTPClient_ValidProxy(t *testing.T) {
	t.Parallel()

	cfg := &Config{Proxy: "http://localhost:10809"}
	client := cfg.ProxyHTTPClient()
	if client == nil {
		t.Fatal("ProxyHTTPClient() returned nil for valid proxy")
	}
	if client.Transport == nil {
		t.Fatal("expected Transport to be set on proxy client")
	}
}

func TestProxyHTTPClient_InvalidProxyURL(t *testing.T) {
	t.Parallel()

	cfg := &Config{Proxy: "://not-a-url"}
	client := cfg.ProxyHTTPClient()
	if client == nil {
		t.Fatal("ProxyHTTPClient() returned nil for invalid proxy")
	}
	if client.Transport != nil {
		t.Errorf("expected default client (nil Transport) for invalid proxy URL, got Transport=%v", client.Transport)
	}
}

func TestProxyHTTPClient_SOCKSProxy(t *testing.T) {
	t.Parallel()

	cfg := &Config{Proxy: "socks5://127.0.0.1:1080"}
	client := cfg.ProxyHTTPClient()
	if client == nil {
		t.Fatal("ProxyHTTPClient() returned nil for SOCKS proxy")
	}
	if client.Transport == nil {
		t.Fatal("expected Transport to be set on SOCKS proxy client")
	}
}

// ---------------------------------------------------------------------------
// CreateAllProviders: provider with no models
// ---------------------------------------------------------------------------

func TestCreateAllProviders_ProviderWithoutModels(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Providers: []Provider{
			{
				Name:   "with-models",
				Keys:   []string{"sk-key2"},
				Models: NewModelsFromMap(map[string]ModelConfig{"real-model": {}}),
				Type:   ProviderTypeOpenAI,
			},
			{
				Name:   "empty-models",
				Keys:   []string{"sk-key"},
				Models: NewModelsFromMap(map[string]ModelConfig{}),
				Type:   ProviderTypeOpenAI,
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 1 {
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		t.Fatalf("expected 1 provider (empty-models skipped), got %d: %v", len(m), names)
	}
	if _, ok := m["with-models"]; !ok {
		t.Errorf("expected provider \"with-models\" in map, got keys: %v", providerMapKeys(m))
	}
	if _, ok := m["empty-models"]; ok {
		t.Errorf("provider \"empty-models\" should be skipped (no models)")
	}
}

func providerMapKeys(m ProviderMap) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---------------------------------------------------------------------------
// IntOrHuman.UnmarshalJSON: ParseIntOrHuman failure path
// ---------------------------------------------------------------------------

func TestIntOrHuman_UnmarshalJSON_ParseIntOrHumanFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"non-numeric string", `"abc"`},
		{"mixed alphanum", `"12x"`},
		{"lone k unit", `"k"`},
		{"lone M unit", `"M"`},
		{"x before M", `"xM"`},
		{"x before k", `"xK"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var h IntOrHuman
			err := json.Unmarshal([]byte(tt.input), &h)
			if err == nil {
				t.Fatalf("expected error for input %s, got nil (value=%d)", tt.input, h.Int())
			}
			if !strings.Contains(err.Error(), "IntOrHuman") {
				t.Errorf("error should mention IntOrHuman prefix, got: %v", err)
			}
			if h.Int() != 0 {
				t.Errorf("value should be 0 after error, got %d", h.Int())
			}
		})
	}
}
