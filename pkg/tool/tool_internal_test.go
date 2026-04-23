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
	var _ Tool = tl //nolint // compile-time interface assertion

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

// ---------------------------------------------------------------------------
// lcsDP — direct tests (unexported, package-internal)
// ---------------------------------------------------------------------------

func TestLcsDP_MatchInMiddle(t *testing.T) {
	t.Parallel()
	// BUG: before fix, lcsDP compares direction values (0/1/2) instead of DP scores.
	// This causes backtrack to never follow diagonal matches.
	// Expected: match "c\n" at (oldIdx:1, newIdx:1)
	result := lcsDP([]string{"b\n", "c\n"}, []string{"x\n", "c\n", "y\n"})
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(result), result)
	}
	if result[0].oldIdx != 1 || result[0].newIdx != 1 {
		t.Errorf("expected match at (1,1), got (%d,%d)", result[0].oldIdx, result[0].newIdx)
	}
}

func TestLcsDP_MultipleMatches(t *testing.T) {
	t.Parallel()
	// a=["a","b","c"], b=["x","b","c","y"] → LCS should be [(1,1),(2,2)]
	result := lcsDP([]string{"a\n", "b\n", "c\n"}, []string{"x\n", "b\n", "c\n", "y\n"})
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(result), result)
	}
	if result[0].oldIdx != 1 || result[0].newIdx != 1 {
		t.Errorf("first match at (1,1), got (%d,%d)", result[0].oldIdx, result[0].newIdx)
	}
	if result[1].oldIdx != 2 || result[1].newIdx != 2 {
		t.Errorf("second match at (2,2), got (%d,%d)", result[1].oldIdx, result[1].newIdx)
	}
}

func TestLcsDP_NoMatch(t *testing.T) {
	t.Parallel()
	result := lcsDP([]string{"x\n", "y\n"}, []string{"a\n", "b\n"})
	if len(result) != 0 {
		t.Errorf("expected 0 matches for disjoint sets, got %d: %+v", len(result), result)
	}
}

func TestLcsDP_EmptyInput(t *testing.T) {
	t.Parallel()
	if result := lcsDP(nil, []string{"a\n"}); result != nil {
		t.Errorf("nil a: expected nil, got %v", result)
	}
	if result := lcsDP([]string{"a\n"}, nil); result != nil {
		t.Errorf("nil b: expected nil, got %v", result)
	}
	if result := lcsDP(nil, nil); result != nil {
		t.Errorf("both nil: expected nil, got %v", result)
	}
}

