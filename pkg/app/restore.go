package app

import (
	"log/slog"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
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
// First run (no meta) synthesizes a single main engine. A stale/ghost
// recorded session falls back to ENGINE-scoped recovery: the engine's own
// latest session, else a fresh one — never the global CurrentSessionID
// (that belongs to whichever engine was last active). Every engine —
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

		// Resume the engine's last session; on failure recover ENGINE-scoped:
		// 1) the engine's own latest session (its older sessions may still
		//    exist even when the recorded id is stale),
		// 2) else a brand-new session bound to this engine's ID.
		// ResumeOrInitSession is NOT usable here: it resumes the GLOBAL
		// meta.CurrentSessionID — the last-active engine's session — so any
		// other engine would silently adopt it (incident #2: agent-3 showed
		// main's chat after a restart).
		resumeID := em.ActiveSessionID
		if resumeID != "" {
			// The recorded id must be (a) an existing row AND (b) owned by
			// THIS engine (sessions.engine_id). A buggy window could persist
			// another engine's session here — valid id, wrong owner — which
			// made the engine silently adopt a sibling's chat.
			adoptable := true
			if d.store != nil {
				ses, err := d.store.GetSession(resumeID)
				switch {
				case err != nil:
					slog.Warn("restore: recorded session missing, recovering engine-scoped", "id", em.ID, "error", err)
					adoptable = false
				case ses.EngineID != "" && ses.EngineID != em.ID:
					slog.Warn("restore: recorded session belongs to another engine, recovering engine-scoped",
						"id", em.ID, "session", resumeID[:8], "owner", ses.EngineID)
					adoptable = false
				}
			}
			if !adoptable {
				resumeID = ""
			} else if _, err := eng.SwitchSession(resumeID); err != nil {
				slog.Warn("restore: switch session failed, recovering engine-scoped", "id", em.ID, "error", err)
				resumeID = ""
			}
		}
		if resumeID == "" && d.store != nil {
			if recent, err := d.store.ListSessionsByEngine(d.projectDir, em.ID, 1); err == nil && len(recent) > 0 {
				if _, err := eng.SwitchSession(recent[0].SessionID); err == nil {
					resumeID = recent[0].SessionID
					slog.Info("restore: resumed engine's own latest session", "id", em.ID, "session", resumeID[:8])
				}
			}
		}
		if resumeID == "" {
			if err := eng.NewSession(d.projectDir, ""); err != nil {
				slog.Warn("restore: session init failed", "id", em.ID, "error", err)
			} else {
				resumeID = eng.SessionID()
			}
		}

		// Restore the persisted effort as a sticky override so a restart
		// doesn't silently drop the user's manual choice. Carry it into the
		// view-state only when it actually took — a garbage value from a
		// hand-edited meta.json must not round-trip forever.
		sticky := llm.Effort("")
		if em.Thinking != "" {
			if err := eng.SetThinking(llm.Effort(em.Thinking)); err != nil {
				slog.Warn("restore: invalid thinking effort", "id", em.ID, "value", em.Thinking, "error", err)
			} else {
				sticky = llm.Effort(em.Thinking)
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
			Thinking:        sticky,
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
