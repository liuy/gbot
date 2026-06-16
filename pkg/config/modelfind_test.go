package config

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestFindModelByLongestPrefix_ExactMatch(t *testing.T) {
	t.Parallel()

	candidates := []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1"}
	got := FindModelByLongestPrefix("gpt-4o", candidates)
	if got != "gpt-4o" {
		t.Errorf("exact match FindModelByLongestPrefix = %q, want %q", got, "gpt-4o")
	}
}

func TestFindModelByLongestPrefix_NoMatch(t *testing.T) {
	t.Parallel()

	candidates := []string{"gpt-4o", "glm-5"}
	got := FindModelByLongestPrefix("nonexistent", candidates)
	if got != "" {
		t.Errorf("no-match FindModelByLongestPrefix = %q, want empty", got)
	}
}

func TestFindModelByLongestPrefix_LongestNormalized(t *testing.T) {
	t.Parallel()

	// All candidates start with the normalized input "glm5";
	// the function must pick the one whose normalized form is longest
	// (i.e. the most specific match). Strict > comparison means
	// candidates with equal normalized length keep the first match.
	// Here "glm-5.1-preview" (norm "glm51preview", 11 chars) beats
	// "glm-5" (norm "glm5", 4 chars).
	candidates := []string{"glm-5", "glm-5.1-preview"}
	got := FindModelByLongestPrefix("glm5", candidates)
	if got != "glm-5.1-preview" {
		t.Errorf("longest normalized FindModelByLongestPrefix = %q, want %q", got, "glm-5.1-preview")
	}
}

func TestFindModelByLongestPrefix_LongestNormalizedStableOrder(t *testing.T) {
	t.Parallel()

	// When two candidates have equal normalized length, the iteration
	// order determines the winner (strict > means later equal-length
	// candidates do NOT replace the earlier one).
	candidates := []string{"glm-5.1", "glm-5.2"}
	got := FindModelByLongestPrefix("glm5", candidates)
	if got != "glm-5.1" {
		t.Errorf("equal-length normalized FindModelByLongestPrefix = %q, want first %q", got, "glm-5.1")
	}
}

func TestFindModelByLongestPrefix_NormalizedInput(t *testing.T) {
	t.Parallel()

	// Input with separators should normalize before comparison.
	candidates := []string{"glm5", "glm-5-lite"}
	got := FindModelByLongestPrefix("glm_5", candidates)
	if got != "glm-5-lite" {
		t.Errorf("normalized input FindModelByLongestPrefix = %q, want %q", got, "glm-5-lite")
	}
}

func TestFindModelByLongestPrefix_PartialPrefixSkipped(t *testing.T) {
	t.Parallel()

	// "gpt5" is not a prefix of "gpt-4o" (normalized "gpt4o"),
	// so the function should return "".
	candidates := []string{"gpt-4o"}
	got := FindModelByLongestPrefix("gpt5", candidates)
	if got != "" {
		t.Errorf("non-prefix FindModelByLongestPrefix = %q, want empty", got)
	}
}

func TestFindModelByLongestPrefix_EmptyCandidates(t *testing.T) {
	t.Parallel()

	got := FindModelByLongestPrefix("glm-5", nil)
	if got != "" {
		t.Errorf("empty candidates FindModelByLongestPrefix = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// ResolveModel error paths
// ---------------------------------------------------------------------------

func TestResolveModel_ProviderHasNoModels(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Providers: []Provider{
			{Name: "empty", Models: map[string]ModelConfig{}},
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
			{Name: "zhipu", Models: map[string]ModelConfig{"glm-5": {}}},
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
			{Name: "zhipu", Models: map[string]ModelConfig{"glm-5": {}}},
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

	// Empty ModelSpec + no providers → "no providers configured"
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
			{Name: "zhipu", Models: map[string]ModelConfig{"glm-5": {}}},
			{Name: "other", Models: map[string]ModelConfig{"other-model": {}}},
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
// normalizeModelName tests
// ---------------------------------------------------------------------------

func TestNormalizeModelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"glm-5", "glm5"},
		{"glm_5", "glm5"},
		{"glm.5", "glm5"},
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

	// An invalid proxy URL should fall back to http.DefaultClient.
	cfg := &Config{Proxy: "://not-a-url"}
	client := cfg.ProxyHTTPClient()
	if client == nil {
		t.Fatal("ProxyHTTPClient() returned nil for invalid proxy")
	}
	// http.DefaultClient has nil Transport; a configured proxy has non-nil Transport.
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

	// The first provider determines the resolved model via ResolveModel,
	// so it must have at least one model. The second provider has a key
	// but no models and should be skipped by the loop's len(p.Models) == 0 guard.
	cfg := &Config{
		Providers: []Provider{
			{
				Name:   "with-models",
				Keys:   []string{"sk-key2"},
				Models: map[string]ModelConfig{"real-model": {}},
				Type:   ProviderTypeOpenAI,
			},
			{
				Name:   "empty-models",
				Keys:   []string{"sk-key"},
				Models: map[string]ModelConfig{},
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
		// Valid JSON string but invalid human format → triggers ParseIntOrHuman error.
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
			// Value must remain zero.
			if h.Int() != 0 {
				t.Errorf("value should be 0 after error, got %d", h.Int())
			}
		})
	}
}
