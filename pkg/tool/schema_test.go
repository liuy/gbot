package tool_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

func TestSchemaBuilder_Empty(t *testing.T) {
	schema := tool.NewSchemaBuilder().Build()

	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Errorf("expected type=object, got %v", parsed["type"])
	}
}

func TestSchemaBuilder_StringProp(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("name", "The name", true).
		AddStringProp("description", "Optional desc", false).
		Build()

	var parsed struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(parsed.Required) != 1 || parsed.Required[0] != "name" {
		t.Errorf("expected required=[name], got %v", parsed.Required)
	}
	if parsed.Properties["name"].Type != "string" {
		t.Error("name should be string type")
	}
	if parsed.Properties["description"].Type != "string" {
		t.Error("description should be string type")
	}
}

func TestSchemaBuilder_NumberProp(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddNumberProp("count", "Number of items", true).
		Build()

	var parsed struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed.Properties["count"].Type != "number" {
		t.Error("expected number type")
	}
}

func TestSchemaBuilder_BooleanProp(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddBooleanProp("verbose", "Enable verbose output", false).
		Build()

	var parsed struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed.Properties["verbose"].Type != "boolean" {
		t.Error("expected boolean type")
	}
}

func TestSchemaBuilder_StringEnumProp(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringEnumProp("mode", "Mode", []string{"fast", "slow"}, true).
		Build()

	var parsed struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	prop := parsed.Properties["mode"]
	if prop.Type != "string" {
		t.Error("expected string type")
	}
	if len(prop.Enum) != 2 || prop.Enum[0] != "fast" {
		t.Errorf("expected [fast slow], got %v", prop.Enum)
	}
}

func TestSchemaBuilder_ObjectProp(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddObjectProp("config", "Configuration", map[string]any{
			"key": map[string]any{"type": "string"},
		}, false).
		Build()

	var parsed struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed.Properties["config"].Type != "object" {
		t.Error("expected object type for config")
	}
}

