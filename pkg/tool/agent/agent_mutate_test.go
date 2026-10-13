package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Description — prompt fallback is byte-truncated at exactly 80
// ---------------------------------------------------------------------------

func TestAgentToolDescription_PromptFallbackBoundaries(t *testing.T) {
	t.Parallel()
	at := New()
	for _, tc := range []struct {
		in   string
		want string
	}{
		{`{"description":"search code","prompt":"prompt is ignored"}`, "search code"},
		{`{"description":"","prompt":"short prompt"}`, "short prompt"},
		{`{"description":"","prompt":"` + strings.Repeat("q", 79) + `"}`, strings.Repeat("q", 79)},
		{`{"description":"","prompt":"` + strings.Repeat("q", 80) + `"}`, strings.Repeat("q", 80)},
		{`{"description":"","prompt":"` + strings.Repeat("q", 81) + `"}`, strings.Repeat("q", 80) + "..."},
		{`{"description":"","prompt":""}`, "Execute a sub-agent task"},
		{`{"description":"unterminated`, "Execute a sub-agent task"},
	} {
		got, err := at.Description(json.RawMessage(tc.in))
		if err != nil {
			t.Fatalf("Description(%s) returned error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("Description(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Call — error wrap shapes
// ---------------------------------------------------------------------------

func TestCall_MalformedInputWrapsJSONSyntaxError(t *testing.T) {
	t.Parallel()
	at := New()
	at.SetEngine(&mockSubEngine{})

	_, err := at.Call(context.Background(), json.RawMessage(`{"description":`), nil)
	if err == nil {
		t.Fatal("Call accepted malformed JSON input")
	}
	want := "invalid Agent input: unexpected end of JSON input"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error %v does not wrap *json.SyntaxError", err)
	}
}

func TestCall_SubagentErrorWrapShape(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("out of memory")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return nil, fmt.Errorf("engine crashed: %w", sentinel)
	}})

	_, err := at.Call(context.Background(), json.RawMessage(`{"description":"t","prompt":"p"}`), nil)
	if err == nil {
		t.Fatal("Call should propagate the engine error")
	}
	want := "sub-agent execution failed: engine crashed: out of memory"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap the sentinel engine error", err)
	}
}

func TestCallFork_SyncErrorWrapShape(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		return nil, sentinel
	}})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	_, err := at.Call(context.Background(), json.RawMessage(`{"description":"d","prompt":"p","fork":true}`), nil)
	if err == nil {
		t.Fatal("sync fork Call should propagate the engine error")
	}
	want := "fork agent execution failed: boom"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap the sentinel engine error", err)
	}
}

// ---------------------------------------------------------------------------
// callFork — model resolution and trigger-assistant construction
// ---------------------------------------------------------------------------

func TestCallFork_ModelResolution(t *testing.T) {
	t.Parallel()
	var captured AgentOpts
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	for _, tc := range []struct {
		in   string
		want string
	}{
		{`{"description":"d","prompt":"p","fork":true}`, ""},
		{`{"description":"d","prompt":"p","fork":true,"model":"inherit"}`, ""},
		{`{"description":"d","prompt":"p","fork":true,"model":"INHERIT"}`, "INHERIT"},
		{`{"description":"d","prompt":"p","fork":true,"model":"opus"}`, "opus"},
	} {
		_, err := at.Call(context.Background(), json.RawMessage(tc.in), nil)
		if err != nil {
			t.Fatalf("Call(%s) returned error: %v", tc.in, err)
		}
		if captured.Model != tc.want {
			t.Errorf("Call(%s): Model = %q, want %q", tc.in, captured.Model, tc.want)
		}
	}
}

func TestCallFork_AssistantContentBuildsTriggerPair(t *testing.T) {
	t.Parallel()
	var captured AgentOpts
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	tctx := &tool.ToolUseContext{
		ToolUseID:        "call_9",
		Messages:         []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("prior turn")}}},
		AssistantContent: []types.ContentBlock{types.NewToolUseBlock("tu_9", "Agent", json.RawMessage(`{}`))},
	}

	if _, err := at.Call(context.Background(), json.RawMessage(`{"description":"d","prompt":"find it","fork":true}`), tctx); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	fm := captured.ForkMessages
	if len(fm) != 3 {
		t.Fatalf("ForkMessages = %d, want 3 (history + cloned assistant + tool_result/directive user)", len(fm))
	}
	if fm[1].Role != types.RoleAssistant || len(fm[1].Content) != 1 || fm[1].Content[0].ID != "tu_9" {
		t.Errorf("ForkMessages[1] = %+v, want cloned assistant holding tool_use tu_9", fm[1])
	}
	if fm[2].Role != types.RoleUser || len(fm[2].Content) != 2 {
		t.Fatalf("ForkMessages[2] = %+v, want user message with 2 blocks", fm[2])
	}
	if fm[2].Content[0].Type != types.ContentTypeToolResult || fm[2].Content[0].ToolUseID != "tu_9" {
		t.Errorf("ForkMessages[2].Content[0] = %+v, want tool_result for tu_9", fm[2].Content[0])
	}
	if !strings.Contains(fm[2].Content[1].Text, "Your directive: find it") {
		t.Errorf("ForkMessages[2].Content[1] = %q, want the fork directive for the prompt", fm[2].Content[1].Text)
	}
}

func TestCallFork_EmptyAssistantContentUsesHistoryOnly(t *testing.T) {
	t.Parallel()
	var captured AgentOpts
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	tctx := &tool.ToolUseContext{
		ToolUseID:        "call_9",
		Messages:         []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("prior turn")}}},
		AssistantContent: []types.ContentBlock{},
	}

	if _, err := at.Call(context.Background(), json.RawMessage(`{"description":"d","prompt":"find it","fork":true}`), tctx); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	fm := captured.ForkMessages
	if len(fm) != 2 {
		t.Fatalf("ForkMessages = %d, want 2 (history + directive user, no cloned assistant)", len(fm))
	}
	if fm[0].Role != types.RoleUser || fm[1].Role != types.RoleUser {
		t.Errorf("ForkMessages roles = %v/%v, want user/user", fm[0].Role, fm[1].Role)
	}
	if len(fm[1].Content) != 1 || fm[1].Content[0].Type != types.ContentTypeText {
		t.Errorf("ForkMessages[1].Content = %+v, want a single text block", fm[1].Content)
	}
}

