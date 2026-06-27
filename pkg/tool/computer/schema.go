// Package computer implements the Computer tool. On macOS/Windows it drives
// the desktop via cua-driver (over a long-lived stdio MCP session); on Linux
// it drives the desktop directly via a pure-Go X11 backend built on
// github.com/jezek/xgb. The model-facing API is the explicit-window model:
// every action except `list` takes a `window` (window_id from `list`/`snapshot`).
package computer

import "encoding/json"

// Action names — the 7-action explicit-window model.
const (
	ActionList     = "list"
	ActionSnapshot = "snapshot"
	ActionClick    = "click"
	ActionType     = "type"
	ActionKey      = "key"
	ActionScroll   = "scroll"
	ActionDrag     = "drag"
)

// actionList is the exact enum list, in order.
var actionList = []string{
	ActionList, ActionSnapshot, ActionClick, ActionType, ActionKey,
	ActionScroll, ActionDrag,
}

// Capture modes for snapshot.
const (
	ModeSom    = "som"
	ModeVision = "vision"
	ModeAx     = "ax"
)

// ModeZoom is removed (R2): the zoom action is dropped from all platforms.

// Mouse button enum.
const (
	ButtonLeft   = "left"
	ButtonRight  = "right"
	ButtonMiddle = "middle"
)

// Scroll directions.
const (
	DirectionUp    = "up"
	DirectionDown  = "down"
	DirectionLeft  = "left"
	DirectionRight = "right"
)

// inputSchema returns the JSON Schema object for the tool's parameters. The
// only `required` field is `["action"]`; per-action `window` requirement is
// validated in dispatch (because `list`/`wait` don't need it).
func inputSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": actionList,
				"description": "Which action to perform. Every action except `list` requires `window` (window_id from list/snapshot).\n\n" +
					"• list — List all on-screen windows. Returns window_id + title + bounds + type (app/desktop/panel) for each. Use this first to discover window_ids.\n\n" +
					"• snapshot — Capture a window's screenshot + numbered element list (SOM mode by default). Returns an image and a list of interactable elements with their indices, roles, labels, and bounds. Coordinates passed to click/scroll are window-relative.\n\n" +
					"• click — Click at a window-relative [x,y] coordinate. Use `count` for double/triple click, `button` for right/middle click.\n\n" +
					"• type — Type a text string into the window (via keyboard input injection).\n\n" +
					"• key — Press a key or key combination (e.g. 'Return', 'ctrl+c', 'cmd+s'). Use '+' to combine modifier+key.\n\n" +
					"• scroll — Scroll the window in a direction (up/down/left/right) by `amount` ticks. Optionally pass `coordinate` to position the cursor first.\n\n" +
					"• drag — Press-drag-release from `from_coordinate` to `to_coordinate`.",
			},
			"window": map[string]any{
				"type":        "integer",
				"description": "X11 window_id from `list` or `snapshot`. Required for all actions except `list`.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{ModeSom, ModeVision, ModeAx},
				"description": "[snapshot] Capture mode. `som` (default) is a screenshot with numbered overlays on every interactable element plus the AX tree — best for vision models, lets you click by element index. `vision` is a plain screenshot. `ax` is the accessibility tree only (no image; useful for text-only models).",
			},
			"max_elements": map[string]any{
				"type":        "integer",
				"description": "[snapshot] Optional cap on the AX `elements` array returned. Default 100, hard maximum 1000. Dense UIs (Electron apps such as Obsidian or VS Code, JetBrains IDEs) can publish 500+ AX nodes — capping prevents a single snapshot from blowing session context.",
				"default":     100,
				"minimum":     1,
				"maximum":     1000,
			},
			"coordinate": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    2,
				"maxItems":    2,
				"description": "[click, scroll] Window-relative [x, y] from the window's top-left corner, matching the width/height returned by snapshot.",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "[click] Click count (1=single, 2=double, 3=triple). Default 1.",
				"default":     1,
				"minimum":     1,
				"maximum":     3,
			},
			"button": map[string]any{
				"type":        "string",
				"enum":        []string{ButtonLeft, ButtonRight, ButtonMiddle},
				"description": "[click] Mouse button. Defaults to left.",
			},
			"modifiers": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"cmd", "shift", "option", "alt", "ctrl", "fn", "win", "windows", "super", "meta"}},
				"description": "[drag] Modifier keys held during the drag.",
			},
			"from_coordinate": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    2,
				"maxItems":    2,
				"description": "[drag] Source [x,y].",
			},
			"to_coordinate": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    2,
				"maxItems":    2,
				"description": "[drag] Target [x,y].",
			},
			"direction": map[string]any{
				"type":        "string",
				"enum":        []string{DirectionUp, DirectionDown, DirectionLeft, DirectionRight},
				"description": "[scroll] Scroll direction.",
			},
			"amount": map[string]any{
				"type":        "integer",
				"description": "[scroll] Scroll wheel ticks. Default 3.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "[type] Text to type (respects the current layout).",
			},
			"keys": map[string]any{
				"type":        "string",
				"description": "[key] Key combo, e.g. 'cmd+s', 'ctrl+alt+t', 'return', 'escape', 'tab'. Use '+' to combine.",
			},
			// region and seconds properties removed (R2/R1): zoom and wait dropped.
		},
		"required": []string{"action"},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		panic("computer: marshal inputSchema: " + err.Error())
	}
	return data
}
