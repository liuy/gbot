package webchat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
)

// TestModelSwitch_PersistsToMetaJSON verifies that handleModelSwitch writes
// the new provider/model to .gbot/meta.json via EngineManager.PersistMeta,
// mirroring the TUI's persistModelSelection. Without this, webchat model
// switches are lost on restart.
func TestModelSwitch_PersistsToMetaJSON(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "Main"})
	if err := mgr.SetActive("main"); err != nil {
		t.Fatalf("mgr.SetActive: %v", err)
	}

	providers := buildTestProviders()
	configs := buildTestProviderConfigs()
	c := newTestConnectorWithConfig(t, hub.NewHub(), providers, configs)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }

	ws := dialAndStore(t, c)
	defer ws.Close()

	switchMsg, _ := json.Marshal(map[string]string{
		"type":     "model_switch",
		"provider": "openai",
		"model":    "gpt-5",
	})
	if err := ws.WriteMessage(websocket.TextMessage, switchMsg); err != nil {
		t.Fatalf("write model_switch: %v", err)
	}

	if !waitFor(2e9, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			return false
		}
		var meta struct {
			Engines []struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"engines"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return false
		}
		for _, e := range meta.Engines {
			if e.ID == "main" && e.Model == "openai/gpt-5" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("timeout: model switch was not persisted to meta.json")
	}

	vs := mgr.Get("main")
	if vs == nil {
		t.Fatal("mgr.Get(main) returned nil")
	}
	if vs.Model != "openai/gpt-5" {
		t.Errorf("mgr active model = %q, want %q", vs.Model, "openai/gpt-5")
	}
}
