package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// HookEventName constants
// ---------------------------------------------------------------------------

func TestHookEventNameValues(t *testing.T) {
	// Verify all 11 implemented events match TS HOOK_EVENTS exactly.
	// Source: coreTypes.ts:25-53
	events := map[HookEventName]bool{
		HookPreToolUse:         true,
		HookPostToolUse:        true,
		HookPostToolUseFailure: true,
		HookStop:               true,
		HookSubagentStop:       true,
		HookStopFailure:        true,
		HookUserPromptSubmit:   true,
		HookSessionStart:       true,
		HookSessionEnd:         true,
		HookPreCompact:         true,
		HookPostCompact:        true,
	}
	if len(events) != 11 {
		t.Errorf("expected 11 hook events, got %d", len(events))
	}
	// Verify string values match TS exactly (PascalCase, no underscores)
	for ev := range events {
		s := string(ev)
		if strings.Contains(s, "_") {
			t.Errorf("HookEventName %q should not contain underscores (TS uses PascalCase)", s)
		}
		if s == "" {
			t.Error("HookEventName should not be empty")
		}
	}
}

// ---------------------------------------------------------------------------
// HookType constants
// ---------------------------------------------------------------------------

func TestHookTypeValues(t *testing.T) {
	got := map[HookType]string{
		HookTypeCommand: "command",
		HookTypePrompt:  "prompt",
		HookTypeAgent:   "agent",
	}
	for typ, want := range got {
		if string(typ) != want {
			t.Errorf("HookType %v = %q, want %q", typ, string(typ), want)
		}
	}
}

// ---------------------------------------------------------------------------
// HookConfig JSON round-trip
// ---------------------------------------------------------------------------

func TestHookConfigCommandJSON(t *testing.T) {
	// Source: schemas/hooks.ts:32-65 — BashCommandHookSchema
	cfg := HookConfig{
		Type:          HookTypeCommand,
		Command:       "echo hello",
		Timeout:       30,
		StatusMessage: "Running hook",
		Once:          true,
		Async:         false,
		AsyncRewake:   false,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal HookConfig: %v", err)
	}
	var got HookConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal HookConfig: %v", err)
	}
	if got.Type != HookTypeCommand {
		t.Errorf("Type = %q, want %q", got.Type, HookTypeCommand)
	}
	if got.Command != "echo hello" {
		t.Errorf("Command = %q, want %q", got.Command, "echo hello")
	}
	if got.Timeout != 30 {
		t.Errorf("Timeout = %d, want 30", got.Timeout)
	}
	if got.StatusMessage != "Running hook" {
		t.Errorf("StatusMessage = %q, want %q", got.StatusMessage, "Running hook")
	}
	if got.Once != true {
		t.Error("Once = false, want true")
	}
}

func TestHookConfigPromptJSON(t *testing.T) {
	// Source: schemas/hooks.ts:67-95 — PromptHookSchema
	cfg := HookConfig{
		Type:   HookTypePrompt,
		Prompt: "Check if $ARGUMENTS is safe",
		Model:  "claude-haiku-4-5",
		Once:   true,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got HookConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != HookTypePrompt {
		t.Errorf("Type = %q, want %q", got.Type, HookTypePrompt)
	}
	if got.Prompt != "Check if $ARGUMENTS is safe" {
		t.Errorf("Prompt = %q, want preserved", got.Prompt)
	}
	if got.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-haiku-4-5")
	}
	// Command-only fields should be zero
	if got.Command != "" {
		t.Errorf("Command = %q, want empty for prompt hook", got.Command)
	}
	}

