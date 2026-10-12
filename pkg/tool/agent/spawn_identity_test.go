package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// spySink implements the SubEngine sink contract for AttachSink tests.
type spySink struct {
	mu    sync.Mutex
	items []types.QueuedItem
}

func (s *spySink) EnqueueAttachment(item types.QueuedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
}

var _ SubEngine = (*spySink)(nil)

// identityTool builds an AgentTool with the messaging identity pre-set, the
// way WireEngine injects it in production.
func identityTool(t *testing.T, engineID string) (*AgentTool, *mockSubEngine) {
	t.Helper()
	at := New()
	at.SetEngineID(engineID)
	eng := &mockSubEngine{}
	at.SetEngine(eng)
	at.SetNotifyFn(func(string) {}, func() string { return "" })
	return at, eng
}

// Spawn mints the id inside the registry, so the run function must learn its
// own id through the new parameter — otherwise opts.AgentID cannot be set
// before the goroutine reads it.
func TestSpawnPassesIDToRunFn(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"agent", "fork"} {
		var mu sync.Mutex
		var gotID string
		reg := NewBackgroundAgentRegistry()
		state, err := reg.Spawn(
			context.Background(),
			func(ctx context.Context, agentID string) (*types.SubQueryResult, error) {
				mu.Lock()
				gotID = agentID
				mu.Unlock()
				return &types.SubQueryResult{Content: "done"}, nil
			},
			nil,
			"desc",
			"call_1",
			prefix,
			prefix,
		)
		if err != nil {
			t.Fatalf("prefix %s: Spawn: %v", prefix, err)
		}
		if _, ok := reg.Wait(state.ID); !ok {
			t.Fatalf("prefix %s: Wait returned false", prefix)
		}
		mu.Lock()
		defer mu.Unlock()
		if gotID != state.ID {
			t.Errorf("prefix %s: runFn agentID = %q, want the minted id %q", prefix, gotID, state.ID)
		}
	}
}

// The spawned agent's first conversation must open with a plain user message
// naming its id, display name, and spawning engine — this is what makes the
// flat message namespace usable from inside the child.
func TestSpawnBackground_IdentityInjected(t *testing.T) {
	t.Run("named subagent", func(t *testing.T) {
		t.Parallel()
		at, eng := identityTool(t, "e2")
		input := json.RawMessage(`{"description":"scrape","prompt":"p","subagent_type":"General","run_in_background":true,"name":"scraper"}`)
		sqr := launchBackground(t, at, input, &tool.ToolUseContext{ToolUseID: "call_id1"})
		at.bgReg.Wait(sqr.AgentID)

		if eng.captured[0].AgentID != "agent-1" {
			t.Errorf("opts.AgentID = %q, want agent-1", eng.captured[0].AgentID)
		}
		msgs := eng.captured[0].UserContextMessages
		if len(msgs) != 1 {
			t.Fatalf("UserContextMessages count = %d, want 1", len(msgs))
		}
		if got, want := msgs[0].Content[0].Text, "你是后台 agent-1（scraper），由 engine:e2 派生"; got != want {
			t.Errorf("identity = %q, want %q", got, want)
		}
		if msgs[0].Role != types.RoleUser {
			t.Errorf("identity Role = %q, want %q", msgs[0].Role, types.RoleUser)
		}
	})

	t.Run("unnamed subagent omits the name part", func(t *testing.T) {
		t.Parallel()
		at, eng := identityTool(t, "e2")
		input := json.RawMessage(`{"description":"scrape","prompt":"p","subagent_type":"General","run_in_background":true}`)
		sqr := launchBackground(t, at, input, &tool.ToolUseContext{ToolUseID: "call_id2"})
		at.bgReg.Wait(sqr.AgentID)

		msgs := eng.captured[0].UserContextMessages
		if len(msgs) != 1 {
			t.Fatalf("UserContextMessages count = %d, want 1", len(msgs))
		}
		if got, want := msgs[0].Content[0].Text, "你是后台 agent-1，由 engine:e2 派生"; got != want {
			t.Errorf("identity = %q, want %q", got, want)
		}
	})

	t.Run("fork path", func(t *testing.T) {
		t.Parallel()
		at, eng := identityTool(t, "e2")
		input := json.RawMessage(`{"description":"audit","prompt":"p","fork":true,"run_in_background":true,"name":"audit"}`)
		sqr := launchBackground(t, at, input, &tool.ToolUseContext{ToolUseID: "call_id3"})
		at.bgReg.Wait(sqr.AgentID)

		if !strings.HasPrefix(sqr.AgentID, "fork-") {
			t.Fatalf("AgentID = %q, want a fork- prefixed id", sqr.AgentID)
		}
		msgs := eng.captured[0].UserContextMessages
		if len(msgs) != 1 {
			t.Fatalf("UserContextMessages count = %d, want 1", len(msgs))
		}
		want := "你是后台 " + sqr.AgentID + "（audit），由 engine:e2 派生"
		if got := msgs[0].Content[0].Text; got != want {
			t.Errorf("identity = %q, want %q", got, want)
		}
		if eng.captured[0].AgentID != sqr.AgentID {
			t.Errorf("opts.AgentID = %q, want %q", eng.captured[0].AgentID, sqr.AgentID)
		}
	})
}