// ---------------------------------------------------------------------------
// BackgroundAgentJobAdapter — not-found error shape
// ---------------------------------------------------------------------------

func TestJobAdapter_WaitUnknownIDErrorShape(t *testing.T) {
	t.Parallel()
	adapter := NewBackgroundAgentJobAdapter(NewBackgroundAgentRegistry())

	code, err := adapter.Wait("agent-404")
	if code != -1 {
		t.Errorf("Wait exit code = %d, want -1", code)
	}
	if err == nil {
		t.Fatal("Wait for an unknown ID should return an error")
	}
	want := `wait "agent-404": job not found`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, job.ErrNotFound) {
		t.Errorf("error %v does not wrap job.ErrNotFound", err)
	}
}

func TestJobAdapter_NilRegistryErrorShapes(t *testing.T) {
	t.Parallel()
	adapter := NewBackgroundAgentJobAdapter(nil)

	code, err := adapter.Wait("agent-1")
	if code != -1 {
		t.Errorf("Wait exit code = %d, want -1", code)
	}
	if err == nil {
		t.Fatal("Wait on a nil registry should return an error")
	}
	if want := `wait "agent-1": job not found`; err.Error() != want {
		t.Errorf("Wait error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Wait error %v does not wrap job.ErrNotFound", err)
	}

	err = adapter.Kill("agent-1")
	if err == nil {
		t.Fatal("Kill on a nil registry should return an error")
	}
	if want := `kill "agent-1": job not found`; err.Error() != want {
		t.Errorf("Kill error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Kill error %v does not wrap job.ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// BackgroundAgentRegistry.CleanupCompleted — terminal-status disjunction
// ---------------------------------------------------------------------------

func TestCleanupCompleted_RemovesEveryTerminalStatusAndKeepsRunning(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()

	release := make(chan struct{})
	running, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-release
			return &types.SubQueryResult{Content: "late"}, nil
		}, nil, "still running", "call_r", "agent")
	if err != nil {
		t.Fatalf("Spawn running: %v", err)
	}

	completed, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "c"}, nil
		}, nil, "completed", "call_c", "agent")
	if err != nil {
		t.Fatalf("Spawn completed: %v", err)
	}
	reg.Wait(completed.ID)

	failed, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return nil, errors.New("engine died")
		}, nil, "failed", "call_f", "agent")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	reg.Wait(failed.ID)

	cancelled, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-ctx.Done()
			return &types.SubQueryResult{Content: "partial"}, ctx.Err()
		}, nil, "cancelled", "call_x", "agent")
	if err != nil {
		t.Fatalf("Spawn cancelled: %v", err)
	}
	if !reg.Cancel(cancelled.ID) {
		t.Fatal("Cancel returned false for a live agent")
	}
	reg.Wait(cancelled.ID)

	for id, want := range map[string]BackgroundAgentStatus{
		running.ID:   AgentRunning,
		completed.ID: AgentCompleted,
		failed.ID:    AgentFailed,
		cancelled.ID: AgentCancelled,
	} {
		state, ok := reg.Get(id)
		if !ok {
			t.Fatalf("Get(%s) returned false before cleanup", id)
		}
		if state.Status != want {
			t.Errorf("status of %s = %q, want %q", id, state.Status, want)
		}
	}
	if got := len(reg.List()); got != 4 {
		t.Fatalf("registry holds %d agents before cleanup, want 4", got)
	}

	reg.CleanupCompleted()

	list := reg.List()
	if len(list) != 1 {
		t.Errorf("registry holds %d agents after cleanup, want 1 (the running one)", len(list))
	} else if list[0].ID != running.ID {
		t.Errorf("surviving agent = %q, want %q", list[0].ID, running.ID)
	}
	for _, id := range []string{completed.ID, failed.ID, cancelled.ID} {
		if _, ok := reg.Get(id); ok {
			t.Errorf("terminal agent %s survived CleanupCompleted", id)
		}
	}

	close(release)
	reg.Wait(running.ID)
}

// ---------------------------------------------------------------------------
// FilterIncompleteToolCalls / BuildForkMessages — block-type and ID guards
// ---------------------------------------------------------------------------

func TestFilterIncompleteToolCalls_IDLessToolUseIsNotIncomplete(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "", Name: "Read"},
		}},
	}
	filtered := FilterIncompleteToolCalls(messages)
	if len(filtered) != 1 {
		t.Errorf("kept %d messages, want 1 — a tool_use without an ID is not an incomplete call", len(filtered))
	}
}

func TestFilterIncompleteToolCalls_NonToolUseBlocksNeverMarkIncomplete(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ID: "r1", ToolUseID: "r1"},
			{Type: types.ContentTypeText, Text: "plain answer"},
		}},
	}
	filtered := FilterIncompleteToolCalls(messages)
	if len(filtered) != 1 {
		t.Errorf("kept %d messages, want 1 — only tool_use blocks can mark a message incomplete", len(filtered))
	}
}

func TestFilterIncompleteToolCalls_TextBlockWithToolUseIDIsNotAResult(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "id1", ToolUseID: "id1"},
		}},
	}
	filtered := FilterIncompleteToolCalls(messages)
	if len(filtered) != 1 {
		t.Fatalf("kept %d messages, want 1 — the assistant's tool_use has no tool_result", len(filtered))
	}
	if filtered[0].Role != types.RoleUser {
		t.Errorf("surviving message role = %q, want %q", filtered[0].Role, types.RoleUser)
	}
}

