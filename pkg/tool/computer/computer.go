package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// safeActions mirrors tool.py:48 `_SAFE_ACTIONS` — read-only actions that
// never go through the destructive-permission gate.
var safeActions = map[string]bool{
	ActionCapture:  true,
	ActionWait:     true,
	ActionListApps: true,
}

// destructiveActions mirrors tool.py:54-57 `_DESTRUCTIVE_ACTIONS` — actions
// that mutate user-visible state and so require approval.
var destructiveActions = map[string]bool{
	ActionClick: true, ActionDoubleClick: true, ActionRightClick: true,
	ActionMiddleClick: true, ActionDrag: true, ActionScroll: true,
	ActionType: true, ActionKey: true, ActionSetValue: true, ActionFocusApp: true,
}

// New constructs the Computer tool, mirroring the Web tool's BuildTool
// structure (pkg/tool/web/web.go). The single Backend instance is owned
// inside the closure and lazily started on first Call — zero config.
func New() tool.Tool {
	backend := NewBackend()
	schema := inputSchema()

	return tool.BuildTool(tool.ToolDef{
		Name_:    "Computer",
		Aliases_: []string{"computer"},
		InputSchema_: func() json.RawMessage {
			return schema
		},
		Description_: func(input json.RawMessage) (string, error) {
			in, err := parseInput(input)
			if err != nil {
				return "Drive the desktop", nil
			}
			return summarizeAction(in), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, input, backend)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			in, err := parseInput(input)
			if err != nil {
				return false
			}
			return safeActions[in.Action]
		},
		IsDestructive_: func(input json.RawMessage) bool {
			in, err := parseInput(input)
			if err != nil {
				return false
			}
			return destructiveActions[in.Action]
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false // drives real desktop state
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 50000,
		Prompt_:            computerPrompt(),
		RenderResult_: func(data any) string {
			// captureResponse returns the summary text in Data; action text
			// results are JSON strings. Both render as their string form.
			switch v := data.(type) {
			case string:
				return v
			case *CaptureResult:
				return captureSummary(v)
			default:
				b, _ := json.Marshal(data)
				return string(b)
			}
		},
		// ShouldDefer_ defaults to false — keep loaded. It's a primary
		// interaction tool like Bash/Web, not a niche MCP tool.
	})
}

// execute is the per-call entry point, translating tool.py:215-282
// `handle_computer_use`. Safety gates (tool.py:240-260) run BEFORE the
// backend is touched; backend-unavailable returns the actionable install
// hint (tool.py:270-276).
func execute(ctx context.Context, raw json.RawMessage, backend *Backend) (*tool.ToolResult, error) {
	in, err := parseInput(raw)
	if err != nil {
		return nil, err
	}
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	if in.Action == "" {
		return nil, fmt.Errorf("missing `action`")
	}

	// Safety gates — translate of tool.py:240-260.
	if in.Action == ActionType {
		if pat := isBlockedType(in.Text); pat != "" {
			return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("blocked pattern in type text: %q", pat))}, nil
		}
	}
	if in.Action == ActionKey {
		combo := canonKeyCombo(in.Keys)
		if blocked, ok := isBlockedKeyCombo(combo); ok {
			return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("blocked key combo: %s", blocked))}, nil
		}
	}

	if err := backend.ensureStarted(ctx); err != nil {
		// Backend unavailable — translate of tool.py:270-276's
		// "computer_use backend unavailable" error JSON.
		slog.Debug("computer: backend unavailable", "err", err)
		return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("computer_use backend unavailable: %v", err))}, nil
	}

	return dispatch(ctx, backend, in)
}

// summarizeAction is the tool-card summary string. Translate of
// tool.py:152-186 `_summarize_action`.
func summarizeAction(in Input) string {
	switch in.Action {
	case ActionClick, ActionDoubleClick, ActionRightClick, ActionMiddleClick:
		if in.Element != nil {
			return fmt.Sprintf("%s element #%d", in.Action, *in.Element)
		}
		if x, y, ok := parseCoordinate(in.Coordinate); ok {
			return fmt.Sprintf("%s at (%d,%d)", in.Action, x, y)
		}
		return in.Action
	case ActionDrag:
		src, dst := "?", "?"
		if in.FromElement != nil {
			src = fmt.Sprintf("#%d", *in.FromElement)
		} else if x, y, ok := parseCoordinate(in.FromCoordinate); ok {
			src = fmt.Sprintf("(%d,%d)", x, y)
		}
		if in.ToElement != nil {
			dst = fmt.Sprintf("#%d", *in.ToElement)
		} else if x, y, ok := parseCoordinate(in.ToCoordinate); ok {
			dst = fmt.Sprintf("(%d,%d)", x, y)
		}
		return fmt.Sprintf("drag %s → %s", src, dst)
	case ActionScroll:
		dir := in.Direction
		if dir == "" {
			dir = "?"
		}
		amount := 3
		if in.Amount != nil {
			amount = *in.Amount
		}
		return fmt.Sprintf("scroll %s x%d", dir, amount)
	case ActionType:
		text := in.Text
		suffix := ""
		if len(text) > 60 {
			text, suffix = text[:60], "..."
		}
		return fmt.Sprintf("type %q%s", text, suffix)
	case ActionKey:
		return fmt.Sprintf("key %q", in.Keys)
	case ActionFocusApp:
		suffix := ""
		if in.RaiseWindow {
			suffix = " (raise)"
		}
		return fmt.Sprintf("focus %q%s", in.App, suffix)
	}
	return in.Action
}

// captureSummary is the RenderResult string for a *CaptureResult. Mirrors
// the summary header captureResponse builds (mode + size + app + window),
// minus the element index (the full summary is in the ToolResult.Data).
func captureSummary(cap *CaptureResult) string {
	if cap == nil {
		return ""
	}
	header := fmt.Sprintf("capture mode=%s %dx%d", cap.Mode, cap.Width, cap.Height)
	if cap.App != "" {
		header += " app=" + cap.App
	}
	if cap.WindowTitle != "" {
		header += fmt.Sprintf(" window=%q", cap.WindowTitle)
	}
	return header
}

// Compile-time check: *Backend satisfies cuaBackend.
var _ cuaBackend = (*Backend)(nil)

// Compile-time check: the unused types import is real (NewMessages).
var _ types.Role = types.RoleUser
