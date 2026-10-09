// Package reload implements the manual two-phase configuration reload:
// parse and validate every config surface into fresh objects first (any
// failure aborts with zero impact), then atomically apply them to the live
// process (env-holder swap, in-place MCP reconcile with in-flight drain,
// hooks/skills/agents rebuilds, engine context refresh).
package reload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/repl"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// mcpDrainTimeout bounds how long the MCP reconcile waits for in-flight tool
// calls before disconnecting a server (assumption: 10s; on timeout the
// disconnect proceeds and the report notes the interruption).
const mcpDrainTimeout = 10 * time.Second

// Env is the post-reload environment consumed by the engine factory and the
// context-refresh closures. Swapped atomically via EnvHolder; live engines
// keep their construction-time values by design.
type Env struct {
	Cfg                *config.Config
	ProviderMap        config.ProviderMap
	Provider           llm.Provider
	Model              string
	PrimaryProviderCfg *config.Provider
	ModelThinking      map[string]llm.Effort
	ToolPrompts        []string
	SkillListing       string
	SkillCmds          []types.SkillCommand
	PermissionChecker  permission.PermissionChecker
	McpReg             *mcp.Registry // nil until first server exists
}

// EnvHolder is an atomic pointer to the current Env — no torn reads across
// concurrent engine creation and reloads.
type EnvHolder struct{ v atomic.Pointer[Env] }

// NewEnvHolder creates a holder with the initial (startup) Env.
func NewEnvHolder(e *Env) *EnvHolder {
	h := &EnvHolder{}
	h.Store(e)
	return h
}

// Load returns the current Env.
func (h *EnvHolder) Load() *Env { return h.v.Load() }

// Store replaces the current Env.
func (h *EnvHolder) Store(e *Env) { h.v.Store(e) }

// Deps are the process-lifetime singletons Start built. The orchestrator
// reconciles them in place; it never reconstructs them.
type Deps struct {
	WorkingDir string
	ProjectDir string
	PluginsDir string // "" = default; tests inject a temp dir
	EngineMgr  *engine.EngineManager
	Hooks      *hooks.Hooks
	SkillReg   *skills.Registry
	Env        *EnvHolder
	// LSPReg/ShortStore are the process-lifetime singletons Start built.
	// The reload needs them only for the ToolPrompts harvest below: the
	// tool set a fresh engine would get (recall iff ShortStore != nil, LSP
	// tool wired to the same registry) must match the harvested prompts.
	LSPReg     *lsp.Registry
	ShortStore *short.Store
}

// Orchestrator runs the two-phase reload.
type Orchestrator struct {
	// mu single-flights Reload (TryLock); also the seam the busy-path
	// test drives.
	mu sync.Mutex
	// afterValidate runs between the validate and apply phases (test seam
	// for the mid-flight settings-change guard).
	afterValidate func()
	// loadSkillsFn defaults to deps.SkillReg.Load and is the skills phase-2
	// entry. skills.Registry.Load is structurally fail-open (loadSkillsFromDir
	// returns nil on every ReadDir/parse error; Load has zero non-nil
	// returns), so the failure path is only reachable by injection. Making
	// Load report errors would change startup semantics — out of scope.
	loadSkillsFn func() error
	deps         Deps
	baseline     snapshot
}

// snapshot is the post-apply state the next reload diffs against.
type snapshot struct {
	userRaw []byte
	projRaw []byte
	prompt  string
	ctxMap  map[string]string
	plugins []*plugins.PluginPieces
}

// NewOrchestrator captures the change-detection baseline (fail-safe: parse
// errors are logged and leave a zero baseline, never failing Start).
func NewOrchestrator(d Deps) *Orchestrator {
	o := &Orchestrator{deps: d}
	o.loadSkillsFn = func() error { return d.SkillReg.Load() }
	o.captureBaseline()
	return o
}

func (o *Orchestrator) captureBaseline() {
	s := snapshot{}
	s.userRaw, s.projRaw = readSettingsRaw(o.deps.WorkingDir)
	if pieces, err := plugins.LoadPluginPieces(o.deps.PluginsDir, nil); err != nil {
		slog.Warn("reload: baseline plugin scan failed; first reload will report all plugin files as changed", "error", err)
	} else {
		s.plugins = pieces
	}
	if env := o.deps.Env.Load(); env != nil {
		s.prompt = ctxbuild.BuildSystemPrompt(o.deps.WorkingDir, o.deps.ProjectDir, env.ToolPrompts, env.SkillListing, "")
	}
	s.ctxMap = ctxbuild.LoadContextFiles(o.deps.WorkingDir)
	o.baseline = s
}

