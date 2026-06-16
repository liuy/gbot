package lsptool

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

// emptyHandler responds to everything with empty results, but lets
// documentSymbol fall through to defaultDocumentSymbols (on-disk scan).
func emptyHandler() fakeHandler {
	return func(method string, _ json.RawMessage) (any, bool) {
		switch method {
		case "textDocument/definition",
			"textDocument/typeDefinition",
			"textDocument/implementation",
			"textDocument/references",
			"textDocument/hover":
			return []any{}, true
		case "textDocument/prepareCallHierarchy":
			return []any{}, true
		case "workspace/symbol":
			return []any{}, true
		case "textDocument/codeAction":
			return []any{}, true
		}
		return nil, false
	}
}

func TestSymbolKindName_All(t *testing.T) {
	tests := []struct {
		kind int
		want string
	}{
		{1, "file"}, {2, "module"}, {3, "namespace"}, {4, "package"},
		{5, "class"}, {6, "method"}, {7, "property"}, {8, "field"},
		{9, "constructor"}, {10, "enum"}, {11, "interface"}, {12, "func"},
		{13, "var"}, {14, "const"}, {15, "string"}, {16, "number"},
		{17, "bool"}, {18, "array"}, {19, "object"}, {20, "key"},
		{21, "null"}, {22, "enum member"}, {23, "struct"}, {24, "event"},
		{25, "operator"}, {26, "type param"}, {0, "symbol"}, {999, "symbol"},
	}
	for _, tt := range tests {
		if got := symbolKindName(lsp.SymbolKind(tt.kind)); got != tt.want {
			t.Errorf("symbolKindName(%d) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestLocationFromMap_Valid(t *testing.T) {
	m := map[string]json.RawMessage{
		"uri":   json.RawMessage(`"file:///test.go"`),
		"range": json.RawMessage(`{"start":{"line":3,"character":5},"end":{"line":3,"character":10}}`),
	}
	loc, ok := locationFromMap(m)
	if !ok {
		t.Fatal("locationFromMap: expected ok")
	}
	if loc.URI != "file:///test.go" {
		t.Errorf("URI = %q", loc.URI)
	}
	if loc.Range.Start.Line != 3 || loc.Range.Start.Character != 5 {
		t.Errorf("Start = %+v", loc.Range.Start)
	}
}

func TestLocationFromMap_MissingURI(t *testing.T) {
	m := map[string]json.RawMessage{
		"range": json.RawMessage(`{"start":{"line":0,"character":0}}`),
	}
	_, ok := locationFromMap(m)
	if ok {
		t.Fatal("expected !ok for missing uri")
	}
}

func TestLocationFromMap_MissingRange(t *testing.T) {
	m := map[string]json.RawMessage{
		"uri": json.RawMessage(`"file:///test.go"`),
	}
	loc, ok := locationFromMap(m)
	if !ok {
		t.Fatal("expected ok — range is optional")
	}
	if loc.URI != "file:///test.go" {
		t.Errorf("URI = %q", loc.URI)
	}
}

func TestLocationFromMap_LocationLink(t *testing.T) {
	m := map[string]json.RawMessage{
		"targetUri":            json.RawMessage(`"file:///link.go"`),
		"targetSelectionRange": json.RawMessage(`{"start":{"line":7,"character":3},"end":{"line":7,"character":8}}`),
	}
	loc, ok := locationFromMap(m)
	if !ok {
		t.Fatal("expected ok for LocationLink")
	}
	if loc.URI != "file:///link.go" {
		t.Errorf("URI = %q, want file:///link.go", loc.URI)
	}
	if loc.Range.Start.Line != 7 {
		t.Errorf("Line = %d, want 7", loc.Range.Start.Line)
	}
}

func TestLocationFromMap_NoURI(t *testing.T) {
	m := map[string]json.RawMessage{}
	_, ok := locationFromMap(m)
	if ok {
		t.Fatal("expected !ok for empty map")
	}
}

func TestFormatJSON_Invalid(t *testing.T) {
	got := formatJSON(json.RawMessage(`not json`))
	if got != "not json" {
		t.Errorf("formatJSON(invalid) = %q, want passthrough", got)
	}
}
