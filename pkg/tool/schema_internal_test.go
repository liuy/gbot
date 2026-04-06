package tool

import (
	"encoding/json"
	"testing"
)

func TestBuild_MarshalError(t *testing.T) {
	// Inject an unmarshallable value into the internal schema map to trigger
	// the json.Marshal error path in Build().
	b := NewSchemaBuilder()
	props, _ := b.schema["properties"].(map[string]any)
	// A channel cannot be marshalled to JSON
	props["bad"] = make(chan int)

	result := b.Build()
	// On marshal error, Build returns the fallback `{"type":"object"}`
	if string(result) != `{"type":"object"}` {
		t.Errorf("expected fallback schema on marshal error, got %s", string(result))
	}
}

func TestRawSchema_MarshalError(t *testing.T) {
	// A map containing an unmarshallable value triggers the error path.
	m := map[string]any{
		"bad": func() {},
	}
	result := RawSchema(m)
	// On marshal error, RawSchema returns `{}`
	if string(result) != `{}` {
		t.Errorf("expected fallback {} on marshal error, got %s", string(result))
	}
}

func TestAddProp_UnmarshallableRequired(t *testing.T) {
	// Ensure addProp correctly appends to required when required=true
	// even with a valid property map.
	b := NewSchemaBuilder()
	b = b.addProp("test", map[string]any{"type": "string"}, true)
	schema := b.Build()

	var parsed struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Required) != 1 || parsed.Required[0] != "test" {
		t.Errorf("expected required=[test], got %v", parsed.Required)
	}
	if parsed.Properties["test"].Type != "string" {
		t.Error("expected test property to be string type")
	}
}
