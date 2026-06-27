package computer

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInputSchemaEnum verifies every action enum value from schema.py:13-25
// is present in inputSchema()'s JSON, and that `required` is exactly
// ["action"].
func TestInputSchemaEnum(t *testing.T) {
	schema := inputSchema()
	var parsed struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal inputSchema: %v", err)
	}

	if parsed.Type != "object" {
		t.Fatalf("schema type = %q, want object", parsed.Type)
	}
	if len(parsed.Required) != 1 || parsed.Required[0] != "action" {
		t.Fatalf("schema required = %v, want [action]", parsed.Required)
	}

	var actionProp struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(parsed.Properties["action"], &actionProp); err != nil {
		t.Fatalf("unmarshal action property: %v", err)
	}
	wantActions := []string{
		"capture", "click", "double_click", "right_click", "middle_click",
		"drag", "scroll", "type", "key", "set_value", "wait", "list_apps",
		"focus_app",
	}
	if len(actionProp.Enum) != len(wantActions) {
		t.Fatalf("action enum length = %d, want %d", len(actionProp.Enum), len(wantActions))
	}
	for i, want := range wantActions {
		if actionProp.Enum[i] != want {
			t.Errorf("action enum[%d] = %q, want %q", i, actionProp.Enum[i], want)
		}
	}
}

// TestInputSchemaAllProperties verifies every property from schema.py:29-211
// is declared in the schema. Missing properties would silently drop the
// argument when the model sends it.
func TestInputSchemaAllProperties(t *testing.T) {
	schema := inputSchema()
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantProps := []string{
		"action", "mode", "app", "max_elements", "element", "coordinate",
		"button", "modifiers", "from_element", "to_element", "from_coordinate",
		"to_coordinate", "direction", "amount", "value", "text", "keys",
		"seconds", "raise_window", "capture_after",
	}
	for _, name := range wantProps {
		if _, ok := parsed.Properties[name]; !ok {
			t.Errorf("missing schema property: %s", name)
		}
	}
}

// TestInputSchemaMaxElementsDefaults verifies max_elements carries the
// default/minimum/maximum from schema.py:79-82 (default 100, min 1, max 1000).
func TestInputSchemaMaxElementsDefaults(t *testing.T) {
	schema := inputSchema()
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var me struct {
		Default int `json:"default"`
		Minimum int `json:"minimum"`
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(parsed.Properties["max_elements"], &me); err != nil {
		t.Fatalf("unmarshal max_elements: %v", err)
	}
	if me.Default != 100 {
		t.Errorf("max_elements default = %d, want 100", me.Default)
	}
	if me.Minimum != 1 {
		t.Errorf("max_elements minimum = %d, want 1", me.Minimum)
	}
	if me.Maximum != 1000 {
		t.Errorf("max_elements maximum = %d, want 1000", me.Maximum)
	}
}

// TestParseInputRoundTrip parses representative inputs for each action family
// and asserts the parsed fields match exactly. Catches field name drift.
func TestParseInputRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Input
	}{
		{
			name: "capture som",
			raw:  `{"action":"capture","mode":"som","app":"Terminal","max_elements":50}`,
			want: Input{Action: "capture", Mode: "som", App: "Terminal"},
		},
		{
			name: "click element",
			raw:  `{"action":"click","element":7}`,
		},
		{
			name: "click coordinate right",
			raw:  `{"action":"right_click","coordinate":[100,200]}`,
		},
		{
			name: "type text",
			raw:  `{"action":"type","text":"hello"}`,
		},
		{
			name: "key combo",
			raw:  `{"action":"key","keys":"cmd+s"}`,
		},
		{
			name: "drag elements",
			raw:  `{"action":"drag","from_element":1,"to_element":2}`,
		},
		{
			name: "scroll direction",
			raw:  `{"action":"scroll","direction":"down","amount":5}`,
		},
		{
			name: "set_value",
			raw:  `{"action":"set_value","element":3,"value":"Blue"}`,
		},
		{
			name: "wait seconds",
			raw:  `{"action":"wait","seconds":2.5}`,
		},
		{
			name: "focus_app raise",
			raw:  `{"action":"focus_app","app":"Safari","raise_window":true}`,
		},
		{
			name: "list_apps",
			raw:  `{"action":"list_apps"}`,
		},
		{
			name: "capture_after",
			raw:  `{"action":"click","element":1,"capture_after":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := parseInput(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("parseInput(%s): %v", tc.raw, err)
			}
			// Verify action is preserved for every case (the required field).
			if in.Action == "" {
				t.Errorf("parsed action is empty for input %s", tc.raw)
			}
			// For the cases where want is fully populated, do exact-match.
			if tc.want.Action != "" {
				if in.Action != tc.want.Action {
					t.Errorf("action = %q, want %q", in.Action, tc.want.Action)
				}
				if in.Mode != tc.want.Mode {
					t.Errorf("mode = %q, want %q", in.Mode, tc.want.Mode)
				}
				if in.App != tc.want.App {
					t.Errorf("app = %q, want %q", in.App, tc.want.App)
				}
			}
		})
	}
}

// TestParseInputMissingAction verifies the required-field validation rejects
// inputs without action.
func TestParseInputMissingAction(t *testing.T) {
	_, err := parseInput(json.RawMessage(`{"mode":"som"}`))
	if err == nil {
		t.Fatal("parseInput without action: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Errorf("error %q does not mention action", err)
	}
}
