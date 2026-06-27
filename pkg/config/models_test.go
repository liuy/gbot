package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestModels_UnmarshalPreservesOrder verifies that UnmarshalJSON keeps the
// object key order intact. Go's default map decoding drops order — this is
// the regression we're protecting against.
func TestModels_UnmarshalPreservesOrder(t *testing.T) {
	raw := []byte(`{"glm-5":{"context":"256k"},"glm-5.5":{"context":"128k"},"glm-air":{"context":"8k"}}`)
	var m Models
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	want := []string{"glm-5", "glm-5.5", "glm-air"}
	got := m.Ordered()
	if len(got) != len(want) {
		t.Fatalf("Ordered() len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Ordered()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	// Reverse-order JSON should produce reverse-order keys — this catches
	// accidental alphabetical sort (which would pass the first case).
	rawReverse := []byte(`{"glm-air":{"context":"8k"},"glm-5.5":{"context":"128k"},"glm-5":{"context":"256k"}}`)
	var m2 Models
	if err := json.Unmarshal(rawReverse, &m2); err != nil {
		t.Fatalf("Unmarshal reverse error: %v", err)
	}
	wantReverse := []string{"glm-air", "glm-5.5", "glm-5"}
	gotReverse := m2.Ordered()
	for i := range wantReverse {
		if gotReverse[i] != wantReverse[i] {
			t.Errorf("reverse Ordered()[%d] = %q, want %q (full: %v)", i, gotReverse[i], wantReverse[i], gotReverse)
		}
	}
}

// TestModels_Get verifies lookup.
func TestModels_Get(t *testing.T) {
	var m Models
	if err := json.Unmarshal([]byte(`{"glm-5":{"context":"256k"}}`), &m); err != nil {
		t.Fatal(err)
	}
	mc, ok := m.Get("glm-5")
	if !ok {
		t.Fatal("Get(glm-5) returned ok=false")
	}
	if int(mc.Context) != 256*1024 {
		t.Errorf("Context = %d, want 262144", int(mc.Context))
	}
	if m.Has("nonexistent") {
		t.Error("Has(nonexistent) = true, want false")
	}
}

// TestModels_MarshalPreservesOrder verifies that Marshal preserves order.
// The exact value serialization is delegated to ModelConfig/IntOrHuman;
// here we only check that keys come out in the original order.
func TestModels_MarshalPreservesOrder(t *testing.T) {
	var m Models
	if err := json.Unmarshal([]byte(`{"glm-5":{"context":"256k"},"glm-5.5":{"context":"128k"},"glm-air":{"context":"8k"}}`), &m); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// Verify key order by index of occurrence in output.
	pos := func(s string) int { return indexOfSubstring(string(out), s) }
	if pos(`"glm-5"`) > pos(`"glm-5.5"`) {
		t.Errorf("glm-5 should come before glm-5.5 in output: %s", out)
	}
	if pos(`"glm-5.5"`) > pos(`"glm-air"`) {
		t.Errorf("glm-5.5 should come before glm-air in output: %s", out)
	}
}

func indexOfSubstring(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestModels_ZeroValue verifies the zero value is usable.
func TestModels_ZeroValue(t *testing.T) {
	var m Models
	if m.Len() != 0 {
		t.Errorf("zero Len() = %d, want 0", m.Len())
	}
	if _, ok := m.Get("anything"); ok {
		t.Error("zero Get should return false")
	}
	if m.Has("anything") {
		t.Error("zero Has should return false")
	}
	if len(m.Ordered()) != 0 {
		t.Errorf("zero Ordered() len = %d, want 0", len(m.Ordered()))
	}
}

func TestModels_UnmarshalNonObject(t *testing.T) {
	t.Parallel()
	var m Models
	err := m.UnmarshalJSON([]byte(`[1,2,3]`))
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("UnmarshalJSON non-object error = %v, want 'JSON object'", err)
	}
}

func TestModels_UnmarshalInvalidValue(t *testing.T) {
	t.Parallel()
	var m Models
	err := m.UnmarshalJSON([]byte(`{"m": 123}`))
	if err == nil {
		t.Fatalf("UnmarshalJSON invalid value: got nil error, want type error")
	}
}

func TestModels_MarshalEmpty(t *testing.T) {
	t.Parallel()
	var m Models
	b, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("empty MarshalJSON = %q, want {}", string(b))
	}
}

func TestModels_MarshalRoundTrip(t *testing.T) {
	t.Parallel()
	var m Models
	if err := json.Unmarshal([]byte(`{"a":{"context":8000},"b":{"context":16000}}`), &m); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var m2 Models
	if err := json.Unmarshal(b, &m2); err != nil {
		t.Fatal(err)
	}
	if m2.Len() != 2 {
		t.Fatalf("roundtrip Len = %d, want 2", m2.Len())
	}
	keys := m2.Ordered()
	if keys[0] != "a" || keys[1] != "b" {
		t.Errorf("roundtrip keys = %v, want [a b]", keys)
	}
}

func TestFindClosestMatchRank(t *testing.T) {
	t.Parallel()
	candidates := []string{"gpt-4", "gpt-3.5", "claude-3"}
	model, dist := FindClosestMatchRank("gpt4", candidates)
	if model != "gpt-4" {
		t.Errorf("model = %q, want gpt-4", model)
	}
	if dist < 0 {
		t.Errorf("distance = %d, want >= 0", dist)
	}

	// Empty input or candidates.
	if m, d := FindClosestMatchRank("", candidates); m != "" || d != -1 {
		t.Errorf("empty input: model=%q dist=%d, want empty/-1", m, d)
	}
	if m, d := FindClosestMatchRank("gpt", nil); m != "" || d != -1 {
		t.Errorf("empty candidates: model=%q dist=%d, want empty/-1", m, d)
	}
}

func TestCreateAllProviders_NoFreeProviders(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Providers: []Provider{
			{Name: "test", URL: "https://api.test.com", Keys: []string{"k"}, Models: Models{}},
		},
	}
	cfg.Providers[0].Models.Set("model-1", ModelConfig{Context: IntOrHuman(8000)})

	pm, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	if len(pm) != 1 {
		t.Errorf("ProviderMap len = %d, want 1", len(pm))
	}
}
