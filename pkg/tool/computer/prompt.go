package computer

// computerPrompt is the short system-prompt fragment telling the model how
// to use the Computer tool: prefer capture then click by element index,
// coordinate conventions, that it drives the background without stealing
// focus. Kept under ~30 lines per the plan.
func computerPrompt() string {
	return `Drive the desktop in the background via cua-driver — screenshots, mouse, keyboard, scroll, drag — without stealing the user's cursor or keyboard focus.

### Preferred workflow
1. Call with action='capture' (mode='som' by default) to get a screenshot with numbered element overlays plus the element index.
2. Click/scroll/set_value by element index — far more reliable than pixel coordinates.
3. Use coordinates only when no element index is available.

### Coordinate conventions
- [x, y] in logical screen space, origin top-left, matching the width/height returned by capture.
- A single image cannot span multiple monitors — capture one window or display at a time.

### Modes
- som (default): screenshot with numbered overlays + AX tree. Best for vision models.
- vision: plain screenshot only (no element noise).
- ax: accessibility tree only (no image; for text-only models).

### Background model
- Input is routed to the target app WITHOUT raising its window (no focus steal).
- focus_app(raise_window=true) is the only path that disrupts the user — use sparingly.
- set_value selects dropdowns/sliders natively without opening the native menu.

### Verifying an action's effect
Pass capture_after=true on click/type/key/scroll/drag/set_value/focus_app to get a follow-up screenshot in the same response.`
}
