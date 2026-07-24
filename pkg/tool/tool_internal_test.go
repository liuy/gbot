package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Build — marshal error paths
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// OnProgress via ToolUseContext
// ---------------------------------------------------------------------------

func TestToolWithOnProgress(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	var receivedUpdates []ProgressUpdate

	tl := BuildTool(ToolDef{
		Name_:        "TestProgress",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) { return "test", nil },
		Call_: func(ctx context.Context, input json.RawMessage, tctx *ToolUseContext) (*ToolResult, error) {
			if tctx != nil && tctx.OnProgress != nil {
				tctx.OnProgress(ProgressUpdate{Lines: []string{"line1"}, TotalLines: 1, TotalBytes: 5})
				tctx.OnProgress(ProgressUpdate{Lines: []string{"line1", "line2"}, TotalLines: 2, TotalBytes: 11})
			}
			return &ToolResult{Data: "done"}, nil
		},
	})

	tctx := &ToolUseContext{
		OnProgress: func(u ProgressUpdate) {
			receivedUpdates = append(receivedUpdates, u)
		},
	}
	result, err := tl.Call(context.Background(), json.RawMessage(`{}`), tctx)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.Data != "done" {
		t.Errorf("Call() data = %v, want done", result.Data)
	}

	if len(receivedUpdates) != 2 {
		t.Fatalf("progress updates = %d, want 2", len(receivedUpdates))
	}
	if receivedUpdates[0].TotalLines != 1 {
		t.Errorf("first update TotalLines = %d, want 1", receivedUpdates[0].TotalLines)
	}
	if receivedUpdates[1].TotalLines != 2 {
		t.Errorf("second update TotalLines = %d, want 2", receivedUpdates[1].TotalLines)
	}
}

func TestToolWithoutOnProgress(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	tl := BuildTool(ToolDef{
		Name_:        "TestNoProgress",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) { return "test", nil },
		Call_: func(ctx context.Context, input json.RawMessage, tctx *ToolUseContext) (*ToolResult, error) {
			return &ToolResult{Data: "ok"}, nil
		},
	})

	// Call without OnProgress — tool should still work fine
	result, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.Data != "ok" {
		t.Errorf("Call() data = %v, want ok", result.Data)
	}
}

func TestProgressUpdate_Fields(t *testing.T) {
	t.Parallel()

	u := ProgressUpdate{
		Lines:      []string{"hello", "world"},
		TotalLines: 2,
		TotalBytes: 11,
	}
	if len(u.Lines) != 2 {
		t.Errorf("Lines = %d, want 2", len(u.Lines))
	}
	if u.TotalLines != 2 {
		t.Errorf("TotalLines = %d, want 2", u.TotalLines)
	}
	if u.TotalBytes != 11 {
		t.Errorf("TotalBytes = %d, want 11", u.TotalBytes)
	}
}

// ---------------------------------------------------------------------------
// UnmarshalSingleBlock / WrapSingleBlock — wire-array helpers
// ---------------------------------------------------------------------------

func TestUnmarshalSingleBlock_Valid(t *testing.T) {
	t.Parallel()
	text, err := UnmarshalSingleBlock(json.RawMessage(`[{"type":"text","text":"hello"}]`))
	if err != nil {
		t.Fatalf("UnmarshalSingleBlock: %v", err)
	}
	if text != "hello" {
		t.Errorf("text = %q, want \"hello\"", text)
	}
}

func TestUnmarshalSingleBlock_MultipleBlocksReturnsFirst(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`)
	text, err := UnmarshalSingleBlock(raw)
	if err != nil {
		t.Fatalf("UnmarshalSingleBlock: %v", err)
	}
	if text != "first" {
		t.Errorf("text = %q, want \"first\"", text)
	}
}

func TestUnmarshalSingleBlock_NotArray(t *testing.T) {
	t.Parallel()
	_, err := UnmarshalSingleBlock(json.RawMessage(`{"foo":"bar"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "array") {
		t.Errorf("error %q must mention array-form", err.Error())
	}
}

func TestUnmarshalSingleBlock_Empty(t *testing.T) {
	t.Parallel()
	_, err := UnmarshalSingleBlock(json.RawMessage(`[]`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no text") {
		t.Errorf("error %q must mention no text block", err.Error())
	}
}

func TestUnmarshalSingleBlock_NoTextBlock(t *testing.T) {
	t.Parallel()
	_, err := UnmarshalSingleBlock(json.RawMessage(`[{"type":"image","source":"x"}]`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no text") {
		t.Errorf("error %q must mention no text block", err.Error())
	}
}

func TestUnmarshalSingleBlock_EmptyRaw(t *testing.T) {
	t.Parallel()
	_, err := UnmarshalSingleBlock(json.RawMessage(``))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "array") {
		t.Errorf("error %q must mention array-form", err.Error())
	}
}

func TestWrapSingleBlock_RoundTrip(t *testing.T) {
	t.Parallel()
	wrapped := WrapSingleBlock("x")
	text, err := UnmarshalSingleBlock(wrapped)
	if err != nil {
		t.Fatalf("UnmarshalSingleBlock: %v", err)
	}
	if text != "x" {
		t.Errorf("text = %q, want \"x\"", text)
	}
}

func TestWrapSingleBlock_Escapes(t *testing.T) {
	t.Parallel()
	wrapped := WrapSingleBlock(`"quoted"`)
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(wrapped, &blocks); err != nil {
		t.Fatalf("WrapSingleBlock produced invalid JSON: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Text != `"quoted"` {
		t.Errorf("expected single block with quoted text, got %+v", blocks)
	}
}
