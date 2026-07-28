package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/send"
)

// TestEngine_Query_RoutesSendToolBySource is the integration test for source
// context propagation. It verifies the full chain:
//
//	Query ctx (WithSource) → queryLoop → refreshTools → callLLM → tool_use
//	→ NewStreamingToolExecutor(rootCtx=ctx) → siblingCtx (WithCancelCause
//	preserves values) → tool.Call(siblingCtx) → send.New(eng).Call_ →
//	eng.SendFile(ctx) → SourceFromContext → fakeFileSender
//
// A break anywhere in this chain (e.g. source not reaching tool.Call) leaves
// fakeFileSender.calls at 0 and the test fails. Registry + ToolsProvider +
// SetToolRefs are required so Send appears in e.tools and eng.ToolRefs().Reg
// is non-nil.
func TestEngine_Query_RoutesSendToolBySource(t *testing.T) {
	t.Parallel()

	tmpFile := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(tmpFile, []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	mp := &mockProvider{}
	// Turn 1: model emits a Send tool_use.
	mp.addResponse(toolUseStreamEvents("test-model",
		"send-1", "Send", `{"file_path":"`+tmpFile+`"}`), nil)
	// Turn 2: model ends the conversation with text.
	mp.addResponse(textStreamEvents("test-model", "done."), nil)

	reg := tool.NewRegistry()
	eng := New(&Params{
		Provider:      mp,
		ToolsProvider: reg.ToolMapFn(),
		Model:         "test-model",
	})
	t.Cleanup(eng.Close)
	eng.SetToolRefs(ToolRefs{Reg: reg})

	sender := &fakeFileSender{}
	eng.RegisterFileSender("test", sender)
	reg.MustRegister(send.New(eng))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := eng.QuerySync(WithSource(ctx, "test"), "send the file", "")
	if result.Error != nil {
		t.Fatalf("QuerySync error: %v", result.Error)
	}

	if sender.calls != 1 {
		t.Errorf("fakeFileSender.calls = %d, want 1 (source did not reach tool.Call)", sender.calls)
	}
	if sender.lastPath != tmpFile {
		t.Errorf("fakeFileSender.lastPath = %q, want %q", sender.lastPath, tmpFile)
	}
}

// TestEngine_Query_SendToolNoSourceErrors verifies that when no source is
// injected (e.g. a caller forgot WithSource), the Send tool surfaces a clear
// error to the LLM rather than silently failing. The tool returns an error
// result, the model sees it, and the turn ends normally.
func TestEngine_Query_SendToolNoSourceErrors(t *testing.T) {
	t.Parallel()

	tmpFile := filepath.Join(t.TempDir(), "y.png")
	if err := os.WriteFile(tmpFile, []byte("png"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model",
		"send-2", "Send", `{"file_path":"`+tmpFile+`"}`), nil)
	mp.addResponse(textStreamEvents("test-model", "ok."), nil)

	reg := tool.NewRegistry()
	eng := New(&Params{
		Provider:      mp,
		ToolsProvider: reg.ToolMapFn(),
		Model:         "test-model",
	})
	t.Cleanup(eng.Close)
	eng.SetToolRefs(ToolRefs{Reg: reg})

	sender := &fakeFileSender{}
	// Register under a source the query will NOT carry — bare ctx has source "".
	eng.RegisterFileSender("test", sender)
	reg.MustRegister(send.New(eng))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Bare ctx: no WithSource → SourceFromContext returns "" → no sender.
	result := eng.QuerySync(ctx, "send the file", "")
	if result.Error != nil {
		t.Fatalf("QuerySync error: %v", result.Error)
	}
	if sender.calls != 0 {
		t.Errorf("fakeFileSender.calls = %d, want 0 (no source → no route)", sender.calls)
	}
}
