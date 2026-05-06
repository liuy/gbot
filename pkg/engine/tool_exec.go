package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ExecuteTool runs a single tool with full permission checking.
// Mirrors StreamingToolExecutor three-phase logic; sessionAllowed/askMu are caller-owned state.
func (e *Engine) ExecuteTool(ctx context.Context, name string, args json.RawMessage, sessionAllowed map[string]bool, askMu *sync.Mutex) (string, error) {
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
	sessionAllowed map[string]bool,
	askMu *sync.Mutex,
) error {
	cacheKey := name
	if sessionAllowed != nil && sessionAllowed[cacheKey] {
		return nil // already allowed
	}

	askMu.Lock()
	defer askMu.Unlock()

	if sessionAllowed != nil && sessionAllowed[cacheKey] {
		return nil
	}

	ruleDetail := ""
	if decision.Rule != nil {
		ruleDetail = decision.Rule.Value.ToolName
		if decision.Rule.Value.RuleContent != nil {
			ruleDetail += "(" + *decision.Rule.Value.RuleContent + ")"
		}
		ruleDetail += " from " + decision.Rule.Source + " settings"
	}

	decisionCh := make(chan types.PermissionUserDecision, 1)
	e.emitEvent(types.QueryEvent{
		Type: types.EventPermissionAsk,
		PermissionAsk: &types.PermissionAskEvent{
			ToolName:   name,
			Input:      args,
			Message:    decision.Message,
			RuleDetail: ruleDetail,
			ResponseCh: decisionCh,
		},
	})

	select {
	case d, ok := <-decisionCh:
		if !ok || d == types.UserDecisionDeny {
			return fmt.Errorf("%s: permission denied by user", name)
		}
		if d == types.UserDecisionAllowAlways {
			if sessionAllowed == nil {
					return nil
			}
			sessionAllowed[cacheKey] = true
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: permission ask cancelled", name)
	}
}