// readSettingsRaw reads the user (~/.gbot) and project (.gbot) settings.json
// bytes; missing files read as nil.
func readSettingsRaw(workingDir string) (user, project []byte) {
	if configDir, err := config.ConfigDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(configDir, "settings.json")); err == nil {
			user = data
		}
	}
	if data, err := os.ReadFile(filepath.Join(workingDir, ".gbot", "settings.json")); err == nil {
		project = data
	}
	return user, project
}

// Env returns the current live environment (post-swap after a reload).
func (o *Orchestrator) Env() *Env { return o.deps.Env.Load() }

// Reload executes the two-phase reload. Phase 0-1 validate with zero
// mutation (any failure → Report{Applied:false} naming the file); phase 2-4
// apply with no aborts (per-surface failures become failed rows and the
// previous state stays live).
func (o *Orchestrator) Reload(ctx context.Context) *Report {
	if !o.mu.TryLock() {
		return &Report{Applied: false, Phase: 0, Err: "reload already in progress", LastReloadAt: time.Now()}
	}
	defer o.mu.Unlock()

	rep := &Report{LastReloadAt: time.Now()}
	d := o.deps

	// ------------------------------------------------------------------
	// Phase 0 — settings.json: load, strict hooks parse, providers, primary.
	userRaw, projRaw := readSettingsRaw(d.WorkingDir)
	cfg, err := config.Load()
	if err != nil {
		return o.abort(rep, 0, err, FileEntry{Path: displaySettingsPath(), Status: StatusFailed, ErrLine: err.Error()})
	}
	var settingsHooks hooks.HooksConfig
	if len(cfg.Hooks) > 0 {
		if err := json.Unmarshal(cfg.Hooks, &settingsHooks); err != nil {
			return o.abort(rep, 0, fmt.Errorf("settings hooks: %w", err),
				FileEntry{Path: displaySettingsPath(), Status: StatusFailed, ErrLine: err.Error()})
		}
	}
	providerMap, err := config.CreateAllProviders(cfg)
	if err != nil {
		return o.abort(rep, 0, err, FileEntry{Path: displaySettingsPath(), Status: StatusFailed, ErrLine: err.Error()})
	}
	provider, model, primaryCfg, err := cfg.ResolvePrimary(providerMap)
	if err != nil {
		return o.abort(rep, 0, err, FileEntry{Path: displaySettingsPath(), Status: StatusFailed, ErrLine: err.Error()})
	}

	// ------------------------------------------------------------------
	// Phase 1 — plugin directories + project .mcp.json.
	pieces, err := plugins.LoadPluginPieces(d.PluginsDir, cfg)
	if err != nil {
		row := FileEntry{Status: StatusFailed, ErrLine: err.Error()}
		if fe, ok := errors.AsType[*plugins.FileError](err); ok {
			row.Path = displayPath(fe.Path)
		} else {
			row.Path = "plugins"
		}
		return o.abort(rep, 1, err, row)
	}
	projectMcp, mcpErrs := mcp.GetProjectMcpConfigsFromCwd(d.WorkingDir)
	if dropped := droppedServerErrors(mcpErrs); len(dropped) > 0 {
		e := dropped[0]
		return o.abort(rep, 1, errors.New(e.Message),
			FileEntry{Path: e.Path, Status: StatusFailed, ErrLine: e.Message})
	}

	if o.afterValidate != nil {
		o.afterValidate()
	}
	// Mid-flight settings-change guard: a save landing between validation
	// and apply would be silently overwritten by the swap; abort instead
	// (the residual ordering window is documented in the plan assumptions).
	if u2, p2 := readSettingsRaw(d.WorkingDir); !bytes.Equal(userRaw, u2) || !bytes.Equal(projRaw, p2) {
		return o.abort(rep, 1, errors.New("settings.json changed during reload; retry"),
			FileEntry{Path: displaySettingsPath(), Status: StatusFailed,
				Note: "mid-flight settings change detected; reload aborted"})
	}

	// ------------------------------------------------------------------
	// Phase 2 — apply: skills, agents, hooks, repl, env build + swap.
	oldEnv := d.Env.Load()
	newEnv := &Env{
		Cfg:                cfg,
		ProviderMap:        providerMap,
		Provider:           provider,
		Model:              model,
		PrimaryProviderCfg: primaryCfg,
		ModelThinking:      cfg.BuildModelThinking(),
		McpReg:             oldEnv.McpReg,
	}

	var pluginServers = map[string]mcp.ScopedMcpServerConfig{}
	var pluginHooks hooks.HooksConfig
	var pluginSkills []types.SkillCommand
	var pluginAgents []types.AgentDefinition
	var pluginRepls []repl.ReplScript
	for _, p := range pieces {
		maps.Copy(pluginServers, p.McpServers)
		pluginHooks = plugins.MergeHooks(pluginHooks, p.Hooks)
		pluginSkills = append(pluginSkills, p.Skills...)
		pluginAgents = append(pluginAgents, p.Agents...)
		for _, rs := range p.ReplScripts {
			pluginRepls = append(pluginRepls, repl.ReplScript{Name: rs.Plugin + ":" + rs.Name, Source: rs.Source})
		}
	}

	skillsFailed := false
	if err := o.loadSkillsFn(); err != nil {
		skillsFailed = true
		rep.Files = append(rep.Files, FileEntry{
			Path:    displayHomePath(filepath.Join(".gbot", "skills")),
			Status:  StatusFailed,
			ErrLine: err.Error(),
		})
		rep.Counts.Failed++
		// The failed Load left r.skills untouched, so OLD plugin skills stay
		// live; RegisterPluginSkills would append duplicates — skip it.
	}
	if !skillsFailed {
		d.SkillReg.RegisterPluginSkills(pluginSkills)
	}

	agent.InitLoader(d.WorkingDir)
	agent.GlobalLoader().RegisterPluginAgents(pluginAgents)

	d.Hooks.ReloadConfig(plugins.MergeHooks(settingsHooks, pluginHooks))
	repl.SetReplScripts(pluginRepls)

	configDir, _ := config.ConfigDir()
	if rules := permission.LoadConfig(configDir, d.WorkingDir); len(rules) > 0 {
		newEnv.PermissionChecker = permission.NewChecker(rules)
	}

	// ToolPrompts harvest: a throwaway CreateTools with the new cfg yields
	// exactly the tool set a fresh engine would get; MCP changes never enter
	// ToolPrompts (MCP tools are per-engine snapshots, never registered into
	// the Start-level refs registry).
	envMcpReg := oldEnv.McpReg
	allServers := map[string]mcp.ScopedMcpServerConfig{}
	maps.Copy(allServers, projectMcp)
	maps.Copy(allServers, pluginServers)
	if envMcpReg == nil && len(allServers) > 0 {
		// First MCP server arrived after a startup that had none: build the
		// registry so new engines get MCP tools; live engines keep nil (they
		// never had MCP tools). No config watcher: startup-only concern.
		envMcpReg = mcp.NewRegistry(
			mcp.NewClientManager(mcp.TransportFactory{}, true, d.WorkingDir),
			mcp.ChangeCallbacks{})
		newEnv.McpReg = envMcpReg
	}
	refs := engine.CreateTools(engine.SharedDeps{
		WorkingDir: d.WorkingDir,
		SkillReg:   d.SkillReg,
		McpReg:     envMcpReg,
		Hooks:      d.Hooks,
		Cfg:        cfg,
		LSPReg:     d.LSPReg,
		// WSRegistry: nil is correct — the reload never wires the daemon's
		// inbound-device pool; a fresh engine build gets it from Start's deps.
		WSRegistry: nil,
		ShortStore: d.ShortStore,
	}, task.NewList(""))
	for _, tl := range refs.Reg.EnabledTools() {
		if p := tl.Prompt(); p != "" {
			newEnv.ToolPrompts = append(newEnv.ToolPrompts, p)
		}
	}
	if skillsFailed {
		// Carry the previous listing: the skills registry is stale-but-live.
		newEnv.SkillListing = oldEnv.SkillListing
		newEnv.SkillCmds = oldEnv.SkillCmds
	} else {
		skillCmds := d.SkillReg.GetSkillToolSkills()
		newEnv.SkillCmds = skillCmds
		newEnv.SkillListing = skilltool.BuildSkillListing(skillCmds, primaryCfg.ResolveContext(model))
	}
	d.Env.Store(newEnv)

	// ------------------------------------------------------------------
	// Phase 3 — MCP reconcile (in place on the shared registry).
	if envMcpReg != nil {
		if res, ok := envMcpReg.Reconcile(ctx, allServers, mcpDrainTimeout); !ok {
			rep.Files = append(rep.Files, FileEntry{Path: "MCP", Status: StatusUnchanged, Note: "MCP reconcile skipped; another reload in progress"})
			rep.Counts.Unchanged++
		} else if len(res.Added)+len(res.Removed)+len(res.Changed)+len(res.ReconnectFailed) > 0 {
			note := fmt.Sprintf("%d added · %d removed · %d changed", len(res.Added), len(res.Removed), len(res.Changed))
			if len(res.ReconnectFailed) > 0 {
				note += fmt.Sprintf(" · %d reconnect failed", len(res.ReconnectFailed))
			}
			if len(res.DrainedLate) > 0 {
				note += fmt.Sprintf(" · %d disconnected with calls in flight", len(res.DrainedLate))
			}
			rep.Files = append(rep.Files, FileEntry{Path: "MCP", Status: StatusReloaded, Note: note})
			rep.Counts.Reloaded++
		}
		// No diff and no failure → no MCP row: nothing to disclose.
	}

	// ------------------------------------------------------------------
	// Phase 4 — refresh live engine context (top-level engines only;
	// sub-engines have nil refreshers by design).
	agentDefs := agent.ListAgentDefinitions()
	if d.EngineMgr != nil {
		for _, vs := range d.EngineMgr.List() {
			if vs.Engine == nil {
				continue
			}
			if vs.Engine.RefreshContext() {
				rep.EnginesRefreshed++
			}
			vs.Engine.SetAgentDefs(agentDefs)
		}
	}

	// ------------------------------------------------------------------
	// Report rows vs the pre-reload baseline, then update the baseline.
	o.appendDiffRows(rep, userRaw, projRaw, pieces, newEnv)

	rep.Applied = true
	rep.Phase = 4
	o.baseline = snapshot{
		userRaw: userRaw,
		projRaw: projRaw,
		plugins: pieces,
		// The carried-over SkillListing (skills-failure path included) is
		// what the next diff must compare against.
		prompt: ctxbuild.BuildSystemPrompt(d.WorkingDir, d.ProjectDir, newEnv.ToolPrompts, newEnv.SkillListing, ""),
		ctxMap: ctxbuild.LoadContextFiles(d.WorkingDir),
	}
	return rep
}

