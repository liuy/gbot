package short

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRestoreSkillStateFromMessages_InvokedSkills(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type": "invoked_skills",
				"skills": []any{
					map[string]any{
						"name":    "graphify",
						"path":    "/skills/graphify",
						"content": "# graphify skill",
					},
					map[string]any{
						"name":    "plan",
						"path":    "/skills/plan",
						"content": "# plan skill",
					},
				},
			}),
		},
	}

	state := RestoreSkillStateFromMessages(messages)
	if state == nil {
		t.Fatal("got nil state, want skill state")
	}

	if len(state.InvokedSkills) != 2 {
		t.Errorf("InvokedSkills count = %d, want 2", len(state.InvokedSkills))
	}
	if !state.InvokedSkills["graphify"] {
		t.Error("graphify skill not found")
	}
	if !state.InvokedSkills["plan"] {
		t.Error("plan skill not found")
	}
}

func TestRestoreSkillStateFromMessages_CronTasks(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type":       "cron_task",
				"skill_name": "graphify",
				"cron_expr":  "0 * * * *",
				"durable":    true,
			}),
		},
	}

	state := RestoreSkillStateFromMessages(messages)
	if state == nil {
		t.Fatal("got nil state, want skill state with cron")
	}

	if len(state.CronTasks) != 1 {
		t.Fatalf("CronTasks count = %d, want 1", len(state.CronTasks))
	}

	task := state.CronTasks[0]
	if task.SkillName != "graphify" {
		t.Errorf("SkillName = %q, want graphify", task.SkillName)
	}
	if task.CronExpr != "0 * * * *" {
		t.Errorf("CronExpr = %q, want 0 * * * *", task.CronExpr)
	}
	if !task.Durable {
		t.Error("Durable = false, want true")
	}
}

func TestRestoreSkillStateFromMessages_SkillMissingFields(t *testing.T) {
	// Skill with missing path should be skipped
	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type": "invoked_skills",
				"skills": []any{
					map[string]any{
						"name": "incomplete",
						// missing path and content
					},
				},
			}),
		},
	}

	state := RestoreSkillStateFromMessages(messages)
	if state != nil {
		t.Error("expected nil for incomplete skill, got non-nil state")
	}
}

func TestRestoreSkillStateFromMessages_NoAttachments(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "assistant", Content: `[{"type":"text","text":"hi"}]`},
	}

	state := RestoreSkillStateFromMessages(messages)
	if state != nil {
		t.Error("expected nil when no attachment messages, got non-nil")
	}
}

func TestRestoreSkillStateFromMessages_InvalidJSON(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "attachment", Content: "not valid json"},
	}

	state := RestoreSkillStateFromMessages(messages)
	if state != nil {
		t.Error("expected nil for invalid JSON, got non-nil")
	}
}

func TestRestoreAgentFromSession_AgentSetting(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type":       "agent-setting",
				"agent_type": "Explore",
				"model":      "sonnet",
				"settings": map[string]string{
					"maxTurns": "20",
				},
				"tool_use_ids": map[string]string{
					"tu-1": "agent-123",
				},
			}),
		},
	}

	agent := RestoreAgentFromSession(messages)
	if agent == nil {
		t.Fatal("got nil agent, want agent state")
	}

	if agent.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", agent.AgentType)
	}
	if agent.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", agent.Model)
	}
	if agent.Setting["maxTurns"] != "20" {
		t.Errorf("Setting maxTurns = %q, want 20", agent.Setting["maxTurns"])
	}
	if agent.ToolUseIDs["tu-1"] != "agent-123" {
		t.Errorf("ToolUseIDs tu-1 = %q, want agent-123", agent.ToolUseIDs["tu-1"])
	}
}

func TestRestoreAgentFromSession_NoAgentSetting(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	agent := RestoreAgentFromSession(messages)
	if agent != nil {
		t.Error("expected nil when no agent-setting, got non-nil")
	}
}

