package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func TestForkRegistry_SpawnAndWait(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	state, err := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "done", AgentType: "fork"}, nil
		},
		nil, "test task", "call_1",
	)
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if state.Status != ForkRunning {
		t.Errorf("initial Status = %q, want %q", state.Status, ForkRunning)
	}

	final, ok := reg.Wait(state.ID)
	if !ok {
		t.Fatal("Wait returned false")
	}
	if final.Status != ForkCompleted {
		t.Errorf("final Status = %q, want %q", final.Status, ForkCompleted)
	}
	if final.Result.Content != "done" {
		t.Errorf("Result.Content = %q, want %q", final.Result.Content, "done")
	}
}

func TestForkRegistry_SpawnWithError(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			return nil, errors.New("boom")
		},
		nil, "failing task", "call_1",
	)

	final, _ := reg.Wait(state.ID)
	if final.Status != ForkFailed {
		t.Errorf("Status = %q, want %q", final.Status, ForkFailed)
	}
}

func TestForkRegistry_Cancel(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	started := make(chan struct{})
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil, "long task", "call_1",
	)

	<-started // wait for goroutine to start
	cancelled := reg.Cancel(state.ID)
	if !cancelled {
		t.Error("Cancel should return true for existing agent")
	}

	final, _ := reg.Wait(state.ID)
	if final.Status != ForkCancelled {
		t.Errorf("Status = %q, want %q", final.Status, ForkCancelled)
	}
}

func TestForkRegistry_CancelNonexistent(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()
	if reg.Cancel("nonexistent") {
		t.Error("Cancel should return false for nonexistent agent")
	}
}

func TestForkRegistry_Get(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "ok"}, nil
		},
		nil, "test", "call_1",
	)

	got, ok := reg.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false")
	}
	if got.ID != state.ID {
		t.Errorf("Get.ID = %q, want %q", got.ID, state.ID)
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("Get should return false for nonexistent agent")
	}
}

func TestForkRegistry_List(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	_, err := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			time.Sleep(100 * time.Millisecond)
			return &types.SubQueryResult{Content: "a"}, nil
		},
		nil, "task a", "call_1",
	)
	if err != nil {
		t.Fatalf("Spawn a: %v", err)
	}
	_, err = reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			time.Sleep(100 * time.Millisecond)
			return &types.SubQueryResult{Content: "b"}, nil
		},
		nil, "task b", "call_2",
	)
	if err != nil {
		t.Fatalf("Spawn b: %v", err)
	}

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(list))
	}
}

func TestForkRegistry_NotifyCalled(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	var mu sync.Mutex
	var notifiedID, notifiedToolUseID string
	var notifiedResult *types.SubQueryResult
	var notifiedErr error

	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "result text", AgentType: "fork"}, nil
		},
		func(agentID, toolUseID string, result *types.SubQueryResult, err error) {
			mu.Lock()
			defer mu.Unlock()
			notifiedID = agentID
			notifiedToolUseID = toolUseID
			notifiedResult = result
			notifiedErr = err
		},
		"notify test", "call_notify",
	)

	reg.Wait(state.ID)

	mu.Lock()
	defer mu.Unlock()
	if notifiedID != state.ID {
		t.Errorf("notify agentID = %q, want %q", notifiedID, state.ID)
	}
	if notifiedToolUseID != "call_notify" {
		t.Errorf("notify toolUseID = %q, want %q", notifiedToolUseID, "call_notify")
	}
	if notifiedResult.Content != "result text" {
		t.Errorf("notify result = %q, want %q", notifiedResult.Content, "result text")
	}
	if notifiedErr != nil {
		t.Errorf("notify err = %v, want nil", notifiedErr)
	}
}