func TestBuildForkMessages_PlaceholdersOnlyForIDedToolUse(t *testing.T) {
	t.Parallel()
	trigger := &types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "", Name: "Agent"},
			{Type: types.ContentTypeToolResult, ID: "r1", ToolUseID: "r1"},
		},
	}

	msgs := BuildForkMessages(trigger, nil, "do the thing")
	if len(msgs) != 2 {
		t.Fatalf("BuildForkMessages returned %d messages, want 2 (cloned assistant + directive user)", len(msgs))
	}
	if msgs[0].Role != types.RoleAssistant || len(msgs[0].Content) != 2 {
		t.Errorf("msgs[0] = %+v, want the cloned assistant with both original blocks", msgs[0])
	}
	if msgs[1].Role != types.RoleUser {
		t.Fatalf("msgs[1].Role = %q, want %q", msgs[1].Role, types.RoleUser)
	}
	if len(msgs[1].Content) != 1 {
		t.Fatalf("directive user message has %d blocks, want 1 (no placeholder for an ID-less tool_use)", len(msgs[1].Content))
	}
	if msgs[1].Content[0].Type != types.ContentTypeText {
		t.Errorf("directive block type = %q, want %q", msgs[1].Content[0].Type, types.ContentTypeText)
	}
	if !strings.Contains(msgs[1].Content[0].Text, "Your directive: do the thing") {
		t.Errorf("directive text = %q, want it to end with the prompt", msgs[1].Content[0].Text)
	}
}

// ---------------------------------------------------------------------------
// parseAgentFromMarkdown — required-field guards
// ---------------------------------------------------------------------------

func TestParseAgentFromMarkdown_RequiredFieldGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		fm   map[string]any
	}{
		{"name absent", map[string]any{"description": "does things"}},
		{"name empty", map[string]any{"name": "", "description": "does things"}},
		{"name not a string", map[string]any{"name": 42, "description": "does things"}},
		{"description absent", map[string]any{"name": "Worker"}},
		{"description empty", map[string]any{"name": "Worker", "description": ""}},
		{"description not a string", map[string]any{"name": "Worker", "description": 7}},
	} {
		got := parseAgentFromMarkdown("worker.md", "", tc.fm, "body", types.AgentSourceProjectSettings)
		if got != nil {
			t.Errorf("%s: parseAgentFromMarkdown = %+v, want nil", tc.name, got)
		}
	}

	ok := parseAgentFromMarkdown("worker.md", "", map[string]any{"name": "Worker", "description": "does things"}, "body", types.AgentSourceProjectSettings)
	if ok == nil {
		t.Fatal("a frontmatter with name and description must parse")
	}
	if ok.AgentType != "Worker" || ok.WhenToUse != "does things" {
		t.Errorf("parsed def = (%q, %q), want (\"Worker\", \"does things\")", ok.AgentType, ok.WhenToUse)
	}
}

// ---------------------------------------------------------------------------
// parseEffortValue — numeric boundaries
// ---------------------------------------------------------------------------

