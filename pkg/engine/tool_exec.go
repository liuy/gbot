package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ExecuteTool runs a single tool with full permission checking.
// Mirrors StreamingToolExecutor three-phase logic; sessionAllowed/askMu are caller-owned state.
// sessionAllowed is a sync.Map keyed by tool name: reads must stay off askMu
// (an in-flight ask holds it for the whole user-answer window) while still
// being race-free against the AllowAlways write.
func (e *Engine) ExecuteTool(ctx context.Context, name string, args json.RawMessage, sessionAllowed *sync.Map, askMu *sync.Mutex) (string, error) {
	toolsMap := e.Tools()
	t, ok := toolsMap[name]
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}

	if e.permissionChecker != nil {
		decision := e.permissionChecker.Check(name, args)

		switch decision.Action {
		case permission.ActionDeny:
			msg := "permission denied"
			if decision.Message != "" {
				msg = decision.Message
			}
			return "", fmt.Errorf("%s: %s", name, msg)

		case permission.ActionAsk:
			if err := e.askAndCheckPermission(ctx, name, args, decision, sessionAllowed, askMu); err != nil {
				return "", err
			}

		case permission.ActionAllow:
			// pass through
		}

		// Content-level check
		if decision.ContentRules != nil {
			contentAction := permission.CheckContent(name, args, decision.ContentRules)
			if contentAction == permission.ActionDeny {
				return "", fmt.Errorf("%s: denied by content rule", name)
			}
		}
	}

	tctx := &tool.ToolUseContext{
		Ctx: ctx,
		Options: tool.ToolUseOptions{
			Tools:     toolsMap,
			SessionID: e.SessionID(),
		},
		UncappedOutput: true, // REPL sub-tool calls get full output
		// REPL sub-calls share the session cwd so a bash cd persists to the
		// next sub-call, same as the streaming executor path.
		WorkingDir:         e.getWorkingDir(),
		OriginalWorkingDir: e.originalWorkingDir,
		SetWorkingDir:      e.setWorkingDir,
	}

	// Same recording contract as the streaming executor's invocation choke
	// point (runTools.go): only tools that actually run enter the Stop
	// matcher's collection. This path carries repl-inner tools.* calls
	// (Repl toolFn → ExecuteTool), which otherwise never reach the recorder.
	// Hook-origin calls are excluded: they run after the Stop snapshot and
	// are not model tool use.
	if !hooks.FromHookOrigin(ctx) {
		e.recordTurnTool(name)
	}

	result, err := t.Call(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return t.RenderResult(result.Data), nil
}

// askAndCheckPermission handles the ask phase with session-scoped caching.
func (e *Engine) askAndCheckPermission(
	ctx context.Context,
	name string,
	args json.RawMessage,
	decision permission.Decision,
	sessionAllowed *sync.Map,
	askMu *sync.Mutex,
) error {
	cacheKey := name
	// The fast path must not touch askMu: an open permission ask holds it for
	// the whole user-answer window, which would serialise already-allowed
	// calls behind it (pinned by TestToolExec_CachedAllowDoesNotTakeAskMutex).
	// sync.Map's Load never waits on Store, so this lock-free read cannot
	// race the allow-always write the way the old map[string]bool fast path
	// did (concurrent map read/write is a process-fatal).
	if sessionAllowed != nil {
		if _, ok := sessionAllowed.Load(cacheKey); ok {
			return nil // already allowed
		}
	}

	askMu.Lock()
	defer askMu.Unlock()

	if sessionAllowed != nil {
		if _, ok := sessionAllowed.Load(cacheKey); ok {
			return nil
		}
	}

	ruleDetail := ""
	if decision.Rule != nil {
		ruleDetail = decision.Rule.Value.ToolName
		if decision.Rule.Value.RuleContent != nil {
			ruleDetail += "(" + *decision.Rule.Value.RuleContent + ")"
		}
		ruleDetail += " from " + decision.Rule.Source + " settings"
	}

	decisionCh := make(chan types.AskResponse, 1)
	e.emitEvent(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:       types.AskPermission,
			ToolName:   name,
			Input:      args,
			Message:    decision.Message,
			RuleDetail: ruleDetail,
			ResponseCh: decisionCh,
		},
	})

	select {
	case resp, ok := <-decisionCh:
		if !ok || resp.Decision == types.DecisionDeny {
			return fmt.Errorf("%s: permission denied by user", name)
		}
		if resp.Decision == types.DecisionAllowAlways {
			if sessionAllowed == nil {
				return nil
			}
			sessionAllowed.Store(cacheKey, true)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: permission ask cancelled", name)
	}
}
