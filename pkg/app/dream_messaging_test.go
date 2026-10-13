package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/message"
	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// dreamSink is a stand-in sub-engine attached to a registry entry so the
// background agent under test is deliverable (status "running" with a sink).
type dreamSink struct {
	mu    sync.Mutex
	items []types.QueuedItem
}

func (s *dreamSink) EnqueueAttachment(item types.QueuedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
}

func (s *dreamSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// dreamMsgFixture is a createDreamEngine dependency set with BOTH message
// address spaces populated: one peer engine in the process manager and one
// running background agent in the registry (blocked in its run function, sink
// attached by hand because no real RunAgent is executing).
type dreamMsgFixture struct {
	deps    dreamEngineDeps
	agentID string
	sink    *dreamSink
}

func newDreamMsgFixture(t *testing.T) *dreamMsgFixture {
	t.Helper()
	primary := config.Provider{
		Name: "t",
		Models: config.NewModelsFromMap(map[string]config.ModelConfig{
			"m": {Context: config.IntOrHuman(4096), MaxTokens: config.IntOrHuman(1024)},
		}),
	}
	cfg := &config.Config{Providers: []config.Provider{primary}}
	spy := &spyProvider{}

	// Recall is on the default whitelist, so the store has to exist for the
	// exposed set to match the list.
	store, err := short.NewStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	peer := engine.New(&engine.Params{Provider: spy, Model: "m", EngineID: "e2"})
	t.Cleanup(func() { peer.Close() })
	mgr.Add(&engine.EngineViewState{Engine: peer, ID: "e2"})

	reg := agenttool.NewBackgroundAgentRegistry()
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	st, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-blocked
			return &types.SubQueryResult{}, nil
		}, nil, "bg agent", "call_1", "agent")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	sink := &dreamSink{}
	reg.AttachSink(st.ID, sink)

	return &dreamMsgFixture{
		deps: dreamEngineDeps{
			Cfg:                cfg,
			ProviderMap:        map[string]llm.Provider{"t": spy},
			Provider:           spy,
			PrimaryProviderCfg: &cfg.Providers[0],
			Model:              "m",
			WorkingDir:         t.TempDir(),
			Store:              store,
			Logger:             slog.Default(),
			NoTUI:              true,
			EngineMgr:          mgr,
			BgAgents:           reg,
		},
		agentID: st.ID,
		sink:    sink,
	}
}

// buildDreamEngine builds one dream engine from the fixture deps, optionally
// overriding the tool whitelist.
func (f *dreamMsgFixture) buildDreamEngine(t *testing.T, tools []string) *engine.Engine {
	t.Helper()
	deps := f.deps
	deps.Tools = tools
	eng, _, _, _ := createDreamEngine(deps)
	t.Cleanup(func() { eng.Close() })
	return eng
}

// dreamSend drives one Message-tool call through the given handle and returns
// the delivered-to text.
func dreamSend(t *testing.T, tl tool.Tool, to string) string {
	t.Helper()
	res, err := tl.Call(context.Background(),
		json.RawMessage(`{"to":"`+to+`","message":"dream ping"}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("send to %s: %v", to, err)
	}
	if res == nil {
		t.Fatalf("send to %s: nil result", to)
	}
	mr, ok := res.Data.(message.MessageResult)
	if !ok {
		t.Fatalf("send to %s: result data %T, want message.MessageResult", to, res.Data)
	}
	if mr.To != to || mr.Message != "dream ping" {
		t.Fatalf("send to %s: result = %+v, want To=%q Message=%q", to, mr, to, "dream ping")
	}
	return "Delivered to " + mr.To
}

// exposedToolNames is the model-visible tool set the dream engine would offer
// on its next request — what the whitelist actually decides.
func exposedToolNames(eng *engine.Engine) []string {
	return slices.Sorted(maps.Keys(eng.Tools()))
}

// TestCreateDreamEngine_WhitelistOpensMessage pins the whitelist path with
// Message listed: the tool must reach the model-visible map (not merely exist
// in ToolRefs) and the handle the model sees must deliver through both address
// spaces — the background-agent registry and the engine manager.
func TestCreateDreamEngine_WhitelistOpensMessage(t *testing.T) {
	f := newDreamMsgFixture(t)
	eng := f.buildDreamEngine(t, []string{
		"Read", "Write", "Edit", "Bash", "Grep", "Glob",
		"Recall", "Message",
	})

	exposed := eng.Tools()["Message"]
	if exposed == nil {
		t.Fatalf("model-visible tools = %v, want Message listed", exposedToolNames(eng))
	}

	if got := dreamSend(t, exposed, f.agentID); got != "Delivered to "+f.agentID {
		t.Errorf("agent send result = %q, want %q", got, "Delivered to "+f.agentID)
	}
	if f.sink.count() != 1 {
		t.Fatalf("sink received %d items, want 1", f.sink.count())
	}
	wantEnvelope := "<message from=\"dream\">\ndream ping\n</message>"
	if f.sink.items[0].Value != wantEnvelope {
		t.Errorf("enqueued value = %q, want %q", f.sink.items[0].Value, wantEnvelope)
	}

	if got := dreamSend(t, exposed, "e2"); got != "Delivered to e2" {
		t.Errorf("engine send result = %q, want %q", got, "Delivered to e2")
	}
}

// TestCreateDreamEngine_DefaultWhitelistHidesMessage pins the other side of the
// whitelist path: the default list keeps Message out of the model-visible map.
// The resolver behind the unexposed tool must still deliver — EngineMgr and
// BgAgents stay wired so opening the whitelist is a one-line config change,
// not a re-wiring exercise.
func TestCreateDreamEngine_DefaultWhitelistHidesMessage(t *testing.T) {
	f := newDreamMsgFixture(t)
	eng := f.buildDreamEngine(t, nil)

	want := []string{"Bash", "Edit", "Glob", "Grep", "Read", "Recall", "Write"}
	if got := exposedToolNames(eng); !slices.Equal(got, want) {
		t.Errorf("model-visible tools = %v, want %v", got, want)
	}
	if eng.ToolRefs().Message == nil {
		t.Fatal("dream ToolRefs().Message is nil — message tool not registered")
	}
	if got := dreamSend(t, eng.ToolRefs().Message, "e2"); got != "Delivered to e2" {
		t.Errorf("engine send result = %q, want %q", got, "Delivered to e2")
	}
	if got := dreamSend(t, eng.ToolRefs().Message, f.agentID); got != "Delivered to "+f.agentID {
		t.Errorf("agent send result = %q, want %q", got, "Delivered to "+f.agentID)
	}
	if f.sink.count() != 1 {
		t.Fatalf("sink received %d items, want 1", f.sink.count())
	}
}
