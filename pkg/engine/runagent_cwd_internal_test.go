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
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/grep"
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

// TestRunAgent_ForkPromptWithCwd_KeepsParentPrompt pins the fork half of the cwd
// override: a fork keeps the parent's prompt bytes while its shell still runs in
// the override dir. TS rebuilds nothing here — AgentTool.tsx:622-626 hands
// forkParentSystemPrompt over unconditionally (the `!worktreeInfo && !cwd` guard
// only covers the non-fork branch) and forkSubagent.ts:54-58 spells out why:
// re-deriving the prompt would break the parent's prompt cache.
//
// The sentinel must stay free of any {{...}} stub: callLLM substitutes
// {{SYSTEM}}/{{SOUL}}/{{MODEL}}/{{MODALITIES}} unconditionally before the text
// reaches the request ({{SYSTEM}} expands to the hot-loaded SYSTEM.md), so a
// placeholder-bearing prompt could never compare byte-equal and a red here would
// be a false "prompt was enhanced" signal.
//
// AgentType "fork" resolves only inside this test binary: pkg/engine never calls
// agent.InitLoader, so GetAgentDefinition falls through to the built-in table's
// fork→General alias. Production fork availability is a separate question.
func TestRunAgent_ForkPromptWithCwd_KeepsParentPrompt(t *testing.T) {
	t.Parallel()

	parentDir := t.TempDir()
	cwd := t.TempDir()
	const forkPrompt = "PREBUILT-FORK-PROMPT-4c1a"
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
		Prompt:          "pwd",
		AgentType:       "fork",
		SystemPrompt:    forkPrompt,
		Cwd:             cwd,
		ParentToolUseID: "call_fork_cwd",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if len(systems) < 1 {
		t.Fatalf("fork requests = %d, want at least 1", len(systems))
	}
	for i, system := range systems {
		if system != forkPrompt {
			t.Errorf("fork request %d system = %q, want the parent's bytes %q verbatim", i, system, forkPrompt)
		}
		if strings.Contains(system, "Working directory: "+cwd) {
			t.Errorf("fork request %d system carries the cwd env line %q; a fork must not re-derive the prompt", i, "Working directory: "+cwd)
		}
	}

	var pwdOut string
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_pwd" {
			pwdOut = evt.ToolResult.DisplayOutput
		}
	}
	if !strings.Contains(pwdOut, realCwd) {
		t.Errorf("fork pwd = %q, want the override dir %q", pwdOut, realCwd)
	}
}

// TestRunAgent_Cwd_FileReadRelativePath drives the Read tool through the cwd
// override: the sub-agent must read marker.txt from the override dir, not from
// the process cwd. Read is the one file tool that had no tctx.WorkingDir consumer
// at all (fileedit/filewrite joined the path long ago), so this is the wiring
// that regressed silently.
//
// The parent's ToolsProvider has to carry "Read" too: RunAgent intersects the
// fresh CreateTools map with the agent-filtered parent tools by NAME
// (engine.go ResolveAgentTools → filtered[name] = subTools[name]), so a parent
// without Read leaves the sub-engine with no Read and the executor answers
// "No such tool available" before Execute ever runs. The instance that actually
// executes is still the sub-engine's own fresh Read (with the sub-engine tctx) —
// the parent's copy only contributes the name.
func TestRunAgent_Cwd_FileReadRelativePath(t *testing.T) {
	t.Parallel()

	parentDir := t.TempDir()
	cwd := t.TempDir()
	const marker = "MARKER-7f3c9d"
	if err := os.WriteFile(filepath.Join(cwd, "marker.txt"), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile marker.txt: %v", err)
	}

	mp := &testProvider{}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_read", Name: "Read"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"file_path":"marker.txt"}`}},
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
			return map[string]tool.Tool{bt.Name(): bt, "Read": fileread.New()}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "read the marker",
		AgentType:       "General",
		Cwd:             cwd,
		ParentToolUseID: "call_parent_fileread",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var out string
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_read" {
			out = evt.ToolResult.DisplayOutput
		}
	}
	if !strings.Contains(out, marker) {
		t.Errorf("sub-agent Read output = %q, want the marker %q from the cwd", out, marker)
	}
	if strings.Contains(out, "file does not exist") {
		t.Errorf("sub-agent Read resolved the relative path outside the cwd: %q", out)
	}
	// Guards against the wiring degrading into a no-op (tool never registered).
	if strings.Contains(out, "No such tool available") {
		t.Errorf("sub-agent had no Read tool; the test never reached Execute: %q", out)
	}
}

