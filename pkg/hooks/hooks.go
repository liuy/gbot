package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Hooks — main facade for the hooks system
//
// Source: hooks.ts — main execution engine
// Provides methods for each hook event, dispatches to registered hooks,
// handles trust checks, once tracking, and short-circuit on blocking.
// ---------------------------------------------------------------------------

// Hooks is the main facade for the hooks system.
// Source: hooks.ts — main execution engine (~5000 lines).
type Hooks struct {
	config         HooksConfig
	executor       HookExecutor
	promptExecutor PromptExecutor
	agentExecutor  AgentExecutor
	jsRunner       JsHookRunner
	onceFired      sync.Map // tracks which hooks have already fired
	trusted        bool     // workspace trust status
	mu             sync.RWMutex

	// compiledMatchers caches compiled pattern functions per event.
	// Rebuilt on NewHooks/ReloadConfig to avoid recompiling on every dispatch.
	compiledMatchers map[string][]compiledMatcher

	// OnRewake is called when an async hook with AsyncRewake=true returns blocking.
	// The engine should inject a message and give the LLM another turn.
	// If nil, async rewake hooks behave like plain async hooks (result discarded).
	OnRewake func(reason string)
}

// compiledMatcher is a pre-compiled pattern function with its hooks.
type compiledMatcher struct {
	pattern    string
	matchFn    func(string) bool
	hooks      []HookConfig
	pluginRoot string // from HookMatcher.PluginRoot
}

// NewHooks creates a new Hooks facade with the given config and executor.
func NewHooks(config HooksConfig, executor HookExecutor) *Hooks {
	if config == nil {
		config = make(HooksConfig)
	}
	h := &Hooks{
		config:   config,
		executor: executor,
		trusted:  true, // default trusted, call SetTrust to change
	}
	h.compiledMatchers = h.buildCompiledMatchers()
	return h
}

// SetPromptExecutor injects a prompt hook executor.
// Breaks circular import: pkg/hooks/ cannot import pkg/llm/.
func (h *Hooks) SetPromptExecutor(pe PromptExecutor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.promptExecutor = pe
}

// SetAgentExecutor injects an agent hook executor.
// Breaks circular import: pkg/hooks/ cannot import pkg/engine/.
func (h *Hooks) SetAgentExecutor(ae AgentExecutor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentExecutor = ae
}

// SetJsHookRunner injects the process-global default js hook runner.
// Dispatch prefers a per-dispatch runner attached via WithJsRunner —
// engine-side dispatch sites pin their own REPL, so js hooks evaluate in
// the dispatching engine's hook session. This default serves dispatches
// that carry no runner: engine-less contexts (tests, boot fallback).
func (h *Hooks) SetJsHookRunner(jr JsHookRunner) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jsRunner = jr
}

// SetTrust marks the workspace as trusted or untrusted.
// Source: hooks.ts:286-296 — shouldSkipHookDueToTrust.
// When untrusted, all hooks are skipped.
func (h *Hooks) SetTrust(trusted bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trusted = trusted
}

// ReloadConfig swaps the hooks configuration at runtime.
func (h *Hooks) ReloadConfig(config HooksConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.config = config
	h.onceFired = sync.Map{} // reset once tracking on reload
	h.compiledMatchers = h.buildCompiledMatchersLocked()
}

// buildCompiledMatchers builds cached matchers from config (no lock held).
func (h *Hooks) buildCompiledMatchers() map[string][]compiledMatcher {
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()
	return buildCompiledMatchersFrom(cfg)
}

// buildCompiledMatchersLocked builds cached matchers (caller holds lock).
func (h *Hooks) buildCompiledMatchersLocked() map[string][]compiledMatcher {
	return buildCompiledMatchersFrom(h.config)
}

func buildCompiledMatchersFrom(cfg HooksConfig) map[string][]compiledMatcher {
	result := make(map[string][]compiledMatcher, len(cfg))
	for event, matchers := range cfg {
		compiled := make([]compiledMatcher, 0, len(matchers))
		for _, m := range matchers {
			compiled = append(compiled, compiledMatcher{
				pattern:    m.Matcher,
				matchFn:    CompileMatcher(m.Matcher),
				hooks:      m.Hooks,
				pluginRoot: m.PluginRoot,
			})
		}
		result[event] = compiled
	}
	return result
}

// ---------------------------------------------------------------------------
// Event methods — source: hooks.ts (dispatch for each event)
// ---------------------------------------------------------------------------