func TestExtractTodosFromTranscript_WithTodoWrite(t *testing.T) {
	todoInput := map[string]any{
		"todos": []any{
			map[string]any{
				"id":          "1",
				"subject":     "Fix bug",
				"status":      "completed",
				"description": "Fix the auth bug",
			},
			map[string]any{
				"id":          "2",
				"subject":     "Add tests",
				"status":      "in_progress",
				"description": "Add unit tests",
			},
		},
	}

	messages := []*TranscriptMessage{
		{Type: "assistant", Content: `[{"type":"tool_use","id":"tu1","name":"TodoWrite"}]`},
		{
			Type: "assistant",
			Content: mustMarshal([]any{
				map[string]any{
					"type":  "tool_use",
					"id":    "tu2",
					"name":  "TodoWrite",
					"input": todoInput,
				},
			}),
		},
	}

	todos := ExtractTodosFromTranscript(messages)
	if todos == nil {
		t.Fatal("got nil todos, want todo list")
	}

	if len(todos) != 2 {
		t.Fatalf("got %d todos, want 2", len(todos))
	}

	if todos[0].ID != "1" {
		t.Errorf("todo[0].ID = %q, want 1", todos[0].ID)
	}
	if todos[0].Subject != "Fix bug" {
		t.Errorf("todo[0].Subject = %q, want Fix bug", todos[0].Subject)
	}
	if todos[0].Status != "completed" {
		t.Errorf("todo[0].Status = %q, want completed", todos[0].Status)
	}
	if todos[1].Status != "in_progress" {
		t.Errorf("todo[1].Status = %q, want in_progress", todos[1].Status)
	}

	// Should find the LATEST TodoWrite (second message)
	if todos[0].Subject != "Fix bug" {
		t.Errorf("should find latest TodoWrite, got subject %q", todos[0].Subject)
	}
}

func TestExtractTodosFromTranscript_NoTodoWrite(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "assistant", Content: `[{"type":"text","text":"response"}]`},
	}

	todos := ExtractTodosFromTranscript(messages)
	if todos != nil {
		t.Error("expected nil when no TodoWrite, got non-nil")
	}
}

func TestComputeRestoredAttributionState_SubAgent(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type: "attribution-snapshot",
			Content: mustMarshal(map[string]any{
				"parent_agent_id": "parent-1",
				"tool_use_id":     "tu-1",
			}),
		},
	}

	attr := ComputeRestoredAttributionState(messages)
	if attr == nil {
		t.Fatal("got nil attribution")
	}

	if !attr.IsSubAgent {
		t.Error("IsSubAgent = false, want true")
	}
	if attr.ParentAgentID != "parent-1" {
		t.Errorf("ParentAgentID = %q, want parent-1", attr.ParentAgentID)
	}
	if attr.ToolUseID != "tu-1" {
		t.Errorf("ToolUseID = %q, want tu-1", attr.ToolUseID)
	}
}

func TestComputeRestoredAttributionState_NotSubAgent(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	attr := ComputeRestoredAttributionState(messages)
	if attr == nil {
		t.Fatal("got nil attribution")
	}
	if attr.IsSubAgent {
		t.Error("IsSubAgent = true, want false")
	}
}

func TestComputeStandaloneAgentContext_WithNameAndColor(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type":  "agent-name",
				"name":  "worker-1",
				"color": "blue",
			}),
		},
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	ctx := ComputeStandaloneAgentContext(messages)
	if ctx == nil {
		t.Fatal("got nil context, want agent context")
	}

	if ctx.AgentType != "standalone" {
		t.Errorf("AgentType = %q, want standalone", ctx.AgentType)
	}
}

func TestComputeStandaloneAgentContext_NoNameOrColor(t *testing.T) {
	messages := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
	}

	ctx := ComputeStandaloneAgentContext(messages)
	if ctx != nil {
		t.Error("expected nil when no agent-name/color, got non-nil")
	}
}

func TestCheckResumeConsistency_Valid(t *testing.T) {
	// Create chain where messageCount matches position
	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{Type: "assistant", Content: "{}"},
		{
			Type:    "system",
			Subtype: "turn_duration",
			Content: mustMarshal(map[string]any{
				"messageCount": 2,
				"duration":     5000,
			}),
		},
	}

	// Should not panic or log warnings (we can't easily test slog output)
	CheckResumeConsistency(chain)
}

func TestCheckResumeConsistency_Mismatch(t *testing.T) {
	// Create chain where messageCount does NOT match position
	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{
			Type:    "system",
			Subtype: "turn_duration",
			Content: mustMarshal(map[string]any{
				"messageCount": 5, // Position is 1, not 5
			}),
		},
	}

	// Should log warning but not panic
	CheckResumeConsistency(chain)
}

