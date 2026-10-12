package computer

import (
	"encoding/json"
	"testing"
)

func TestInputSchema_RequiredIsActionOnly(t *testing.T) {
	t.Parallel()
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(inputSchema(), &s); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	if len(s.Required) != 1 {
		t.Fatalf("Required len = %d, want 1", len(s.Required))
	}
	if s.Required[0] != "action" {
		t.Errorf("Required[0] = %q, want action", s.Required[0])
	}
}

func TestInputSchema_ActionEnumHasFifteen(t *testing.T) {
	t.Parallel()
	var s struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema(), &s); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	want := []string{
		"connect", "disconnect", "screen", "screenshot",
		"click", "click_element", "open_menu", "open_menu_element",
		"type", "send_key", "scroll", "zoom", "device_info", "open_app",
		"send_file",
	}
	if len(s.Properties.Action.Enum) != 15 {
		t.Fatalf("action enum len = %d, want 15", len(s.Properties.Action.Enum))
	}
	for i, a := range s.Properties.Action.Enum {
		if a != want[i] {
			t.Errorf("action enum[%d] = %q, want %q", i, a, want[i])
		}
	}
}

func TestInputSchema_CoordinateMinMaxItems(t *testing.T) {
	t.Parallel()
	var s struct {
		Properties struct {
			Coordinate struct {
				MinItems int `json:"minItems"`
				MaxItems int `json:"maxItems"`
			} `json:"coordinate"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema(), &s); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	if s.Properties.Coordinate.MinItems != 2 {
		t.Errorf("coordinate minItems = %d, want 2", s.Properties.Coordinate.MinItems)
	}
	if s.Properties.Coordinate.MaxItems != 2 {
		t.Errorf("coordinate maxItems = %d, want 2", s.Properties.Coordinate.MaxItems)
	}
}

func TestInputSchema_ConnectFieldsRemoved(t *testing.T) {
	t.Parallel()
	var s struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema(), &s); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	for _, field := range []string{"host", "port", "password"} {
		if _, ok := s.Properties[field]; ok {
			t.Errorf("properties still contains %q; connect is local-only", field)
		}
	}
}

func TestInputSchema_ActionDescriptionLocalOnly(t *testing.T) {
	t.Parallel()
	var s struct {
		Properties struct {
			Action struct {
				Description string `json:"description"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema(), &s); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	if !contains(s.Properties.Action.Description, "no parameters") {
		t.Errorf("action description = %q, want it to mention \"no parameters\" for connect", s.Properties.Action.Description)
	}
}

func TestInputSchema_AllPropertiesPresent(t *testing.T) {
	t.Parallel()
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema(), &s); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	want := []string{
		"action", "max_depth",
		"ref", "coordinate", "direction", "text", "key", "scale", "package", "path",
	}
	for _, p := range want {
		if _, ok := s.Properties[p]; !ok {
			t.Errorf("properties missing %q", p)
		}
	}
}
