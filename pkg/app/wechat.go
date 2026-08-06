package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/connector/wechat"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tui"
)

// startWeChatDeps bundles the shared dependencies captured from main() plus the
// per-account state for one WeChat connector. Explicit struct beats a long
// parameter list and lets the caller stay a readable loop.
type startWeChatDeps struct {
	state              *wechat.State
	engineMgr          *engine.EngineManager
	provider           llm.Provider
	model              string
	maxTokens          int
	contextWindow      int
	logger             *slog.Logger
	mcpRegistry        *mcp.Registry
	hookSystem         *hooks.Hooks
	modelThinking      map[string]llm.ThinkingMode
	workingDir         string
	projectDir         string
	store              *short.Store
	deps               engine.SharedDeps
	primaryProviderCfg *config.Provider
	mediaStores        *[]*media.Store
	daemonMode         bool
	toolPrompts        []string
	skillListing       string
	lspReg             *lsp.Registry
}

// startWeChatConnector wires one WeChat account: builds (or adopts a restored)
// engine, subscribes the connector to the engine's hub, and starts the poll
// loop. Returns an error only when the engine cannot be resolved at all;
// softer failures (fresh session, start) are logged and swallowed so one
// account does not block the others.
func startWeChatConnector(d startWeChatDeps) error {
	state := d.state
	engineID := "wechat-" + state.AccountID
	engineName := "WeChat " + state.AccountID

	// The shared Hub the TUI handler and connector both subscribe to.
	// Built once per process for this engine; the handler is stored on
	// the EngineViewState so switchEngine can flip drain roles.
	var wcHub *hub.Hub
	var wcHandler *tui.TUIHandler

	// Check if engine already exists (restored from meta.json)
	if d.engineMgr.Get(engineID) == nil {
		wcTaskList := task.NewList("")
		refs := engine.CreateTools(d.deps, wcTaskList)
		wcHub, wcHandler = tui.NewEngineHubWithHandler(engineID, nil)
		wcEng := engine.New(&engine.Params{
			Provider:          d.provider,
			Model:             d.model,
			InputModalities:   d.primaryProviderCfg.ResolveInput(d.model),
			MaxTokens:         d.maxTokens,
			TokenBudget:       d.contextWindow,
			MaxTurns:          0,
			Logger:            d.logger,
			Compactor:         nil,
			AutoCompact:       engine.AutoCompactConfig{},
			MCPRegistry:       d.mcpRegistry,
			Hooks:             d.hookSystem,
			PermissionChecker: nil,
			WorkingDir:        d.workingDir,
			TaskList:          wcTaskList,
			ModelThinking:     d.modelThinking,
			EngineID:          engineID,
			Dispatcher:        wcHub,
			ToolsProvider:     refs.Reg.ToolMapFn(),
		})
		engine.WireEngine(wcEng, refs, d.deps)
		wcEng.SetToolRefs(refs)
		wcEng.SetStore(d.store, d.projectDir)

		// Create fresh session — ResumeOrInitSession would resume
		// meta.CurrentSessionID which is shared with other engines.
		if err := wcEng.NewSession(d.projectDir, "WeChat"); err != nil {
			slog.Warn("wechat: new session failed", "error", err)
		}

		d.engineMgr.Add(&engine.EngineViewState{
			Engine:          wcEng,
			Repl:            tui.NewReplSnapshot(),
			Handler:         wcHandler,
			ReadOnly:        true,
			History:         nil,
			ID:              engineID,
			Name:            engineName,
			ActiveSessionID: wcEng.SessionID(),
			Model:           d.primaryProviderCfg.Name + "/" + d.model,
			CreatedAt:       time.Now(),
			LastActiveAt:    time.Now(),
			System:          false,
		})
	} else {
		// Restored from meta.json via restoreEngines, which builds the
		// engine through engineFactory with ReadOnly: false and never
		// wires the connector's hub. Two things must be restored here:
		//   1. ReadOnly: true — the TUI input must stay disabled because
		//      this engine is driven by the WeChat connector, not the TUI.
		//   2. The connector hub — recovered below from wcEng.Dispatcher()
		//      so the connector shares the same Hub the factory built
		//      (engineFactory sets Dispatcher: engineHub).
		//
		// In daemon mode engineFactory returns handler=nil (no TUIHandler),
		// so vs.Handler is nil here — the hub recovery below still works
		// because it reads from the engine's Dispatcher, not the handler.
		if vs := d.engineMgr.Get(engineID); vs != nil {
			vs.ReadOnly = true
		}
	}

	wcEng := d.engineMgr.Get(engineID).Engine
	if wcEng == nil {
		return fmt.Errorf("engine not found after creation: %s", engineID)
	}
	// The connector needs the same *hub.Hub the engine dispatches to
	// so it sees the engine's events. In the fresh-build branch wcHub
	// is set directly. In the restore branch the hub only exists as
	// the engine's Dispatcher (an interface) — recover it by type
	// assertion so the connector subscribes to the real hub the TUI
	// is already on, instead of a detached hub that sees no events.
	connectorHub := wcHub
	if connectorHub == nil {
		if h, ok := wcEng.Dispatcher().(*hub.Hub); ok && h != nil {
			connectorHub = h
		}
	}
	if connectorHub == nil {
		// Engine's dispatcher is not a *hub.Hub (shouldn't happen —
		// engineFactory always sets a *hub.Hub). Fall back to a fresh
		// hub so the connector doesn't crash; it just won't see TUI.
		slog.Warn("wechat: engine dispatcher is not a *hub.Hub, using isolated hub", "id", engineID)
		connectorHub = hub.NewHub()
	}
	wc := wechat.New(wcEng, connectorHub)
	// Register the WeChat-only Send tool on the engine's mutable registry.
	// Both branches (fresh build + restore) stash their ToolRefs on wcEng, so
	// this single call covers both. The TUI engine never reaches this path.
	wc.RegisterSendTool(wcEng, wcEng.ToolRefs().Reg)
	if d.daemonMode {
		memDir := filepath.Join(d.projectDir, "memory", engineID)
		wcEng.SetMemoryDir(memDir)
		wcEng.SetSystemPrompt(ctxbuild.BuildSystemPrompt(d.workingDir, d.projectDir, d.toolPrompts, d.skillListing, d.lspReg, memDir))
	}
	// Capture the media cache for shutdown teardown (the cache owns its
	// cleanup goroutine; main must Close() it to stop that goroutine).
	*d.mediaStores = append(*d.mediaStores, wc.MediaCache())
	go func() {
		if err := wc.Start(context.Background(), state, d.projectDir); err != nil {
			slog.Warn("wechat: start failed", "error", err)
		}
	}()
	return nil
}
