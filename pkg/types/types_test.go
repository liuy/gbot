package types_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Role constants
// ---------------------------------------------------------------------------

func TestRoleConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		role     types.Role
		expected string
	}{
		{"user", types.RoleUser, "user"},
		{"assistant", types.RoleAssistant, "assistant"},
		{"system", types.RoleSystem, "system"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.role) != tc.expected {
				t.Errorf("Role %s = %q, want %q", tc.name, tc.role, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContentType constants
// ---------------------------------------------------------------------------

func TestContentTypeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ct       types.ContentType
		expected string
	}{
		{"text", types.ContentTypeText, "text"},
		{"tool_use", types.ContentTypeToolUse, "tool_use"},
		{"tool_result", types.ContentTypeToolResult, "tool_result"},
		{"thinking", types.ContentTypeThinking, "thinking"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.ct) != tc.expected {
				t.Errorf("ContentType %s = %q, want %q", tc.name, tc.ct, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContentBlock constructors
// ---------------------------------------------------------------------------

func TestNewTextBlock(t *testing.T) {
	t.Parallel()

	block := types.NewTextBlock("hello world")
	if block.Type != types.ContentTypeText {
		t.Errorf("Type = %q, want %q", block.Type, types.ContentTypeText)
	}
	if block.Text != "hello world" {
		t.Errorf("Text = %q, want %q", block.Text, "hello world")
	}
}

func TestNewToolUseBlock(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"cmd":"ls"}`)
	block := types.NewToolUseBlock("id-1", "Bash", input)
	if block.Type != types.ContentTypeToolUse {
		t.Errorf("Type = %q, want %q", block.Type, types.ContentTypeToolUse)
	}
	if block.ID != "id-1" {
		t.Errorf("ID = %q, want %q", block.ID, "id-1")
	}
	if block.Name != "Bash" {
		t.Errorf("Name = %q, want %q", block.Name, "Bash")
	}
	if string(block.Input) != `{"cmd":"ls"}` {
		t.Errorf("Input = %s, want %s", block.Input, `{"cmd":"ls"}`)
	}
}

func TestNewToolResultBlock(t *testing.T) {
	t.Parallel()

	content := json.RawMessage(`"done"`)
	block := types.NewToolResultBlock("use-1", content, false)
	if block.Type != types.ContentTypeToolResult {
		t.Errorf("Type = %q, want %q", block.Type, types.ContentTypeToolResult)
	}
	if block.ToolUseID != "use-1" {
		t.Errorf("ToolUseID = %q, want %q", block.ToolUseID, "use-1")
	}
	if block.IsError {
		t.Error("IsError = true, want false")
	}

	errBlock := types.NewToolResultBlock("use-2", content, true)
	if !errBlock.IsError {
		t.Error("IsError = false, want true")
	}
}

// ---------------------------------------------------------------------------
// ContentBlock JSON round-trip
// ---------------------------------------------------------------------------

func TestContentBlockJSONRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		block types.ContentBlock
	}{
		{
			"text block",
			types.NewTextBlock("some text"),
		},
		{
			"tool use block",
			types.NewToolUseBlock("id-2", "Grep", json.RawMessage(`{"pattern":"foo"}`)),
		},
		{
			"tool result block",
			types.NewToolResultBlock("use-3", json.RawMessage(`"output"`), true),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tc.block)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var got types.ContentBlock
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if got.Type != tc.block.Type {
				t.Errorf("Type = %q, want %q", got.Type, tc.block.Type)
			}
			if got.Text != tc.block.Text {
				t.Errorf("Text = %q, want %q", got.Text, tc.block.Text)
			}
			if got.ID != tc.block.ID {
				t.Errorf("ID = %q, want %q", got.ID, tc.block.ID)
			}
			if got.Name != tc.block.Name {
				t.Errorf("Name = %q, want %q", got.Name, tc.block.Name)
			}
			if got.ToolUseID != tc.block.ToolUseID {
				t.Errorf("ToolUseID = %q, want %q", got.ToolUseID, tc.block.ToolUseID)
			}
			if got.IsError != tc.block.IsError {
				t.Errorf("IsError = %v, want %v", got.IsError, tc.block.IsError)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Message JSON
// ---------------------------------------------------------------------------

func TestMessageJSON(t *testing.T) {
	t.Parallel()

	msg := types.Message{
		ID:         "msg-1",
		Role:       types.RoleUser,
		Content:    []types.ContentBlock{types.NewTextBlock("hello")},
		Model:      "claude-4-sonnet",
		StopReason: "end_turn",
		Usage: &types.Usage{
			InputTokens:  10,
			OutputTokens: 5,
		},
		Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ID != "msg-1" {
		t.Errorf("ID = %q, want %q", got.ID, "msg-1")
	}
	if got.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", got.Role, types.RoleUser)
	}
	if len(got.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(got.Content))
	}
	if got.Content[0].Text != "hello" {
		t.Errorf("Content[0].Text = %q, want %q", got.Content[0].Text, "hello")
	}
	if got.Model != "claude-4-sonnet" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-4-sonnet")
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", got.StopReason, "end_turn")
	}
	if got.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if got.Usage.InputTokens != 10 {
		t.Errorf("Usage.InputTokens = %d, want 10", got.Usage.InputTokens)
	}
	if got.Usage.OutputTokens != 5 {
		t.Errorf("Usage.OutputTokens = %d, want 5", got.Usage.OutputTokens)
	}
}

func TestMessageOmitEmpty(t *testing.T) {
	t.Parallel()

	// Minimal message — optional fields should be omitted
	msg := types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Ensure optional fields are not present in JSON output
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	if _, ok := raw["id"]; ok {
		t.Error("id should be omitted")
	}
	if _, ok := raw["model"]; ok {
		t.Error("model should be omitted")
	}
	if _, ok := raw["stop_reason"]; ok {
		t.Error("stop_reason should be omitted")
	}
	if _, ok := raw["usage"]; ok {
		t.Error("usage should be omitted")
	}
}

// ---------------------------------------------------------------------------
// Usage JSON
// ---------------------------------------------------------------------------

func TestUsageJSON(t *testing.T) {
	t.Parallel()

	u := types.Usage{
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 20,
		CacheReadInputTokens:     10,
	}

	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.Usage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", got.InputTokens)
	}
	if got.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", got.OutputTokens)
	}
	if got.CacheCreationInputTokens != 20 {
		t.Errorf("CacheCreationInputTokens = %d, want 20", got.CacheCreationInputTokens)
	}
	if got.CacheReadInputTokens != 10 {
		t.Errorf("CacheReadInputTokens = %d, want 10", got.CacheReadInputTokens)
	}
}

// ---------------------------------------------------------------------------
// PermissionMode constants
// ---------------------------------------------------------------------------

func TestPermissionModeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode types.PermissionMode
		want string
	}{
		{"acceptEdits", types.PermissionModeAcceptEdits, "acceptEdits"},
		{"bypass", types.PermissionModeBypass, "bypassPermissions"},
		{"default", types.PermissionModeDefault, "default"},
		{"dontAsk", types.PermissionModeDontAsk, "dontAsk"},
		{"plan", types.PermissionModePlan, "plan"},
		{"auto", types.PermissionModeAuto, "auto"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.mode) != tc.want {
				t.Errorf("PermissionMode %s = %q, want %q", tc.name, tc.mode, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PermissionBehavior constants
// ---------------------------------------------------------------------------

func TestPermissionBehaviorConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		b    types.PermissionBehavior
		want string
	}{
		{"allow", types.BehaviorAllow, "allow"},
		{"deny", types.BehaviorDeny, "deny"},
		{"ask", types.BehaviorAsk, "ask"},
		{"passthrough", types.BehaviorPassthrough, "passthrough"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.b) != tc.want {
				t.Errorf("PermissionBehavior %s = %q, want %q", tc.name, tc.b, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PermissionResult implementations
// ---------------------------------------------------------------------------

func TestPermissionAllowDecision(t *testing.T) {
	t.Parallel()

	var d types.PermissionResult = types.PermissionAllowDecision{}
	if d.Behavior() != types.BehaviorAllow {
		t.Errorf("Behavior() = %q, want %q", d.Behavior(), types.BehaviorAllow)
	}
}

func TestPermissionAskDecision(t *testing.T) {
	t.Parallel()

	var d types.PermissionResult = types.PermissionAskDecision{Message: "confirm?"}
	if d.Behavior() != types.BehaviorAsk {
		t.Errorf("Behavior() = %q, want %q", d.Behavior(), types.BehaviorAsk)
	}
}

func TestPermissionDenyDecision(t *testing.T) {
	t.Parallel()

	var d types.PermissionResult = types.PermissionDenyDecision{Message: "forbidden"}
	if d.Behavior() != types.BehaviorDeny {
		t.Errorf("Behavior() = %q, want %q", d.Behavior(), types.BehaviorDeny)
	}
}

// ---------------------------------------------------------------------------
// QueryEventType constants
// ---------------------------------------------------------------------------

func TestQueryEventTypeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		et   types.QueryEventType
		want string
	}{
		{"query_start", types.EventQueryStart, "query_start"},
		{"query_end", types.EventQueryEnd, "query_end"},
		{"turn_start", types.EventTurnStart, "turn_start"},
		{"turn_end", types.EventTurnEnd, "turn_end"},
		{"text_delta", types.EventTextDelta, "text_delta"},
		{"tool_start", types.EventToolStart, "tool_start"},
		{"tool_param_delta", types.EventToolParamDelta, "tool_param_delta"},
		{"tool_output_delta", types.EventToolOutputDelta, "tool_output_delta"},
		{"tool_end", types.EventToolEnd, "tool_end"},
		{"usage", types.EventUsage, "usage"},
		{"error", types.EventError, "error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.et) != tc.want {
				t.Errorf("QueryEventType %s = %q, want %q", tc.name, tc.et, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// QueryEvent JSON
// ---------------------------------------------------------------------------

func TestQueryEventJSON(t *testing.T) {
	t.Parallel()

	evt := types.QueryEvent{
		Type: types.EventTextDelta,
		Text: "hello",
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.QueryEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Type != types.EventTextDelta {
		t.Errorf("Type = %q, want %q", got.Type, types.EventTextDelta)
	}
	if got.Text != "hello" {
		t.Errorf("Text = %q, want %q", got.Text, "hello")
	}
}

// ---------------------------------------------------------------------------
// TerminalReason constants
// ---------------------------------------------------------------------------

func TestTerminalReasonConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason types.TerminalReason
		want   string
	}{
		{"completed", types.TerminalCompleted, "completed"},
		{"aborted_streaming", types.TerminalAbortedStreaming, "aborted_streaming"},
		{"aborted_tools", types.TerminalAbortedTools, "aborted_tools"},
		{"model_error", types.TerminalModelError, "model_error"},
		{"blocking_limit", types.TerminalBlockingLimit, "blocking_limit"},
		{"prompt_too_long", types.TerminalPromptTooLong, "prompt_too_long"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.reason) != tc.want {
				t.Errorf("TerminalReason %s = %q, want %q", tc.name, tc.reason, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContinueReason constants
// ---------------------------------------------------------------------------

func TestContinueReasonConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason types.ContinueReason
		want   string
	}{
		{"next_turn", types.ContinueNextTurn, "next_turn"},
		{"max_tokens_retry", types.ContinueMaxTokensRetry, "max_tokens_retry"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.reason) != tc.want {
				t.Errorf("ContinueReason %s = %q, want %q", tc.name, tc.reason, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LoopAction
// ---------------------------------------------------------------------------

func TestLoopAction(t *testing.T) {
	t.Parallel()

	action := types.LoopAction{
		Continue: true,
		Reason:   types.ContinueNextTurn,
	}

	if !action.Continue {
		t.Error("Continue = false, want true")
	}
	if action.Reason != types.ContinueNextTurn {
		t.Errorf("Reason = %q, want %q", action.Reason, types.ContinueNextTurn)
	}

	terminal := types.LoopAction{
		Continue: false,
		Terminal: types.TerminalCompleted,
	}

	if terminal.Continue {
		t.Error("Continue = true, want false")
	}
	if terminal.Terminal != types.TerminalCompleted {
		t.Errorf("Terminal = %q, want %q", terminal.Terminal, types.TerminalCompleted)
	}
}

// ---------------------------------------------------------------------------
// ToolUseEvent / ToolResultEvent JSON
// ---------------------------------------------------------------------------

func TestToolUseEventJSON(t *testing.T) {
	t.Parallel()

	evt := types.ToolUseEvent{
		ID:    "tu-1",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"ls"}`),
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.ToolUseEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ID != "tu-1" {
		t.Errorf("ID = %q, want %q", got.ID, "tu-1")
	}
	if got.Name != "Bash" {
		t.Errorf("Name = %q, want %q", got.Name, "Bash")
	}
}

func TestToolResultEventJSON(t *testing.T) {
	t.Parallel()

	evt := types.ToolResultEvent{
		ToolUseID: "tu-1",
		Output:    json.RawMessage(`"ok"`),
		IsError:   false,
		Timing:    150 * time.Millisecond,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.ToolResultEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ToolUseID != "tu-1" {
		t.Errorf("ToolUseID = %q, want %q", got.ToolUseID, "tu-1")
	}
	if got.IsError {
		t.Error("IsError = true, want false")
	}
}

// ---------------------------------------------------------------------------
// ToolUseContext
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Usage.TotalInputTokens
// ---------------------------------------------------------------------------

func TestTotalInputTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		u    types.Usage
		want int
	}{
		{"zero", types.Usage{}, 0},
		{"input only", types.Usage{InputTokens: 100}, 100},
		{"all fields", types.Usage{InputTokens: 100, CacheReadInputTokens: 30, CacheCreationInputTokens: 20}, 150},
		{"cache only", types.Usage{CacheReadInputTokens: 50, CacheCreationInputTokens: 50}, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.u.TotalInputTokens()
			if got != tc.want {
				t.Errorf("TotalInputTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ToolUseContext
// ---------------------------------------------------------------------------

func TestToolUseContext(t *testing.T) {
	t.Parallel()

	tctx := &types.ToolUseContext{
		ToolUseID:  "tu-ctx-1",
		WorkingDir: "/tmp",
		Options: types.ToolUseOptions{
			Debug:   true,
			Verbose: true,
		},
	}

	if tctx.ToolUseID != "tu-ctx-1" {
		t.Errorf("ToolUseID = %q, want %q", tctx.ToolUseID, "tu-ctx-1")
	}
	if tctx.WorkingDir != "/tmp" {
		t.Errorf("WorkingDir = %q, want %q", tctx.WorkingDir, "/tmp")
	}
	if !tctx.Options.Debug {
		t.Error("Options.Debug = false, want true")
	}
	if !tctx.Options.Verbose {
		t.Error("Options.Verbose = false, want true")
	}
}

// ---------------------------------------------------------------------------
// EventDispatcher (merged from events_test.go)
// ---------------------------------------------------------------------------

// mockDispatcher satisfies EventDispatcher for testing.
type mockDispatcher struct {
	events []types.QueryEvent
}

func (d *mockDispatcher) Dispatch(event types.QueryEvent) {
	d.events = append(d.events, event)
}

func TestEventDispatcher_Interface(t *testing.T) {
	var d types.EventDispatcher = &mockDispatcher{}

	d.Dispatch(types.QueryEvent{
		Type: types.EventQueryStart,
		Text: "test",
	})

	md := d.(*mockDispatcher)
	if len(md.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(md.events))
	}
	if md.events[0].Type != types.EventQueryStart {
		t.Errorf("expected EventQueryStart, got %s", md.events[0].Type)
	}
	if md.events[0].Text != "test" {
		t.Errorf("expected text 'test', got %q", md.events[0].Text)
	}
}

func TestEventDispatcher_NilCheck(t *testing.T) {
	var d types.EventDispatcher
	if d != nil {
		t.Error("expected nil EventDispatcher")
	}
}

// ---------------------------------------------------------------------------
// Skill types (merged from skills_test.go)
// ---------------------------------------------------------------------------

func TestSkillCommand_IsHidden(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{IsUserInvocable: true}
	if cmd.IsHidden() {
		t.Error("user-invocable skill should not be hidden")
	}

	cmd2 := &types.SkillCommand{IsUserInvocable: false}
	if !cmd2.IsHidden() {
		t.Error("agent-only skill should be hidden")
	}
}

func TestSkillCommand_UserFacingName(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{Name: "commit", DisplayName: "Git Commit"}
	if got := cmd.UserFacingName(); got != "Git Commit" {
		t.Errorf("UserFacingName() = %q, want %q", got, "Git Commit")
	}

	cmd2 := &types.SkillCommand{Name: "commit"}
	if got := cmd2.UserFacingName(); got != "commit" {
		t.Errorf("UserFacingName() = %q, want %q", got, "commit")
	}
}

func TestSkillCommand_MeetsAvailabilityRequirement(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{}
	if !cmd.MeetsAvailabilityRequirement() {
		t.Error("skill with no availability should meet requirement")
	}

	cmd2 := &types.SkillCommand{Availability: []string{"claude-ai"}}
	if !cmd2.MeetsAvailabilityRequirement() {
		t.Error("gbot has no auth tiers, should always pass")
	}
}

func TestSkillSource_Constants(t *testing.T) {
	t.Parallel()

	sources := map[types.SkillSource]string{
		types.SkillSourceBundled: "bundled",
		types.SkillSourceUser:    "user",
		types.SkillSourceProject: "project",
		types.SkillSourceManaged: "managed",
		types.SkillSourceMCP:     "mcp",
		types.SkillSourcePlugin:  "plugin",
	}
	for src, want := range sources {
		if string(src) != want {
			t.Errorf("SkillSource %q = %q, want %q", src, string(src), want)
		}
	}
}

func TestSkillCommand_Defaults(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{}
	if !cmd.IsHidden() {
		t.Error("zero-value SkillCommand should be hidden (IsUserInvocable defaults to false)")
	}
}

func TestSkillCommand_Context(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{Context: ""}
	if cmd.Context != "" {
		t.Error("empty Context should mean inline")
	}

	cmd2 := &types.SkillCommand{Context: "fork"}
	if cmd2.Context != "fork" {
		t.Error("fork Context should be 'fork'")
	}
}

func TestCommandPermissionsAttachment(t *testing.T) {
	t.Parallel()

	att := types.CommandPermissionsAttachment{
		AllowedTools: []string{"Bash", "Read", "Write"},
		Model:        "haiku",
	}
	if len(att.AllowedTools) != 3 {
		t.Errorf("expected 3 allowed tools, got %d", len(att.AllowedTools))
	}
	if att.Model != "haiku" {
		t.Errorf("Model = %q, want %q", att.Model, "haiku")
	}
}

func TestInvokedSkillInfo(t *testing.T) {
	t.Parallel()

	info := types.InvokedSkillInfo{
		SkillName: "commit",
		SkillPath: "project:commit",
		Content:   "skill content here",
		AgentID:   "agent-1",
	}
	if info.SkillName != "commit" {
		t.Errorf("SkillName = %q, want %q", info.SkillName, "commit")
	}
	if info.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", info.AgentID, "agent-1")
	}
}
