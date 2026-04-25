package hooks

import (
	"context"
	"fmt"
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
	onceFired      sync.Map // 修正 6: atomic once tracking
	trusted        bool     // 修正 5: workspace trust
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
	pattern string
	matchFn func(string) bool
	hooks   []HookConfig
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
				pattern: m.Matcher,
				matchFn: CompileMatcher(m.Matcher),
				hooks:   m.Hooks,
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

// ---------------------------------------------------------------------------
// dispatch — source: hooks.ts:1603-1848
//
// Core dispatch logic: find matchers → filter by pattern → check once →
// execute by type → short-circuit on blocking.
// ---------------------------------------------------------------------------

func (h *Hooks) dispatch(ctx context.Context, event HookEventName, input *HookInput) []HookResult {
	// 1. Trust check — 修正 5
	h.mu.RLock()
	trusted := h.trusted
	compiled := h.compiledMatchers[string(event)]
	exec := h.executor
	pe := h.promptExecutor
	ae := h.agentExecutor
	h.mu.RUnlock()

	if !trusted {
		return nil
	}

	if len(compiled) == 0 {
		return nil
	}

	// 2. For each matcher, check pattern match
	var results []HookResult
	for _, cm := range compiled {
		if !cm.matchFn(input.ToolName) {
			continue
		}
		for _, hookCfg := range cm.hooks {
			// 4. Once tracking — 修正 6 (atomic via sync.Map)
			if hookCfg.Once {
				key := onceKey(event, cm.pattern, hookCfg)
				if _, existed := h.onceFired.LoadOrStore(key, true); existed {
					continue
				}
			}

			// 5. Execute by type
			timeout := TimeoutForHook(hookCfg.Timeout, event)
			// Async hooks run in background, don't block dispatch.
			// Source: hooks.ts:995-1030 — async/asyncRewake path.
			if hookCfg.Async || hookCfg.AsyncRewake {
				h.runAsyncHook(ctx, exec, pe, ae, hookCfg, input, timeout)
				continue
			}

			var result HookResult
			switch hookCfg.Type {
			case HookTypeCommand:
				if exec == nil {
					continue
				}
				result = exec.ExecuteHook(ctx, hookCfg.Command, input, timeout)
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
			default:
				continue
			}
			if hookCfg.Command != "" {
				result.HookName = hookCfg.Command
			} else if hookCfg.Prompt != "" {
				result.HookName = hookCfg.Prompt
			}
			results = append(results, result)
			// 6. Short-circuit on blocking
			if result.Outcome == HookOutcomeBlocking {
				return results
			}
		}
	}
	return results
}

// ---------------------------------------------------------------------------
// onceKey — dedup key for once-fired hooks
// ---------------------------------------------------------------------------

// onceKey builds a unique key for once-fired hook tracking.
// Source: hooks.ts:1733+ — dedup key includes event + matcher + hook command.
func onceKey(event HookEventName, matcher string, cfg HookConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s", event, matcher, cfg.Type)
	if cfg.Command != "" {
		fmt.Fprintf(&b, "|%s", cfg.Command)
	}
	if cfg.Prompt != "" {
		fmt.Fprintf(&b, "|%s", cfg.Prompt)
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
) {
	go func() {
		var result HookResult
		switch hookCfg.Type {
		case HookTypeCommand:
			if exec != nil {
				result = exec.ExecuteHook(ctx, hookCfg.Command, input, timeout)
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