// abort fills the failure envelope and counts the rows it is given.
func (o *Orchestrator) abort(rep *Report, phase int, err error, rows ...FileEntry) *Report {
	rep.Applied = false
	rep.Phase = phase
	rep.Err = err.Error()
	for _, r := range rows {
		if r.Status == StatusFailed {
			rep.Counts.Failed++
		}
	}
	rep.Files = append(rep.Files, rows...)
	return rep
}

// appendDiffRows computes the settings/plugin/prompt rows against the
// baseline and updates the counts.
func (o *Orchestrator) appendDiffRows(rep *Report, userRaw, projRaw []byte, pieces []*plugins.PluginPieces, newEnv *Env) {
	b := o.baseline
	addRow := func(row FileEntry) {
		switch row.Status {
		case StatusReloaded:
			rep.Counts.Reloaded++
		case StatusUnchanged:
			rep.Counts.Unchanged++
		case StatusFailed:
			rep.Counts.Failed++
		}
		rep.Files = append(rep.Files, row)
	}

	// settings.json row (raw bytes; note = JSON tree diff).
	settingsChanged := !bytes.Equal(b.userRaw, userRaw) || !bytes.Equal(b.projRaw, projRaw)
	row := FileEntry{Path: displaySettingsPath()}
	if settingsChanged {
		row.Status = StatusReloaded
		row.Note = strings.Join(diffSettingsJSON(b.userRaw, userRaw), "; ")
		if row.Note == "" {
			row.Note = strings.Join(diffSettingsJSON(b.projRaw, projRaw), "; ")
		}
		if row.Note == "" {
			row.Note = "changed"
		}
		row.NoteKey = "newSessionsOnly"
		rep.SettingsChanged = true
	} else {
		row.Status = StatusUnchanged
	}
	addRow(row)

	// Per-plugin rows, keyed by plugin name against the baseline pieces.
	oldPieces := map[string]*plugins.PluginPieces{}
	for _, p := range b.plugins {
		oldPieces[p.Name] = p
	}
	for _, p := range pieces {
		old := oldPieces[p.Name] // nil = new plugin, every surface counts as changed
		declaredMcp := p.Manifest != nil && p.Manifest.McpServers != ""
		if declaredMcp {
			added, removed, changed := diffMcpServers(old, p)
			row := FileEntry{Path: displayPluginPath(p.RootPath, "mcp.json")}
			if added+removed+changed > 0 {
				row.Status = StatusReloaded
				row.Note = fmt.Sprintf("%d added · %d removed · %d changed", added, removed, changed)
			} else {
				row.Status = StatusUnchanged
			}
			addRow(row)
		}
		if len(p.Hooks) > 0 || (old != nil && len(old.Hooks) > 0) {
			row := FileEntry{Path: displayPluginPath(p.RootPath, filepath.Join("hooks", "hooks.json"))}
			if old == nil || !reflect.DeepEqual(old.Hooks, p.Hooks) {
				row.Status = StatusReloaded
				row.Note = "effective on next event dispatch"
			} else {
				row.Status = StatusUnchanged
			}
			addRow(row)
		}
		if p.Manifest != nil && p.Manifest.Skills != "" {
			row := FileEntry{Path: displayPluginPath(p.RootPath, "skills") + string(os.PathSeparator)}
			row.Status = fingerprintStatus(skillsFingerprint, old, p)
			addRow(row)
		}
		{
			// agents dir is scanned unconditionally (loadAgents is not
			// manifest-gated), so the row is always present per plugin.
			row := FileEntry{Path: displayPluginPath(p.RootPath, "agents") + string(os.PathSeparator)}
			row.Status = fingerprintStatus(agentsFingerprint, old, p)
			addRow(row)
		}
		if p.Manifest != nil && p.Manifest.Repls != "" {
			row := FileEntry{Path: displayPluginPath(p.RootPath, "repl") + string(os.PathSeparator)}
			row.Status = fingerprintStatus(replsFingerprint, old, p)
			addRow(row)
		}
	}

	// Prompt/context row: prompt built with the respective env parts, plus
	// the context files. Any prompt-affecting change (MEMORY.md/CLAUDE.md,
	// tool prompts, skill listing) flips it — that mirrors what the engine
	// refresh actually swapped.
	newPrompt := ctxbuild.BuildSystemPrompt(o.deps.WorkingDir, o.deps.ProjectDir, newEnv.ToolPrompts, newEnv.SkillListing, "")
	newCtx := ctxbuild.LoadContextFiles(o.deps.WorkingDir)
	promptRow := FileEntry{Path: "MEMORY.md · CLAUDE.md"}
	if newPrompt != b.prompt || !reflect.DeepEqual(newCtx, b.ctxMap) {
		promptRow.Status = StatusReloaded
		promptRow.NoteKey = "effectiveNextRequest"
		rep.PromptChanged = true
	} else {
		promptRow.Status = StatusUnchanged
	}
	addRow(promptRow)
}

// displaySettingsPath renders the user settings path in display form.
func displaySettingsPath() string {
	if configDir, err := config.ConfigDir(); err == nil {
		return displayPath(filepath.Join(configDir, "settings.json"))
	}
	return "~/.gbot/settings.json"
}

// displayPath replaces the home prefix with ~ for readable report rows.
func displayPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return filepath.Join("~", rest)
	}
	return p
}

func displayHomePath(rel string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return rel
	}
	return displayPath(filepath.Join(home, rel))
}

func displayPluginPath(root, rel string) string {
	return displayPath(filepath.Join(root, rel))
}