// PreToolUse runs before tool execution. Returns decision + results.
// Blocking result (exit 2) → HookDecisionBlock.
// Source: toolHooks.ts:435 — runPreToolUseHooks.
func (h *Hooks) PreToolUse(ctx context.Context, input *HookInput) (HookDecision, []HookResult) {
	results := h.dispatch(ctx, HookPreToolUse, input)
	decision := HookDecisionPassthrough
	for _, r := range results {
		if r.Outcome == HookOutcomeBlocking {
			decision = HookDecisionBlock
			break
		}
		if r.Output != nil && r.Output.Decision == "approve" {
			decision = HookDecisionApprove
		}
	}
	return decision, results
}

// PostToolUse runs after successful tool execution.
// Source: toolHooks.ts:39 — runPostToolUseHooks.
func (h *Hooks) PostToolUse(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookPostToolUse, input)
}

// PostToolUseFailure runs after tool failure.
// Source: toolHooks.ts:193 — runPostToolUseFailureHooks.
func (h *Hooks) PostToolUseFailure(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookPostToolUseFailure, input)
}

// Stop runs before Claude concludes response.
// Returns non-nil result if blocking (engine gives LLM another turn).
// Source: stopHooks.ts — handleStopHooks.
func (h *Hooks) Stop(ctx context.Context, input *HookInput) *HookResult {
	results := h.dispatch(ctx, HookStop, input)
	return findBlockingResult(results)
}

// SubagentStop runs before sub-agent concludes.
// Source: registerFrontmatterHooks.ts:40-41 — Stop → SubagentStop conversion.
func (h *Hooks) SubagentStop(ctx context.Context, input *HookInput) *HookResult {
	results := h.dispatch(ctx, HookSubagentStop, input)
	return findBlockingResult(results)
}

// SubagentStart runs when a sub-agent starts.
// Source: coreSchemas.ts:540-547 — SubagentStartHookInputSchema.
func (h *Hooks) SubagentStart(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookSubagentStart, input)
}

// PermissionRequest runs before a permission prompt is shown to the user.
// Source: coreSchemas.ts:425-428 — PermissionRequestHookInputSchema.
func (h *Hooks) PermissionRequest(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookPermissionRequest, input)
}

// StopFailure runs when turn ends due to API error.
func (h *Hooks) StopFailure(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookStopFailure, input)
}

// UserPromptSubmit runs when user submits a prompt.
func (h *Hooks) UserPromptSubmit(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookUserPromptSubmit, input)
}

// SessionStart runs on session startup/resume.
func (h *Hooks) SessionStart(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookSessionStart, input)
}

// SessionEnd runs on session shutdown.
func (h *Hooks) SessionEnd(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookSessionEnd, input)
}

// PreCompact runs before conversation compaction.
func (h *Hooks) PreCompact(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookPreCompact, input)
}

// PostCompact runs after conversation compaction.
func (h *Hooks) PostCompact(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookPostCompact, input)
}

// TaskCreated runs after the task file already exists, so the hook sees a real
// task_id. A blocking result means the caller must delete the task it created.
// Source: hooks.ts:3745-3773 — executeTaskCreatedHooks.
func (h *Hooks) TaskCreated(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookTaskCreated, input)
}

// TaskCompleted runs before a completion is applied. A blocking result means
// the caller must abort the update.
// Source: hooks.ts:3789-3817 — executeTaskCompletedHooks.
func (h *Hooks) TaskCompleted(ctx context.Context, input *HookInput) []HookResult {
	return h.dispatch(ctx, HookTaskCompleted, input)
}

// StopHookMessage formats a blocking Stop/SubagentStop result for the model.
// Source: hooks.ts:1894-1896 — getStopHookMessage.
func StopHookMessage(r HookResult) string {
	return "Stop hook feedback:\n" + blockingErrorText(r)
}

// TaskCreatedHookMessage formats a blocking TaskCreated result for the model.
// Source: hooks.ts:1914-1918 — getTaskCreatedHookMessage.
func TaskCreatedHookMessage(r HookResult) string {
	return "TaskCreated hook feedback:\n" + blockingErrorText(r)
}

// TaskCompletedHookMessage formats a blocking TaskCompleted result for the model.
// Source: hooks.ts:1925-1929 — getTaskCompletedHookMessage.
func TaskCompletedHookMessage(r HookResult) string {
	return "TaskCompleted hook feedback:\n" + blockingErrorText(r)
}