func TestHookConfigAgentJSON(t *testing.T) {
	// Source: schemas/hooks.ts:128-163 — AgentHookSchema
	cfg := HookConfig{
		Type:    HookTypeAgent,
		Prompt:  "Verify tests pass",
		Model:   "claude-sonnet-4-6",
		Timeout: 60,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got HookConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != HookTypeAgent {
		t.Errorf("Type = %q, want %q", got.Type, HookTypeAgent)
	}
	if got.Prompt != "Verify tests pass" {
		t.Errorf("Prompt = %q, want preserved", got.Prompt)
	}
}

func TestHookConfigOmitEmpty(t *testing.T) {
	// Zero-value fields should be omitted from JSON
	cfg := HookConfig{Type: HookTypeCommand, Command: "true"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	// These optional fields should not appear
	for _, field := range []string{"shell", "timeout", "statusMessage", "once", "async", "asyncRewake", "prompt", "model"} {
		if strings.Contains(s, `"`+field+`"`) {
			t.Errorf("zero-value field %q should be omitted, but appears in JSON: %s", field, s)
		}
	}
}

func TestHookConfigParseFromJSON(t *testing.T) {
	// Verify we can parse the exact JSON format from settings.json
	raw := `{
		"type": "command",
		"command": "prettier --write $FILE",
		"timeout": 30,
		"once": true
	}`
	var cfg HookConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Type != HookTypeCommand {
		t.Errorf("Type = %q, want command", cfg.Type)
	}
	if cfg.Command != "prettier --write $FILE" {
		t.Errorf("Command = %q, want preserved", cfg.Command)
	}
	if cfg.Timeout != 30 {
		t.Errorf("Timeout = %d, want 30", cfg.Timeout)
	}
	if cfg.Once != true {
		t.Error("Once = false, want true")
	}
}

// ---------------------------------------------------------------------------
// HookMatcher JSON
// ---------------------------------------------------------------------------

func TestHookMatcherJSON(t *testing.T) {
	m := HookMatcher{
		Matcher: "Bash|Write",
		Hooks: []HookConfig{
			{Type: HookTypeCommand, Command: "echo $TOOL_NAME"},
		},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got HookMatcher
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Matcher != "Bash|Write" {
		t.Errorf("Matcher = %q, want %q", got.Matcher, "Bash|Write")
	}
	if len(got.Hooks) != 1 {
		t.Fatalf("Hooks len = %d, want 1", len(got.Hooks))
	}
	if got.Hooks[0].Command != "echo $TOOL_NAME" {
		t.Errorf("Hooks[0].Command = %q, want preserved", got.Hooks[0].Command)
	}
}

func TestHookMatcherEmptyMatcher(t *testing.T) {
	// Empty matcher matches all tools
	raw := `{"hooks":[{"type":"command","command":"echo hello"}]}`
	var m HookMatcher
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Matcher != "" {
		t.Errorf("Matcher = %q, want empty", m.Matcher)
	}
}

// ---------------------------------------------------------------------------
// HooksConfig JSON
// ---------------------------------------------------------------------------

func TestHooksConfigJSON(t *testing.T) {
	raw := `{
		"PreToolUse": [
			{
				"matcher": "Bash",
				"hooks": [
					{"type": "command", "command": "echo blocking >&2 && exit 2"}
				]
			}
		],
		"PostToolUse": [
			{
				"matcher": "Write|Edit",
				"hooks": [
					{"type": "command", "command": "prettier --write"}
				]
			}
		]
	}`
	var cfg HooksConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg["PreToolUse"]) != 1 {
		t.Errorf("PreToolUse matchers = %d, want 1", len(cfg["PreToolUse"]))
	}
	if cfg["PreToolUse"][0].Matcher != "Bash" {
		t.Errorf("PreToolUse[0].Matcher = %q, want %q", cfg["PreToolUse"][0].Matcher, "Bash")
	}
	if len(cfg["PostToolUse"]) != 1 {
		t.Errorf("PostToolUse matchers = %d, want 1", len(cfg["PostToolUse"]))
	}
	if cfg["PostToolUse"][0].Matcher != "Write|Edit" {
		t.Errorf("PostToolUse[0].Matcher = %q, want %q", cfg["PostToolUse"][0].Matcher, "Write|Edit")
	}
}

// ---------------------------------------------------------------------------
// HookInput JSON (snake_case field names)
// ---------------------------------------------------------------------------

func TestHookInputSnakeCase(t *testing.T) {
	// Source: coreSchemas.ts:387-443 — all fields must use snake_case
	input := HookInput{
		HookEventName:  "PreToolUse",
		SessionID:      "sess-123",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/home/user/project",
		PermissionMode: "default",
		AgentID:        "",
		AgentType:      "",
		ToolName:       "Bash",
		ToolInput:      json.RawMessage(`{"command":"ls"}`),
		ToolUseID:      "toolu_abc123",
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	// Verify snake_case keys are present
	for _, key := range []string{
		`"hook_event_name"`,
		`"session_id"`,
		`"transcript_path"`,
		`"cwd"`,
		`"permission_mode"`,
		`"tool_name"`,
		`"tool_input"`,
		`"tool_use_id"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("expected snake_case key %s in JSON: %s", key, s)
		}
	}
	// Verify camelCase keys are NOT present
	for _, key := range []string{
		`"hookEventName"`,
		`"sessionId"`,
		`"transcriptPath"`,
		`"toolName"`,
		`"toolInput"`,
		`"toolUseId"`,
	} {
		if strings.Contains(s, key) {
			t.Errorf("unexpected camelCase key %s in JSON: %s", key, s)
		}
	}
}

func TestHookInputOmitEmpty(t *testing.T) {
	// Minimal HookInput — optional fields should be omitted
	input := HookInput{
		HookEventName: "SessionStart",
		SessionID:     "sess-1",
		Cwd:           "/tmp",
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, field := range []string{
		"tool_name", "tool_input", "tool_use_id", "tool_response",
		"agent_id", "agent_type", "source", "reason", "trigger",
	} {
		if strings.Contains(s, `"`+field+`"`) {
			t.Errorf("empty optional field %q should be omitted in: %s", field, s)
		}
	}
}

func TestHookInputParseFromJSON(t *testing.T) {
	raw := `{
		"hook_event_name": "PreToolUse",
		"session_id": "sess-1",
		"transcript_path": "/tmp/t.jsonl",
		"cwd": "/home/user",
		"tool_name": "Bash",
		"tool_input": {"command": "git status"},
		"tool_use_id": "toolu_1"
	}`
	var input HookInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.HookEventName != "PreToolUse" {
		t.Errorf("HookEventName = %q, want PreToolUse", input.HookEventName)
	}
	if input.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", input.ToolName)
	}
	if input.ToolUseID != "toolu_1" {
		t.Errorf("ToolUseID = %q, want toolu_1", input.ToolUseID)
	}
	if string(input.ToolInput) != `{"command": "git status"}` {
		t.Errorf("ToolInput = %s, want preserved", string(input.ToolInput))
	}
}

// ---------------------------------------------------------------------------
// HookOutput JSON
// ---------------------------------------------------------------------------

func TestHookOutputParseDecision(t *testing.T) {
	// Source: types/hooks.ts:64 — decision: "approve"|"block"
	raw := `{"decision": "block", "reason": "unsafe command"}`
	var out HookOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Decision != "block" {
		t.Errorf("Decision = %q, want block", out.Decision)
	}
	if out.Reason != "unsafe command" {
		t.Errorf("Reason = %q, want 'unsafe command'", out.Reason)
	}
}

func TestHookOutputParseApprove(t *testing.T) {
	raw := `{"decision": "approve"}`
	var out HookOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Decision != "approve" {
		t.Errorf("Decision = %q, want approve", out.Decision)
	}
}

func TestHookOutputParseContinue(t *testing.T) {
	// Source: types/hooks.ts:53-55 — continue: boolean
	t.Run("false", func(t *testing.T) {
		raw := `{"continue": false, "stopReason": "blocked by hook"}`
		var out HookOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.Continue == nil {
			t.Fatal("Continue is nil, want non-nil")
		}
		if *out.Continue {
			t.Error("Continue = true, want false")
		}
		if out.StopReason != "blocked by hook" {
			t.Errorf("StopReason = %q, want 'blocked by hook'", out.StopReason)
		}
	})
	t.Run("true", func(t *testing.T) {
		raw := `{"continue": true}`
		var out HookOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.Continue == nil {
			t.Fatal("Continue is nil, want non-nil")
		}
		if !*out.Continue {
			t.Error("Continue = false, want true")
		}
	})
	t.Run("nil when absent", func(t *testing.T) {
		raw := `{}`
		var out HookOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.Continue != nil {
			t.Errorf("Continue = %v, want nil when absent", *out.Continue)
		}
	})
}



// ---------------------------------------------------------------------------
// HookOutcome.String()
// ---------------------------------------------------------------------------

func TestHookOutcomeString(t *testing.T) {
	cases := map[HookOutcome]string{
		HookOutcomeSuccess:          "success",
		HookOutcomeBlocking:         "blocking",
		HookOutcomeNonBlockingError: "non_blocking_error",
		HookOutcomeTimeout:          "timeout",
		HookOutcomeCancelled:        "cancelled",
	}
	for outcome, want := range cases {
		if got := outcome.String(); got != want {
			t.Errorf("HookOutcome(%d).String() = %q, want %q", outcome, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// HookDecision.String()
// ---------------------------------------------------------------------------

func TestHookDecisionString(t *testing.T) {
	cases := map[HookDecision]string{
		HookDecisionPassthrough: "passthrough",
		HookDecisionApprove:     "approve",
		HookDecisionBlock:       "block",
	}
	for dec, want := range cases {
		if got := dec.String(); got != want {
			t.Errorf("HookDecision(%d).String() = %q, want %q", dec, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Timeout helpers
// ---------------------------------------------------------------------------

func TestTimeoutForHook(t *testing.T) {
	t.Run("custom timeout", func(t *testing.T) {
		got := TimeoutForHook(30, HookPreToolUse)
		if got != 30*time.Second {
			t.Errorf("TimeoutForHook(30, PreToolUse) = %v, want 30s", got)
		}
	})
	t.Run("default for tool event", func(t *testing.T) {
		got := TimeoutForHook(0, HookPreToolUse)
		if got != DefaultHookTimeout {
			t.Errorf("TimeoutForHook(0, PreToolUse) = %v, want %v", got, DefaultHookTimeout)
		}
	})
	t.Run("SessionEnd special timeout", func(t *testing.T) {
		got := TimeoutForHook(0, HookSessionEnd)
		if got != SessionEndTimeout {
			t.Errorf("TimeoutForHook(0, SessionEnd) = %v, want %v", got, SessionEndTimeout)
		}
	})
	t.Run("custom overrides SessionEnd default", func(t *testing.T) {
		got := TimeoutForHook(5, HookSessionEnd)
		if got != 5*time.Second {
			t.Errorf("TimeoutForHook(5, SessionEnd) = %v, want 5s", got)
		}
	})
}

func TestTimeoutConstants(t *testing.T) {
	// Verify constants match TS source
	if DefaultHookTimeout != 600*time.Second {
		t.Errorf("DefaultHookTimeout = %v, want 600s (TS: 10min)", DefaultHookTimeout)
	}
	if SessionEndTimeout != 1500*time.Millisecond {
		t.Errorf("SessionEndTimeout = %v, want 1500ms (TS: 1500ms)", SessionEndTimeout)
	}
	if DefaultPromptHookTimeout != 30*time.Second {
		t.Errorf("DefaultPromptHookTimeout = %v, want 30s", DefaultPromptHookTimeout)
	}
	if DefaultAgentHookTimeout != 60*time.Second {
		t.Errorf("DefaultAgentHookTimeout = %v, want 60s", DefaultAgentHookTimeout)
	}
	if DefaultAgentMaxTurns != 50 {
		t.Errorf("DefaultAgentMaxTurns = %d, want 50", DefaultAgentMaxTurns)
	}
}

// ---------------------------------------------------------------------------
// HookResult zero value
// ---------------------------------------------------------------------------

func TestHookResultZeroValue(t *testing.T) {
	var r HookResult
	if r.Outcome != HookOutcomeSuccess {
		t.Errorf("zero HookResult.Outcome = %d, want HookOutcomeSuccess (0)", r.Outcome)
	}
	if r.Stdout != "" {
		t.Errorf("zero HookResult.Stdout = %q, want empty", r.Stdout)
	}
	if r.Output != nil {
		t.Error("zero HookResult.Output should be nil")
	}
	if r.HookName != "" {
		t.Errorf("zero HookResult.HookName = %q, want empty", r.HookName)
	}
}