func TestParseEffortValue_NumericBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   any
		want string
	}{
		{"0", ""},
		{"-3", ""},
		{"7", "7"},
		{"007", "007"},
		{" 7", ""},
		{"7.5", ""},
		{"", ""},
		{int(0), ""},
		{int(-1), ""},
		{int(5), "5"},
		{float64(0), ""},
		{float64(-0.5), ""},
		{float64(0.5), ""},
		{float64(3), "3"},
		{float64(1e2), "100"},
	} {
		if got := parseEffortValue(tc.in); got != tc.want {
			t.Errorf("parseEffortValue(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// AgentPrompt — General-reference rewrite is gated on allowedAgentTypes
// ---------------------------------------------------------------------------

func TestAgentPrompt_GeneralReferencesFollowAllowedTypes(t *testing.T) {
	prev := globalLoader
	globalLoader = nil
	defer func() { globalLoader = prev }()

	const omittedSentence = `If omitted, the General agent is used.`
	const exampleLine = `  subagent_type: "General",`

	unfiltered := AgentPrompt(nil)
	if got := strings.Count(unfiltered, omittedSentence); got != 1 {
		t.Errorf("unfiltered prompt has %d copies of %q, want 1", got, omittedSentence)
	}
	if got := strings.Count(unfiltered, exampleLine); got != 1 {
		t.Errorf("unfiltered prompt has %d copies of %q, want 1", got, exampleLine)
	}

	withGeneral := AgentPrompt([]string{"Explore", "General"})
	if got := strings.Count(withGeneral, omittedSentence); got != 1 {
		t.Errorf("prompt allowing General has %d copies of %q, want 1", got, omittedSentence)
	}
	if got := strings.Count(withGeneral, exampleLine); got != 1 {
		t.Errorf("prompt allowing General has %d copies of %q, want 1", got, exampleLine)
	}

	exploreOnly := AgentPrompt([]string{"Explore"})
	if strings.Contains(exploreOnly, "General") {
		t.Errorf("prompt filtering out General still mentions it:\n%s", exploreOnly)
	}
	if got := strings.Count(exploreOnly, `  // subagent_type omitted`); got != 1 {
		t.Errorf("filtered prompt has %d copies of the rewritten example line, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// ExtractPartialResult — text is only taken from text blocks
// ---------------------------------------------------------------------------

func TestExtractPartialResult_IgnoresTextOnNonTextBlocks(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("earlier report")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "t1", Name: "Read", Text: "tool input must not leak"},
		}},
	}
	got := ExtractPartialResult(messages)
	if got != "earlier report" {
		t.Errorf("ExtractPartialResult = %q, want %q", got, "earlier report")
	}
}

func TestExtractPartialResult_IgnoresNonAssistantMessages(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("assistant text")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("user echo of the report")}},
	}
	got := ExtractPartialResult(messages)
	if got != "assistant text" {
		t.Errorf("ExtractPartialResult = %q, want %q", got, "assistant text")
	}

	onlyUser := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("user only")}},
	}
	if got := ExtractPartialResult(onlyUser); got != "" {
		t.Errorf("ExtractPartialResult(user only) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Progress helpers — assistant-only guards
// ---------------------------------------------------------------------------

func TestCountToolUses_IgnoresToolUseOutsideAssistant(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolUseBlock("u1", "Read", json.RawMessage(`{}`)),
			types.NewToolResultBlock("a1", json.RawMessage(`"x"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("a1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("a2", "Grep", json.RawMessage(`{}`)),
		}},
	}
	if got := CountToolUses(messages); got != 2 {
		t.Errorf("CountToolUses = %d, want 2 (assistant tool_use blocks only)", got)
	}
}

func TestGetLastToolUseName_IgnoresNonAssistant(t *testing.T) {
	t.Parallel()
	msg := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
		types.NewToolUseBlock("u1", "Read", json.RawMessage(`{}`)),
	}}
	if got := GetLastToolUseName(msg); got != "" {
		t.Errorf("GetLastToolUseName(user message) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// buildEnvBlock / EnhanceSystemPrompt — exact rendered shape
// ---------------------------------------------------------------------------

func TestBuildEnvBlock_ExactRendering(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/zsh")

	uname, err := exec.Command("uname", "-sr").Output()
	if err != nil {
		t.Fatalf("uname -sr: %v", err)
	}
	want := "Here is useful information about the environment you are running in:\n<env>" +
		"\nWorking directory: " + dir +
		"\nIs directory a git repo: No" +
		"\nPlatform: " + runtime.GOOS +
		"\nShell: /bin/zsh" +
		"\nOS Version: " + strings.TrimSpace(string(uname)) +
		"\n</env>" +
		"\nYou are powered by the model gpt-x."

	if got := buildEnvBlock(dir, "gpt-x"); got != want {
		t.Errorf("buildEnvBlock =\n%q\nwant\n%q", got, want)
	}
}

func TestBuildEnvBlock_ShellFallbackWhenUnset(t *testing.T) {
	t.Setenv("SHELL", "")
	got := buildEnvBlock(t.TempDir(), "")
	if !strings.Contains(got, "\nShell: /bin/bash\nOS Version: ") {
		t.Errorf("env block does not fall back to /bin/bash:\n%q", got)
	}
	if strings.Contains(got, "You are powered by the model") {
		t.Errorf("model line must be absent for an empty model:\n%q", got)
	}
	if !strings.HasSuffix(got, "\n</env>") {
		t.Errorf("env block must end with </env> when no model is set:\n%q", got)
	}
}

func TestEnhanceSystemPrompt_ExactSectionOrder(t *testing.T) {
	dir := t.TempDir()
	tools := map[string]tool.Tool{"Read": &mockTool{name: "Read"}, "Bash": &mockTool{name: "Bash"}}

	got := EnhanceSystemPrompt("BASE", tools, dir, "")
	want := strings.Join([]string{
		"BASE",
		agentNotes,
		"\nEnabled tools:\n- Bash\n- Read\n",
		buildEnvBlock(dir, ""),
	}, "\n\n")
	if got != want {
		t.Errorf("EnhanceSystemPrompt =\n%q\nwant\n%q", got, want)
	}

	empty := EnhanceSystemPrompt("", nil, dir, "")
	wantEmpty := strings.Join([]string{defaultAgentPrompt, agentNotes, buildEnvBlock(dir, "")}, "\n\n")
	if empty != wantEmpty {
		t.Errorf("EnhanceSystemPrompt(empty base, no tools) =\n%q\nwant\n%q", empty, wantEmpty)
	}
}

// ---------------------------------------------------------------------------
// FinalizeResult — every return path carries tokens, duration, agent type and
// tool-use count (TS finalizeAgentTool has a single return with
// totalToolUseCount, agentToolUtils.ts:341).
// ---------------------------------------------------------------------------

func TestFinalizeResult_FallbackRecordsAreComplete(t *testing.T) {
	t.Parallel()
	usage := types.Usage{InputTokens: 120, OutputTokens: 30}
	start := time.Now().Add(-3 * time.Second) // REAL-TIME: the duration assertion measures against the wall clock

	noText := FinalizeResult([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
		}},
	}, "Explore", start, usage, 4)
	if noText.Content != "(agent completed with no text output)" {
		t.Errorf("Content = %q, want the no-text sentinel", noText.Content)
	}
	if noText.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", noText.AgentType, "Explore")
	}
	if noText.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150 (input+output)", noText.TotalTokens)
	}
	if noText.TotalDurationMs < 3000 {
		t.Errorf("TotalDurationMs = %d, want at least 3000 (measured from the supplied start)", noText.TotalDurationMs)
	}

	interrupted := FinalizeResult([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock("do it"),
			types.NewTextBlock(types.InterruptMessage),
		}},
	}, "General", start, usage, 2)
	if interrupted.Content != "(agent interrupted by user)" {
		t.Errorf("Content = %q, want the interrupt sentinel", interrupted.Content)
	}
	if interrupted.AgentType != "General" {
		t.Errorf("AgentType = %q, want %q", interrupted.AgentType, "General")
	}
	if interrupted.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150 (input+output)", interrupted.TotalTokens)
	}
	if interrupted.TotalDurationMs < 3000 {
		t.Errorf("TotalDurationMs = %d, want at least 3000 (measured from the supplied start)", interrupted.TotalDurationMs)
	}
	if strings.Contains(interrupted.Content, "do it") {
		t.Errorf("interrupt fallback leaked user text into Content: %q", interrupted.Content)
	}
}

// The two sentinel fallbacks must carry the tool-use count the caller passed
// (production passes CountToolUses(messages)): the wire trailer renders it, so
// dropping it makes a tool-heavy agent report tool_uses: 0 to the parent.
func TestFinalizeResult_FallbacksCarryToolUseCount(t *testing.T) {
	t.Parallel()
	usage := types.Usage{InputTokens: 120, OutputTokens: 30}
	start := time.Now() // REAL-TIME: FinalizeResult derives duration from it

	noText := FinalizeResult([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Grep", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t3", "Glob", json.RawMessage(`{}`)),
		}},
	}, "General", start, usage, 3)
	if noText.Content != "(agent completed with no text output)" {
		t.Fatalf("Content = %q, want the no-text sentinel", noText.Content)
	}
	if noText.TotalToolUseCount != 3 {
		t.Errorf("no-text fallback TotalToolUseCount = %d, want 3", noText.TotalToolUseCount)
	}

	interrupted := FinalizeResult([]types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("t1", "Read", json.RawMessage(`{}`)),
			types.NewToolUseBlock("t2", "Bash", json.RawMessage(`{}`)),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewTextBlock(types.InterruptMessage),
		}},
	}, "General", start, usage, 2)
	if interrupted.Content != "(agent interrupted by user)" {
		t.Fatalf("Content = %q, want the interrupt sentinel", interrupted.Content)
	}
	if interrupted.TotalToolUseCount != 2 {
		t.Errorf("interrupt fallback TotalToolUseCount = %d, want 2", interrupted.TotalToolUseCount)
	}

	at := New()
	for _, tc := range []struct {
		name   string
		result *types.SubQueryResult
		want   string
	}{
		{"no-text", noText, "tool_uses: 3"},
		{"interrupted", interrupted, "tool_uses: 2"},
	} {
		blocks := at.FormatWireBlocks(tc.result)
		if len(blocks) != 1 {
			t.Fatalf("%s: got %d wire blocks, want 1", tc.name, len(blocks))
		}
		if !strings.Contains(blocks[0].Text, tc.want) {
			t.Errorf("%s wire text = %q, want it to carry %q", tc.name, blocks[0].Text, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// SubagentDeps / AgentOpts plumbing
// ---------------------------------------------------------------------------

func TestSubagentDeps_CarriesEveryField(t *testing.T) {
	t.Parallel()
	eng := &mockSubEngine{}
	at := New()
	at.SetEngine(eng)
	at.SetResolveTierFn(func(tier string) string { return "tier-" + tier })
	at.SetMcpConnect(func(ctx context.Context, agentID string, raw []json.RawMessage) (*McpConnectResult, error) {
		return nil, nil
	})
	at.SetNotifyFn(func(xml string) {}, func() string { return "sys prompt" })

	deps := at.SubagentDeps()
	if deps == nil {
		t.Fatal("SubagentDeps returned nil for a non-nil tool")
	}
	if deps.Engine != SubagentEngine(eng) {
		t.Error("SubagentDeps.Engine is not the injected engine")
	}
	if deps.ResolveTierFn == nil || deps.ResolveTierFn("fast") != "tier-fast" {
		t.Errorf("SubagentDeps.ResolveTierFn did not carry the injected resolver")
	}
	if deps.McpConnect == nil {
		t.Error("SubagentDeps.McpConnect is nil")
	}
	if deps.SysPromptFn == nil || deps.SysPromptFn() != "sys prompt" {
		t.Error("SubagentDeps.SysPromptFn did not carry the injected function")
	}

	var nilTool *AgentTool
	if got := nilTool.SubagentDeps(); got != nil {
		t.Errorf("SubagentDeps on a nil receiver = %+v, want nil", got)
	}
}

func TestCall_PassesPromptTypeModelTierAndToolUseID(t *testing.T) {
	t.Parallel()
	var captured AgentOpts
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	at.SetResolveTierFn(func(tier string) string { return "resolved:" + tier })

	input := json.RawMessage(`{"description":"d","prompt":"the prompt","subagent_type":"Explore","model":"opus"}`)
	if _, err := at.Call(context.Background(), input, &tool.ToolUseContext{ToolUseID: "call_42"}); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if captured.Prompt != "the prompt" {
		t.Errorf("Prompt = %q, want %q", captured.Prompt, "the prompt")
	}
	if captured.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", captured.AgentType, "Explore")
	}
	if captured.Model != "opus" {
		t.Errorf("Model = %q, want %q", captured.Model, "opus")
	}
	if captured.ParentToolUseID != "call_42" {
		t.Errorf("ParentToolUseID = %q, want %q", captured.ParentToolUseID, "call_42")
	}
	if captured.ResolveTierFn == nil || captured.ResolveTierFn("x") != "resolved:x" {
		t.Error("ResolveTierFn was not carried into AgentOpts")
	}
}

func TestCallFork_CarriesParentToolUseIDAndSystemPrompt(t *testing.T) {
	t.Parallel()
	var captured AgentOpts
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	at.SetNotifyFn(func(xml string) {}, func() string { return "rendered parent prompt" })
	at.SetResolveTierFn(func(tier string) string { return "resolved:" + tier })
	at.SetMcpConnect(func(ctx context.Context, agentID string, raw []json.RawMessage) (*McpConnectResult, error) {
		return nil, nil
	})

	if _, err := at.Call(context.Background(),
		json.RawMessage(`{"description":"d","prompt":"p","fork":true}`),
		&tool.ToolUseContext{ToolUseID: "call_7"}); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if captured.ParentToolUseID != "call_7" {
		t.Errorf("ParentToolUseID = %q, want %q", captured.ParentToolUseID, "call_7")
	}
	if captured.SystemPrompt != "rendered parent prompt" {
		t.Errorf("SystemPrompt = %q, want the rendered parent prompt", captured.SystemPrompt)
	}
	if captured.MaxTurns != forkMaxTurns {
		t.Errorf("MaxTurns = %d, want %d", captured.MaxTurns, forkMaxTurns)
	}
	if captured.ResolveTierFn == nil || captured.ResolveTierFn("x") != "resolved:x" {
		t.Error("the injected tier resolver must reach the fork's AgentOpts")
	}
	if at.McpConnectFn() == nil {
		t.Error("McpConnectFn must return the connector injected by SetMcpConnect")
	}
}

// ---------------------------------------------------------------------------
// DecodeResult / RenderResult
// ---------------------------------------------------------------------------

func TestDecodeResult_RejectsNonJSONPayload(t *testing.T) {
	t.Parallel()
	at := New()
	raw := json.RawMessage(`[{"type":"text","text":"not json"}]`)
	got, err := at.DecodeResult(raw)
	if err == nil {
		t.Fatalf("DecodeResult(%s) = %+v, want a JSON error", raw, got)
	}
	if got != nil {
		t.Errorf("DecodeResult returned %+v alongside error %v, want nil result", got, err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error %v does not wrap *json.SyntaxError", err)
	}
}

func TestRenderResult_StringPassthrough(t *testing.T) {
	t.Parallel()
	at := New()
	if got := at.RenderResult("plain string"); got != "plain string" {
		t.Errorf("RenderResult(string) = %q, want %q", got, "plain string")
	}
	oneShotAsync := &types.SubQueryResult{AgentType: "Explore", AgentID: "agent-3", Content: "launched", AsyncLaunched: true}
	if got := at.RenderResult(oneShotAsync); got != "Explore agent agent-3 running in background..." {
		t.Errorf("RenderResult(async Explore) = %q", got)
	}
}

func TestFormatWireBlocks_OneShotAsyncSkipsTrailer(t *testing.T) {
	t.Parallel()
	at := New()
	blocks := at.FormatWireBlocks(&types.SubQueryResult{
		AgentType:         "Explore",
		AgentID:           "agent-9",
		Content:           "launched",
		AsyncLaunched:     true,
		TotalTokens:       10,
		TotalToolUseCount: 2,
		TotalDurationMs:   5,
	})
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0].Text != "launched" {
		t.Errorf("wire text = %q, want the launch message alone", blocks[0].Text)
	}
}

// ---------------------------------------------------------------------------
// BackgroundAgentRegistry — snapshots and notification XML
// ---------------------------------------------------------------------------

func TestSpawn_SnapshotReportsRunningState(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	release := make(chan struct{})
	snapshot, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-release
			return &types.SubQueryResult{Content: "late"}, nil
		}, nil, "my task", "call_p", "agent")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if snapshot.ID != "agent-1" {
		t.Errorf("ID = %q, want %q", snapshot.ID, "agent-1")
	}
	if snapshot.Description != "my task" {
		t.Errorf("Description = %q, want %q", snapshot.Description, "my task")
	}
	if snapshot.Status != AgentRunning {
		t.Errorf("Status = %q, want %q", snapshot.Status, AgentRunning)
	}
	if snapshot.Result != nil {
		t.Errorf("Result = %+v, want nil while running", snapshot.Result)
	}
	close(release)
	reg.Wait(snapshot.ID)
}

func TestWait_ResultCopyIsIndependent(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	state, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "original", TotalTokens: 7}, nil
		}, nil, "copy test", "call_1", "agent")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	done, ok := reg.Wait(state.ID)
	if !ok {
		t.Fatal("Wait returned false for a known ID")
	}
	if done.Result == nil || done.Result.Content != "original" || done.Result.TotalTokens != 7 {
		t.Fatalf("Wait result = %+v, want the completed result", done.Result)
	}
	done.Result.Content = "tampered"

	again, ok := reg.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false after Wait")
	}
	if again.Result == nil || again.Result.Content != "original" {
		t.Errorf("registry result = %+v, want it unaffected by the caller's edit", again.Result)
	}

	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("List = %d entries, want 1", len(list))
	}
	list[0].Result.Content = "tampered again"
	if after, _ := reg.Get(state.ID); after.Result.Content != "original" {
		t.Errorf("registry result = %q, want %q after editing the List copy", after.Result.Content, "original")
	}
}

func TestBuildAgentNotificationXML_ExactDocument(t *testing.T) {
	t.Parallel()
	completed := buildAgentNotificationXML("fork-1", "call_abc",
		&types.SubQueryResult{Content: "found 3 files", TotalDurationMs: 1234, TotalTokens: 5000, TotalToolUseCount: 3},
		nil, "search code", "ship-audit")
	wantCompleted := `<job-notification>
<job-id>fork-1</job-id>
<tool-use-id>call_abc</tool-use-id>
<status>completed</status>
<agent-type>ship-audit</agent-type>
<duration-ms>1234</duration-ms>
<tokens>5000</tokens>
<tool-uses>3</tool-uses>
<summary>Background agent "search code" completed</summary>
<result>
found 3 files
</result>
</job-notification>`
	if completed != wantCompleted {
		t.Errorf("completed notification =\n%s\nwant\n%s", completed, wantCompleted)
	}

	failed := buildAgentNotificationXML("agent-2", "call_err", nil, errors.New("timeout"), "task", "")
	wantFailed := `<job-notification>
<job-id>agent-2</job-id>
<tool-use-id>call_err</tool-use-id>
<status>failed</status>
<agent-type>fork</agent-type>
<duration-ms>0</duration-ms>
<tokens>0</tokens>
<tool-uses>0</tool-uses>
<summary>Background agent "task" failed</summary>
<result>
Error: timeout
</result>
</job-notification>`
	if failed != wantFailed {
		t.Errorf("failed notification =\n%s\nwant\n%s", failed, wantFailed)
	}
}

// ---------------------------------------------------------------------------
// FilterIncompleteToolCalls — results only count from user messages
// ---------------------------------------------------------------------------

func TestFilterIncompleteToolCalls_AssistantToolResultDoesNotComplete(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"x"`), false),
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewToolUseBlock("id1", "Read", json.RawMessage(`{}`)),
		}},
	}
	filtered := FilterIncompleteToolCalls(messages)
	if len(filtered) != 1 {
		t.Fatalf("kept %d messages, want 1 — only user tool_result blocks complete a call", len(filtered))
	}
	if len(filtered[0].Content) != 1 || filtered[0].Content[0].Type != types.ContentTypeToolResult {
		t.Errorf("surviving message = %+v, want the assistant tool_result message", filtered[0])
	}
}

