package computer

// computerPrompt is the short system-prompt fragment telling the model how
// to use the Computer tool under the explicit-window model. Kept under ~30
// lines.
func computerPrompt() string {
	return `Drive the desktop in the background via cua-driver — screenshots, mouse, keyboard, scroll, drag — without stealing the user's cursor or keyboard focus.

### Explicit-window model
Every action except ` + "`list`" + ` and ` + "`wait`" + ` takes a ` + "`window`" + ` (X11 window_id). Get window_ids from ` + "`list`" + ` or ` + "`snapshot`" + `.

### Preferred workflow
1. Call with action='list' to enumerate windows and their window_ids.
2. Call action='snapshot' with window=<id> to get a screenshot + numbered elements of that window.
3. Act by element index (preferred) or coordinates, always passing window=<id>.
4. Call action='zoom' with window=<id> + region=[x1,y1,x2,y2] for a high-detail sub-region screenshot.
5. Call action='wait' to let an animation settle before the next snapshot.

### click
count (1=single, 2=double, 3=triple) + button (left|right|middle, default left).

### Coordinate conventions
- [x, y] in logical screen space, origin top-left, matching the width/height returned by snapshot.
- A single image cannot span multiple monitors — snapshot one window at a time.

### Modes (snapshot only)
- som (default): screenshot with numbered overlays + AX tree. Best for vision models.
- vision: plain screenshot only (no element noise).
- ax: accessibility tree only (no image; for text-only models).

### Background model
- Input is routed to the target window WITHOUT raising it (no focus steal).`
}
