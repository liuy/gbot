package engine

import (
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/message"
)

// EngineMessageResolver maps flat ids to message-deliverable targets.
// Lookup order: the process-global background-agent registry first (its ids
// are the flat agent-N namespace), then the EngineManager by id, then the
// EngineManager by name.
//
// Registry-first is safe because the id counter is seeded at boot above every
// persisted agent-N suffix — engine ids AND display names — so while an engine
// answers to agent-3 no sub-agent can ever mint agent-3: a name-fallback
// address can never be shadowed by a live id. Custom names in the agent-N
// grammar are rejected at creation for the same reason.
//
// Every engine registered in the manager is addressable — the full
// EngineManager set (main, dream, wechat), not just main. That is a
// settled consequence of the flat namespace, not an accident: start.go
// registers all of them in one manager.
type EngineMessageResolver struct {
	mgr    *EngineManager
	agents *agenttool.BackgroundAgentRegistry
}

// Resolve implements message.Resolver. A nil EngineMgr AND nil BgAgents
// (reload's throwaway harvest toolset) degrades to not-found — Get takes
// m.mu.RLock, which would panic on a nil manager.
func (r *EngineMessageResolver) Resolve(id string) (message.Target, string, bool) {
	if r.agents != nil {
		if st, ok := r.agents.Get(id); ok {
			if st.Status != agenttool.AgentRunning {
				// Known but not deliverable: v1 does not resume stopped agents.
				return nil, string(st.Status), true
			}
			if st.Sink == nil {
				// Running but RunAgent has not attached the sub-engine yet.
				return nil, "starting", true
			}
			return st.Sink, "running", true
		}
	}
	if r.mgr == nil {
		return nil, "", false
	}
	vs := r.mgr.Get(id)
	if vs == nil {
		vs = r.mgr.GetByName(id)
	}
	if vs == nil || vs.Engine == nil {
		// Lazy view states (meta.json entries before first switch) have a nil
		// Engine — nothing to deliver to, by id or by name.
		return nil, "", false
	}
	return vs.Engine, "", true
}
