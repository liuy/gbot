package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// convertState tests
// ---------------------------------------------------------------------------

func TestConvertState_Completed(t *testing.T) {
	t.Parallel()
	s := &BackgroundAgentState{
		ID:          "fork-1",
		Description: "search code",
		Status:      AgentCompleted,
		Result: &types.SubQueryResult{
			Content:         "found 3 files",
			AgentType:       "fork",
			TotalTokens:     5000,
			TotalDurationMs: 1234,
		},
	}
	info := convertState(s)

	if info.ID != "fork-1" {
		t.Errorf("ID = %q, want fork-1", info.ID)
	}
	if info.Type != "fork" {
		t.Errorf("Type = %q, want fork", info.Type)
	}
	if info.Status != "completed" {
		t.Errorf("Status = %q, want completed", info.Status)
	}
	if info.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", info.ExitCode)
	}
	if info.Output != "found 3 files" {
		t.Errorf("Output = %q, want found 3 files", info.Output)
	}
	if info.AgentType != "fork" {
		t.Errorf("AgentType = %q, want fork", info.AgentType)
	}
	if info.Tokens != 5000 {
		t.Errorf("Tokens = %d, want 5000", info.Tokens)
	}
	if info.DurationMs != 1234 {
		t.Errorf("DurationMs = %d, want 1234", info.DurationMs)
	}
}

func TestConvertState_Type(t *testing.T) {
	t.Parallel()
	fork := convertState(&BackgroundAgentState{ID: "fork-7", Status: AgentRunning})
	if fork.Type != "fork" {
		t.Errorf("fork-7 Type = %q, want %q", fork.Type, "fork")
	}
	agent := convertState(&BackgroundAgentState{ID: "agent-3", Status: AgentRunning})
	if agent.Type != "agent" {
		t.Errorf("agent-3 Type = %q, want %q", agent.Type, "agent")
	}
}

func TestConvertState_Running(t *testing.T) {
	t.Parallel()
	s := &BackgroundAgentState{
		ID:     "fork-2",
		Status: AgentRunning,
	}
	info := convertState(s)
	if info.Status != "running" {
		t.Errorf("Status = %q, want running", info.Status)
	}
	if info.Output != "" {
		t.Errorf("Output = %q, want empty for running with no result", info.Output)
	}
}

func TestConvertState_Failed(t *testing.T) {
	t.Parallel()
	s := &BackgroundAgentState{
		ID:     "fork-3",
		Status: AgentFailed,
		Result: &types.SubQueryResult{
			Content: "context canceled",
		},
	}
	info := convertState(s)
	if info.Status != "failed" {
		t.Errorf("Status = %q, want failed", info.Status)
	}
	if info.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", info.ExitCode)
	}
	if info.Output != "context canceled" {
		t.Errorf("Output = %q, want context canceled", info.Output)
	}
}

func TestConvertState_Failed_NilResult(t *testing.T) {
	t.Parallel()
	s := &BackgroundAgentState{
		ID:     "fork-4",
		Status: AgentFailed,
		Result: nil,
	}
	info := convertState(s)
	if info.Status != "failed" {
		t.Errorf("Status = %q, want failed", info.Status)
	}
	if info.Output != "agent failed with no result" {
		t.Errorf("Output = %q, want fallback error message", info.Output)
	}
}

func TestConvertState_Cancelled_MapsToKilled(t *testing.T) {
	t.Parallel()
	s := &BackgroundAgentState{
		ID:     "fork-5",
		Status: AgentCancelled,
	}
	info := convertState(s)
	if info.Status != "killed" {
		t.Errorf("Status = %q, want killed (TS convention)", info.Status)
	}
	if info.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", info.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// Adapter method tests
// ---------------------------------------------------------------------------

func TestAdapter_GetCompleted(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{
				Content:         "done",
				AgentType:       "fork",
				TotalTokens:     100,
				TotalDurationMs: 200,
			}, nil
		},
		nil, "test task", "call_1", "fork", "fork",
	)
	reg.Wait(state.ID)

	adapter := NewBackgroundAgentJobAdapter(reg)
	info, ok := adapter.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false")
	}
	if info.Status != "completed" {
		t.Errorf("Status = %q, want completed", info.Status)
	}
	if info.Output != "done" {
		t.Errorf("Output = %q, want done", info.Output)
	}
	if info.AgentType != "fork" {
		t.Errorf("AgentType = %q, want fork", info.AgentType)
	}
	if info.Tokens != 100 {
		t.Errorf("Tokens = %d, want 100", info.Tokens)
	}
	if info.DurationMs != 200 {
		t.Errorf("DurationMs = %d, want 200", info.DurationMs)
	}
}

func TestAdapter_GetRunning(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	blockCh := make(chan struct{})
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-blockCh // block until test has read the status
			return &types.SubQueryResult{Content: "slow"}, nil
		},
		nil, "slow task", "call_1", "fork", "fork",
	)

	adapter := NewBackgroundAgentJobAdapter(reg)
	info, ok := adapter.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false")
	}
	if info.Status != "running" {
		t.Errorf("Status = %q, want running", info.Status)
	}
	if info.Output != "" {
		t.Errorf("Output = %q, want empty for running task", info.Output)
	}

	close(blockCh)       // unblock the goroutine
	reg.Cancel(state.ID) // cleanup
}

func TestAdapter_GetFailed(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return nil, errors.New("boom")
		},
		nil, "failing task", "call_1", "fork", "fork",
	)
	reg.Wait(state.ID)

	adapter := NewBackgroundAgentJobAdapter(reg)
	info, ok := adapter.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false")
	}
	if info.Status != "failed" {
		t.Errorf("Status = %q, want failed", info.Status)
	}
	if info.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", info.ExitCode)
	}
}

