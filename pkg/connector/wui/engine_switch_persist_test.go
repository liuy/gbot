package wui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
)

// TestEngineSwitch_PersistsToMetaJSON verifies that switching engines via
// the WUI engine picker writes the new active_engine_id to meta.json.
// Without PersistMeta after SetActive, the active engine is lost on restart
// and always reverts to whatever was last persisted.
func TestEngineSwitch_PersistsToMetaJSON(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatalf("mgr.SetActive(main): %v", err)
	}
	// Persist initial state with main as active
	if err := mgr.PersistMeta(dir); err != nil {
		t.Fatalf("mgr.PersistMeta: %v", err)
	}

	// Build connector with two engine slots
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr

	// Add e2 slot with its own mockEngine that returns projectDir
	e2Mock := &mockEngine{}
	e2Mock.projectDirFn = func() string { return dir }
	e2Slot := &engineSlot{
		engineID:    "e2",
		engine:      e2Mock,
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	c.slots["e2"] = e2Slot

	// Set main's projectDir too
	c.mock().projectDirFn = func() string { return dir }

	ws := dialAndStore(t, c)
	defer ws.Close()

	switchMsg, _ := json.Marshal(map[string]string{
		"type":     "engine_switch",
		"engineID": "e2",
	})
	if err := ws.WriteMessage(websocket.TextMessage, switchMsg); err != nil {
		t.Fatalf("write engine_switch: %v", err)
	}

	if !waitFor(2e9, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			return false
		}
		var meta struct {
			ActiveEngineID string `json:"active_engine_id"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return false
		}
		return meta.ActiveEngineID == "e2"
	}) {
		t.Fatal("timeout: engine switch was not persisted to meta.json (active_engine_id != e2)")
	}

	if mgr.ActiveID() != "e2" {
		t.Errorf("mgr.ActiveID() = %q, want %q", mgr.ActiveID(), "e2")
	}
}