// ---------------------------------------------------------------------------
// Adapter — lazy cleanup only fires for terminal agents
// ---------------------------------------------------------------------------

func TestJobAdapter_ListKeepsRunningAgents(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	release := make(chan struct{})
	running, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-release
			return &types.SubQueryResult{Content: "late"}, nil
		}, nil, "running", "call_r", "agent")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	adapter := NewBackgroundAgentJobAdapter(reg)

	list := adapter.List()
	if len(list) != 1 {
		t.Fatalf("List = %d entries, want 1", len(list))
	}
	if list[0].Status != "running" || list[0].ID != running.ID || list[0].Type != "agent" {
		t.Errorf("List entry = %+v, want a running agent with ID %s", list[0], running.ID)
	}
	if _, ok := reg.Get(running.ID); !ok {
		t.Error("List dropped the running agent — cleanup must only remove terminal agents")
	}
	close(release)
	reg.Wait(running.ID)
}

func TestConvertState_EveryStatusMapping(t *testing.T) {
	t.Parallel()
	result := &types.SubQueryResult{Content: "body", AgentType: "Explore", TotalTokens: 11, TotalDurationMs: 22}
	for _, tc := range []struct {
		status   BackgroundAgentStatus
		wantStat string
		wantCode int
	}{
		{AgentCompleted, "completed", 0},
		{AgentFailed, "failed", 1},
		{AgentCancelled, "killed", -1},
		{AgentRunning, "running", 0},
	} {
		info := convertState(&BackgroundAgentState{ID: "agent-1", Description: "d", Status: tc.status, Result: result})
		if info.Status != tc.wantStat {
			t.Errorf("status %q → %q, want %q", tc.status, info.Status, tc.wantStat)
		}
		if info.ExitCode != tc.wantCode {
			t.Errorf("status %q → exit code %d, want %d", tc.status, info.ExitCode, tc.wantCode)
		}
		if info.Output != "body" || info.AgentType != "Explore" || info.Tokens != 11 || info.DurationMs != 22 {
			t.Errorf("status %q → result fields = %+v, want them copied from Result", tc.status, info)
		}
	}
}