// blockingErrorText reproduces the two texts TS puts after the "hook feedback"
// header. TS parses hook stdout as JSON first, so a hook that emits
// {"decision":"block","reason":...} is reported by its reason
// (hooks.ts:532-533) and never reaches the exit-2 fallback, which names the
// hook and quotes its stderr (hooks.ts:2660-2661).
func blockingErrorText(r HookResult) string {
	if r.Output != nil && r.Output.Decision == "block" {
		if r.Output.Reason != "" {
			return r.Output.Reason
		}
		return "Blocked by hook"
	}
	if r.Stderr != "" {
		return "[" + r.HookName + "]: " + r.Stderr
	}
	return "[" + r.HookName + "]: No stderr output"
}

// ---------------------------------------------------------------------------
// dispatch — source: hooks.ts:1603-1848
//
// Core dispatch logic: find matchers → filter by pattern → check once →
// execute by type → short-circuit on blocking.
// ---------------------------------------------------------------------------

func (h *Hooks) dispatch(ctx context.Context, event HookEventName, input *HookInput) []HookResult {
	// 1. Trust check
	h.mu.RLock()
	trusted := h.trusted
	compiled := h.compiledMatchers[string(event)]
	exec := h.executor
	pe := h.promptExecutor
	ae := h.agentExecutor
	jr := h.jsRunner
	h.mu.RUnlock()

	// Per-dispatch runner (WithJsRunner) shadows the injected default:
	// engine-side dispatch sites pin their own REPL so js hooks evaluate
	// in the dispatching engine's hook session, not the global one.
	if jrFromCtx := JsRunnerFrom(ctx); jrFromCtx != nil {
		jr = jrFromCtx
	}

	if !trusted {
		return nil
	}

	if len(compiled) == 0 {
		return nil
	}

	// 2. For each matcher, check pattern match
	filterByPattern := eventHasMatchQuery(event)
	var results []HookResult
	for _, cm := range compiled {
		if filterByPattern && !matcherAllows(cm.matchFn, input) {
			continue
		}
		for _, hookCfg := range cm.hooks {
			// 4. Once tracking (atomic via sync.Map)
			if hookCfg.Once {
				key := onceKey(event, cm.pattern, hookCfg)
				if _, existed := h.onceFired.LoadOrStore(key, true); existed {
					continue
				}
			}

			// 5. Execute by type
			timeout := TimeoutForHook(hookCfg.Timeout, event)
			if hookCfg.Type == HookTypeJS && hookCfg.Timeout <= 0 {
				// js hooks use one flat default; event-based defaults (e.g.
				// SessionEnd's 1.5s) do not apply.
				timeout = DefaultJsHookTimeout
			}
			// Async hooks run in background, don't block dispatch.
			// Source: hooks.ts:995-1030 — async/asyncRewake path.
			if hookCfg.Async || hookCfg.AsyncRewake {
				// js hooks do not support async yet — a misconfigured js+async
				// hook is skipped with a warning instead of silently dropped.
				if hookCfg.Type == HookTypeJS {
					slog.Warn("hooks: js hook does not support async, skipping", "event", event)
					continue
				}
				h.runAsyncHook(ctx, exec, pe, ae, hookCfg, input, timeout, cm.pluginRoot)
				continue
			}

			var result HookResult
			switch hookCfg.Type {
			case HookTypeCommand:
				if exec == nil {
					continue
				}
				var extraEnv []string
				if cm.pluginRoot != "" {
					extraEnv = []string{"GBOT_PLUGIN_ROOT=" + cm.pluginRoot}
				}
				result = exec.ExecuteHook(ctx, hookCfg.Command, input, timeout, extraEnv)
			case HookTypePrompt:
				if pe == nil {
					continue
				}
				result = execPromptHook(ctx, pe, hookCfg, input, timeout)
			case HookTypeAgent:
				if ae == nil {
					continue
				}
				result = execAgentHook(ctx, ae, hookCfg, input, timeout)
			case HookTypeJS:
				// A tool call issued from js hook code re-enters dispatch
				// with the hook-origin marker on its ctx: running another js
				// hook here would re-enter the hook session whose mutex the
				// outer hook holds for its whole run. Skipping only the js
				// hook keeps the event lossless — every other hook type runs.
				if FromHookOrigin(ctx) {
					continue
				}
				if jr == nil {
					slog.Warn("hooks: js hook skipped, no runner injected", "event", event)
					continue
				}
				if hookCfg.Code == "" {
					slog.Warn("hooks: js hook skipped, empty code", "event", event)
					continue
				}
				// PluginRoot injection is command-specific (GBOT_PLUGIN_ROOT for
				// file resolution); js hooks are inline code with no path, so
				// cm.pluginRoot is intentionally unused here.
				result = execJsHook(ctx, jr, hookCfg, input, timeout)
			default:
				continue
			}
			if hookCfg.Command != "" {
				result.HookName = hookCfg.Command
			} else if hookCfg.Prompt != "" {
				result.HookName = hookCfg.Prompt
			} else if hookCfg.Code != "" {
				result.HookName = hookCfg.Code
			}
			// One INFO per executed hook — skip-path silence is the matcher
			// gate's live evidence, and outcome makes failures greppable.
			slog.Info("hooks: hook ran", "type", string(hookCfg.Type), "event", string(event), "outcome", result.Outcome.String())
			results = append(results, result)
			// 6. Short-circuit on blocking
			if result.Outcome == HookOutcomeBlocking {
				return results
			}
		}
	}
	return results
}

