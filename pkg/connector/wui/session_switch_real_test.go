package wui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// seedRealSessions creates a real store with two sessions owned by engineID,
// each carrying a distinct user message, and returns the store plus both IDs.
func seedRealSessions(t *testing.T, engineID string) (*short.Store, string, string) {
	t.Helper()
	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var ids [2]string
	for i, label := range []string{"alpha", "beta"} {
		ses, err := store.CreateSessionWithEngine("/proj", "test-model", engineID)
		if err != nil {
			t.Fatalf("CreateSessionWithEngine %s: %v", label, err)
		}
		ids[i] = ses.SessionID
		em := types.Message{
			ID:        "msg-" + label,
			Role:      types.RoleUser,
			Timestamp: time.Date(2026, 8, 25, 9, 0, i, 0, time.UTC),
			Content:   []types.ContentBlock{types.NewTextBlock("hello from " + label)},
		}
		ts, err := short.EngineMessagesToStore([]types.Message{em})
		if err != nil {
			t.Fatalf("EngineMessagesToStore %s: %v", label, err)
		}
		if err := store.AppendMessage(ses.SessionID, ts[0]); err != nil {
			t.Fatalf("AppendMessage %s: %v", label, err)
		}
	}
	return store, ids[0], ids[1]
}

// newRealEngineConnector builds a connector whose active slot wraps a REAL
// engine.Engine (with a real store), not a mockEngine.
//
// Caveat: the slot's hub has no engine-hub subscription and the engine's
// dispatcher hub is a different instance — fine for handler paths tested
// here (sendMetadata/buildSessionList don't go through hub events), but
// future tests asserting streamed events must wire the subscription first.
func newRealEngineConnector(t *testing.T, eng *engine.Engine) *WUIConnector {
	t.Helper()
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	slot := &engineSlot{
		engineID:    "main",
		engine:      &engineAdapter{eng: eng},
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	slot.active.Store(true)
	c.slots["main"] = slot
	return c
}

// TestSessionSwitch_RealEngine_HistoryAndList verifies the full production
// path with a real engine + store: clicking a session in the sidebar must
// (a) switch the engine's in-memory messages, (b) push a metadata frame whose
// history carries the NEW session's transcript, and (c) refresh the session
// list with the new session marked current.
func TestSessionSwitch_RealEngine_HistoryAndList(t *testing.T) {
	store, sidA, sidB := seedRealSessions(t, "main")

	eng := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	if _, err := eng.SwitchSession(sidA); err != nil {
		t.Fatalf("SwitchSession(A): %v", err)
	}

	c := newRealEngineConnector(t, eng)
	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]string{"type": "session_switch", "sessionID": sidB})

	meta := readMetadata(t, ws)
	var cs struct {
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(meta.Connect, &cs); err != nil {
		t.Fatalf("unmarshal connect: %v", err)
	}
	if cs.SessionID != sidB {
		t.Fatalf("connect.sessionID = %q, want session B %q", cs.SessionID, sidB)
	}

	var hist struct {
		Messages []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(meta.History, &hist); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	if len(hist.Messages) != 1 || hist.Messages[0].Text != "hello from beta" {
		t.Fatalf("history after switch = %+v, want the beta session transcript", hist.Messages)
	}

	// The metadata frame triggers the frontend to re-request the session
	// list; verify the refreshed list reflects the switch.
	sendJSON(t, ws, map[string]string{"type": "session_list_request"})
	list := readSessionList(t, ws)
	found := false
	for _, s := range list {
		if s.ID == sidB {
			found = true
		}
	}
	if !found {
		t.Fatalf("session list after switch missing session B: %+v", list)
	}
}

// TestSessionSwitch_RestoresContextTokens verifies SwitchSession restores the
// persisted ContextTokens of the target session (like ResumeOrInitSession).
// Without it the header shows the old session's usage and auto-compact
// thresholds run on a stale value after a switch.
func TestSessionSwitch_RestoresContextTokens(t *testing.T) {
	store, sidA, sidB := seedRealSessions(t, "main")
	if err := store.UpdateContextTokens(sidA, 12000); err != nil {
		t.Fatalf("UpdateContextTokens(A): %v", err)
	}
	if err := store.UpdateContextTokens(sidB, 34000); err != nil {
		t.Fatalf("UpdateContextTokens(B): %v", err)
	}

	eng := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	if _, err := eng.SwitchSession(sidA); err != nil {
		t.Fatalf("SwitchSession(A): %v", err)
	}
	if got := eng.GetContextTokens(); got != 12000 {
		t.Fatalf("initial context tokens = %d, want 12000", got)
	}

	if _, err := eng.SwitchSession(sidB); err != nil {
		t.Fatalf("SwitchSession(B): %v", err)
	}
	if got := eng.GetContextTokens(); got != 34000 {
		t.Fatalf("context tokens after switch = %d, want session B's 34000", got)
	}
}

func readSessionList(t *testing.T, ws *websocket.Conn) []struct {
	ID string `json:"id"`
} {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {           // REAL-TIME
		_ = ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
		_, data, err := ws.ReadMessage()
		// Read timeouts are retried until the outer deadline — the shared CI
		// box can take longer than one 500ms slice under load.
		if err != nil && !time.Now().Before(deadline) {
			t.Fatalf("read session_list: %v", err)
		}
		if err != nil {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &probe) != nil || probe.Type != "session_list" {
			continue
		}
		var msg struct {
			Sessions []struct {
				ID string `json:"id"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal session_list: %v", err)
		}
		return msg.Sessions
	}
	t.Fatal("timeout waiting for session_list frame")
	return nil
}

// TestSessionSwitch_PersistsToMetaJSON verifies that switching sessions via
// the WUI sidebar writes the new session id to meta.json — mirroring what
// engine_switch does (TestEngineSwitch_PersistsToMetaJSON). Without it the
// selected session is lost on restart.
func TestSessionSwitch_PersistsToMetaJSON(t *testing.T) {
	store, sidA, sidB := seedRealSessions(t, "main")
	dir := t.TempDir()

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatalf("mgr.SetActive: %v", err)
	}
	if err := mgr.PersistMeta(dir); err != nil {
		t.Fatalf("mgr.PersistMeta: %v", err)
	}

	eng := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, dir)
	if _, err := eng.SwitchSession(sidA); err != nil {
		t.Fatalf("SwitchSession(A): %v", err)
	}

	c := newRealEngineConnector(t, eng)
	c.mgr = mgr
	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]string{"type": "session_switch", "sessionID": sidB})
	readMetadata(t, ws)

	meta := readWorkspaceMetaUntil(t, dir, sidB)
	if !meta {
		t.Fatal("session switch was not persisted to meta.json (current_session_id never became session B)")
	}
}

func readWorkspaceMetaUntil(t *testing.T, dir, wantSession string) bool {
	t.Helper()
	return waitFor(2e9, func() bool {
		meta, err := short.ReadWorkspaceMeta(dir)
		return err == nil && meta != nil && meta.CurrentSessionID == wantSession
	})
}

// TestSessionNew_PersistsToMetaJSON mirrors the switch variant for the FAB
// path: creating a session via session_new must write the new session id to
// meta.json so a restart resumes into it.
func TestSessionNew_PersistsToMetaJSON(t *testing.T) {
	store, _, _ := seedRealSessions(t, "main")
	dir := t.TempDir()

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatalf("mgr.SetActive: %v", err)
	}
	if err := mgr.PersistMeta(dir); err != nil {
		t.Fatalf("mgr.PersistMeta: %v", err)
	}

	eng := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, dir)

	c := newRealEngineConnector(t, eng)
	c.mgr = mgr
	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]string{"type": "session_new"})
	readMetadata(t, ws)

	if !readWorkspaceMetaUntil(t, dir, eng.SessionID()) {
		t.Fatalf("session_new was not persisted to meta.json (current_session_id never became %s)", eng.SessionID())
	}
}
