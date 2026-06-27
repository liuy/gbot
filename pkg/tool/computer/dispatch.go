//go:build !linux

package computer

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// CuaBackend action methods. Each maps an action to a cua-driver MCP tool
// call (or an xdotool fallback for type/key on Linux-CuaBackend, retained for
// the mac/windows paths). The shared dispatch surface, response shapers, and
// helpers live in dispatch_core.go.

// list enumerates on-screen windows via list_windows, warms winCache, and
// returns a text summary. The `type` field is heuristic-derived (cua-driver's
// Linux list_windows exposes no window-type or app_name field; only title).
func (b *CuaBackend) list(ctx context.Context) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	out, err := b.call(ctx, "list_windows", map[string]any{
		"on_screen_only": true,
		"session":        b.sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("list_windows: %w", err)
	}
	windows := parseWindows(extractResult(out))
	b.mu.Lock()
	for _, w := range windows {
		b.winCache[w.WindowID] = w.PID
	}
	b.mu.Unlock()

	lines := make([]string, 0, len(windows))
	for _, w := range windows {
		lines = append(lines, formatWindowLine(w))
	}
	summary := "no windows found"
	if len(lines) > 0 {
		summary = strings.Join(lines, "\n")
	}
	return &ActionResult{
		OK:      true,
		Action:  "list",
		Message: summary,
		Meta: map[string]any{
			"windows": windows,
			"count":   len(windows),
		},
	}, nil
}