// ---------------------------------------------------------------------------
// Built-in agent definitions
// ---------------------------------------------------------------------------

func TestBuiltInAgentDefinitionFields(t *testing.T) {
	t.Parallel()
	general := builtInAgents["General"]
	explore := builtInAgents["Explore"]

	if !slices.Equal(general.Tools, []string{"*"}) {
		t.Errorf("General.Tools = %v, want [\"*\"]", general.Tools)
	}
	if general.DisallowedTools != nil {
		t.Errorf("General.DisallowedTools = %v, want nil", general.DisallowedTools)
	}
	if general.Model != "inherit" || explore.Model != "inherit" {
		t.Errorf("Model = (%q, %q), want both %q", general.Model, explore.Model, "inherit")
	}
	if general.OmitClaudeMd {
		t.Error("General.OmitClaudeMd should be false")
	}
	if !explore.OmitClaudeMd {
		t.Error("Explore.OmitClaudeMd should be true")
	}
	if general.MaxTurns != 0 || explore.MaxTurns != 0 {
		t.Errorf("MaxTurns = (%d, %d), want (0, 0)", general.MaxTurns, explore.MaxTurns)
	}
	if !slices.Equal(explore.DisallowedTools, []string{"ExitPlanMode", "Edit", "Write", "NotebookEdit"}) {
		t.Errorf("Explore.DisallowedTools = %v", explore.DisallowedTools)
	}
	if explore.Tools != nil {
		t.Errorf("Explore.Tools = %v, want nil (no whitelist)", explore.Tools)
	}
	if general.BaseDir != "built-in" || explore.BaseDir != "built-in" {
		t.Errorf("BaseDir = (%q, %q), want %q", general.BaseDir, explore.BaseDir, "built-in")
	}
	if general.Source != types.AgentSourceBuiltIn || explore.Source != types.AgentSourceBuiltIn {
		t.Error("built-in agents must carry AgentSourceBuiltIn")
	}
	if got := general.SystemPrompt(); got != generalPurposeSystemPrompt {
		t.Error("General.SystemPrompt should return generalPurposeSystemPrompt")
	}
	if got := explore.SystemPrompt(); got != exploreSystemPrompt {
		t.Error("Explore.SystemPrompt should return exploreSystemPrompt")
	}
}