func TestSchemaBuilder_ArrayProp(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddArrayProp("items", "List of items", map[string]any{"type": "string"}, true).
		Build()

	var parsed struct {
		Properties map[string]struct {
			Type  string         `json:"type"`
			Items map[string]any `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed.Properties["items"].Type != "array" {
		t.Error("expected array type")
	}
}

func TestValidateInput_Valid(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("name", "Name", true).
		Build()

	input := json.RawMessage(`{"name":"test"}`)
	if err := tool.ValidateInput(schema, input); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateInput_MissingRequired(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("name", "Name", true).
		Build()

	input := json.RawMessage(`{"other":"value"}`)
	err := tool.ValidateInput(schema, input)
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error should mention missing required field, got: %v", err)
	}
}

func TestValidateInput_NoRequired(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("name", "Name", false).
		Build()

	input := json.RawMessage(`{}`)
	if err := tool.ValidateInput(schema, input); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateInput_InvalidSchema(t *testing.T) {
	schema := json.RawMessage(`not json`)
	input := json.RawMessage(`{}`)
	err := tool.ValidateInput(schema, input)
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
	if !strings.Contains(err.Error(), "invalid schema") {
		t.Errorf("error should mention invalid schema, got: %v", err)
	}
}

func TestValidateInput_InvalidInput(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("name", "Name", true).
		Build()

	input := json.RawMessage(`not json`)
	err := tool.ValidateInput(schema, input)
	if err == nil {
		t.Fatal("expected error for invalid input")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error should mention invalid input, got: %v", err)
	}
}

func TestRawSchema(t *testing.T) {
	m := map[string]any{"type": "string"}
	raw := tool.RawSchema(m)

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["type"] != "string" {
		t.Errorf("expected type=string, got %v", parsed["type"])
	}
}

func TestSchemaBuilder_MultipleRequired(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("name", "Name", true).
		AddStringProp("age", "Age", true).
		AddStringProp("city", "City", true).
		Build()

	var parsed struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(parsed.Required) != 3 {
		t.Errorf("expected 3 required fields, got %d", len(parsed.Required))
	}
	if len(parsed.Properties) != 3 {
		t.Errorf("expected 3 properties, got %d", len(parsed.Properties))
	}
}

func TestSchemaBuilder_AllPropTypes(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("s", "string prop", true).
		AddNumberProp("n", "number prop", true).
		AddBooleanProp("b", "bool prop", false).
		AddStringEnumProp("e", "enum prop", []string{"a", "b"}, false).
		AddObjectProp("o", "object prop", map[string]any{"k": map[string]any{"type": "string"}}, false).
		AddArrayProp("a", "array prop", map[string]any{"type": "number"}, false).
		Build()

	var parsed struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Only s and n are required
	if len(parsed.Required) != 2 {
		t.Errorf("expected 2 required, got %d", len(parsed.Required))
	}
	// All 6 properties present
	if len(parsed.Properties) != 6 {
		t.Errorf("expected 6 properties, got %d", len(parsed.Properties))
	}
	for _, tc := range []struct{ name, typ string }{
		{"s", "string"}, {"n", "number"}, {"b", "boolean"},
		{"e", "string"}, {"o", "object"}, {"a", "array"},
	} {
		if parsed.Properties[tc.name].Type != tc.typ {
			t.Errorf("property %q: expected type %q, got %q", tc.name, tc.typ, parsed.Properties[tc.name].Type)
		}
	}
}

func TestSchemaBuilder_Chaining(t *testing.T) {
	// Verify that the builder returns itself for chaining
	b := tool.NewSchemaBuilder()
	if b.AddStringProp("x", "X", true) != b {
		t.Error("AddStringProp should return the same builder")
	}
	if b.AddNumberProp("y", "Y", false) != b {
		t.Error("AddNumberProp should return the same builder")
	}
}

func TestSchemaBuilder_BuildIsValidJSON(t *testing.T) {
	// Build with no properties at all — should still be valid JSON
	schema := tool.NewSchemaBuilder().Build()
	if !json.Valid(schema) {
		t.Errorf("Build() returned invalid JSON: %s", string(schema))
	}
}

func TestRawSchema_NestedMap(t *testing.T) {
	m := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key": map[string]any{"type": "string"},
		},
	}
	raw := tool.RawSchema(m)
	if !json.Valid(raw) {
		t.Fatalf("RawSchema returned invalid JSON: %s", string(raw))
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties should be a map")
	}
	if _, ok := props["key"]; !ok {
		t.Error("expected 'key' property")
	}
}

func TestRawSchema_EmptyMap(t *testing.T) {
	raw := tool.RawSchema(map[string]any{})
	if !json.Valid(raw) {
		t.Fatalf("RawSchema returned invalid JSON: %s", string(raw))
	}
	if string(raw) != "{}" {
		t.Errorf("expected '{}', got %s", string(raw))
	}
}

func TestValidateInput_AllRequiredPresent(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("a", "A", true).
		AddStringProp("b", "B", true).
		Build()

	input := json.RawMessage(`{"a":"1","b":"2"}`)
	if err := tool.ValidateInput(schema, input); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateInput_OneOfManyRequiredMissing(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("a", "A", true).
		AddStringProp("b", "B", true).
		AddStringProp("c", "C", true).
		Build()

	input := json.RawMessage(`{"a":"1","c":"3"}`)
	err := tool.ValidateInput(schema, input)
	if err == nil {
		t.Fatal("expected error for missing 'b'")
	}
	if !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error should mention missing required field, got: %v", err)
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("error should mention field 'b', got: %v", err)
	}
}

func TestValidateInput_EmptyRequired(t *testing.T) {
	// Schema with no required fields — any input should pass
	schema := json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	input := json.RawMessage(`{"anything":"goes"}`)
	if err := tool.ValidateInput(schema, input); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestValidateInput_EmptyRequiredSkipsInputParsing pins the early return for
// schemas without required fields: the input is never parsed, so even
// malformed input JSON is accepted.
func TestValidateInput_EmptyRequiredSkipsInputParsing(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("name", "Name", false).
		Build()
	input := json.RawMessage(`not json at all`)
	if err := tool.ValidateInput(schema, input); err != nil {
		t.Errorf("no required fields means input is not parsed; got error: %v", err)
	}
}

// TestValidateInput_ErrorWrapping verifies both error paths wrap the
// underlying json error so callers can errors.As/Unwrap into it.
func TestValidateInput_ErrorWrapping(t *testing.T) {
	var syntax *json.SyntaxError
	err := tool.ValidateInput(json.RawMessage(`not json`), json.RawMessage(`{}`))
	if !errors.As(err, &syntax) {
		t.Errorf("invalid-schema error should wrap *json.SyntaxError, got: %v", err)
	}
	err = tool.ValidateInput(tool.NewSchemaBuilder().AddStringProp("a", "A", true).Build(), json.RawMessage(`{bad`))
	if !errors.As(err, &syntax) {
		t.Errorf("invalid-input error should wrap *json.SyntaxError, got: %v", err)
	}
}

// TestSchemaBuilder_EmptyHasRequiredAndPropertiesKeys pins the shape of a
// fresh builder: the "required" and "properties" keys must exist (as an empty
// array / object), not be omitted.
func TestSchemaBuilder_EmptyHasRequiredAndPropertiesKeys(t *testing.T) {
	schema := tool.NewSchemaBuilder().Build()

	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	req, ok := parsed["required"]
	if !ok {
		t.Fatalf("fresh schema must carry a \"required\" key, got %s", schema)
	}
	reqList, ok := req.([]any)
	if !ok || len(reqList) != 0 {
		t.Errorf("\"required\" = %#v, want empty array", req)
	}
	props, ok := parsed["properties"]
	if !ok {
		t.Fatalf("fresh schema must carry a \"properties\" key, got %s", schema)
	}
	if propsMap, ok := props.(map[string]any); !ok || len(propsMap) != 0 {
		t.Errorf("\"properties\" = %#v, want empty object", props)
	}
}

// TestSchemaBuilder_PropMetadataPinned asserts each Add*Prop helper emits both
// the type and the description verbatim, and that nested object properties /
// array items survive into the built JSON.
func TestSchemaBuilder_PropMetadataPinned(t *testing.T) {
	schema := tool.NewSchemaBuilder().
		AddStringProp("s", "string desc", false).
		AddNumberProp("n", "number desc", false).
		AddBooleanProp("b", "bool desc", false).
		AddStringEnumProp("e", "enum desc", []string{"x"}, false).
		AddObjectProp("o", "object desc", map[string]any{
			"key": map[string]any{"type": "string"},
		}, false).
		AddArrayProp("a", "array desc", map[string]any{"type": "number"}, false).
		Build()

	var parsed struct {
		Properties map[string]struct {
			Type        string         `json:"type"`
			Description string         `json:"description"`
			Properties  map[string]any `json:"properties"`
			Items       map[string]any `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for name, wantDesc := range map[string]string{
		"s": "string desc", "n": "number desc", "b": "bool desc",
		"e": "enum desc", "o": "object desc", "a": "array desc",
	} {
		if got := parsed.Properties[name].Description; got != wantDesc {
			t.Errorf("property %q description = %q, want %q", name, got, wantDesc)
		}
	}

	nested, ok := parsed.Properties["o"].Properties["key"].(map[string]any)
	if !ok || nested["type"] != "string" {
		t.Errorf("object prop nested properties lost: %#v", parsed.Properties["o"].Properties)
	}
	if items := parsed.Properties["a"].Items; items["type"] != "number" {
		t.Errorf("array prop items lost: %#v", parsed.Properties["a"].Items)
	}
}