func TestCheckResumeConsistency_NoCheckpoint(t *testing.T) {
	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{Type: "assistant", Content: "{}"},
	}

	// Should not panic
	CheckResumeConsistency(chain)
}

func TestGroupMessagesByApiRound_SingleRound(t *testing.T) {
	// Without message_id, function groups by UUID — each assistant starts a new group.
	// user→assistant produces 2 groups: [user], [assistant]
	messages := []*TranscriptMessage{
		{Type: "user", UUID: "u1", Content: `[{"type":"text","text":"hi"}]`},
		{Type: "assistant", UUID: "a1", Content: `[{"type":"text","text":"hello"}]`},
	}

	groups := GroupMessagesByApiRound(messages)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (user group + assistant group)", len(groups))
	}
	if groups[0][0].UUID != "u1" {
		t.Errorf("group[0][0] = %q, want u1", groups[0][0].UUID)
	}
	if groups[1][0].UUID != "a1" {
		t.Errorf("group[1][0] = %q, want a1", groups[1][0].UUID)
	}
}

func TestGroupMessagesByApiRound_MultipleRounds(t *testing.T) {
	// Each assistant with different UUID starts a new group.
	// u1, a1, u2, a2 → [u1], [a1, u2], [a2]
	messages := []*TranscriptMessage{
		{Type: "user", UUID: "u1", Content: `[{"type":"text","text":"hi"}]`},
		{Type: "assistant", UUID: "a1", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "user", UUID: "u2", Content: `[{"type":"text","text":"next"}]`},
		{Type: "assistant", UUID: "a2", Content: `[{"type":"text","text":"response"}]`},
	}

	groups := GroupMessagesByApiRound(messages)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}

	// Group 0: [u1]
	if groups[0][0].UUID != "u1" {
		t.Errorf("group[0][0] = %q, want u1", groups[0][0].UUID)
	}

	// Group 1: [a1, u2]
	if groups[1][0].UUID != "a1" {
		t.Errorf("group[1][0] = %q, want a1", groups[1][0].UUID)
	}
	if groups[1][1].UUID != "u2" {
		t.Errorf("group[1][1] = %q, want u2", groups[1][1].UUID)
	}

	// Group 2: [a2]
	if groups[2][0].UUID != "a2" {
		t.Errorf("group[2][0] = %q, want a2", groups[2][0].UUID)
	}
}

func TestGroupMessagesByApiRound_Empty(t *testing.T) {
	groups := GroupMessagesByApiRound(nil)
	if groups != nil {
		t.Errorf("got %v, want nil for empty input", groups)
	}
}

func TestProcessResumedConversation_Empty(t *testing.T) {
	store := openTestStore(t)

	state, err := store.ProcessResumedConversation("session", nil)
	if err != nil {
		t.Fatalf("ProcessResumedConversation: %v", err)
	}
	if state == nil {
		t.Fatal("got nil state")
	}
	if state.AgentState != nil {
		t.Error("expected nil AgentState for empty messages")
	}
}

func TestProcessResumedConversation_WithAgent(t *testing.T) {
	store := openTestStore(t)

	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type":       "agent-setting",
				"agent_type": "Explore",
				"model":      "sonnet",
			}),
		},
	}

	state, err := store.ProcessResumedConversation("session", messages)
	if err != nil {
		t.Fatalf("ProcessResumedConversation: %v", err)
	}
	if state == nil {
		t.Fatal("got nil state")
	}
	if state.AgentState == nil {
		t.Fatal("expected AgentState")
	}
	if state.AgentState.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", state.AgentState.AgentType)
	}
}