// The registry entry carries the deliverable sub-engine handle; attaching is
// a no-op for unknown ids and the snapshot copies keep exposing the sink.
func TestAttachSink(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	defer close(blocked)
	reg := NewBackgroundAgentRegistry()
	state, err := reg.Spawn(
		context.Background(),
		func(ctx context.Context, agentID string) (*types.SubQueryResult, error) {
			<-blocked
			return &types.SubQueryResult{Content: "done"}, nil
		},
		nil,
		"desc",
		"call_1",
		"agent",
		"agent",
	)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	spy := &spySink{}
	reg.AttachSink(state.ID, spy)
	got, ok := reg.Get(state.ID)
	if !ok {
		t.Fatal("Get returned false for the running agent")
	}
	if got.Sink != spy {
		t.Errorf("Get(id).Sink = %v, want the attached spy", got.Sink)
	}

	// Snapshot copies (Get/List) carry the sink — the resolver reads copies.
	snapshots := reg.List()
	if len(snapshots) != 1 || snapshots[0].Sink != spy {
		t.Errorf("List snapshots = %+v, want the sink exposed", snapshots)
	}

	// Unknown id: no panic, no state change.
	reg.AttachSink("missing", spy)
	if got.ID != state.ID {
		t.Errorf("state mutated by AttachSink(missing): ID = %q", got.ID)
	}
}

// AttachSink's map lookup and Sink write must stay inside one critical
// section. If Unlock runs before the write (defer dropped), the unlocked
// map read collides with CleanupCompleted's delete windows and the Go
// runtime raises a fatal concurrent-map error. Cleanup is used as the
// competing writer because its single critical section contains hundreds of
// back-to-back deletes — far wider than Spawn's one-entry write.
func TestAttachSink_ConcurrentWithCleanup(t *testing.T) {
	t.Parallel()
	reg := NewBackgroundAgentRegistry()
	for range 200 {
		state, err := reg.Spawn(context.Background(),
			func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
				return &types.SubQueryResult{}, nil
			},
			nil, "d", "c", "agent", "agent")
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		if _, ok := reg.Wait(state.ID); !ok {
			t.Fatalf("Wait(%s) returned false", state.ID)
		}
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			reg.AttachSink("agent-1", &spySink{})
		}
	}()
	reg.CleanupCompleted()
	close(stop)
	<-done
}

// SetBgRegistry shares one process-global registry; SetNotifyFn must keep
// that registry instead of minting its own.
func TestSetBgRegistry_SharedRegistryWins(t *testing.T) {
	t.Parallel()
	at := New()
	reg := NewBackgroundAgentRegistry()
	at.SetBgRegistry(reg)
	if at.bgReg != reg {
		t.Fatal("SetBgRegistry did not store the shared registry")
	}
	at.SetNotifyFn(func(string) {}, func() string { return "" })
	if at.bgReg != reg {
		t.Fatal("SetNotifyFn replaced the shared registry with a fresh one")
	}
	if at.JobAdapter() == nil {
		t.Fatal("JobAdapter must wrap the shared registry")
	}
}

// SetEngineID's value must reach both the identity builder (spawn line) and
// the exported accessor.
func TestSetEngineID_Captured(t *testing.T) {
	t.Parallel()
	at, eng := identityTool(t, "e2")
	if got := at.EngineID(); got != "e2" {
		t.Errorf("EngineID() = %q, want e2", got)
	}
	input := json.RawMessage(`{"description":"scrape","prompt":"p","subagent_type":"General","run_in_background":true}`)
	sqr := launchBackground(t, at, input, &tool.ToolUseContext{ToolUseID: "call_id4"})
	at.bgReg.Wait(sqr.AgentID)

	msgs := eng.captured[0].UserContextMessages
	if len(msgs) != 1 {
		t.Fatalf("UserContextMessages count = %d, want 1", len(msgs))
	}
	want := "你是后台 " + sqr.AgentID + "，由 engine:e2 派生"
	if msgs[0].Content[0].Text != want {
		t.Errorf("identity = %q, want %q", msgs[0].Content[0].Text, want)
	}
}
