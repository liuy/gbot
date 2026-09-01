package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/liuy/gbot/pkg/connector/wui"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool/send"
	"github.com/liuy/gbot/pkg/tui"
)

// registerWUISendTool wires the WUI connector as the engine's "wui"
// FileSender and registers the Send tool (bound to the engine) on the
// engine's mutable registry. Called for every WUI-driven engine: those
// restored at boot (start.go WUI block) and those created at runtime via
// engine_new (createEngineForWUI). Without this, the Send tool is absent
// from WUI engines and the LLM cannot deliver files to the browser.
func registerWUISendTool(eng *engine.Engine, wc *wui.WUIConnector) {
	eng.RegisterFileSender("wui", wc)
	reg := eng.ToolRefs().Reg
	if _, ok := reg.Lookup("Send"); ok {
		return
	}
	reg.MustRegister(send.New(eng))
}

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
	registerWUISendTool(eng, connector)
	vs := &engine.EngineViewState{
		Engine:          eng,
		Handler:         handler,
		Thinking:        "", // fresh engine: no sticky override
		ID:              id,
		Name:            name,
		ActiveSessionID: eng.SessionID(),
		Model:           eng.Model(),
		CreatedAt:       time.Now(),
		LastActiveAt:    time.Now(),
		Repl:            nil,
		History:         nil,
		ReadOnly:        false,
		System:          false,
	}
	mgr.Add(vs)
	connector.RegisterEngine(vs)
	if err := mgr.PersistMeta(projectDir); err != nil {
		slog.Warn("wui: persist meta after engine new", "error", err)
	}
	return id, nil
}