// snapshot captures a window's screenshot + AX elements via get_window_state.
// It caches snapTokens (element_index→token) and snapshotID for subsequent
// element-based actions, and warms winCache for this window.
func (b *CuaBackend) snapshot(ctx context.Context, in Input) (*CaptureResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	pid, err := b.resolvePID(ctx, *in.Window)
	if err != nil {
		return nil, err
	}
	mode := in.Mode
	if mode == "" {
		mode = ModeSom
	}

	maxElems := coerceMaxElements(rawAny(in.MaxElements))

	out, err := b.call(ctx, "get_window_state", map[string]any{
		"pid":          pid,
		"window_id":    *in.Window,
		"capture_mode": mode,
		"max_elements": maxElems,
		"session":      b.sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("get_window_state: %w", err)
	}
	res := extractResult(out)

	var elements []UIElement
	if mode != ModeVision {
		// R3: element_token caching removed — element-based click is gone, so
		// tokens are never read back. Elements are still parsed for display.
		elements = parseElementsFromResult(res)
	}

	pngB64, mime := imageFromResult(res)
	var windowTitle string
	if dataStr, ok := res["data"].(string); ok {
		if _, tree := splitTreeText(dataStr); tree != "" {
			windowTitle = extractWindowTitle(tree)
		}
	}

	pngBytesLen := 0
	width, height := 0, 0
	if pngB64 != "" {
		if raw, decErr := base64.StdEncoding.DecodeString(pngB64); decErr == nil {
			pngBytesLen = len(raw)
			if dw, dh, ok := imageDimensionsFromBytes(raw); ok {
				width, height = dw, dh
			}
		} else {
			pngBytesLen = len(pngB64) * 3 / 4
		}
	}

	b.mu.Lock()
	b.winCache[*in.Window] = pid
	b.mu.Unlock()

	return &CaptureResult{
		Mode:          mode,
		Width:         width,
		Height:        height,
		PngB64:        pngB64,
		Elements:      elements,
		WindowTitle:   windowTitle,
		PngBytesLen:   pngBytesLen,
		ImageMimeType: mime,
	}, nil
}

// click routes all click variants (single/double/triple × left/right/middle)
// through the single cua-driver `click` tool with button+count. Modifiers are
// NOT passed: the click schema has additionalProperties:false and no modifier
// field (cua-driver rejects it). The drag tool DOES accept modifier.
func (b *CuaBackend) click(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	pid, err := b.resolvePID(ctx, *in.Window)
	if err != nil {
		return nil, err
	}
	button := in.Button
	if button == "" {
		button = ButtonLeft
	}
	buttonLower := strings.ToLower(button)
	if buttonLower != ButtonLeft && buttonLower != ButtonRight && buttonLower != ButtonMiddle {
		return actionErr("click", fmt.Sprintf("unknown button %q — expected left, right, middle.", button)), nil
	}
	count := 1
	if in.Count != nil {
		count = *in.Count
	}
	if count < 1 {
		count = 1
	}
	if count > 3 {
		count = 3
	}

	args := map[string]any{
		"pid":       pid,
		"window_id": *in.Window,
		"button":    buttonLower,
		"count":     count,
	}
	// R3: element-based click removed — coordinate is the only targeting mode.
	if x, y, ok := parseCoordinate(in.Coordinate); ok {
		args["x"] = x
		args["y"] = y
	} else {
		return actionErr("click", "click requires coordinate=[x,y]."), nil
	}
	return b.runAction(ctx, "click", args)
}

// typeText types a string into the window using xdotool XTest injection.
// Key insight: xdotool without --window uses XTest (goes to focused window),
// while cua-driver's type_text uses XSendEvent (apps reject it). So we focus
// first, then type without --window flag.
func (b *CuaBackend) typeText(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	wid := *in.Window
	// Focus the window first (XTest goes to focused window, not specific window)
	if err := xdotoolExec(ctx, "windowfocus", "--sync", fmt.Sprintf("%d", wid)); err != nil {
		return nil, fmt.Errorf("windowfocus: %w", err)
	}
	time.Sleep(50 * time.Millisecond)
	// Type without --window flag (uses XTest to focused window)
	if err := xdotoolExec(ctx, "type", "--delay", "20", in.Text); err != nil {
		return nil, fmt.Errorf("xdotool type: %w", err)
	}
	return &ActionResult{OK: true, Action: "type_text", Message: fmt.Sprintf("Typed %d character(s) via XTest.", utf8.RuneCountInString(in.Text))}, nil
}

// key presses a key or hotkey combo in the window using xdotool XTest.
func (b *CuaBackend) key(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	wid := *in.Window
	keyName, modifiers := parseKeyCombo(in.Keys)
	if keyName == "" {
		return actionErr("key", fmt.Sprintf("Could not parse key from '%s'.", in.Keys)), nil
	}
	// Focus the window first
	if err := xdotoolExec(ctx, "windowfocus", "--sync", fmt.Sprintf("%d", wid)); err != nil {
		return nil, fmt.Errorf("windowfocus: %w", err)
	}
	time.Sleep(50 * time.Millisecond)
	// Build key combo: modifiers+key
	combo := keyName
	if len(modifiers) > 0 {
		combo = strings.Join(append(modifiers, keyName), "+")
	}
	if err := xdotoolExec(ctx, "key", combo); err != nil {
		return nil, fmt.Errorf("xdotool key: %w", err)
	}
	return &ActionResult{OK: true, Action: "press_key", Message: fmt.Sprintf("Pressed key '%s' via XTest.", in.Keys)}, nil
}

// xdotoolExec runs xdotool with the given arguments.
// Uses detected DISPLAY if gbot's shell doesn't have one set.
func xdotoolExec(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "xdotool", args...)
	env := os.Environ()
	if os.Getenv("DISPLAY") == "" {
		if d, err := detectDisplay(); err == nil && d != "" {
			env = append(env, "DISPLAY="+d)
		}
	}
	cmd.Env = env
	return cmd.Run()
}

// scroll scrolls the window. Default direction=down, default amount=3 clamped
// to [1,50].
func (b *CuaBackend) scroll(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	pid, err := b.resolvePID(ctx, *in.Window)
	if err != nil {
		return nil, err
	}
	direction := in.Direction
	if direction == "" {
		direction = DirectionDown
	}
	amount := 3
	if in.Amount != nil {
		amount = *in.Amount
	}
	if amount < 1 {
		amount = 1
	}
	if amount > 50 {
		amount = 50
	}
	args := map[string]any{
		"pid":       pid,
		"window_id": *in.Window,
		"direction": direction,
		"amount":    amount,
	}
	// R3: element-based scroll removed — coordinate-only.
	if x, y, ok := parseCoordinate(in.Coordinate); ok {
		args["x"] = x
		args["y"] = y
	}
	return b.runAction(ctx, "scroll", args)
}

