package app

import (
	"log/slog"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tui"
)

// restoreEnginesDeps bundles everything restoreEngines needs. Explicit struct
// beats a long parameter list and makes the function signature stable when
// new wiring is added.
type restoreEnginesDeps struct {
	mgr        *engine.EngineManager
	factory    tui.EngineFactoryFn
	store      *short.Store
	projectDir string
	model      string
}

// restoreEngines reads meta.json, migrates legacy format if needed, then
// rebuilds every engine listed there via the factory. Returns the active
// engine's session ID (empty when no store was configured).
//
// First run (no meta) synthesizes a single main engine. Missing sessions
// fall back to a fresh session via ResumeOrInitSession. Every engine —
// including main — goes through the factory, so wiring stays uniform.
func restoreEngines(d restoreEnginesDeps) string {
	meta, err := short.ReadWorkspaceMeta(d.projectDir)
	if err != nil {
		slog.Warn("restore: read workspace meta failed, will synthesize default", "error", err)
	}
	enginesToRestore, activeID := planRestore(meta, d.model)

	for _, em := range enginesToRestore {
		// em.Model is "provider/model" (e.g. "zhipu/glm-5.2"). The engine's
		// own model field needs the bare registration name (e.g. "glm-5.2")
		// — that's what the provider API accepts and what status bar shows.
		// Split on the FIRST "/" only so providers whose model name itself
		// contains a slash (e.g. openrouter's "openrouter/owl-alpha") keep
		// their internal prefix intact.
		engineModel := em.Model
		engineProviderName := ""
		if before, after, ok := strings.Cut(em.Model, "/"); ok {
			engineProviderName = before
			engineModel = after
		}
		eng, handler, err := d.factory(em.ID, em.Name, engineProviderName, engineModel)
		if err != nil {
			slog.Error("restore: build engine failed", "id", em.ID, "error", err)
			continue
		}
		eng.SetStore(d.store, d.projectDir)

		// Resume the engine's last session; fall back to a new session if
		// the recorded one is missing or unresumable (user deleted DB row,
		// partial write, schema drift, etc.).
		resumeID := em.ActiveSessionID
		if resumeID != "" {
			if _, err := eng.SwitchSession(resumeID); err != nil {
				slog.Warn("restore: switch session failed, creating new", "id", em.ID, "error", err)
				resumeID = ""
			}
		}
		if resumeID == "" {
			id, err := eng.ResumeOrInitSession(d.projectDir, engineModel)
			if err != nil {
				slog.Warn("restore: session init failed", "id", em.ID, "error", err)
			} else {
				resumeID = id
			}
		}

		d.mgr.Add(&engine.EngineViewState{
			Engine:          eng,
			Repl:            nil, // set by tui on first switch
			Handler:         handler,
			History:         nil, // set by tui.NewAppWithManager
			ID:              em.ID,
			Name:            em.Name,
			ActiveSessionID: resumeID,
			Model:           em.Model,
			CreatedAt:       time.Now(),
			LastActiveAt:    time.Now(),
			ReadOnly:        false,
			System:          false,
		})
	}

	if err := d.mgr.SetActive(activeID); err != nil {
		slog.Warn("restore: set active engine failed", "id", activeID, "error", err)
	}
	if err := d.mgr.PersistMeta(d.projectDir); err != nil {
		slog.Warn("restore: write workspace meta failed", "error", err)
	}
	if vs := d.mgr.Active(); vs != nil {
		return vs.ActiveSessionID
	}
	return ""
}

// planRestore decides which engines to rebuild on startup and which one is
// active. Pure function over meta.json contents so it can be tested without
// spinning up engines or a store.
//
// Two cases:
//   - nil meta (first run or read error): synthesize a single main engine.
//   - non-empty meta: restore every engine in Engines array, honor ActiveEngineID.
func planRestore(meta *short.WorkspaceMeta, defaultModel string) ([]short.EngineMeta, string) {
	if meta == nil || len(meta.Engines) == 0 {
		return []short.EngineMeta{{
			ID:    "main",
			Name:  "main",
			Model: defaultModel,
		}}, "main"
	}
	activeID := "main"
	if meta.ActiveEngineID != "" {
		activeID = meta.ActiveEngineID
	}
	return meta.Engines, activeID
}