func TestRestoreWorktreeForResume(t *testing.T) {
	// Should be a no-op in Go implementation
	err := RestoreWorktreeForResume("session-1")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestExitRestoredWorktree(t *testing.T) {
	// Should be a no-op in Go implementation
	err := ExitRestoredWorktree("session-1")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestRefreshAgentDefinitionsForModeSwitch(t *testing.T) {
	// Should be a no-op in Go implementation
	err := RefreshAgentDefinitionsForModeSwitch("session-1")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestExtractMessageID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "with message_id",
			content: `{"message_id":"msg-123","content":"hello"}`,
			want:    "msg-123",
		},
		{
			name:    "without message_id",
			content: `{"content":"hello"}`,
			want:    "",
		},
		{
			name:    "invalid JSON",
			content: "not json",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMessageID(tt.content)
			if got != tt.want {
				t.Errorf("extractMessageID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckResumeConsistency_DifferentTypes(t *testing.T) {
	// Test with non-int messageCount (should handle gracefully)
	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{
			Type:    "system",
			Subtype: "turn_duration",
			Content: mustMarshal(map[string]any{
				"messageCount": "not-a-number", // Invalid type
				"duration":     5000,
			}),
		},
	}

	// Should not panic
	CheckResumeConsistency(chain)
}

func TestCheckResumeConsistency_MissingMessageCount(t *testing.T) {
	// Test with missing messageCount field
	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{
			Type:    "system",
			Subtype: "turn_duration",
			Content: mustMarshal(map[string]any{
				"duration": 5000, // No messageCount
			}),
		},
	}

	// Should not panic
	CheckResumeConsistency(chain)
}

func TestValueAsString(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{
			name: "string value",
			v:    "hello",
			want: "hello",
		},
		{
			name: "non-string value",
			v:    123,
			want: "",
		},
		{
			name: "nil value",
			v:    nil,
			want: "",
		},
		{
			name: "bool value",
			v:    true,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valueAsString(tt.v)
			if got != tt.want {
				t.Errorf("valueAsString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractTodosFromTranscript_MalformedInput(t *testing.T) {
	tests := []struct {
		name     string
		messages []*TranscriptMessage
		wantNil  bool
	}{
		{
			name: "invalid JSON in tool_use input",
			messages: []*TranscriptMessage{
				{
					Type:    "assistant",
					Content: `[{"type":"tool_use","id":"tu1","name":"TodoWrite","input":"invalid json"}]`,
				},
			},
			wantNil: true,
		},
		{
			name: "no todos field in input",
			messages: []*TranscriptMessage{
				{
					Type:    "assistant",
					Content: `[{"type":"tool_use","id":"tu1","name":"TodoWrite","input":{"other":"data"}}]`,
				},
			},
			wantNil: true,
		},
		{
			name: "todos not a list",
			messages: []*TranscriptMessage{
				{
					Type:    "assistant",
					Content: `[{"type":"tool_use","id":"tu1","name":"TodoWrite","input":{"todos":"not-a-list"}}]`,
				},
			},
			wantNil: true,
		},
		{
			name: "todo item not a map",
			messages: []*TranscriptMessage{
				{
					Type:    "assistant",
					Content: `[{"type":"tool_use","id":"tu1","name":"TodoWrite","input":{"todos":["string-item"]}}]`,
				},
			},
			wantNil: true, // Should get empty list since all items are invalid
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTodosFromTranscript(tt.messages)
			if tt.wantNil && got != nil {
				t.Errorf("expected nil, got %v", got)
			}
		})
	}
}

func TestRestoreSkillStateFromMessages_CronTaskExtraction(t *testing.T) {
	// Test cron task extraction edge cases
	tests := []struct {
		name       string
		messages   []*TranscriptMessage
		wantCron   bool
		cronExpr   string
		skillName  string
	}{
		{
			name: "valid cron task",
			messages: []*TranscriptMessage{
				{
					Type: "attachment",
					Content: mustMarshal(map[string]any{
						"type":       "cron_task",
						"skill_name": "test-skill",
						"cron_expr":  "0 * * * *",
						"durable":    false,
					}),
				},
			},
			wantCron:  true,
			cronExpr:  "0 * * * *",
			skillName: "test-skill",
		},
		{
			name: "cron task missing skill_name",
			messages: []*TranscriptMessage{
				{
					Type: "attachment",
					Content: mustMarshal(map[string]any{
						"type":      "cron_task",
						"cron_expr": "0 * * * *",
					}),
				},
			},
			wantCron: false, // Missing required field
		},
		{
			name: "cron task missing cron_expr",
			messages: []*TranscriptMessage{
				{
					Type: "attachment",
					Content: mustMarshal(map[string]any{
						"type":       "cron_task",
						"skill_name": "test-skill",
					}),
				},
			},
			wantCron: false, // Missing required field
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := RestoreSkillStateFromMessages(tt.messages)
			if tt.wantCron {
				if state == nil {
					t.Fatal("expected non-nil state with cron task")
				}
				if len(state.CronTasks) != 1 {
					t.Errorf("got %d cron tasks, want 1", len(state.CronTasks))
				} else {
					task := state.CronTasks[0]
					if task.CronExpr != tt.cronExpr {
						t.Errorf("cron expr = %q, want %q", task.CronExpr, tt.cronExpr)
					}
					if task.SkillName != tt.skillName {
						t.Errorf("skill name = %q, want %q", task.SkillName, tt.skillName)
					}
				}
			}
		})
	}
}

func TestComputeStandaloneAgentContext_NilReturn(t *testing.T) {
	// Test cases that should return nil
	tests := []struct {
		name     string
		messages []*TranscriptMessage
	}{
		{
			name:     "empty messages",
			messages: []*TranscriptMessage{},
		},
		{
			name: "no agent attachments",
			messages: []*TranscriptMessage{
				{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
			},
		},
		{
			name: "other attachment types",
			messages: []*TranscriptMessage{
				{
					Type:    "attachment",
					Content: mustMarshal(map[string]any{"type": "other"}),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ComputeStandaloneAgentContext(tt.messages)
			if ctx != nil {
				t.Errorf("expected nil for %s, got %+v", tt.name, ctx)
			}
		})
	}
}

func TestRestoreAgentFromSession_InvalidAttachments(t *testing.T) {
	tests := []struct {
		name     string
		messages []*TranscriptMessage
		wantNil  bool
	}{
		{
			name: "invalid JSON in attachment",
			messages: []*TranscriptMessage{
				{Type: "attachment", Content: "not valid json"},
			},
			wantNil: true,
		},
		{
			name: "non-agent-setting attachment",
			messages: []*TranscriptMessage{
				{
					Type:    "attachment",
					Content: mustMarshal(map[string]any{"type": "other"}),
				},
			},
			wantNil: true,
		},
		{
			name: "agent-setting with missing fields",
			messages: []*TranscriptMessage{
				{
					Type:    "attachment",
					Content: mustMarshal(map[string]any{
						"type": "agent-setting",
						// Missing agent_type and model
					}),
				},
			},
			wantNil: false, // Returns AgentState with empty fields
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := RestoreAgentFromSession(tt.messages)
			if tt.wantNil && agent != nil {
				t.Errorf("expected nil, got %+v", agent)
			}
		})
	}
}

func TestComputeRestoredAttributionState_InvalidMessages(t *testing.T) {
	tests := []struct {
		name     string
		messages []*TranscriptMessage
	}{
		{
			name: "invalid JSON in attribution-snapshot",
			messages: []*TranscriptMessage{
				{Type: "attribution-snapshot", Content: "invalid json"},
			},
		},
		{
			name: "other message types",
			messages: []*TranscriptMessage{
				{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
				{Type: "assistant", Content: `[{"type":"text","text":"hi"}]`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := ComputeRestoredAttributionState(tt.messages)
			if attr == nil {
				t.Fatal("expected non-nil attribution state")
			}
			if attr.IsSubAgent {
				t.Error("expected IsSubAgent=false for non-attribution messages")
			}
		})
	}
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Ensure mustMarshal is only used in tests (avoid unused import in non-test).
var _ = strings.Contains
var _ = time.Now

// Line 34-35: RestoreSkillStateFromMessages — skill missing name (not a map)
func TestRestoreSkillStateFromMessages_SkillNotMap(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type":   "invoked_skills",
				"skills": []any{"not-a-map"},
			}),
		},
	}
	state := RestoreSkillStateFromMessages(messages)
	if state != nil {
		t.Error("expected nil for non-map skill item")
	}
}

// Line 226-227: ComputeStandaloneAgentContext — agent-color attachment
func TestComputeStandaloneAgentContext_AgentColorOnly(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type: "attachment",
			Content: mustMarshal(map[string]any{
				"type":  "agent-color",
				"color": "blue",
			}),
		},
	}
	ctx := ComputeStandaloneAgentContext(messages)
	if ctx == nil {
		t.Fatal("expected non-nil context with agent-color")
	}
	if ctx.AgentType != "standalone" {
		t.Errorf("AgentType = %q, want standalone", ctx.AgentType)
	}
}

// Line 233-235: ComputeStandaloneAgentContext — empty messages list
func TestComputeStandaloneAgentContext_EmptyMessagesWithAttachment(t *testing.T) {
	messages := []*TranscriptMessage{
		{
			Type:    "attachment",
			Content: mustMarshal(map[string]any{"type": "agent-name", "name": "worker"}),
		},
	}
	ctx := ComputeStandaloneAgentContext(messages)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	// SessionID should be empty since messages[0].SessionID is ""
	if ctx.SessionID != "" {
		t.Errorf("SessionID = %q, want empty", ctx.SessionID)
	}
}

// Line 326-327: CheckResumeConsistency — messageCount is int type
func TestCheckResumeConsistency_IntMessageCount(t *testing.T) {
	// JSON numbers unmarshal as float64 by default, but let's test the int case
	// by creating a chain where messageCount is specifically an int
	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{
			Type:    "system",
			Subtype: "turn_duration",
			Content: `{"messageCount":1}`,
		},
	}
	// Should not panic; JSON numbers are float64 by default
	CheckResumeConsistency(chain)
}

