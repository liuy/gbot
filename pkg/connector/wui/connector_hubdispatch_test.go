package wui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// TestHubDispatch_SubAgentEvent_PreservesAgentMeta verifies the full
// hub → connector → wire JSON pipeline preserves agent meta on sub-agent
// events. This is the minimal reproduction for "Reviewer output not visible
// in webchat" — if agent meta is lost, the frontend cannot route sub-agent
// events to the parent tool block's children.
func TestHubDispatch_SubAgentEvent_PreservesAgentMeta(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)

	// Simulate what taggedDispatcher does: dispatch a sub-agent text_delta
	// with AgentMeta set.
	h.Dispatch(types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "reviewer output",
		Agent: &types.AgentMeta{ParentToolUseID: "skill-1", AgentType: "Reviewer", Depth: 1},
	})

	msg := readWSMessage(t, ws)

	var env struct {
		Event struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Agent *struct {
				ParentToolUseID string `json:"parent_tool_use_id"`
				AgentType       string `json:"agent_type"`
				Depth           int    `json:"depth"`
			} `json:"agent"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Agent == nil {
		t.Fatal("agent meta lost in hub → connector pipeline")
	}
	if env.Event.Agent.ParentToolUseID != "skill-1" {
		t.Errorf("parent_tool_use_id = %q", env.Event.Agent.ParentToolUseID)
	}
	if env.Event.Agent.AgentType != "Reviewer" {
		t.Errorf("agent_type = %q, want \"Reviewer\"", env.Event.Agent.AgentType)
	}
	if env.Event.Agent.Depth != 1 {
		t.Errorf("depth = %d, want 1", env.Event.Agent.Depth)
	}
}

// TestHubDispatch_SubAgentQueryEnd_NoDoubleInterrupt verifies that aborting
// during a sub-agent does not inject [Request interrupted by user] for the
// sub-agent's query_end — only the main engine's query_end should inject.
func TestHubDispatch_SubAgentQueryEnd_NoDoubleInterrupt(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)

	// Sub-agent query_end with AbortError + agent meta
	h.Dispatch(types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: &engine.AbortError{},
		Agent: &types.AgentMeta{ParentToolUseID: "skill-1", AgentType: "Reviewer", Depth: 1},
	})

	msg := readWSMessage(t, ws)

	var env struct {
		Event struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if env.Event.Type == "text_delta" {
		t.Fatalf("sub-agent query_end injected interrupt — should only happen for main engine")
	}
	if env.Event.Type != "query_end" {
		t.Errorf("type = %q, want query_end", env.Event.Type)
	}

	// No extra messages: set a short deadline, read should time out.
	_ = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond)) // REAL-TIME
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatalf("unexpected extra message on ws")
	}
}
