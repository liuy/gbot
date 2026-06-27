//go:build linux

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

// safeActionsLinux — read-only actions on Linux (mirrors the !linux safeActions).
// Kept as a per-platform var so New() on each platform references the right map.
var safeActionsLinux = map[string]bool{
	ActionList:     true,
	ActionSnapshot: true,
}

var destructiveActionsLinux = map[string]bool{
	ActionClick:  true,
	ActionType:   true,
	ActionKey:    true,
	ActionScroll: true,
	ActionDrag:   true,
}

// New constructs the Computer tool backed by the pure-Go X11Backend (no
// cua-driver, no xdotool, no CGO). The single X11Backend instance is owned
// inside the closure and lazily connected on first Call.
func New() tool.Tool {
	b := NewX11Backend()
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
			return execute(ctx, input, b)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			in, err := parseInput(input)
			if err != nil {
				return false
			}
			return safeActionsLinux[in.Action]
		},
		IsDestructive_: func(input json.RawMessage) bool {
			in, err := parseInput(input)
			if err != nil {
				return false
			}
			return destructiveActionsLinux[in.Action]
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false // drives real desktop state
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 50000,
		Prompt_:            computerPrompt(),
		RenderResult_: func(data any) string {
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
	})
}

// execute is the per-call entry point on Linux. Safety gates run before the
// backend is touched; an X11 connection failure is surfaced as an actionable
// error message, matching the CuaBackend contract. Signature is IDENTICAL to
// the !linux execute (C4): both take `b backend`.
func execute(ctx context.Context, raw json.RawMessage, b backend) (*tool.ToolResult, error) {
	in, err := parseInput(raw)
	if err != nil {
		return nil, err
	}
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	if in.Action == "" {
		return nil, fmt.Errorf("missing `action`")
	}

	// Safety gates.
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

	if err := b.ensureStarted(ctx); err != nil {
		slog.Debug("computer: backend unavailable", "err", err)
		return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("computer_use backend unavailable: %v", err))}, nil
	}

	return dispatch(ctx, b, in)
}

// Compile-time check: *X11Backend satisfies backend (linux path).
var _ backend = (*X11Backend)(nil)

// Compile-time check: the unused types import is real (RenderResult / NewMessages).
var _ types.Role = types.RoleUser
