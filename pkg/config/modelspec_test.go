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
