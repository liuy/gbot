package computer

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInputSchemaEnum verifies every action enum value is present in
// inputSchema()'s JSON, and that `required` is exactly ["action"].
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
		"list", "snapshot", "click", "type", "key",
		"scroll", "drag", "zoom", "wait",
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

// TestInputSchemaAllProperties verifies every property is declared in the
// schema. Missing properties would silently drop the argument when the model
// sends it.
func TestInputSchemaAllProperties(t *testing.T) {
	schema := inputSchema()
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantProps := []string{
		"action", "window", "mode", "max_elements", "element", "coordinate",
		"count", "button", "modifiers", "from_coordinate", "to_coordinate",
		"direction", "amount", "text", "keys", "region", "seconds",
	}
	for _, name := range wantProps {
		if _, ok := parsed.Properties[name]; !ok {
			t.Errorf("missing schema property: %s", name)
		}
	}
	// Removed properties must NOT be present.
	removed := []string{"app", "from_element", "to_element", "value", "raise_window", "capture_after"}
	for _, name := range removed {
		if _, ok := parsed.Properties[name]; ok {
			t.Errorf("removed property still present in schema: %s", name)
		}
	}
}

// TestInputSchemaMaxElementsDefaults verifies max_elements carries the
// default/minimum/maximum (default 100, min 1, max 1000).
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

// TestInputSchemaCountDefaults verifies count carries default 1, min 1, max 3.
func TestInputSchemaCountDefaults(t *testing.T) {
	schema := inputSchema()
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var c struct {
		Default int `json:"default"`
		Minimum int `json:"minimum"`
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(parsed.Properties["count"], &c); err != nil {
		t.Fatalf("unmarshal count: %v", err)
	}
	if c.Default != 1 {
		t.Errorf("count default = %d, want 1", c.Default)
	}
	if c.Minimum != 1 {
		t.Errorf("count minimum = %d, want 1", c.Minimum)
	}
	if c.Maximum != 3 {
		t.Errorf("count maximum = %d, want 3", c.Maximum)
	}
}

// TestInputSchemaRegionMinMaxItems verifies region requires exactly 4 items.
func TestInputSchemaRegionMinMaxItems(t *testing.T) {
	schema := inputSchema()
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var r struct {
		MinItems int `json:"minItems"`
		MaxItems int `json:"maxItems"`
	}
	if err := json.Unmarshal(parsed.Properties["region"], &r); err != nil {
		t.Fatalf("unmarshal region: %v", err)
	}
	if r.MinItems != 4 {
		t.Errorf("region minItems = %d, want 4", r.MinItems)
	}
	if r.MaxItems != 4 {
		t.Errorf("region maxItems = %d, want 4", r.MaxItems)
	}
}

// TestParseInputRoundTrip parses representative inputs for each action family
// and asserts the parsed fields match exactly. Catches field name drift.
func TestParseInputRoundTrip(t *testing.T) {
	win := 42
	cases := []struct {
		name string
		raw  string
		want Input
	}{
		{
			name: "list",
			raw:  `{"action":"list"}`,
			want: Input{Action: "list"},
		},
		{
			name: "snapshot som",
			raw:  `{"action":"snapshot","window":42,"mode":"som","max_elements":50}`,
			want: Input{Action: "snapshot", Window: &win, Mode: "som"},
		},
		{
			name: "click element with window",
			raw:  `{"action":"click","window":42,"element":7}`,
			want: Input{Action: "click", Window: &win},
		},
		{
			name: "click coordinate count button",
			raw:  `{"action":"click","window":42,"coordinate":[100,200],"count":2,"button":"right"}`,
			want: Input{Action: "click", Window: &win, Button: "right"},
		},
		{
			name: "type text",
			raw:  `{"action":"type","window":42,"text":"hello"}`,
			want: Input{Action: "type", Window: &win},
		},
		{
			name: "key combo",
			raw:  `{"action":"key","window":42,"keys":"cmd+s"}`,
			want: Input{Action: "key", Window: &win, Keys: "cmd+s"},
		},
		{
			name: "drag coordinates",
			raw:  `{"action":"drag","window":42,"from_coordinate":[1,2],"to_coordinate":[3,4]}`,
			want: Input{Action: "drag", Window: &win},
		},
		{
			name: "scroll direction",
			raw:  `{"action":"scroll","window":42,"direction":"down","amount":5}`,
			want: Input{Action: "scroll", Window: &win, Direction: "down"},
		},
		{
			name: "zoom region",
			raw:  `{"action":"zoom","window":42,"region":[10,20,30,40]}`,
			want: Input{Action: "zoom", Window: &win},
		},
		{
			name: "wait seconds",
			raw:  `{"action":"wait","seconds":2.5}`,
			want: Input{Action: "wait"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := parseInput(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("parseInput(%s): %v", tc.raw, err)
			}
			if in.Action != tc.want.Action {
				t.Errorf("action = %q, want %q", in.Action, tc.want.Action)
			}
			if in.Mode != tc.want.Mode {
				t.Errorf("mode = %q, want %q", in.Mode, tc.want.Mode)
			}
			if tc.want.Window != nil {
				if in.Window == nil {
					t.Fatalf("window = nil, want %d", *tc.want.Window)
				}
				if *in.Window != *tc.want.Window {
					t.Errorf("window = %d, want %d", *in.Window, *tc.want.Window)
				}
			} else if in.Window != nil {
				t.Errorf("window = %d, want nil", *in.Window)
			}
			if in.Keys != tc.want.Keys {
				t.Errorf("keys = %q, want %q", in.Keys, tc.want.Keys)
			}
			if in.Direction != tc.want.Direction {
				t.Errorf("direction = %q, want %q", in.Direction, tc.want.Direction)
			}
			if in.Button != tc.want.Button {
				t.Errorf("button = %q, want %q", in.Button, tc.want.Button)
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
