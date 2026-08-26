package types

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestToolResultData_MetadataRoundTrip pins the SQLite round-trip of the
// rich-data slot: MetadataToJSON must serialize it under the TS-parity
// toolUseResult key (and not short-circuit to "" when it is the only field),
// and SetMetadataFromJSON must restore it as the JSON-decoded map form.
func TestToolResultData_MetadataRoundTrip(t *testing.T) {
	m := Message{
		Role: RoleUser,
		ToolResultData: map[string]any{
			"toolu_1": &struct {
				FilePath string `json:"filePath"`
			}{FilePath: "/tmp/a.go"},
		},
	}

	meta := m.MetadataToJSON()
	if meta == "" {
		t.Fatal("MetadataToJSON returned empty string with only ToolResultData set")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(meta), &raw); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	tr, ok := raw["toolUseResult"]
	if !ok {
		t.Fatalf("metadata = %q, want toolUseResult key", meta)
	}
	var keyMap map[string]json.RawMessage
	if err := json.Unmarshal(tr, &keyMap); err != nil {
		t.Fatalf("toolUseResult is not keyed by tool_use_id: %v", err)
	}
	if string(keyMap["toolu_1"]) != `{"filePath":"/tmp/a.go"}` {
		t.Errorf("toolUseResult[toolu_1] = %s, want the marshalled rich struct", keyMap["toolu_1"])
	}

	var restored Message
	restored.SetMetadataFromJSON(meta)
	want := map[string]any{"toolu_1": map[string]any{"filePath": "/tmp/a.go"}}
	if !reflect.DeepEqual(restored.ToolResultData, want) {
		t.Errorf("restored ToolResultData = %#v, want %#v", restored.ToolResultData, want)
	}
}

// TestToolResultData_MessageJSONTag pins the Message-level JSON tag as "-":
// whole-Message marshalling (e.g. the autocompact path json.Marshal's the
// provider request verbatim) must never leak rich tool payloads to a
// provider. Persistence goes through the metadata channel instead.
func TestToolResultData_MessageJSONTag(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleUser, ToolResultData: map[string]any{"t": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("marshalled message is invalid JSON: %s", b)
	}
	if strings.Contains(string(b), "toolUseResult") {
		t.Errorf("message JSON = %s, want NO toolUseResult key (json:\"-\" tag)", b)
	}
	// Non-tool messages keep their exact prior shape (unchanged by the field).
	bEmpty, err := json.Marshal(Message{Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if string(bEmpty) != `{"role":"user","content":null,"timestamp":"0001-01-01T00:00:00Z"}` {
		t.Errorf("empty-slot message JSON = %s, want exact prior shape", bEmpty)
	}
}
