package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/types"
)

// realPath resolves a temp dir to its physical form — `pwd -P` in the bash
// wrapper reports physical paths, so expectations must match that form
// (same pattern as the bash package's cwd_internal_test.go).
func realPath(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return real
}

// Source: TS Shell.ts keeps a global cwd per session; gbot stores it on the
// Engine (multi-session process, per-engine state). getWorkingDir must return
// the latest value set through setWorkingDir.
func TestEngine_WorkingDirAccessors(t *testing.T) {
	t.Parallel()

	eng := New(&Params{Provider: &testProvider{}, Model: "test", WorkingDir: "/start"})
	t.Cleanup(func() { eng.Close() })

	if got := eng.getWorkingDir(); got != "/start" {
		t.Errorf("getWorkingDir() = %q, want /start", got)
	}
	eng.setWorkingDir("/moved")
	if got := eng.getWorkingDir(); got != "/moved" {
		t.Errorf("getWorkingDir() after set = %q, want /moved", got)
	}
}

// Params.WorkingDir is the session-start fallback for deleted-cwd recovery.
// When unset, construction-time os.Getwd() is the fallback — matching what
// the bash tool itself uses when tctx.WorkingDir is empty.
func TestEngine_OriginalWorkingDirDefaults(t *testing.T) {
	t.Parallel()

	withDir := t.TempDir()
	eng := New(&Params{Provider: &testProvider{}, Model: "test", WorkingDir: withDir})
	t.Cleanup(func() { eng.Close() })
	if eng.originalWorkingDir != withDir {
		t.Errorf("originalWorkingDir = %q, want %q", eng.originalWorkingDir, withDir)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	eng2 := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng2.Close() })
	if eng2.originalWorkingDir != wd {
		t.Errorf("originalWorkingDir = %q, want os.Getwd() %q", eng2.originalWorkingDir, wd)
	}
}

// Full REPL chain: Engine.ExecuteTool → bash `cd` → SetWorkingDir write-back
// → second ExecuteTool spawns in the updated directory. Before cd
// persistence, ExecuteTool never set WorkingDir so every command ran in the
// gbot process cwd.
func TestExecuteTool_BashCdPersistsAcrossCalls(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	realSub := realPath(t, sub)

	eng := New(&Params{
		Provider:   &testProvider{},
		Model:      "test",
		WorkingDir: tmp,
		ToolsProvider: func() map[string]tool.Tool {
			bt := bash.New(nil)
			return map[string]tool.Tool{bt.Name(): bt}
		},
	})
	t.Cleanup(func() { eng.Close() })

	sessionAllowed := map[string]bool{"Bash": true}
	var mu sync.Mutex

	if _, err := eng.ExecuteTool(context.Background(), "Bash",
		json.RawMessage(`{"command":"cd sub"}`), sessionAllowed, &mu); err != nil {
		t.Fatalf("ExecuteTool cd: %v", err)
	}

	if got := eng.getWorkingDir(); got != realSub {
		t.Fatalf("engine workingDir after cd = %q, want %q", got, realSub)
	}

	result, err := eng.ExecuteTool(context.Background(), "Bash",
		json.RawMessage(`{"command":"pwd"}`), sessionAllowed, &mu)
	if err != nil {
		t.Fatalf("ExecuteTool pwd: %v", err)
	}
	if !strings.Contains(result, realSub) {
		t.Errorf("pwd after cd = %q, want to contain %q", result, realSub)
	}
}