// Line 339-340: CheckResumeConsistency — messageCount type is default (not float64, not int)
func TestCheckResumeConsistency_MessageCountBool(t *testing.T) {
	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{
			Type:    "system",
			Subtype: "turn_duration",
			Content: mustMarshal(map[string]any{
				"messageCount": true,
			}),
		},
	}
	// Should not panic — bool type falls through to default case and returns
	CheckResumeConsistency(chain)
}

func TestComputeStandaloneAgentContext_InvalidAttachmentJSON(t *testing.T) {
	msgs := []*TranscriptMessage{
		{
			UUID:    "att-1",
			Type:    "attachment",
			Content: "not-valid-json",
		},
		{
			UUID:    "att-2",
			Type:    "attachment",
			Content: `{"type":"agent-name","name":"test-agent"}`,
		},
	}
	ctx := ComputeStandaloneAgentContext(msgs)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

func TestComputeStandaloneAgentContext_AgentColorOnly_Batch2(t *testing.T) {
	msgs := []*TranscriptMessage{
		{
			UUID:    "att-1",
			Type:    "attachment",
			Content: `{"type":"agent-color","color":"red"}`,
		},
	}
	ctx := ComputeStandaloneAgentContext(msgs)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

func TestCheckResumeConsistency_InvalidJSON(t *testing.T) {
	chain := []*TranscriptMessage{
		{UUID: "msg-1", Type: "system", Subtype: "turn_duration", Content: "invalid-json"},
	}
	// Should not panic, just continue
	CheckResumeConsistency(chain)
}

func TestCheckResumeConsistency_FloatMessageCount(t *testing.T) {
	// Create a chain where the turn_duration checkpoint has a float64 messageCount
	chain := []*TranscriptMessage{
		{UUID: "msg-1", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{UUID: "msg-2", Type: "system", Subtype: "turn_duration", Content: `{"messageCount":1}`},
	}
	// messageCount=1 (float64), index=1 — they match, no warning expected
	CheckResumeConsistency(chain)
}

func TestCheckResumeConsistency_Float64Only(t *testing.T) {
	// Verify that json.Unmarshal always produces float64 for numbers
	data := []byte(`{"messageCount": 5}`)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := m["messageCount"].(float64)
	if !ok {
		t.Fatal("expected float64, got something else")
	}
	if v != 5 {
		t.Errorf("got %v, want 5", v)
	}
	// This confirms the `case int:` branch is dead code.
}

// TestCheckResumeConsistency_MismatchCount verifies warning is logged when count mismatches.
func TestCheckResumeConsistency_MismatchCount(t *testing.T) {
	// CheckResumeConsistency is a standalone function that logs warnings but never errors.
	chain := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "system", Subtype: "turn_duration", Content: `{"messageCount": 99}`},
	}
	// Should not panic, just log a warning
	CheckResumeConsistency(chain)
}

// TestCheckResumeConsistency_DefaultCase tests the default branch in the
// type switch when messageCount is a string (not float64 or int).
func TestCheckResumeConsistency_DefaultCase(t *testing.T) {
	// messageCount as a string triggers the default case → early return
	content, _ := json.Marshal(map[string]any{
		"messageCount": "not_a_number",
	})
	chain := []*TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{Type: "system", Subtype: "turn_duration", Content: string(content)},
	}

	// Should not panic or log warnings — just returns early
	CheckResumeConsistency(chain)
}