// drag drags from from_coordinate to to_coordinate. Coordinate-only (element-
// based drag is dropped per the explicit-window design).
func (b *CuaBackend) drag(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	pid, err := b.resolvePID(ctx, *in.Window)
	if err != nil {
		return nil, err
	}
	fx, fy, fromOK := parseCoordinate(in.FromCoordinate)
	tx, ty, toOK := parseCoordinate(in.ToCoordinate)
	if !fromOK || !toOK {
		return actionErr("drag", "drag requires from_coordinate and to_coordinate."), nil
	}
	args := map[string]any{
		"pid":       pid,
		"window_id": *in.Window,
		"from_x":    fx,
		"from_y":    fy,
		"to_x":      tx,
		"to_y":      ty,
	}
	if len(in.Modifiers) > 0 {
		args["modifier"] = in.Modifiers
	}
	return b.runAction(ctx, "drag", args)
}

// runAction is the shared call+result-shape path for all mutating actions.
// It injects the session id and flattens the MCP result into an ActionResult.
func (b *CuaBackend) runAction(ctx context.Context, name string, args map[string]any) (*ActionResult, error) {
	if _, ok := args["session"]; !ok {
		args["session"] = b.sessionID
	}
	out, err := b.call(ctx, name, args)
	if err != nil {
		return &ActionResult{OK: false, Action: name, Message: fmt.Sprintf("cua-driver error: %v", err)}, nil
	}
	res := extractResult(out)
	msg := ""
	switch d := res["data"].(type) {
	case map[string]any:
		if s, ok := d["message"].(string); ok {
			msg = s
		}
	case string:
		msg = d
	}
	meta := map[string]any{}
	if m, ok := res["data"].(map[string]any); ok {
		meta = m
	}
	return &ActionResult{OK: true, Action: name, Message: msg, Meta: meta}, nil
}

// attachElementToken removed (R3): element-based click/scroll dropped, so the
// element_token cache is never read.

// parseWindows translates the list_windows structured payload to normalized
// windowInfo. CuaBackend-only: the X11Backend builds windowInfo directly from
// EWMH properties.
func parseWindows(out map[string]any) []windowInfo {
	sc, _ := out["structuredContent"].(map[string]any)
	raw, _ := sc["windows"].([]any)
	windows := make([]windowInfo, 0, len(raw))
	for _, w := range raw {
		wmap, ok := w.(map[string]any)
		if !ok {
			continue
		}
		pid, _ := asInt(wmap["pid"])
		winID, _ := asInt(wmap["window_id"])
		z, _ := asInt(wmap["z_index"])
		app, _ := wmap["app_name"].(string)
		title, _ := wmap["title"].(string)
		// is_on_screen is absent on Linux; since the list_windows call passes
		// on_screen_only=true, treat missing as on-screen.
		onScreen := true
		if v, ok := wmap["is_on_screen"].(bool); ok {
			onScreen = v
		}
		x, _ := asInt(wmap["x"])
		y, _ := asInt(wmap["y"])
		width, _ := asInt(wmap["width"])
		height, _ := asInt(wmap["height"])
		windows = append(windows, windowInfo{
			AppName:   app,
			PID:       pid,
			WindowID:  winID,
			OffScreen: !onScreen,
			Title:     title,
			ZIndex:    z,
			X:         x,
			Y:         y,
			Width:     width,
			Height:    height,
		})
	}
	return windows
}

// parseElementsFromResult picks the structured elements array when present,
// else falls back to the tree regex. CuaBackend-only: the X11Backend has no
// elements array to parse (Elements is always nil).
func parseElementsFromResult(out map[string]any) []UIElement {
	if sc, ok := out["structuredContent"].(map[string]any); ok {
		if raw, ok := sc["elements"].([]any); ok && len(raw) > 0 {
			return parseElementsFromStructured(raw)
		}
	}
	if dataStr, ok := out["data"].(string); ok {
		_, tree := splitTreeText(dataStr)
		if tree != "" {
			return parseElementsFromTree(tree)
		}
	}
	return nil
}