// Full streaming-loop e2e: Query turn 1 runs Bash `cd sub` through the real
// executor (baseTctx + supplier), turn 2 runs Bash `pwd`. The pwd result must
// land in sub — proving the supplier refresh and SetWorkingDir write-back
// across turns of the same engine.
func TestQuery_BashCdPersistsAcrossTurns(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	realSub := realPath(t, sub)

	eventCh := make(chan types.QueryEvent, 50)
	dispatcher := &chanDispatcher{ch: eventCh}

	mp := &mockProvider{}
	bt := bash.New(nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:      "test",
		Dispatcher: dispatcher,
		WorkingDir: tmp,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	drainUntilQueryEnd := func() string {
		var lastResult string
		for {
			select {
			case evt := <-eventCh:
				if evt.Type == types.EventToolEnd && evt.ToolResult != nil {
					lastResult = evt.ToolResult.DisplayOutput
				}
				if evt.Type == types.EventQueryEnd {
					return lastResult
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for EventQueryEnd")
				return ""
			}
		}
	}

	mp.addResponse(toolUseStreamEvents("test", "tu_cd", "Bash", `{"command":"cd sub"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	// QuerySync, not Query: the async variant returns before runTurns'
	// deferred funcs read e.messages, so a back-to-back query would race
	// them under -race. Sync returns only after everything is done.
	eng.QuerySync(ctx, "cd sub", "")
	drainUntilQueryEnd()

	if got := eng.getWorkingDir(); got != realSub {
		t.Fatalf("engine workingDir after cd turn = %q, want %q", got, realSub)
	}

	mp.addResponse(toolUseStreamEvents("test", "tu_pwd", "Bash", `{"command":"pwd"}`), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	eng.QuerySync(ctx, "pwd", "")
	pwdOut := drainUntilQueryEnd()

	if !strings.Contains(pwdOut, realSub) {
		t.Errorf("pwd result = %q, want to contain %q", pwdOut, realSub)
	}
}

// The executor's base ToolUseContext is built once per query, but cd can
// change the engine working dir mid-query. buildToolCtx must re-read it via
// the supplier so each tool call sees the current directory.
func TestBuildToolCtx_RefreshesWorkingDirViaSupplier(t *testing.T) {
	t.Parallel()

	base := &tool.ToolUseContext{ToolUseID: "base", WorkingDir: "/stale"}
	exec := NewStreamingToolExecutor(nil, base, nil, context.Background())

	got := exec.buildToolCtx("t1")
	if got.WorkingDir != "/stale" {
		t.Errorf("WorkingDir without supplier = %q, want /stale", got.WorkingDir)
	}

	exec.SetWorkingDirSupplier(func() string { return "/fresh" })
	got2 := exec.buildToolCtx("t2")
	if got2.WorkingDir != "/fresh" {
		t.Errorf("WorkingDir with supplier = %q, want /fresh", got2.WorkingDir)
	}
}

// Two tool_use blocks in ONE assistant response: `cd sub` then `pwd`. Bash
// is serialized, so the second call's buildToolCtx must re-read the engine
// dir through the supplier — the cross-turn test can't catch a stale
// base-tctx snapshot reused within a single response.
func TestQuery_BashCdPersistsWithinSingleResponse(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	realSub := realPath(t, sub)

	eventCh := make(chan types.QueryEvent, 50)
	dispatcher := &chanDispatcher{ch: eventCh}

	mp := &mockProvider{}
	bt := bash.New(nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:      "test",
		Dispatcher: dispatcher,
		WorkingDir: tmp,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	twoTools := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_cd", Name: "Bash"}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"cd sub"}`}},
		{Type: "content_block_stop", Index: 0},
		{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_pwd", Name: "Bash"}},
		{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"pwd"}`}},
		{Type: "content_block_stop", Index: 1},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
		{Type: "message_stop"},
	}
	mp.addResponse(twoTools, nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)
	eng.QuerySync(ctx, "cd sub and report pwd", "")

	var pwdOut string
	for {
		select {
		case evt := <-eventCh:
			if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_pwd" {
				pwdOut = evt.ToolResult.DisplayOutput
			}
			if evt.Type == types.EventQueryEnd {
				goto drained
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for EventQueryEnd")
			return
		}
	}
drained:
	if got := eng.getWorkingDir(); got != realSub {
		t.Errorf("engine workingDir = %q, want %q", got, realSub)
	}
	if !strings.Contains(pwdOut, realSub) {
		t.Errorf("pwd result = %q, want to contain %q", pwdOut, realSub)
	}
}

// NewSubEngine snapshots the parent's live dir at construction; afterwards
// the two engines never sync — a sub-agent's cd must not move the parent.
func TestNewSubEngine_WorkingDirInheritance(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	other := filepath.Join(tmp, "other")
	for _, d := range []string{sub, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
	}
	realSub, realOther := realPath(t, sub), realPath(t, other)

	bt := bash.New(nil)
	tools := func() map[string]tool.Tool {
		return map[string]tool.Tool{bt.Name(): bt}
	}
	eng := New(&Params{
		Provider:      &testProvider{},
		Model:         "test",
		WorkingDir:    tmp,
		ToolsProvider: tools,
	})
	t.Cleanup(func() { eng.Close() })

	sessionAllowed := map[string]bool{"Bash": true}
	var mu sync.Mutex

	if _, err := eng.ExecuteTool(context.Background(), "Bash",
		json.RawMessage(`{"command":"cd sub"}`), sessionAllowed, &mu); err != nil {
		t.Fatalf("parent cd: %v", err)
	}

	subEng := eng.NewSubEngine(SubEngineOptions{Tools: tools()})
	if got := subEng.getWorkingDir(); got != realSub {
		t.Fatalf("sub-engine workingDir = %q, want parent's live dir %q", got, realSub)
	}

	if _, err := subEng.ExecuteTool(context.Background(), "Bash",
		json.RawMessage(`{"command":"cd `+realOther+`"}`), sessionAllowed, &mu); err != nil {
		t.Fatalf("sub-engine cd: %v", err)
	}
	if got := subEng.getWorkingDir(); got != realOther {
		t.Errorf("sub-engine workingDir after cd = %q, want %q", got, realOther)
	}
	if got := eng.getWorkingDir(); got != realSub {
		t.Errorf("parent workingDir after sub-agent cd = %q, want unchanged %q", got, realSub)
	}
}

// TestUserContextFrozenAcrossCWD pins the cache-prefix stability property:
// the injected CLAUDE.md comes from the construction-time dir and survives a
// bash `cd` (setWorkingDir). A per-call re-read flipped the prepend on/off
// with the dir and broke the whole prompt-cache prefix on every flip.
func TestUserContextFrozenAcrossCWD(t *testing.T) {
	t.Parallel()
	dirA, dirB := t.TempDir(), t.TempDir()
	const sentinelA, sentinelB = "CTX-FROZEN-A-7c2d", "CTX-FROZEN-B-19ef"
	if err := os.WriteFile(filepath.Join(dirA, "CLAUDE.md"), []byte("# A\n"+sentinelA), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "CLAUDE.md"), []byte("# B\n"+sentinelB), 0644); err != nil {
		t.Fatal(err)
	}
	var reqTexts []string
	mp := &testProvider{onStream: func(req *llm.Request) {
		var b strings.Builder
		for _, m := range req.Messages {
			for _, cb := range m.Content {
				b.WriteString(cb.Text)
			}
		}
		reqTexts = append(reqTexts, b.String())
	}}
	mp.responses = []testResponse{{events: subTextEvents("t", "ok")}}
	eng := New(&Params{Provider: mp, Model: "test", WorkingDir: dirA})
	t.Cleanup(func() { eng.Close() })

	eng.setWorkingDir(dirB) // a bash `cd` after construction
	if r := eng.QuerySync(context.Background(), "do it", ""); r.Error != nil {
		t.Fatalf("QuerySync: %v", r.Error)
	}
	if len(reqTexts) == 0 {
		t.Fatal("no request captured")
	}
	if got := strings.Count(reqTexts[0], sentinelA); got != 1 {
		t.Errorf("construction dir's CLAUDE.md sentinel count = %d, want 1 (injection must survive cd)", got)
	}
	if got := strings.Count(reqTexts[0], sentinelB); got != 0 {
		t.Errorf("post-cd dir's CLAUDE.md sentinel count = %d, want 0 (must not pick up the new dir)", got)
	}
}

// TestUserContextMapNotMutatedBySubAgent guards the shared-map immutability:
// a sub-engine's omitClaudeMd filtering must copy, never delete from the
// frozen map — otherwise the parent (and sibling sub-agents) silently lose
// their CLAUDE.md injection.
func TestUserContextMapNotMutatedBySubAgent(t *testing.T) {
	t.Parallel()
	dirA := t.TempDir()
	const sentinelA = "CTX-SHARED-A-31ab"
	if err := os.WriteFile(filepath.Join(dirA, "CLAUDE.md"), []byte("# A\n"+sentinelA), 0644); err != nil {
		t.Fatal(err)
	}
	mp := &testProvider{}
	eng := New(&Params{Provider: mp, Model: "test", WorkingDir: dirA})
	t.Cleanup(func() { eng.Close() })

	sub := eng.NewSubEngine(SubEngineOptions{})
	sub.omitClaudeMd = true
	_ = sub.DumpAPIRequest() // omit path runs its filter

	dump := eng.DumpAPIRequest()
	var all strings.Builder
	for _, m := range dump.Messages {
		for _, cb := range m.Content {
			all.WriteString(cb.Text)
		}
	}
	if got := strings.Count(all.String(), sentinelA); got != 1 {
		t.Errorf("parent's CLAUDE.md sentinel after a sub-agent omit = %d, want 1 (shared map was mutated)", got)
	}
}
