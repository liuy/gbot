package tool

import (
	"encoding/json"
	"testing"
)

func TestWrapRichToolResult(t *testing.T) {
	raw := WrapRichToolResult(map[string]any{"filePath": "/tmp/a.go"})
	if raw == nil {
		t.Fatal("WrapRichToolResult returned nil for a marshalled struct")
	}
	text, err := UnmarshalSingleBlock(raw)
	if err != nil {
		t.Fatalf("wrapped form is not array-form: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("inner text is not JSON: %v", err)
	}
	if got["filePath"] != "/tmp/a.go" {
		t.Errorf("inner JSON = %v, want the rich struct", got)
	}

	if raw := WrapRichToolResult(make(chan int)); raw != nil {
		t.Errorf("unmarshalable input = %s, want nil so callers fall back to wire content", raw)
	}
}