// eventHasMatchQuery reports whether the event derives a value to match
// matcher patterns against. TS's switch in getMatchingHooks ends in
// `default: break` (hooks.ts:1668-1669), so matchQuery stays undefined for
// every event the switch does not name: TeammateIdle/TaskCreated/TaskCompleted
// are listed only to break explicitly (hooks.ts:1649-1652), while Stop is not
// listed at all — TS applies no matcher filtering to Stop either. With no
// query TS skips filtering outright (hooks.ts:1681-1686): every matcher
// configured under the event runs whatever its pattern says.
// gbot follows TS only for the two task events; its Stop/SubagentStop
// filtering is the ToolNames gate (matcherAllows), and TS filters SubagentStop
// on agent_type and Stop not at all — so that gate is a gbot divergence rather
// than TS semantics. TeammateIdle is not implemented.
func eventHasMatchQuery(event HookEventName) bool {
	switch event {
	case HookTaskCreated, HookTaskCompleted:
		return false
	}
	return true
}

// matcherAllows decides whether a compiled matcher's hooks run for input.
// Tool events keep TS semantics — the pattern is matched against the single
// ToolName. Stop-class events carry no ToolName; when the engine filled
// ToolNames (the tools executed this query), the matcher any-matches across
// that collection so a hook only runs when the query actually used a
// matching tool. An empty ToolNames falls through to the legacy
// match-on-empty-string, preserving empty-matcher-runs-always semantics.
func matcherAllows(matchFn func(string) bool, input *HookInput) bool {
	if input.ToolName != "" {
		return matchFn(input.ToolName)
	}
	if len(input.ToolNames) > 0 {
		return slices.ContainsFunc(input.ToolNames, matchFn)
	}
	return matchFn("")
}

// ---------------------------------------------------------------------------
// onceKey — dedup key for once-fired hooks
// ---------------------------------------------------------------------------

