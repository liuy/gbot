package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/liuy/gbot/pkg/connector/wui"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tui"
)

// createEngineForWUI builds a new engine via engineFactory, registers it
// in the manager, subscribes the wui connector to its hub, and wires
// OnStreamDone. Returns the new engine ID. Called by the connector's
// engine_new handler via the closure injected by SetCreateEngineFn.
func createEngineForWUI(
	name string,
	mgr *engine.EngineManager,
	factory tui.EngineFactoryFn,
	store *short.Store,
	projectDir string,
	connector *wui.WUIConnector,
	currentProvider string,
	currentModel string,
) (string, error) {
	if name == "" {
		name = mgr.NewEngineName()
	}
	id := mgr.NewEngineID()
	eng, handler, err := factory(id, name, currentProvider, currentModel)
	if err != nil {
		return "", fmt.Errorf("factory: %w", err)
	}
	if store != nil {
		eng.SetStore(store, projectDir)
	}
	if err := eng.NewSession(projectDir, ""); err != nil {
		eng.Close()
		return "", fmt.Errorf("new session: %w", err)
	}
	vs := &engine.EngineViewState{
		Engine:          eng,
		Handler:         handler,
		ID:              id,
		Name:            name,
		ActiveSessionID: eng.SessionID(),
		Model:           eng.Model(),
		CreatedAt:       time.Now(),
		LastActiveAt:    time.Now(),
		Repl:            nil,
		History:         nil,
		ReadOnly:        false,
	}
	mgr.Add(vs)
	connector.RegisterEngine(vs)
	if err := mgr.PersistMeta(projectDir); err != nil {
		slog.Warn("wui: persist meta after engine new", "error", err)
	}
	return id, nil
}
