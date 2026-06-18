package config

import (
	"encoding/json"
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