// onceKey builds a unique key for once-fired hook tracking.
// Source: hooks.ts:1733+ — dedup key includes event + matcher + hook command.
// once:true stays PROCESS-level even under per-engine hook sessions (TS
// semantics: once fires once per process). Only the first engine's dispatch
// builds the hook's state in its own session; later engines skip it. That
// cross-engine skip is TS-aligned behavior, not a routing bug.
func onceKey(event HookEventName, matcher string, cfg HookConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s", event, matcher, cfg.Type)
	if cfg.Command != "" {
		fmt.Fprintf(&b, "|%s", cfg.Command)
	}
	if cfg.Prompt != "" {
		fmt.Fprintf(&b, "|%s", cfg.Prompt)
	}
	if cfg.Code != "" {
		fmt.Fprintf(&b, "|%s", cfg.Code)
	}
	if cfg.If != "" {
		fmt.Fprintf(&b, "|%s", cfg.If)
	}
	if cfg.Model != "" {
		fmt.Fprintf(&b, "|%s", cfg.Model)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// findBlockingResult — finds the first blocking result
// ---------------------------------------------------------------------------

func findBlockingResult(results []HookResult) *HookResult {
	for i := range results {
		if results[i].Outcome == HookOutcomeBlocking {
			return &results[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Prompt/Agent hook execution stubs (Step 10 will fill in)
// ---------------------------------------------------------------------------

// execPromptHook runs a single LLM call for hook evaluation.
// Source: execPromptHook.ts:21-211.
// Full implementation in Step 10; stub returns success for now.
func execPromptHook(ctx context.Context, pe PromptExecutor, hook HookConfig, input *HookInput, timeout time.Duration) HookResult {
	prompt := strings.ReplaceAll(hook.Prompt, "$ARGUMENTS", string(input.ToolInput))
	model := hook.Model
	if model == "" {
		model = "haiku"
	}
	ok, reason, err := pe.ExecutePromptHook(ctx, prompt, model, timeout)
	if err != nil {
		return HookResult{Outcome: HookOutcomeNonBlockingError, Stderr: err.Error()}
	}
	if !ok {
		return HookResult{Outcome: HookOutcomeBlocking, Stderr: reason}
	}
	return HookResult{Outcome: HookOutcomeSuccess}
}

// execAgentHook runs a hook using a sub-agent with tool access.
// Source: execAgentHook.ts:36-339.
// Full implementation in Step 10; stub returns success for now.
func execAgentHook(ctx context.Context, ae AgentExecutor, hook HookConfig, input *HookInput, timeout time.Duration) HookResult {
	prompt := strings.ReplaceAll(hook.Prompt, "$ARGUMENTS", string(input.ToolInput))
	model := hook.Model
	if model == "" {
		model = "haiku"
	}
	ok, reason, err := ae.ExecuteAgentHook(ctx, prompt, model, nil, DefaultAgentMaxTurns, timeout)
	if err != nil {
		return HookResult{Outcome: HookOutcomeNonBlockingError, Stderr: err.Error()}
	}
	if !ok {
		return HookResult{Outcome: HookOutcomeBlocking, Stderr: reason}
	}
	return HookResult{Outcome: HookOutcomeSuccess}
}

// execJsHook evaluates a js hook through the injected runner.
// Input JSON is byte-identical to what ExecuteHook pipes to command stdin.
// The runner's return value is recorded verbatim (Stdout) and not interpreted —
// semantic handling (e.g. {block:true} for PreToolUse) is future work.
func execJsHook(ctx context.Context, jr JsHookRunner, hook HookConfig, input *HookInput, timeout time.Duration) HookResult {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return HookResult{Outcome: HookOutcomeNonBlockingError, Stderr: fmt.Sprintf("hooks: marshal input: %v", err)}
	}
	result, err := jr.RunHook(ctx, hook.Code, inputJSON, timeout)
	if err != nil {
		// Logged here because result consumers discard Stderr — a failed hook
		// would otherwise vanish (the ErrNoSession doc relies on this warn).
		slog.Warn("hooks: js hook failed", "event", input.HookEventName, "error", err)
		return HookResult{Outcome: HookOutcomeNonBlockingError, Stderr: err.Error()}
	}
	return HookResult{Outcome: HookOutcomeSuccess, Stdout: result}
}

// ---------------------------------------------------------------------------
// runAsyncHook — async hook execution
// Source: hooks.ts:995-1030 — async/asyncRewake path
// ---------------------------------------------------------------------------

// runAsyncHook runs a hook in a background goroutine.
// If AsyncRewake=true and the hook returns blocking, calls OnRewake callback.
func (h *Hooks) runAsyncHook(
	ctx context.Context,
	exec HookExecutor,
	pe PromptExecutor,
	ae AgentExecutor,
	hookCfg HookConfig,
	input *HookInput,
	timeout time.Duration,
	pluginRoot string,
) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("hooks: panic in async hook", "error", r, "stack", string(debug.Stack()))
			}
		}()
		var result HookResult
		switch hookCfg.Type {
		case HookTypeCommand:
			if exec != nil {
				var extraEnv []string
				if pluginRoot != "" {
					extraEnv = []string{"GBOT_PLUGIN_ROOT=" + pluginRoot}
				}
				result = exec.ExecuteHook(ctx, hookCfg.Command, input, timeout, extraEnv)
			}
		case HookTypePrompt:
			if pe != nil {
				result = execPromptHook(ctx, pe, hookCfg, input, timeout)
			}
		case HookTypeAgent:
			if ae != nil {
				result = execAgentHook(ctx, ae, hookCfg, input, timeout)
			}
		}
		if hookCfg.Command != "" {
			result.HookName = hookCfg.Command
		} else if hookCfg.Prompt != "" {
			result.HookName = hookCfg.Prompt
		}

		// AsyncRewake: if blocking, notify engine to give LLM another turn.
		if hookCfg.AsyncRewake && result.Outcome == HookOutcomeBlocking && h.OnRewake != nil {
			reason := result.Stderr
			if reason == "" {
				reason = "async hook blocked"
			}
			h.OnRewake(reason)
		}
	}()
}
