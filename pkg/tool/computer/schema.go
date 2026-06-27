// Package computer implements the Computer tool, a 1:1 translation of the
// Hermes `computer_use` tool (tools/computer_use/) into gbot. The tool drives
// the desktop in the background via cua-driver (over a long-lived stdio MCP
// session) and returns screenshots to the LLM as image content blocks.
//
// Source reference: /home/yliu/repos/hermes-agent/tools/computer_use/
package computer

import "encoding/json"

// Action names — translate Hermes schema.py:13-25 `action.enum` 1:1.
const (
	ActionCapture     = "capture"
	ActionClick       = "click"
	ActionDoubleClick = "double_click"
	ActionRightClick  = "right_click"
	ActionMiddleClick = "middle_click"
	ActionDrag        = "drag"
	ActionScroll      = "scroll"
	ActionType        = "type"
	ActionKey         = "key"
	ActionSetValue    = "set_value"
	ActionWait        = "wait"
	ActionListApps    = "list_apps"
	ActionFocusApp    = "focus_app"
)

// actionList is the exact enum list, in order, from schema.py:13-25.
var actionList = []string{
	ActionCapture, ActionClick, ActionDoubleClick, ActionRightClick,
	ActionMiddleClick, ActionDrag, ActionScroll, ActionType,
	ActionKey, ActionSetValue, ActionWait, ActionListApps, ActionFocusApp,
}

// Capture modes — translate schema.py:44-46 `mode.enum`.
const (
	ModeSom    = "som"
	ModeVision = "vision"
	ModeAx     = "ax"
)

// Mouse button enum — schema.py:101-102.
const (
	ButtonLeft   = "left"
	ButtonRight  = "right"
	ButtonMiddle = "middle"
)

// Scroll directions — schema.py:137-138.
const (
	DirectionUp    = "up"
	DirectionDown  = "down"
	DirectionLeft  = "left"
	DirectionRight = "right"
)

// inputSchema returns the exact JSON Schema object from
// schema.py `COMPUTER_USE_SCHEMA["parameters"]`. Every property, enum,
// default, minimum, and maximum is translated 1:1; the only `required`
// field is `["action"]`.
func inputSchema() json.RawMessage {
	// Built as a Go map (then marshaled) so the structure mirrors schema.py
	// field-for-field. Returning a raw string literal would be less legible
	// and easier to drift from the source.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        actionList,
				"description": "Which action to perform. `capture` is free (no side effects). All other actions require approval unless auto-approved. Use `set_value` for select/popup elements and sliders — it selects the matching option directly without opening the native menu (no focus steal).",
			},
			// ── capture ────────────────────────────────────────────
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{ModeSom, ModeVision, ModeAx},
				"description": "Capture mode. `som` (default) is a screenshot with numbered overlays on every interactable element plus the AX tree — best for vision models, lets you click by element index. `vision` is a plain screenshot. `ax` is the accessibility tree only (no image; useful for text-only models).",
			},
			"app": map[string]any{
				"type":        "string",
				"description": "Optional. Limit capture/action to a specific app (by name, e.g. 'Safari', or bundle ID, 'com.apple.Safari'). If omitted, operates on the frontmost app's window. Pass app='screen' (or 'desktop') to capture the OS desktop/shell surface — e.g. to see the wallpaper or click the taskbar. Note: capture is per-window; a single image cannot span multiple monitors, so on a multi-screen setup capture one window or display at a time.",
			},
			"max_elements": map[string]any{
				"type":        "integer",
				"description": "Optional cap on the AX `elements` array returned by `action='capture'`. Default 100, hard maximum 1000. Dense UIs (Electron apps such as Obsidian or VS Code, JetBrains IDEs) can publish 500+ AX nodes — capping prevents a single capture from blowing session context. When the cap trims the response, `total_elements` and `truncated_elements` are surfaced in the result so you can re-call with `app=` to narrow scope or raise `max_elements` when the full tree is required. Has no effect on `mode='som'` / `mode='vision'` when a screenshot is included in the response; only the rare image-missing fallback returns an `elements` array and is subject to the cap.",
				"default":     100,
				"minimum":     1,
				"maximum":     1000,
			},
			// ── click / drag / scroll targeting ────────────────────
			"element": map[string]any{
				"type":        "integer",
				"description": "The 1-based SOM index returned by the last `capture(mode='som')` call. Strongly preferred over raw coordinates.",
			},
			"coordinate": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    2,
				"maxItems":    2,
				"description": "Pixel coordinates [x, y] in logical screen space (as returned by capture width/height). Only use this if no element index is available.",
			},
			"button": map[string]any{
				"type":        "string",
				"enum":        []string{ButtonLeft, ButtonRight, ButtonMiddle},
				"description": "Mouse button. Defaults to left.",
			},
			"modifiers": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"cmd", "shift", "option", "alt", "ctrl", "fn", "win", "windows", "super", "meta"}},
				"description": "Modifier keys held during the action.",
			},
			// ── drag ───────────────────────────────────────────────
			"from_element": map[string]any{
				"type":        "integer",
				"description": "Source element index (drag).",
			},
			"to_element": map[string]any{
				"type":        "integer",
				"description": "Target element index (drag).",
			},
			"from_coordinate": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    2,
				"maxItems":    2,
				"description": "Source [x,y] (drag; use when no element available).",
			},
			"to_coordinate": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"minItems":    2,
				"maxItems":    2,
				"description": "Target [x,y] (drag; use when no element available).",
			},
			// ── scroll ─────────────────────────────────────────────
			"direction": map[string]any{
				"type":        "string",
				"enum":        []string{DirectionUp, DirectionDown, DirectionLeft, DirectionRight},
				"description": "Scroll direction.",
			},
			"amount": map[string]any{
				"type":        "integer",
				"description": "Scroll wheel ticks. Default 3.",
			},
			// ── set_value ──────────────────────────────────────────
			"value": map[string]any{
				"type":        "string",
				"description": "For action='set_value': the value to set on the element. For AXPopUpButton / select dropdowns, pass the option's display label (e.g. 'Blue'). For sliders and other AXValue-settable elements, pass the numeric or string value.",
			},
			// ── type / key / wait ──────────────────────────────────
			"text": map[string]any{
				"type":        "string",
				"description": "Text to type (respects the current layout).",
			},
			"keys": map[string]any{
				"type":        "string",
				"description": "Key combo, e.g. 'cmd+s', 'ctrl+alt+t', 'return', 'escape', 'tab'. Use '+' to combine.",
			},
			"seconds": map[string]any{
				"type":        "number",
				"description": "Seconds to wait. Max 30.",
			},
			// ── focus_app ──────────────────────────────────────────
			"raise_window": map[string]any{
				"type":        "boolean",
				"description": "Only for action='focus_app'. If true, brings the window to front (DISRUPTS the user). Default false — input is routed to the app without raising, matching the background co-work model.",
			},
			// ── return shape ───────────────────────────────────────
			"capture_after": map[string]any{
				"type":        "boolean",
				"description": "If true, take a follow-up capture after the action and include it in the response. Saves a round-trip when you need to verify an action's effect.",
			},
		},
		"required": []string{"action"},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		// schema map is statically defined above; the only way to hit this
		// is a programming error in the map construction.
		panic("computer: marshal inputSchema: " + err.Error())
	}
	return data
}
