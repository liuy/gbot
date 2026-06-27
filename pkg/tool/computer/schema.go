// Package computer implements the Computer tool. It drives the desktop in
// the background via cua-driver (over a long-lived stdio MCP session) and
// returns screenshots to the LLM as image content blocks. The model-facing
// API is the explicit-window model: every action except `list` and `wait`
// takes a `window` (X11 window_id from `list`/`snapshot`).
package computer

import "encoding/json"

// Action names — the 9-action explicit-window model.
const (
	ActionList     = "list"
	ActionSnapshot = "snapshot"
	ActionClick    = "click"
	ActionType     = "type"
	ActionKey      = "key"
	ActionScroll   = "scroll"
	ActionDrag     = "drag"
	ActionZoom     = "zoom"
	ActionWait     = "wait"
)

// actionList is the exact enum list, in order.
var actionList = []string{
	ActionList, ActionSnapshot, ActionClick, ActionType, ActionKey,
	ActionScroll, ActionDrag, ActionZoom, ActionWait,
}

// Capture modes for snapshot.
const (
	ModeSom    = "som"
	ModeVision = "vision"
	ModeAx     = "ax"
)

// ModeZoom labels a CaptureResult produced by the zoom action (distinct from
// snapshot modes so zoomResponse can render its own header).
const ModeZoom = "zoom"

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
				"description": "Which action to perform. Every action except `list` and `wait` requires `window` (X11 window_id from list/snapshot).\n\n" +
					"• list — List all on-screen windows. Returns window_id + title + bounds + type (app/desktop/panel) for each. Use this first to discover window_ids.\n\n" +
					"• snapshot — Capture a window's screenshot + numbered element list (SOM mode by default). Returns an image and a list of interactable elements with their indices, roles, labels, and bounds. Use `element` indices from the snapshot for precise clicks.\n\n" +
					"• click — Click at an element index (preferred) or [x,y] coordinate. Use `count` for double/triple click, `button` for right/middle click.\n\n" +
					"• type — Type a text string into the window (via keyboard input injection).\n\n" +
					"• key — Press a key or key combination (e.g. 'Return', 'ctrl+c', 'cmd+s'). Use '+' to combine modifier+key.\n\n" +
					"• scroll — Scroll the window in a direction (up/down/left/right) by `amount` ticks.\n\n" +
					"• drag — Press-drag-release from `from_coordinate` to `to_coordinate`.\n\n" +
					"• zoom — Capture a high-detail screenshot of a sub-region [x1,y1,x2,y2] of a window. Use when you need to read small text or inspect a tight area.\n\n" +
					"• wait — Sleep for `seconds` (max 30). Use between actions when waiting for UI to update.",
			},
			"window": map[string]any{
				"type":        "integer",
				"description": "X11 window_id from `list` or `snapshot`. Required for all actions except `list` and `wait`.",
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
			"element": map[string]any{
				"type":        "integer",
				"description": "[click, scroll] The 1-based SOM index returned by the last `snapshot` of this window. Strongly preferred over raw coordinates.",
			},
			"coordinate": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    2,
				"maxItems":    2,
				"description": "[click, scroll] Pixel coordinates [x, y] in logical screen space (as returned by snapshot width/height). Only use this if no element index is available.",
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
			"region": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    4,
				"maxItems":    4,
				"description": "[zoom] Region [x1,y1,x2,y2] to capture.",
			},
			"seconds": map[string]any{
				"type":        "number",
				"description": "[wait] Seconds to wait. Max 30.",
			},
		},
		"required": []string{"action"},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		panic("computer: marshal inputSchema: " + err.Error())
	}
	return data
}
