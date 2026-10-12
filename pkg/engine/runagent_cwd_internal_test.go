package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/project"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/memory/recall"
	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// TestRunAgent_Cwd_OverridesSubAgentWorkingDir pins the Agent cwd override end
// to end: the sub-agent's env block names the override dir AND its shell runs
// there, so a relative path resolves under cwd instead of the parent's dir.
// The two halves are separate wirings — the prompt reads sharedDeps, the tools
// read the sub-engine's live workingDir through the executor's supplier — so a
// fix that only patches one of them still fails here.
func TestRunAgent_Cwd_OverridesSubAgentWorkingDir(t *testing.T) {
	t.Parallel()

	parentDir := t.TempDir()
	cwd := t.TempDir()
	const marker = "MARKER-7f3c9d"
	if err := os.WriteFile(filepath.Join(cwd, "marker.txt"), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile marker.txt: %v", err)
	}
	// pwd reports the symlink-resolved path; the prompt reports the path as passed.
	realCwd := realPath(t, cwd)

	var systems []string
	mp := &testProvider{onStream: func(req *llm.Request) {
		var b strings.Builder
		for _, sb := range req.SystemBlocks {
			b.WriteString(sb.Text)
		}
		systems = append(systems, b.String())
	}}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_pwd", Name: "Bash"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"pwd"}`}},
			{Type: "content_block_stop", Index: 0},
			{Type: "content_block_start", Index: 1, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_cat", Name: "Bash"}},
			{Type: "content_block_delta", Index: 1, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"cat marker.txt"}`}},
			{Type: "content_block_stop", Index: 1},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
			{Type: "message_stop"},
		}},
		{events: subTextEvents("test", "done")},
	}

	deps := SharedDeps{
		WorkingDir: parentDir,
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	bt := bash.New(nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		WorkingDir: parentDir,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "report where you are",
		AgentType:       "General",
		Cwd:             cwd,
		ParentToolUseID: "call_parent_cwd",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if len(systems) < 1 {
		t.Fatalf("sub-engine requests = %d, want at least 1", len(systems))
	}
	for i, system := range systems {
		if !strings.Contains(system, "Working directory: "+cwd) {
			t.Errorf("sub-engine request %d prompt missing the cwd env line %q, got: %.300q", i, "Working directory: "+cwd, system)
		}
	}

	outs := map[string]string{}
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil {
			outs[evt.ToolResult.ToolUseID] = evt.ToolResult.DisplayOutput
		}
	}
	if got := outs["tu_pwd"]; !strings.Contains(got, realCwd) {
		t.Errorf("sub-agent pwd = %q, want it to report the cwd %q", got, realCwd)
	}
	if got := outs["tu_cat"]; !strings.Contains(got, marker) {
		t.Errorf("sub-agent relative-path read = %q, want the marker file %q from the cwd", got, marker)
	}

	// The override is per-sub-agent: the parent engine must not move.
	if got := eng.getWorkingDir(); got != parentDir {
		t.Errorf("parent workingDir = %q, want the untouched %q", got, parentDir)
	}
}

// TestRunAgent_Cwd_OverridesSubAgentToolDeps covers the other half of the
// override: the fresh tool set CreateTools builds. Recall's archive scan is the
// one tool behavior keyed on SharedDeps.WorkingDir, so it is the probe — the
// sub-agent must find the cwd project's record, not the parent's.
// Not parallel: HOME is process-global.
func TestRunAgent_Cwd_OverridesSubAgentToolDeps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	parentDir := t.TempDir()
	cwd := t.TempDir()
	parentRecord := writeArchiveRecord(t, parentDir, "PARENTRECORD-8m2")
	cwdRecord := writeArchiveRecord(t, cwd, "CWDRECORD-3k9")

	store, err := short.NewStore(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("short.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	deps := SharedDeps{
		WorkingDir: parentDir,
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
		ShortStore: store,
	}
	parentRecall := recall.New(store, long.GetMemoryPath(parentDir))
	ec := newEventCollector()
	mp := &testProvider{}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_recall", Name: parentRecall.Name()}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"query":"PARENTRECORD-8m2 CWDRECORD-3k9"}`}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
			{Type: "message_stop"},
		}},
		{events: subTextEvents("test", "done")},
	}
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		WorkingDir: parentDir,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{parentRecall.Name(): parentRecall}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "recall the record",
		AgentType:       "General",
		Cwd:             cwd,
		ParentToolUseID: "call_parent_recall",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var out string
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_recall" {
			out = string(evt.ToolResult.Output)
		}
	}
	if !strings.Contains(out, cwdRecord) {
		t.Errorf("sub-agent recall missing the cwd archive record %q, got: %.400q", cwdRecord, out)
	}
	if strings.Contains(out, parentRecord) {
		t.Errorf("sub-agent recall read the parent archive record %q; the cwd override must reach the sub-agent's tools", parentRecord)
	}
}

// writeArchiveRecord drops a marker file in the working dir's projectspace
// memory/archive/ and returns its path.
func writeArchiveRecord(t *testing.T, workingDir, token string) string {
	t.Helper()
	dir := filepath.Join(project.Dir(workingDir), "memory", "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	path := filepath.Join(dir, "record.md")
	if err := os.WriteFile(path, []byte("closed record for "+token+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

// TestRunAgent_NoCwd_InheritsParentDir is the control: without the override the
// sub-agent keeps the parent's dir, so the assertions above cannot pass vacuously.
func TestRunAgent_NoCwd_InheritsParentDir(t *testing.T) {
	t.Parallel()

	parentDir := t.TempDir()
	realParent := realPath(t, parentDir)

	mp := &testProvider{}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_pwd", Name: "Bash"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"pwd"}`}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
			{Type: "message_stop"},
		}},
		{events: subTextEvents("test", "done")},
	}

	deps := SharedDeps{
		WorkingDir: parentDir,
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	bt := bash.New(nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		WorkingDir: parentDir,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "report where you are",
		AgentType:       "General",
		ParentToolUseID: "call_parent_nocwd",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var pwdOut string
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_pwd" {
			pwdOut = evt.ToolResult.DisplayOutput
		}
	}
	if !strings.Contains(pwdOut, realParent) {
		t.Errorf("sub-agent pwd = %q, want the parent dir %q", pwdOut, realParent)
	}
}