func TestGetAgentDefinition_ForkMapsToGeneral(t *testing.T) {
	t.Parallel()
	prev := globalLoader
	globalLoader = nil
	defer func() { globalLoader = prev }()

	def, err := GetAgentDefinition("fork")
	if err != nil {
		t.Fatalf("GetAgentDefinition(\"fork\") returned error: %v", err)
	}
	if def != builtInAgents["General"] {
		t.Errorf("fork resolved to %+v, want the General definition", def)
	}
	if _, err := GetAgentDefinition(""); err != nil {
		t.Fatalf("GetAgentDefinition(\"\") returned error: %v", err)
	}
	if def2, _ := GetAgentDefinition(""); def2 != builtInAgents["General"] {
		t.Error("the empty type must resolve to General")
	}
}

// ---------------------------------------------------------------------------
// Tool filtering helpers
// ---------------------------------------------------------------------------

func TestIsWildcard_OnlyASingleStar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tools []string
		want  bool
	}{
		{[]string{"*"}, true},
		{[]string{"*", "Read"}, false},
		{[]string{"Read"}, false},
		{[]string{}, false},
		{nil, false},
		{[]string{"**"}, false},
	} {
		if got := isWildcard(tc.tools); got != tc.want {
			t.Errorf("isWildcard(%v) = %v, want %v", tc.tools, got, tc.want)
		}
	}
}

