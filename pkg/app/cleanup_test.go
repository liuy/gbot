package app

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
)

func TestCleanup_ClosesAllEngines(t *testing.T) {
	mgr := engine.NewEngineManager()
	var closeCount atomic.Int32
	for i := range 3 {
		eng := engine.New(&engine.Params{})
		eng.SetOnClose(func(sessionID string) {
			closeCount.Add(1)
		})
		mgr.Add(&engine.EngineViewState{
			Engine: eng,
			Model:  "test",
			ID:     fmt.Sprintf("e%d", i),
			Name:   fmt.Sprintf("e%d", i),
		})
	}
	inst := &Instance{EngineMgr: mgr}
	inst.Cleanup()
	if got := closeCount.Load(); got != 3 {
		t.Errorf("closeCount = %d, want 3", got)
	}
}

func TestCleanup_NilSafe(t *testing.T) {
	inst := &Instance{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Cleanup panicked on zero-value Instance: %v", r)
		}
	}()
	inst.Cleanup()
}

func TestCleanup_EmptyEngineMgr(t *testing.T) {
	mgr := engine.NewEngineManager()
	inst := &Instance{EngineMgr: mgr}
	inst.Cleanup()
}
