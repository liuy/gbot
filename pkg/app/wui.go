package app

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/wui"
)

// deleteSessionsFor backs engine_delete's session cleanup (injected via
// SetDeleteSessionsFn in start.go): one engine's sessions, messages and FTS
// rows, transactionally. Named so the error forwarding is unit-testable —
// the wiring closure in start.go stays a one-liner.
func deleteSessionsFor(store *short.Store, projectDir string) func(engineID string) error {
	return func(engineID string) error {
		_, err := store.DeleteSessionsByEngine(projectDir, engineID)
		return err
	}
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
	// Normalize before anything else: the name is stored and duplicate-checked
	// as-is, so an untrimmed "  x  " would slip past ValidateName (which trims
	// its own copy) and register a second engine that resolves to the same
	// address. A whitespace-only name is an empty name — auto-named, not rejected.
	name = strings.TrimSpace(name)
	if name != "" {
		if err := mgr.ValidateName(name); err != nil {
			return "", err
		}
	}
	// One mint per engine: the minted agent-N is both the address and the
	// display name (see EngineManager.NewEngineID).
	id := mgr.NewEngineID()
	if name == "" {
		name = id
	}
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