func TestForkRegistry_NotifyOnError(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	var mu sync.Mutex
	var notifiedErr error

	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			return nil, errors.New("agent crashed")
		},
		func(agentID, toolUseID string, result *types.SubQueryResult, err error) {
			mu.Lock()
			defer mu.Unlock()
			notifiedErr = err
		},
		"error test", "call_err",
	)

	reg.Wait(state.ID)

	mu.Lock()
	defer mu.Unlock()
	if notifiedErr == nil {
		t.Fatal("expected error in notification")
	}
	if !strings.Contains(notifiedErr.Error(), "agent crashed") {
		t.Errorf("notified err = %q, want to contain %q", notifiedErr.Error(), "agent crashed")
	}
}

func TestForkRegistry_ContextCancellation(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	ctx, cancel := context.WithCancel(context.Background())

	state, _ := reg.Spawn(ctx,
		func(runCtx context.Context) (*types.SubQueryResult, error) {
			<-runCtx.Done()
			return nil, runCtx.Err()
		},
		nil, "ctx test", "call_ctx",
	)

	// Cancel the parent context — should propagate to the child
	cancel()

	final, _ := reg.Wait(state.ID)
	if final.Status != ForkCancelled {
		t.Errorf("Status = %q, want %q", final.Status, ForkCancelled)
	}
}

func TestBuildForkNotificationXML_Success(t *testing.T) {
	result := &types.SubQueryResult{
		Content:         "found 3 files",
		TotalDurationMs: 1234,
		TotalTokens:     5000,
	}
	xml := buildForkNotificationXML("fork-1", "call_abc", result, nil, "search code", "ship-audit")

	if !strings.Contains(xml, "<task-notification>") {
		t.Error("should contain <task-notification>")
	}
	if !strings.Contains(xml, "<task-id>fork-1</task-id>") {
		t.Error("should contain task-id")
	}
	if !strings.Contains(xml, "<tool-use-id>call_abc</tool-use-id>") {
		t.Error("should contain tool-use-id")
	}
	if !strings.Contains(xml, "<status>completed</status>") {
		t.Error("should contain completed status")
	}
	if !strings.Contains(xml, "found 3 files") {
		t.Error("should contain result content")
	}
	if !strings.Contains(xml, "<duration-ms>1234</duration-ms>") {
		t.Error("should contain duration")
	}
	if !strings.Contains(xml, "<tokens>5000</tokens>") {
		t.Error("should contain tokens")
	}
	if !strings.Contains(xml, "<agent-type>ship-audit</agent-type>") {
		t.Error("should use agent name as agent-type")
	}
	if !strings.Contains(xml, "<summary>Fork agent search code completed</summary>") {
		t.Error("summary should use description, got:", xml)
	}
}

func TestBuildForkNotificationXML_Error(t *testing.T) {
	xml := buildForkNotificationXML("fork-2", "call_err", nil, errors.New("timeout"), "test", "")

	if !strings.Contains(xml, "<status>failed</status>") {
		t.Error("should contain failed status")
	}
	if !strings.Contains(xml, "Error: timeout") {
		t.Error("should contain error message")
	}
}

func TestBuildForkNotificationXML_EscapesSpecialChars(t *testing.T) {
	// XML special characters in description and content must be escaped
	result := &types.SubQueryResult{
		Content:         `<script>alert("xss")</script>`,
		TotalDurationMs: 100,
		TotalTokens:     200,
	}
	xml := buildForkNotificationXML("fork-1", "call_xss", result, nil, `search "code" & <stuff>`, "")

	// Raw <script> should NOT appear — must be escaped to &lt;script&gt;
	if strings.Contains(xml, "<script>") {
		t.Error("content contains unescaped <script> — XML injection vulnerability")
	}
	if !strings.Contains(xml, "&lt;script&gt;") {
		t.Error("content should contain escaped &lt;script&gt;")
	}
	// Description with special chars should also be escaped
	if strings.Contains(xml, `<stuff>`) {
		t.Error("description contains unescaped <stuff>")
	}
	if !strings.Contains(xml, "&lt;stuff&gt;") {
		t.Error("description should contain escaped &lt;stuff&gt;")
	}
	// Quotes in description should be escaped
	if !strings.Contains(xml, "&#34;code&#34;") {
		t.Error("description should contain escaped quotes")
	}
	// Ampersand should be escaped
	if !strings.Contains(xml, "&amp;") {
		t.Error("should contain escaped ampersand")
	}
}

