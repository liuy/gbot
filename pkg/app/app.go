package app

import (
	"log/slog"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/types"
)

type Options struct {
	DaemonMode bool
	WSPort     string
	Verbose    bool
}

type Instance struct {
	EngineMgr          *engine.EngineManager
	EngineFactory      tui.EngineFactoryFn
	Store              *short.Store
	SessionID          string
	Cfg                *config.Config
	ProviderMap        config.ProviderMap
	Provider           llm.Provider
	Model              string
	PrimaryProviderCfg *config.Provider
	SystemPrompt       string
	SkillListing       string
	ToolPrompts        []string
	MainRefs           *engine.ToolRefs
	SkillCmdsForTUI    []types.SkillCommand
	HookSystem         *hooks.Hooks
	WorkingDir         string
	ProjectDir         string
	DaemonMode         bool
	WSPort             string
	Hub                *hub.Hub
	MediaStores        []*media.Store
	LSPReg             *lsp.Registry
	Logger             *slog.Logger
	PIDCleanup         func()
}