func TestExtractMCPServerName_EveryShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want string
	}{
		{"mcp__server__tool", "server"},
		{"mcp__server", "server"},
		{"mcp__server__sub__tool", "server"},
		{"mcp__", ""},
		{"mcp", ""},
		{"Read", ""},
		{"", ""},
	} {
		if got := extractMCPServerName(tc.name); got != tc.want {
			t.Errorf("extractMCPServerName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tool-name list formatting — the empty guard must trigger only at zero
// ---------------------------------------------------------------------------

func TestFormatToolNamesList_CountBoundaries(t *testing.T) {
	t.Parallel()
	if got := formatToolNamesList(map[string]tool.Tool{}); got != "" {
		t.Errorf("formatToolNamesList(empty) = %q, want empty", got)
	}
	if got := formatToolNamesList(nil); got != "" {
		t.Errorf("formatToolNamesList(nil) = %q, want empty", got)
	}
	if got := formatToolNamesList(map[string]tool.Tool{"Read": &mockTool{name: "Read"}}); got != "- Read\n" {
		t.Errorf("formatToolNamesList(one tool) = %q, want %q", got, "- Read\n")
	}
	if got := formatToolNamesList(map[string]tool.Tool{"Read": &mockTool{name: "Read"}, "Bash": &mockTool{name: "Bash"}}); got != "- Bash\n- Read\n" {
		t.Errorf("formatToolNamesList(two tools) = %q, want %q", got, "- Bash\n- Read\n")
	}
}

func TestBuiltInAgentWhenToUse(t *testing.T) {
	t.Parallel()
	wantGeneral := "General-purpose agent for researching complex questions, searching for code, and executing multi-step tasks. When you are searching for a keyword or file and are not confident that you will find the right match in the first few tries use this agent to perform the search for you."
	wantExplore := "Use this agent when you need to quickly find files by patterns, search code for keywords, or answer questions about the codebase."
	if got := builtInAgents["General"].WhenToUse; got != wantGeneral {
		t.Errorf("General.WhenToUse = %q, want %q", got, wantGeneral)
	}
	if got := builtInAgents["Explore"].WhenToUse; got != wantExplore {
		t.Errorf("Explore.WhenToUse = %q, want %q", got, wantExplore)
	}
}

func TestIsOneShotAgent_EveryType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		agentType string
		want      bool
	}{
		{"Explore", true},
		{"Plan", true},
		{"Planner", true},
		{"Critic", true},
		{"General", false},
		{"Executor", false},
		{"Reviewer", false},
		{"fork", false},
		{"", false},
	} {
		if got := IsOneShotAgent(tc.agentType); got != tc.want {
			t.Errorf("IsOneShotAgent(%q) = %v, want %v", tc.agentType, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Fork paths — the caller's context must reach the engine
// ---------------------------------------------------------------------------

type parentCtxKey struct{}

// A fork runs inside the parent's query: dropping ctx at the callFork hand-off
// or at the engine call silently kills cancellation and per-call tracing.
func TestCallFork_SyncPathPropagatesCallerContext(t *testing.T) {
	t.Parallel()
	var seen any
	var sawNil bool
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, _ AgentOpts) (*types.SubQueryResult, error) {
		if ctx == nil {
			sawNil = true
			return &types.SubQueryResult{Content: "ok"}, nil
		}
		seen = ctx.Value(parentCtxKey{})
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	at.SetNotifyFn(func(xml string) {}, func() string { return "" })

	ctx := context.WithValue(context.Background(), parentCtxKey{}, "parent-value")
	if _, err := at.Call(ctx, json.RawMessage(`{"description":"d","prompt":"p","fork":true}`), nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if sawNil {
		t.Fatal("the fork path handed a nil context to the engine")
	}
	if seen != "parent-value" {
		t.Errorf("engine context value = %v, want %q", seen, "parent-value")
	}
}

// ---------------------------------------------------------------------------
// DecodeResult — the single-block decode error is the caller-facing error
// ---------------------------------------------------------------------------

func TestDecodeResult_SingleBlockErrorIsReturnedVerbatim(t *testing.T) {
	t.Parallel()
	at := New()
	raw := json.RawMessage(`{"content":"not array form"}`)
	got, err := at.DecodeResult(raw)
	if got != nil {
		t.Errorf("DecodeResult(%s) = %+v, want nil alongside the error", raw, got)
	}
	want := `tool: UnmarshalSingleBlock expects array-form content, got `
	if err == nil {
		t.Fatalf("DecodeResult(%s) returned no error, want the %q error", raw, want)
	}
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("DecodeResult error = %q, want it to start with %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// FinalizeResult — duration is measured, and the interrupt marker is text-only
// ---------------------------------------------------------------------------

func TestFinalizeResult_DurationIsMeasuredFromStartTime(t *testing.T) {
	t.Parallel()
	start := time.Now().Add(-2 * time.Second) // REAL-TIME: a past start makes the measured duration distinguishable from zero
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}},
	}
	res := FinalizeResult(messages, "General", start, types.Usage{InputTokens: 3, OutputTokens: 4}, 2)
	if res.TotalDurationMs < 1900 {
		t.Errorf("TotalDurationMs = %d, want >= 1900 for a start time 2s in the past", res.TotalDurationMs)
	}
	if res.TotalTokens != 7 {
		t.Errorf("TotalTokens = %d, want 7 (input+output)", res.TotalTokens)
	}
	if res.TotalToolUseCount != 2 {
		t.Errorf("TotalToolUseCount = %d, want 2", res.TotalToolUseCount)
	}
}

func TestFinalizeResult_InterruptMarkerCountsOnlyOnTextBlocks(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "t1", Name: "Bash", Text: types.InterruptMessage},
		}},
	}
	res := FinalizeResult(messages, "General", time.Now(), types.Usage{}, 1) // REAL-TIME: duration is not under test here
	if res.Content != "(agent completed with no text output)" {
		t.Errorf("Content = %q, want the no-text fallback — the marker sits on a tool_use block, not a text block", res.Content)
	}
}

// ---------------------------------------------------------------------------
// ExtractPartialResult — an empty text block must not end the backward walk
// ---------------------------------------------------------------------------

func TestExtractPartialResult_EmptyTextBlockKeepsWalkingBackward(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("earlier report")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: ""}}},
	}
	if got := ExtractPartialResult(messages); got != "earlier report" {
		t.Errorf("ExtractPartialResult = %q, want %q — the trailing empty text block must not count as content", got, "earlier report")
	}
}