// TestRunAgent_Cwd_FileReadTildePath pins the other half of the path expansion:
// the join guard deliberately skips a "~/..." input, so only expandPath can
// resolve it. HOME points at a scratch dir (not the override dir) so the tilde
// target is unambiguous. Not parallel: HOME is process-global.
func TestRunAgent_Cwd_FileReadTildePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "tilde.txt"), []byte("TILDEMARK-2b\n"), 0o644); err != nil {
		t.Fatalf("WriteFile tilde.txt: %v", err)
	}

	parentDir := t.TempDir()
	cwd := t.TempDir()

	mp := &testProvider{}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_tilde", Name: "Read"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"file_path":"~/tilde.txt"}`}},
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
			return map[string]tool.Tool{bt.Name(): bt, "Read": fileread.New()}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "read the tilde file",
		AgentType:       "General",
		Cwd:             cwd,
		ParentToolUseID: "call_parent_tilde",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var out string
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_tilde" {
			out = evt.ToolResult.DisplayOutput
		}
	}
	if !strings.Contains(out, "TILDEMARK-2b") {
		t.Errorf("sub-agent Read of ~/tilde.txt = %q, want the HOME-dir contents", out)
	}
	if strings.Contains(out, "file does not exist") {
		t.Errorf("sub-agent Read left ~/tilde.txt unexpanded: %q", out)
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

// TestRunAgent_Cwd_GrepUsesOverrideDir pins the Grep tool's working-dir consumer:
// with no `path` argument the search falls back to tctx.WorkingDir, so the
// override must reach it. Like Read, the parent's ToolsProvider has to carry
// "Grep" for the name intersection to keep the tool on the sub-engine — the
// instance that runs is still CreateTools' fresh Grep.
//
// The assertions target FILE NAMES, not the content tokens: output_mode defaults
// to files_with_matches, rg therefore runs with -l, and RenderResult_ joins only
// out.Filenames — the file contents never reach the rendered string. Filenames
// work on both branches (rg renders toRelativePath output, the goGrep fallback
// renders JSON with absolute Match.File), which is also why no assertion may
// require the override dir's path prefix.
func TestRunAgent_Cwd_GrepUsesOverrideDir(t *testing.T) {
	t.Parallel()

	parentDir := t.TempDir()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "cwd_marker.txt"), []byte("GREPCWDMARKER-5q\n"), 0o644); err != nil {
		t.Fatalf("WriteFile cwd_marker.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentDir, "parent_marker.txt"), []byte("GREPPARENTMARKER-9x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile parent_marker.txt: %v", err)
	}

	mp := &testProvider{}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_grep", Name: "Grep"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"pattern":"GREP(CWD|PARENT)MARKER-[0-9a-z]+"}`}},
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
			return map[string]tool.Tool{bt.Name(): bt, "Grep": grep.New()}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "find the markers",
		AgentType:       "General",
		Cwd:             cwd,
		ParentToolUseID: "call_parent_grep",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var out string
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil && evt.ToolResult.ToolUseID == "tu_grep" {
			out = evt.ToolResult.DisplayOutput
		}
	}
	// The positive half carries the test: a zero-hit render falls back to the
	// previous tool's display output, which would let the negative half pass
	// vacuously.
	if !strings.Contains(out, "cwd_marker.txt") {
		t.Errorf("sub-agent Grep output = %q, want the override dir's file cwd_marker.txt", out)
	}
	if strings.Contains(out, "parent_marker.txt") {
		t.Errorf("sub-agent Grep output = %q, must not reach the parent dir's file parent_marker.txt", out)
	}
	if strings.Contains(out, "No such tool available") {
		t.Errorf("sub-agent had no Grep tool; the test never reached Execute: %q", out)
	}
}

// TestRunAgent_DeletedOverrideDir_FallsBackToParentOriginal pins the boundary
// from audit row (c): when the override dir no longer exists, the sub-agent's
// shell recovers to the session's ORIGINAL dir — not the parent's live dir, and
// not the process cwd — and the recovery sticks in the sub-engine's session state.
//
// The asymmetry worth recording: TS's Shell.ts:216-238 setCwdState rewrites the
// process-global cwd state (still shadowed by the AsyncLocalStorage override
// inside runWithCwdOverride), while gbot's SetWorkingDir moves the sub-engine's
// own workingDir. What both guarantee, and what this pins, is that the fallback
// never moves the parent engine.
func TestRunAgent_DeletedOverrideDir_FallsBackToParentOriginal(t *testing.T) {
	t.Parallel()

	parentDir := t.TempDir()
	dirB := t.TempDir()
	override := t.TempDir()
	if err := os.RemoveAll(override); err != nil {
		t.Fatalf("RemoveAll override: %v", err)
	}
	realParent := realPath(t, parentDir)
	realDirB := realPath(t, dirB)

	mp := &testProvider{}
	mp.responses = []testResponse{
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: "Bash"}},
			{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{"command":"pwd"}`}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
			{Type: "message_stop"},
		}},
		{events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 20}}},
			{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_2", Name: "Bash"}},
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
	// Make the parent's live dir distinguishable from its original dir, so the
	// fallback target cannot be confused with either.
	eng.setWorkingDir(dirB)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:          "report where you are",
		AgentType:       "General",
		Cwd:             override,
		ParentToolUseID: "call_parent_deleted",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	outs := map[string]string{}
	for _, evt := range ec.Events() {
		if evt.Type == types.EventToolEnd && evt.ToolResult != nil {
			outs[evt.ToolResult.ToolUseID] = evt.ToolResult.DisplayOutput
		}
	}
	first := outs["tu_1"]
	if !strings.Contains(first, realParent) {
		t.Errorf("first pwd after the override dir was deleted = %q, want the session original dir %q", first, realParent)
	}
	if strings.Contains(first, realDirB) {
		t.Errorf("first pwd = %q, must not fall back to the parent's live dir %q", first, realDirB)
	}
	if got := outs["tu_2"]; !strings.Contains(got, realParent) {
		t.Errorf("second pwd = %q, want the fallback to persist across calls in the sub-engine", got)
	}
	if got := eng.getWorkingDir(); got != dirB {
		t.Errorf("parent workingDir = %q, want the untouched %q", got, dirB)
	}
}
