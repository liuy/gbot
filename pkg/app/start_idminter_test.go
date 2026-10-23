package app

import (
	"context"
	"testing"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/types"
)

// Every background table Start() brings up must mint from the ONE shared
// idMinter: a SharedDeps literal that drops IDMinter hands that engine's
// table a private counter restarting at agent-1, so two engines mint the
// same id and job ids / message addresses collide across engines. Probes the
// first mint of every boot path's table — the MainRefs toolset (main deps
// literal), the manager engines (engine-factory literal via restoreEngines's
// synthesized main, and the dream deps chain) — and asserts the ids are
// pairwise distinct.
func TestStart_AllEnginesShareIDMinter(t *testing.T) {
	t.Setenv("HOME", homeWithSettings(t, ""))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")
	t.Setenv("GBOT_SUPERVISED", "")
	startInTempProject(t)

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)

	// Probe spawns block in their run functions; one close releases all.
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })

	type bgTable struct {
		owner string
		reg   *agenttool.AgentRegistry
	}
	tables := []bgTable{{"MainRefs", inst.MainRefs.Agent.Registry()}}
	for _, vs := range inst.EngineMgr.List() {
		if vs.Engine == nil || vs.Engine.ToolRefs().Agent == nil {
			continue
		}
		tables = append(tables, bgTable{owner: vs.ID, reg: vs.Engine.ToolRefs().Agent.Registry()})
	}
	if len(tables) < 3 {
		t.Fatalf("probed %d background tables, want at least 3 (MainRefs, main, dream)", len(tables))
	}

	seen := make(map[string]string) // minted id -> owning table
	for _, tb := range tables {
		if tb.reg == nil {
			t.Fatalf("%s has no background registry", tb.owner)
		}
		st, err := tb.reg.Spawn(context.Background(),
			func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
				<-blocked
				return &types.SubQueryResult{}, nil
			}, nil, "minter probe", "call_1", "agent")
		if err != nil {
			t.Fatalf("%s: probe spawn: %v", tb.owner, err)
		}
		if prev, dup := seen[st.ID]; dup {
			t.Errorf("background tables of %s and %s both minted %s — a SharedDeps literal dropped IDMinter, so one of them runs a private counter", prev, tb.owner, st.ID)
		}
		seen[st.ID] = tb.owner
	}
}
