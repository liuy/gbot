package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/types"
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
// ToolWithStreaming interface
// ---------------------------------------------------------------------------

func TestToolWithStreaming_Interface(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	tl := BuildTool(ToolDef{
		Name_:        "TestStreaming",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) { return "test", nil },
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error) {
			return &ToolResult{Data: "ok"}, nil
		},
		ExecuteStream_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(ProgressUpdate)) (*ToolResult, error) {
			onProgress(ProgressUpdate{Lines: []string{"streaming"}, TotalLines: 1, TotalBytes: 9})
			return &ToolResult{Data: "streamed"}, nil
		},
	})

	st, ok := tl.(ToolWithStreaming)
	if !ok {
		t.Fatal("tool with ExecuteStream_ should implement ToolWithStreaming")
	}

	result, err := st.ExecuteStream(context.Background(), json.RawMessage(`{}`), nil, func(u ProgressUpdate) {})
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}
	if result.Data != "streamed" {
		t.Errorf("ExecuteStream() data = %v, want streamed", result.Data)
	}
}

func TestToolWithoutStreaming(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	tl := BuildTool(ToolDef{
		Name_:        "TestNonStreaming",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) { return "test", nil },
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error) {
			return &ToolResult{Data: "ok"}, nil
		},
	})

	_, ok := tl.(ToolWithStreaming)
	if ok {
		t.Error("tool without ExecuteStream_ should NOT implement ToolWithStreaming")
	}
}

func TestToolWithStreaming_ProgressCallback(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	var updates []ProgressUpdate

	tl := BuildTool(ToolDef{
		Name_:        "TestProgress",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) { return "test", nil },
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error) {
			return &ToolResult{Data: "ok"}, nil
		},
		ExecuteStream_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(ProgressUpdate)) (*ToolResult, error) {
			onProgress(ProgressUpdate{Lines: []string{"line1"}, TotalLines: 1, TotalBytes: 5})
			onProgress(ProgressUpdate{Lines: []string{"line1", "line2"}, TotalLines: 2, TotalBytes: 11})
			return &ToolResult{Data: "done"}, nil
		},
	})

	st := tl.(ToolWithStreaming)
	_, err := st.ExecuteStream(context.Background(), json.RawMessage(`{}`), nil, func(u ProgressUpdate) {
		updates = append(updates, u)
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	if len(updates) != 2 {
		t.Fatalf("progress updates = %d, want 2", len(updates))
	}
	if updates[0].TotalLines != 1 {
		t.Errorf("first update TotalLines = %d, want 1", updates[0].TotalLines)
	}
	if updates[1].TotalLines != 2 {
		t.Errorf("second update TotalLines = %d, want 2", updates[1].TotalLines)
	}
}

func TestToolWithStreaming_NilProgressCallback(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	tl := BuildTool(ToolDef{
		Name_:        "TestNilProgress",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) { return "test", nil },
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error) {
			return &ToolResult{Data: "ok"}, nil
		},
		ExecuteStream_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(ProgressUpdate)) (*ToolResult, error) {
			return &ToolResult{Data: "done"}, nil
		},
	})

	st := tl.(ToolWithStreaming)
	result, err := st.ExecuteStream(context.Background(), json.RawMessage(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("ExecuteStream() with nil callback error: %v", err)
	}
	if result.Data != "done" {
		t.Errorf("ExecuteStream() data = %v, want done", result.Data)
	}
}

func TestToolWithStreaming_StillImplementsTool(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	tl := BuildTool(ToolDef{
		Name_:        "TestBoth",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) { return "test", nil },
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error) {
			return &ToolResult{Data: "call-result"}, nil
		},
		ExecuteStream_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(ProgressUpdate)) (*ToolResult, error) {
			return &ToolResult{Data: "stream-result"}, nil
		},
	})

	// Should implement base Tool interface
	var _ Tool = tl //nolint:staticcheck // intentional interface assertion

	result, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.Data != "call-result" {
		t.Errorf("Call() data = %v, want call-result", result.Data)
	}

	if tl.Name() != "TestBoth" {
		t.Errorf("Name() = %q, want TestBoth", tl.Name())
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
