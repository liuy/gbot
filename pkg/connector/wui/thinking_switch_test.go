package wui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
)

func writeWSMessage(t *testing.T, ws *websocket.Conn, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestThinkingSwitch_SetsEngineAndPushesConfig covers the full chain:
// ws thinking_switch → engine.SetThinking → config frame back with the
// server-resolved effort.
func TestThinkingSwitch_SetsEngineAndPushesConfig(t *testing.T) {
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), buildTestProviderConfigs())
	c.mock().thinkingFn = func() llm.Effort { return llm.EffortHigh }

	ws := dialAndStore(t, c)
	defer ws.Close()

	writeWSMessage(t, ws, map[string]string{"type": "thinking_switch", "effort": "high"})

	if !waitFor(10e9, func() bool {
		c.mock().mu.Lock()
		defer c.mock().mu.Unlock()
		return len(c.mock().setThinkingCalls) == 1
	}) {
		t.Fatal("timeout waiting for SetThinking call")
	}
	c.mock().mu.RLock()
	got := c.mock().setThinkingCalls[0]
	c.mock().mu.RUnlock()
	if got != llm.EffortHigh {
		t.Fatalf("setThinkingCalls[0] = %q, want high", got)
	}

	data := readWSMessage(t, ws)
	var resp struct {
		Type     string `json:"type"`
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "config" {
		t.Fatalf("type = %q, want config (effort is synced via the config frame)", resp.Type)
	}
	if resp.Thinking != "high" {
		t.Errorf("config.thinking = %q, want high", resp.Thinking)
	}
}

func TestThinkingSwitch_InvalidEffort(t *testing.T) {
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), buildTestProviderConfigs())

	ws := dialAndStore(t, c)
	defer ws.Close()

	writeWSMessage(t, ws, map[string]string{"type": "thinking_switch", "effort": "xhigh"})

	data := readWSMessage(t, ws)
	var resp struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if !strings.Contains(resp.Message, "none|auto|low|medium|high|max") {
		t.Errorf("error message = %q, want it to list the six valid values", resp.Message)
	}
	c.mock().mu.RLock()
	calls := len(c.mock().setThinkingCalls)
	c.mock().mu.RUnlock()
	if calls != 0 {
		t.Errorf("setThinkingCalls = %d, want 0 (invalid input must not reach the engine)", calls)
	}
}