func TestLcsDP_Identical(t *testing.T) {
	t.Parallel()
	result := lcsDP([]string{"a\n", "b\n", "c\n"}, []string{"a\n", "b\n", "c\n"})
	if len(result) != 3 {
		t.Fatalf("expected 3 matches for identical, got %d", len(result))
	}
	for i, e := range result {
		if e.oldIdx != i || e.newIdx != i {
			t.Errorf("match %d: expected (%d,%d), got (%d,%d)", i, i, i, e.oldIdx, e.newIdx)
		}
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

// ---------------------------------------------------------------------------
// lineDiff — direct tests (unexported, package-internal)
// ---------------------------------------------------------------------------

func TestLineDiff_EmptyOld(t *testing.T) {
	t.Parallel()
	result := lineDiff(nil, []string{"a\n", "b\n"})
	if len(result) != 1 {
		t.Fatalf("expected 1 component, got %d", len(result))
	}
	if !result[0].added || result[0].count != 2 {
		t.Errorf("expected added count=2, got %+v", result[0])
	}
}

func TestLineDiff_EmptyNew(t *testing.T) {
	t.Parallel()
	result := lineDiff([]string{"a\n", "b\n"}, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 component, got %d", len(result))
	}
	if !result[0].removed || result[0].count != 2 {
		t.Errorf("expected removed count=2, got %+v", result[0])
	}
}

func TestLineDiff_BothEmpty(t *testing.T) {
	t.Parallel()
	result := lineDiff(nil, nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestLineDiff_LCSWithDeletionsBetween(t *testing.T) {
	t.Parallel()
	// oldMid=["x","b","y"], newMid=["b"] → LCS=[(1,0)]
	// First LCS entry at oldIdx=1, so deletions before it.
	// But commonCount starts at 0, so the commonCount>0 guard in deletion loop
	// won't fire on the first entry. We need TWO LCS entries with deletions between.
	// old=["a","x","b","y","c"], new=["a","b","c"]
	// Prefix strips "a". Suffix strips "c". oldMid=["x","b","y"], newMid=["b"].
	// LCS=[(1,0)] — only one match. After match, commonCount=1.
	// Remaining deletions: oldPos=2, len(oldMid)=3, delete "y" → flushes commonCount.
	//
	// For the commonCount>0 in deletion loop (line 172), need:
	// LCS entry AFTER another match, with deletions between them.
	// old=["a","x","b","y","c"], new=["a","b","z","c"]
	// Prefix "a", Suffix "c". oldMid=["x","b","y"], newMid=["b","z"].
	// LCS=[(1,0)] — only "b" matches.
	// Entry (1,0): oldPos=0→1 (del x), newPos=0 (no ins), commonCount=1, oldPos=2, newPos=1.
	// Remaining dels: oldPos=2, len=3, del "y" → commonCount>0 fires at line 196.
	// That's the "remaining deletions" commonCount guard, not the LCS loop one.
	//
	// For line 172 (commonCount>0 in LCS deletion loop), need 2+ LCS entries:
	// old=["a","b","x","c","y","d"], new=["a","b","c","d"]
	// Prefix "a","b". Suffix "d". oldMid=["x","c","y"], newMid=["c"].
	// LCS=[(1,0)]. Only 1 match.
	//
	// Need oldMid and newMid both having multiple matches with changes between:
	// oldMid=["x","b","y","c","z"], newMid=["b","c"]
	// LCS=[(1,0),(3,1)]
	// Entry (1,0): oldPos=0→1 (del x), newPos=0 (no ins), commonCount=1, oldPos=2, newPos=1.
	// Entry (3,1): oldPos=2, entry.oldIdx=3. oldPos<3 → delete "y".
	//   commonCount=1>0 → line 172 fires! Flush commonCount, then delete.
	result := lineDiff(
		[]string{"a\n", "x\n", "b\n", "y\n", "c\n", "z\n"},
		[]string{"a\n", "b\n", "c\n"},
	)
	// Verify structure: prefix(equal) del(x) equal(b) del(y) equal(c) del(z)
	hasCtx := false
	hasDel := false
	for _, c := range result {
		if !c.added && !c.removed {
			hasCtx = true
		}
		if c.removed {
			hasDel = true
		}
	}
	if !hasCtx {
		t.Error("expected context components")
	}
	if !hasDel {
		t.Error("expected deletion components")
	}
}

func TestLineDiff_LCSWithInsertionsBetween(t *testing.T) {
	t.Parallel()
	// For line 181 (commonCount>0 in LCS insertion loop):
	// Need 2+ LCS entries with insertions between them.
	// oldMid=["b","c"], newMid=["x","b","y","c","z"]
	// LCS=[(0,1),(1,3)]
	// Entry (0,1): oldPos=0 (no del), newPos=0→1 (ins x), commonCount=1, oldPos=1, newPos=2.
	// Entry (1,3): oldPos=1 (no del), newPos=2, entry.newIdx=3. newPos<3 → ins "y".
	//   commonCount=1>0 → line 181 fires!
	result := lineDiff(
		[]string{"a\n", "b\n", "c\n"},
		[]string{"a\n", "x\n", "b\n", "y\n", "c\n", "z\n"},
	)
	hasCtx := false
	hasIns := false
	for _, c := range result {
		if !c.added && !c.removed {
			hasCtx = true
		}
		if c.added {
			hasIns = true
		}
	}
	if !hasCtx {
		t.Error("expected context components")
	}
	if !hasIns {
		t.Error("expected insertion components")
	}
}

func TestLineDiff_CommonCountAtEnd(t *testing.T) {
	t.Parallel()
	// For line 212 (commonCount>0 at end of LCS processing):
	// Need LCS matches at the end of oldMid/newMid with no remaining deletions/insertions.
	// old=["p","a","x","b","q"], new=["p","a","y","b","q"]
	// Prefix strips "p","a". Suffix strips "q". oldMid=["x","b"], newMid=["y","b"].
	// LCS=[(1,1)] (match "b"). After entry: oldPos=0→1 (del x), newPos=0→1 (ins y),
	// commonCount=1, oldPos=2, newPos=2. No remaining dels/ins. commonCount=1>0 → line 212 fires.
	result := lineDiff(
		[]string{"p\n", "a\n", "x\n", "b\n", "q\n"},
		[]string{"p\n", "a\n", "y\n", "b\n", "q\n"},
	)
	// Should have: prefix(2) del(1) ins(1) equal(1) suffix(1)
	totalCtx := 0
	for _, c := range result {
		if !c.added && !c.removed {
			totalCtx += c.count
		}
	}
	if totalCtx < 3 {
		t.Errorf("expected at least 3 context lines (prefix+match+suffix), got %d", totalCtx)
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