func TestForkRegistry_SpawnNilRunFn(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()
	_, err := reg.Spawn(context.Background(), nil, nil, "test", "call_1")
	if err == nil {
		t.Fatal("expected error for nil runFn, got nil")
	}
	if !strings.Contains(err.Error(), "runFn") {
		t.Errorf("error should mention runFn, got: %v", err)
	}
}

func TestForkRegistry_GetReturnsSnapshot(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			time.Sleep(200 * time.Millisecond)
			return &types.SubQueryResult{Content: "done"}, nil
		},
		nil, "snapshot test", "call_1",
	)

	// Get while running — returned value should be a copy, not the original pointer
	got, ok := reg.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false")
	}
	// Modifying the returned copy should not affect the registry's internal state
	got.Status = ForkCompleted
	got.Result = &types.SubQueryResult{Content: "tampered"}

	// Get again — internal state should be unchanged (still Running)
	got2, _ := reg.Get(state.ID)
	if got2.Status != ForkRunning {
		t.Errorf("internal state was mutated via returned pointer: Status = %q, want %q", got2.Status, ForkRunning)
	}
	if got2.Result != nil {
		t.Error("internal Result was mutated via returned pointer")
	}

	// Wait for completion to clean up
	reg.Wait(state.ID)
}

func TestForkRegistry_CleanupCompleted(t *testing.T) {
	t.Parallel()
	reg := NewForkAgentRegistry()

	// Spawn two agents that complete quickly
	s1, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "a"}, nil
		},
		nil, "task a", "call_1",
	)
	s2, _ := reg.Spawn(context.Background(),
		func(ctx context.Context) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "b"}, nil
		},
		nil, "task b", "call_2",
	)

	// Wait for both to complete
	reg.Wait(s1.ID)
	reg.Wait(s2.ID)

	// Before cleanup: both should be in List
	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 agents before cleanup, got %d", len(list))
	}

	// Cleanup completed agents
	reg.CleanupCompleted()

	// After cleanup: List should be empty
	list = reg.List()
	if len(list) != 0 {
		t.Errorf("expected 0 agents after cleanup, got %d", len(list))
	}

	// Get should return false for cleaned-up agents
	_, ok := reg.Get(s1.ID)
	if ok {
		t.Error("Get should return false for cleaned-up agent")
	}
}

func TestIsInForkChild_NoMarker(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	if IsInForkChild(messages) {
		t.Error("should return false when no fork-boilerplate marker present")
	}
}

func TestIsInForkChild_WithMarker(t *testing.T) {
	t.Parallel()
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("<fork-boilerplate>\nSTOP. READ THIS FIRST.\n</fork-boilerplate>\ndo it")}},
	}
	if !IsInForkChild(messages) {
		t.Error("should return true when fork-boilerplate marker is present in user message")
	}
}

func TestIsInForkChild_MarkerInToolResult(t *testing.T) {
	t.Parallel()
	// Marker in tool_result block (not text) — should NOT be detected
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("id1", json.RawMessage(`"<fork-boilerplate>some output</fork-boilerplate>"`), false),
		}},
	}
	if IsInForkChild(messages) {
		t.Error("should return false — marker in tool_result block, not text block")
	}
}

func TestIsInForkChild_MarkerInAssistantMessage(t *testing.T) {
	t.Parallel()
	// Marker in assistant message — should NOT be detected (only checks user messages)
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("<fork-boilerplate>ignore this</fork-boilerplate>")}},
	}
	if IsInForkChild(messages) {
		t.Error("should return false — marker in assistant message, not user message")
	}
}

func TestIsInForkChild_EmptyMessages(t *testing.T) {
	t.Parallel()
	if IsInForkChild(nil) {
		t.Error("should return false for nil messages")
	}
	if IsInForkChild([]types.Message{}) {
		t.Error("should return false for empty messages")
	}
}
