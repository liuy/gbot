package computer

// computerPrompt is the short system-prompt fragment telling the model how
// to use the Computer tool under the explicit-window model. Kept under ~30
// lines.
func computerPrompt() string {
	return `Drive the desktop — screenshots, mouse, keyboard, scroll, drag — by driving the target window.

### Explicit-window model
Every action except ` + "`list`" + ` takes a ` + "`window`" + ` (window_id from list/snapshot). Get window_ids from ` + "`list`" + ` or ` + "`snapshot`" + `.

### Preferred workflow
1. Call with action='list' to enumerate windows and their window_ids.
2. Call action='snapshot' with window=<id> to get a screenshot + numbered elements of that window.
3. Act by window-relative coordinates, always passing window=<id>.
4. Call action='snapshot' again to verify the result of an action.

### click
count (1=single, 2=double, 3=triple) + button (left|right|middle, default left).

### Coordinate conventions
- [x, y] window-relative, origin the window's top-left corner, matching snapshot's width/height.
- A single image cannot span multiple monitors — snapshot one window at a time.

### Modes (snapshot only)
- som (default): screenshot with numbered overlays + AX tree. Best for vision models.
- vision: plain screenshot only (no element noise).
- ax: accessibility tree only (no image; for text-only models).

### Window focus model
- Each action activates/focuses the target window first, then routes input to it.`
}
