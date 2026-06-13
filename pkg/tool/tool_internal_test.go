package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/tool/lcs"
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

// ---------------------------------------------------------------------------
// lcsDiffsToComponents — direct tests (unexported, package-internal)
// ---------------------------------------------------------------------------

func TestLcsDiffsToComponents_Basic(t *testing.T) {
	t.Parallel()
	// One edit: delete lines 0-1, insert lines 0-2
	diffs := []lcs.Diff{{Start: 0, End: 1, ReplStart: 0, ReplEnd: 2}}
	result := lcsDiffsToComponents(diffs, 3, 4)
	// Expected: delete 1, insert 2, then trailing common 2
	if len(result) != 3 {
		t.Fatalf("expected 3 components, got %d: %+v", len(result), result)
	}
	if !result[0].removed || result[0].count != 1 {
		t.Errorf("comp[0]: expected removed=1, got added=%v removed=%v count=%d", result[0].added, result[0].removed, result[0].count)
	}
	if !result[1].added || result[1].count != 2 {
		t.Errorf("comp[1]: expected added=2, got added=%v removed=%v count=%d", result[1].added, result[1].removed, result[1].count)
	}
	if result[2].added || result[2].removed || result[2].count != 2 {
		t.Errorf("comp[2]: expected common=2, got added=%v removed=%v count=%d", result[2].added, result[2].removed, result[2].count)
	}
}

func TestLcsDiffsToComponents_Empty(t *testing.T) {
	t.Parallel()
	result := lcsDiffsToComponents(nil, 3, 3)
	if len(result) != 1 || result[0].count != 3 {
		t.Errorf("no diffs with equal lengths: expected 1 common component of 3, got %+v", result)
	}
}

func TestLcsDiffsToComponents_MultipleEdits(t *testing.T) {
	t.Parallel()
	// Two edits separated by common lines
	diffs := []lcs.Diff{
		{Start: 0, End: 1, ReplStart: 0, ReplEnd: 0}, // delete line 0, insert nothing
		{Start: 3, End: 4, ReplStart: 2, ReplEnd: 3}, // delete line 3, insert 1
	}
	result := lcsDiffsToComponents(diffs, 5, 4)
	// Expected: del 1, common 2, del 1, ins 1, trailing common 1
	if len(result) != 5 {
		t.Fatalf("expected 5 components, got %d: %+v", len(result), result)
	}
	if !result[0].removed || result[0].count != 1 {
		t.Errorf("comp[0]: expected del 1, got %+v", result[0])
	}
	if result[1].added || result[1].removed || result[1].count != 2 {
		t.Errorf("comp[1]: expected common 2, got %+v", result[1])
	}
	if !result[2].removed || result[2].count != 1 {
		t.Errorf("comp[2]: expected del 1, got %+v", result[2])
	}
	if !result[3].added || result[3].count != 1 {
		t.Errorf("comp[3]: expected ins 1, got %+v", result[3])
	}
	if result[4].added || result[4].removed || result[4].count != 1 {
		t.Errorf("comp[4]: expected common 1, got %+v", result[4])
	}
}

func TestAppendDiffComponent_ZeroCount(t *testing.T) {
	t.Parallel()
	var list []diffComponent
	list = appendDiffComponent(list, true, false, 0)
	if len(list) != 0 {
		t.Errorf("count==0 should not append, got %d items", len(list))
	}
}

func TestAppendDiffComponent_MergeSame(t *testing.T) {
	t.Parallel()
	list := []diffComponent{{added: true, removed: false, count: 2}}
	list = appendDiffComponent(list, true, false, 3)
	if len(list) != 1 {
		t.Fatalf("expected merge into 1 component, got %d", len(list))
	}
	if list[0].count != 5 {
		t.Errorf("merged count = %d, want 5", list[0].count)
	}
}

func TestAppendDiffComponent_Different(t *testing.T) {
	t.Parallel()
	list := []diffComponent{{added: true, removed: false, count: 2}}
	list = appendDiffComponent(list, false, true, 1)
	if len(list) != 2 {
		t.Fatalf("expected new component, got %d", len(list))
	}
	if list[1].removed != true {
		t.Error("second component should be removed")
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