func TestAdapter_GetNotFound(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	adapter := NewBackgroundAgentJobAdapter(reg)
	_, ok := adapter.Get("nonexistent")
	if ok {
		t.Error("Get should return false for nonexistent ID")
	}
}

func TestAdapter_List(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	s1, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "a"}, nil
		},
		nil, "task a", "call_1", "fork", "fork",
	)
	s2, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "b"}, nil
		},
		nil, "task b", "call_2", "fork", "fork",
	)
	reg.Wait(s1.ID)
	reg.Wait(s2.ID)

	adapter := NewBackgroundAgentJobAdapter(reg)
	list := adapter.List()
	if len(list) != 2 {
		t.Fatalf("List returned %d tasks, want 2", len(list))
	}
	ids := map[string]bool{}
	for _, info := range list {
		ids[info.ID] = true
		if info.Type != "fork" {
			t.Errorf("Type = %q, want fork", info.Type)
		}
	}
	if !ids[s1.ID] || !ids[s2.ID] {
		t.Errorf("List missing agents, got IDs: %v", ids)
	}
}

func TestAdapter_List_TriggerCleanup(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "done"}, nil
		},
		nil, "cleanup test", "call_1", "fork", "fork",
	)
	reg.Wait(state.ID)

	adapter := NewBackgroundAgentJobAdapter(reg)
	// List triggers cleanup of terminal agents
	adapter.List()

	// After cleanup, Get should return false
	_, ok := adapter.Get(state.ID)
	if ok {
		t.Error("Get should return false after List-triggered cleanup")
	}
}

func TestAdapter_KillSuccess(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	started := make(chan struct{})
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil, "killable task", "call_1", "fork", "fork",
	)
	<-started

	adapter := NewBackgroundAgentJobAdapter(reg)
	if err := adapter.Kill(state.ID); err != nil {
		t.Errorf("Kill returned error: %v", err)
	}
	reg.Wait(state.ID)
}

func TestAdapter_KillNotFound(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	adapter := NewBackgroundAgentJobAdapter(reg)
	err := adapter.Kill("nonexistent")
	if err == nil {
		t.Error("Kill should return error for nonexistent ID")
	}
	if !errors.Is(err, job.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestAdapter_WaitCompleted(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "done"}, nil
		},
		nil, "wait test", "call_1", "fork", "fork",
	)

	adapter := NewBackgroundAgentJobAdapter(reg)
	code, err := adapter.Wait(state.ID)
	if err != nil {
		t.Fatalf("Wait error: %v", err)
	}
	if code != 0 {
		t.Errorf("ExitCode = %d, want 0", code)
	}
}

func TestAdapter_WaitFailed(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return nil, errors.New("crash")
		},
		nil, "fail test", "call_1", "fork", "fork",
	)

	adapter := NewBackgroundAgentJobAdapter(reg)
	code, err := adapter.Wait(state.ID)
	if err != nil {
		t.Fatalf("Wait error: %v", err)
	}
	if code != 1 {
		t.Errorf("ExitCode = %d, want 1", code)
	}
}

func TestAdapter_WaitNotFound(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	adapter := NewBackgroundAgentJobAdapter(reg)
	_, err := adapter.Wait("nonexistent")
	if err == nil {
		t.Error("Wait should return error for nonexistent ID")
	}
	if !errors.Is(err, job.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestAdapter_NilRegistry(t *testing.T) {
	t.Parallel()
	adapter := NewBackgroundAgentJobAdapter(nil)

	_, ok := adapter.Get("any")
	if ok {
		t.Error("Get should return false for nil registry")
	}

	if err := adapter.Kill("any"); err == nil {
		t.Error("Kill should return error for nil registry")
	}

	list := adapter.List()
	if len(list) != 0 {
		t.Errorf("List = %d, want 0 for nil registry", len(list))
	}

	_, err := adapter.Wait("any")
	if err == nil {
		t.Error("Wait should return error for nil registry")
	}
}

func TestConvertState_ExposesParentToolUseIDAndAgentType(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	blockCh := make(chan struct{})
	state, _ := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-blockCh
			return &types.SubQueryResult{Content: "done"}, nil
		},
		nil, "explore stuff", "toolu_9", "agent", "Explore",
	)

	adapter := NewBackgroundAgentJobAdapter(reg)
	info, ok := adapter.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false")
	}
	// AgentType must be populated while STILL RUNNING (spawn-time field), not
	// only from the eventual Result.
	if info.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore (running state)", info.AgentType)
	}
	if info.ParentToolUseID != "toolu_9" {
		t.Errorf("ParentToolUseID = %q, want toolu_9", info.ParentToolUseID)
	}
	if !info.Backgrounded {
		t.Error("Backgrounded = false, want true for registry agents")
	}
	if info.StartedMs <= 0 {
		t.Errorf("StartedMs = %d, want > 0", info.StartedMs)
	}

	close(blockCh)
	reg.Wait(state.ID)

	// SummaryLister contract: same fields, Output always empty.
	summaries := adapter.ListSummaries()
	if len(summaries) != 1 {
		t.Fatalf("ListSummaries len = %d, want 1", len(summaries))
	}
	s := summaries[0]
	if s.ID != state.ID || s.AgentType != "Explore" || s.ParentToolUseID != "toolu_9" || !s.Backgrounded {
		t.Errorf("summary = {id:%s type:%s parent:%s bg:%v}, want full panel fields", s.ID, s.AgentType, s.ParentToolUseID, s.Backgrounded)
	}
	if s.Output != "" {
		t.Errorf("summary Output = %q, want empty (SummaryLister contract)", s.Output)
	}
}
