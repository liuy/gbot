package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Result types — translate of backend.py:25-171 (UIElement, CaptureResult,
// ActionResult dataclasses).
// ---------------------------------------------------------------------------

// UIElement is one interactable element on the current screen.
// Translate of backend.py:25-49.
type UIElement struct {
	Index        int    // 1-based SOM index
	Role         string // AX role (AXButton, AXTextField, ...)
	Label        string // AXTitle / AXDescription / AXValue snippet
	Bounds       [4]int // x, y, w, h (logical px)
	App          string // owning bundle ID or app name
	PID          int    // owning process PID
	WindowID     int    // SkyLight / CG / X11 window ID
	ElementToken string // opaque per-snapshot token (trycua/cua#1961)
}

// CaptureResult is the result of a screen capture call.
// Translate of backend.py:52-83.
type CaptureResult struct {
	Mode          string
	Width         int
	Height        int
	PngB64        string // base64-encoded image bytes (PNG/JPEG)
	Elements      []UIElement
	App           string
	WindowTitle   string
	PngBytesLen   int
	ImageMimeType string // explicit MIME when the backend supplied one
}

// ActionResult is the result of any action (click / type / scroll / ...).
// Translate of backend.py:96-104.
type ActionResult struct {
	OK      bool
	Action  string
	Message string
	Meta    map[string]any
}

// ---------------------------------------------------------------------------
// cuaBackend — narrow dispatch surface. *Backend satisfies it; a testBackend
// stub records calls and returns canned results so dispatch is testable
// without a live cua-driver. Plan Step 5.
// ---------------------------------------------------------------------------

type cuaBackend interface {
	capture(ctx context.Context, mode, app string, maxElements int) (*CaptureResult, error)
	click(ctx context.Context, in Input) (*ActionResult, error)
	drag(ctx context.Context, in Input) (*ActionResult, error)
	scroll(ctx context.Context, in Input) (*ActionResult, error)
	typeText(ctx context.Context, in Input) (*ActionResult, error)
	key(ctx context.Context, in Input) (*ActionResult, error)
	setValue(ctx context.Context, in Input) (*ActionResult, error)
	wait(ctx context.Context, in Input) (*ActionResult, error)
	listApps(ctx context.Context) (*ActionResult, error)
	focusApp(ctx context.Context, in Input) (*ActionResult, error)
}

// dispatch routes a parsed Input to the matching backend action.
// Translate of tool.py:288-426 `_dispatch` 1:1, including the capture_after
// follow-up capture (tool.py:158-185 `_maybe_follow_capture`).
func dispatch(ctx context.Context, b cuaBackend, in Input) (*tool.ToolResult, error) {
	captureAfter := in.CaptureAfter

	switch in.Action {
	case ActionCapture:
		mode := in.Mode
		if mode == "" {
			mode = ModeSom
		}
		if mode != ModeSom && mode != ModeVision && mode != ModeAx {
			return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("bad mode %q; use som|vision|ax", mode))}, nil
		}
		cap, err := b.capture(ctx, mode, in.App, coerceMaxElements(rawAny(in.MaxElements)))
		if err != nil {
			return nil, err
		}
		return captureResponse(cap, coerceMaxElements(rawAny(in.MaxElements))), nil

	case ActionWait:
		res, err := b.wait(ctx, in)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil

	case ActionListApps:
		res, err := b.listApps(ctx)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil

	case ActionFocusApp:
		if in.App == "" {
			return &tool.ToolResult{Data: errorResponse("focus_app requires `app`")}, nil
		}
		res, err := b.focusApp(ctx, in)
		if err != nil {
			return nil, err
		}
		return maybeFollowCapture(ctx, b, res, captureAfter, ""), nil

	case ActionClick, ActionDoubleClick, ActionRightClick, ActionMiddleClick:
		res, err := b.click(ctx, in)
		if err != nil {
			return nil, err
		}
		return maybeFollowCapture(ctx, b, res, captureAfter, ""), nil

	case ActionDrag:
		hasElements := in.FromElement != nil && in.ToElement != nil
		_, _, fromOK := parseCoordinate(in.FromCoordinate)
		_, _, toOK := parseCoordinate(in.ToCoordinate)
		if !hasElements && (!fromOK || !toOK) {
			return &tool.ToolResult{Data: errorResponse("drag requires from_coordinate/to_coordinate or from_element/to_element")}, nil
		}
		res, err := b.drag(ctx, in)
		if err != nil {
			return nil, err
		}
		return maybeFollowCapture(ctx, b, res, captureAfter, ""), nil

	case ActionScroll:
		res, err := b.scroll(ctx, in)
		if err != nil {
			return nil, err
		}
		return maybeFollowCapture(ctx, b, res, captureAfter, ""), nil

	case ActionType:
		res, err := b.typeText(ctx, in)
		if err != nil {
			return nil, err
		}
		return maybeFollowCapture(ctx, b, res, captureAfter, ""), nil

	case ActionKey:
		res, err := b.key(ctx, in)
		if err != nil {
			return nil, err
		}
		return maybeFollowCapture(ctx, b, res, captureAfter, ""), nil

	case ActionSetValue:
		if in.Value == "" {
			return &tool.ToolResult{Data: errorResponse("set_value requires value")}, nil
		}
		res, err := b.setValue(ctx, in)
		if err != nil {
			return nil, err
		}
		return maybeFollowCapture(ctx, b, res, captureAfter, ""), nil
	}

	return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("unknown action %q", in.Action))}, nil
}

// maybeFollowCapture mirrors tool.py:158-185 `_maybe_follow_capture`. When
// doCapture is false OR the action failed, return the action text result
// unchanged. Otherwise re-capture (preserving app context) and prepend the
// action summary to the capture's text block.
func maybeFollowCapture(ctx context.Context, b cuaBackend, res *ActionResult, doCapture bool, lastApp string) *tool.ToolResult {
	if !doCapture || !res.OK {
		return textResponse(res)
	}
	cap, err := b.capture(ctx, ModeSom, lastApp, defaultMaxElements)
	if err != nil {
		// Match Hermes: log + fall back to text. We have no logger here so
		// fall back silently — the caller sees the action text result.
		return textResponse(res)
	}
	resp := captureResponse(cap, defaultMaxElements)
	// Prepend "[action] ok=ok — message" to the text block, mirroring
	// _maybe_follow_capture's `prefix + "\n\n" + content[0]["text"]`.
	prefix := fmt.Sprintf("[%s] ok=%t", res.Action, res.OK)
	if res.Message != "" {
		prefix += " — " + res.Message
	}
	if len(resp.NewMessages) > 0 {
		msg := resp.Data.(string)
		resp.Data = prefix + "\n\n" + msg
		// The image block's preceding text block mirrors the data string.
		blocks := resp.NewMessages[0].Content
		for i := range blocks {
			if blocks[i].Type == types.ContentTypeText {
				blocks[i].Text = prefix + "\n\n" + blocks[i].Text
			}
		}
	}
	return resp
}

// ---------------------------------------------------------------------------
// Backend action methods — each maps action args to the matching cua-driver
// tool call. Translate of cua_backend.py:590-1190 (capture/click/drag/scroll/
// type_text/key/set_value/list_apps/focus_app).
// ---------------------------------------------------------------------------

// capture mirrors cua_backend.py:590-744 `CuaDriverBackend.capture()`.
func (b *Backend) capture(ctx context.Context, mode, app string, _ int) (*CaptureResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}

	lwOut, err := b.call(ctx, "list_windows", map[string]any{
		"on_screen_only": true,
		"session":        b.sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("list_windows: %w", err)
	}
	windows := parseWindows(extractResult(lwOut))
	// Sort by z_index ascending (lowest = frontmost on macOS).
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].ZIndex < windows[j].ZIndex })

	if len(windows) == 0 {
		return &CaptureResult{Mode: mode}, nil
	}

	// On Linux, cua-driver reports z_index=0 for all windows. Read the real
	// active window from X11 so resolveTargetWindow can pick it when z-order
	// is uniform. No-op on macOS/Windows (returns 0, fallback never triggers).
	if active := activeWindowID(b.display); active != 0 {
		b.mu.Lock()
		b.activeWin = active
		b.mu.Unlock()
	}

	target, err := b.resolveTargetWindow(windows, app)
	if err != nil {
		return &CaptureResult{Mode: mode, WindowTitle: err.Error()}, nil
	}
	b.mu.Lock()
	b.activePID = target.PID
	b.activeWin = target.WindowID
	if app != "" || b.lastApp == "" {
		b.lastApp = target.AppName
	}
	b.mu.Unlock()

	var (
		pngB64, mime  string
		elements      []UIElement
		width, height int
		windowTitle   string
	)
	if mode == ModeVision {
		pngB64, mime, width, height, windowTitle, err = b.captureVision(ctx, target)
		if err != nil {
			return nil, err
		}
	} else {
		pngB64, mime, elements, width, height, windowTitle, err = b.captureAxSom(ctx, target)
		if err != nil {
			return nil, err
		}
	}

	pngBytesLen := 0
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

	return &CaptureResult{
		Mode:          mode,
		Width:         width,
		Height:        height,
		PngB64:        pngB64,
		Elements:      elements,
		App:           target.AppName,
		WindowTitle:   windowTitle,
		PngBytesLen:   pngBytesLen,
		ImageMimeType: mime,
	}, nil
}

// windowInfo is a normalized list_windows entry. Translate of the inline dict
// construction at cua_backend.py:605-614.
type windowInfo struct {
	AppName   string
	PID       int
	WindowID  int
	OffScreen bool
	Title     string
	ZIndex    int
}

// resolveTargetWindow picks the window to capture for an `app` filter.
// Translate of cua_backend.py:625-712 (screen-sentinel + desktop-name
// resolution + app-name substring filter + off-screen fallback).
func (b *Backend) resolveTargetWindow(windows []windowInfo, app string) (windowInfo, error) {
	return resolveTargetWindow(windows, app, b.activeWin)
}

// resolveTargetWindow picks the window to capture. When app=="" and every
// window shares the same z_index (X11/xrdp reports 0 for all), z-order can't
// pick the frontmost. Fall back to the active window id (read from X11's
// _NET_ACTIVE_WINDOW on Linux, 0 on macOS/Windows) when it matches a visible
// window in the list.
func resolveTargetWindow(windows []windowInfo, app string, activeWinID int) (windowInfo, error) {
	if app == "" {
		// When all z_index values are identical, z-order is meaningless.
		// Prefer the X11 active window if it's in the list and visible.
		allSameZ := len(windows) > 0
		firstZ := windows[0].ZIndex
		for _, w := range windows {
			if w.ZIndex != firstZ {
				allSameZ = false
				break
			}
		}
		if allSameZ && activeWinID != 0 {
			for _, w := range windows {
				if w.WindowID == activeWinID && !w.OffScreen {
					return w, nil
				}
			}
		}
		for _, w := range windows {
			if !w.OffScreen {
				return w, nil
			}
		}
		return windows[0], nil
	}

	appLower := strings.ToLower(strings.TrimSpace(app))
	if _, isScreen := screenCaptureSentinels[appLower]; isScreen {
		// Whole-screen / desktop request. Resolve to OS shell/desktop window.
		var desktop []windowInfo
		for _, w := range windows {
			hay := strings.ToLower(w.AppName + " " + w.Title)
			if containsAny(hay, desktopWindowNames) {
				desktop = append(desktop, w)
			}
		}
		if len(desktop) == 0 {
			return windowInfo{}, fmt.Errorf(
				"<no desktop/shell window found for app=%q; cua-driver captures "+
					"one window at a time and exposes no whole-virtual-desktop "+
					"or per-monitor capture. Call list_apps / capture(app='<AppName>') "+
					"to target a specific window instead.>", app)
		}
		// Prefer the desktop backdrop (Progman/WorkerW/Finder) over taskbar.
		backdropNames := []string{"progman", "workerw", "program manager", "finder", "desktop"}
		sort.SliceStable(desktop, func(i, j int) bool {
			hi := strings.ToLower(desktop[i].AppName + " " + desktop[i].Title)
			return containsAny(hi, backdropNames)
		})
		return desktop[0], nil
	}

	// App-name substring filter.
	var filtered []windowInfo
	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.AppName), appLower) {
			filtered = append(filtered, w)
		}
	}
	if len(filtered) == 0 {
		return windowInfo{}, fmt.Errorf(
			"<no on-screen window matched app=%q; call list_apps to see "+
				"available app names (macOS reports localized names, e.g. "+
				"'計算機' instead of 'Calculator')>", app)
	}
	for _, w := range filtered {
		if !w.OffScreen {
			return w, nil
		}
	}
	return filtered[0], nil
}

// captureVision translates cua_backend.py:677-728 vision-mode capture.
// Calls `get_window_state` but discards the AX tree, keeping only the PNG.
func (b *Backend) captureVision(ctx context.Context, target windowInfo) (pngB64, mime string, width, height int, title string, err error) {
	out, err := b.call(ctx, "get_window_state", map[string]any{
		"pid":       target.PID,
		"window_id": target.WindowID,
		"session":   b.sessionID,
	})
	if err != nil {
		return "", "", 0, 0, "", fmt.Errorf("get_window_state: %w", err)
	}
	res := extractResult(out)
	pngB64, mime = imageFromResult(res)
	if dataStr, ok := res["data"].(string); ok {
		if _, tree := splitTreeText(dataStr); tree != "" {
			title = extractWindowTitle(tree)
		}
	}
	return pngB64, mime, 0, 0, title, nil
}

// captureAxSom translates cua_backend.py:730-790 ax/som-mode capture.
// Calls `get_window_state`, parses structuredContent.elements and the image.
func (b *Backend) captureAxSom(ctx context.Context, target windowInfo) (pngB64, mime string, elements []UIElement, width, height int, title string, err error) {
	out, err := b.call(ctx, "get_window_state", map[string]any{
		"pid":       target.PID,
		"window_id": target.WindowID,
		"session":   b.sessionID,
	})
	if err != nil {
		return "", "", nil, 0, 0, "", fmt.Errorf("get_window_state: %w", err)
	}
	res := extractResult(out)
	elements = parseElementsFromResult(res)
	b.mu.Lock()
	b.snapTokens = map[int]string{}
	for _, e := range elements {
		if e.ElementToken != "" {
			b.snapTokens[e.Index] = e.ElementToken
		}
	}
	b.mu.Unlock()

	pngB64, mime = imageFromResult(res)
	if dataStr, ok := res["data"].(string); ok {
		if _, tree := splitTreeText(dataStr); tree != "" {
			title = extractWindowTitle(tree)
		}
	}
	return pngB64, mime, elements, 0, 0, title, nil
}

// click translates cua_backend.py:794-855 click() including the
// action→button/click_count shaping from tool.py:360-376.
func (b *Backend) click(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	pid, winID := b.activePID, b.activeWin
	b.mu.Unlock()
	if pid == 0 {
		return actionErr("click", "No active window — call capture() first."), nil
	}

	button := in.Button
	clickCount := 1
	switch in.Action {
	case ActionDoubleClick:
		clickCount = 2
	case ActionRightClick:
		button = ButtonRight
	case ActionMiddleClick:
		button = ButtonMiddle
	default:
		if button == "" {
			button = ButtonLeft
		}
	}
	buttonLower := strings.ToLower(button)
	if buttonLower != ButtonLeft && buttonLower != ButtonRight && buttonLower != ButtonMiddle {
		return actionErr("click", fmt.Sprintf("unknown button %q — expected left, right, middle.", button)), nil
	}
	toolName := "click"
	if clickCount == 2 {
		toolName = "double_click"
	}

	args := map[string]any{"pid": pid, "button": buttonLower}
	if in.Element != nil {
		if winID == 0 {
			return actionErr(toolName, "No active window_id for element_index click."), nil
		}
		args["element_index"] = *in.Element
		args["window_id"] = winID
		b.attachElementToken(toolName, *in.Element, args)
	} else if x, y, ok := parseCoordinate(in.Coordinate); ok {
		args["x"] = x
		args["y"] = y
	} else {
		return actionErr(toolName, "click requires element= or x/y."), nil
	}
	if len(in.Modifiers) > 0 {
		args["modifier"] = in.Modifiers
	}
	return b.runAction(ctx, toolName, args)
}

// drag translates cua_backend.py:859-898 drag().
func (b *Backend) drag(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	pid, winID := b.activePID, b.activeWin
	b.mu.Unlock()
	if pid == 0 {
		return actionErr("drag", "No active window — call capture() first."), nil
	}

	args := map[string]any{"pid": pid}
	hasElements := in.FromElement != nil && in.ToElement != nil
	fx, fy, fromOK := parseCoordinate(in.FromCoordinate)
	tx, ty, toOK := parseCoordinate(in.ToCoordinate)
	if hasElements {
		if winID == 0 {
			return actionErr("drag", "No active window_id for element-based drag."), nil
		}
		args["from_element"] = *in.FromElement
		args["to_element"] = *in.ToElement
		args["window_id"] = winID
	} else if fromOK && toOK {
		args["from_x"], args["from_y"] = fx, fy
		args["to_x"], args["to_y"] = tx, ty
	} else {
		return actionErr("drag", "drag requires from_coordinate/to_coordinate or from_element/to_element."), nil
	}
	return b.runAction(ctx, "drag", args)
}

// scroll translates cua_backend.py:902-933 scroll(). Default direction=down,
// default amount=3 clamped to [1,50].
func (b *Backend) scroll(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	pid, winID := b.activePID, b.activeWin
	b.mu.Unlock()
	if pid == 0 {
		return actionErr("scroll", "No active window — call capture() first."), nil
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
		"direction": direction,
		"amount":    amount,
	}
	if in.Element != nil && winID != 0 {
		args["element_index"] = *in.Element
		args["window_id"] = winID
		b.attachElementToken("scroll", *in.Element, args)
	} else if x, y, ok := parseCoordinate(in.Coordinate); ok {
		args["x"] = x
		args["y"] = y
	}
	return b.runAction(ctx, "scroll", args)
}

// typeText translates cua_backend.py:937-944 type_text().
func (b *Backend) typeText(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	pid := b.activePID
	b.mu.Unlock()
	if pid == 0 {
		return actionErr("type_text", "No active window — call capture() first."), nil
	}
	return b.runAction(ctx, "type_text", map[string]any{"pid": pid, "text": in.Text})
}

// key translates cua_backend.py:946-963 key() + _parse_key_combo.
func (b *Backend) key(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	pid := b.activePID
	b.mu.Unlock()
	if pid == 0 {
		return actionErr("key", "No active window — call capture() first."), nil
	}
	keyName, modifiers := parseKeyCombo(in.Keys)
	if keyName == "" {
		return actionErr("key", fmt.Sprintf("Could not parse key from '%s'.", in.Keys)), nil
	}
	if len(modifiers) > 0 {
		return b.runAction(ctx, "hotkey", map[string]any{
			"pid":  pid,
			"keys": append(modifiers, keyName),
		})
	}
	return b.runAction(ctx, "press_key", map[string]any{"pid": pid, "key": keyName})
}

// setValue translates cua_backend.py:979-1000 set_value().
func (b *Backend) setValue(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	pid, winID := b.activePID, b.activeWin
	b.mu.Unlock()
	if pid == 0 || winID == 0 {
		return actionErr("set_value", "No active window — call capture() first."), nil
	}
	if in.Element == nil {
		return actionErr("set_value", "set_value requires element= (element index)."), nil
	}
	args := map[string]any{
		"pid":           pid,
		"window_id":     winID,
		"element_index": *in.Element,
		"value":         in.Value,
	}
	b.attachElementToken("set_value", *in.Element, args)
	return b.runAction(ctx, "set_value", args)
}

// wait translates backend.py:165-168 wait() — pure sleep, no backend call.
func (b *Backend) wait(ctx context.Context, in Input) (*ActionResult, error) {
	seconds := 1.0
	if in.Seconds != nil {
		seconds = *in.Seconds
	}
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 30 {
		seconds = 30
	}
	select {
	case <-ctx.Done():
		return &ActionResult{OK: false, Action: "wait", Message: ctx.Err().Error()}, nil
	case <-timeAfter(timeDuration(seconds)):
	}
	return &ActionResult{OK: true, Action: "wait", Message: fmt.Sprintf("waited %.2fs", seconds)}, nil
}

// listApps translates cua_backend.py:1029-1050 list_apps().
func (b *Backend) listApps(ctx context.Context) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	out, err := b.call(ctx, "list_apps", map[string]any{"session": b.sessionID})
	if err != nil {
		return nil, fmt.Errorf("list_apps: %w", err)
	}
	res := extractResult(out)
	apps := parseAppsList(res["data"])
	return &ActionResult{
		OK:     true,
		Action: "list_apps",
		Meta:   map[string]any{"apps": apps, "count": len(apps)},
	}, nil
}

// focusApp translates cua_backend.py:1052-1093 focus_app() — a pure
// window-selector that does NOT raise the window (background co-work model).
func (b *Backend) focusApp(ctx context.Context, in Input) (*ActionResult, error) {
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
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].ZIndex < windows[j].ZIndex })

	appLower := strings.ToLower(in.App)
	var matched []windowInfo
	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.AppName), appLower) {
			matched = append(matched, w)
		}
	}
	if len(matched) == 0 {
		return actionErr("focus_app", fmt.Sprintf("No on-screen window found for app '%s'.", in.App)), nil
	}
	t := matched[0]
	b.mu.Lock()
	b.activePID = t.PID
	b.activeWin = t.WindowID
	b.lastApp = t.AppName
	b.mu.Unlock()
	return &ActionResult{
		OK:      true,
		Action:  "focus_app",
		Message: fmt.Sprintf("Targeted %s (pid %d, window %d) without raising window.", t.AppName, t.PID, t.WindowID),
	}, nil
}

// runAction is the shared call+result-shape path for all mutating actions.
// Translate of cua_backend.py:1486-1508 `_action`. runAction injects the
// session id, attaches element tokens where applicable (done by callers), and
// flattens the MCP result into an ActionResult.
func (b *Backend) runAction(ctx context.Context, name string, args map[string]any) (*ActionResult, error) {
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

// attachElementToken is the gbot translation of cua_backend.py:1451-1484
// `_maybe_attach_element_token`. The Hermes version gates on a per-tool
// capability claim discovered from `tools/list`; gbot's MCP client surface
// doesn't expose per-tool capability sets, so we attach the token
// unconditionally when one is cached for the element. cua-driver-rs treats
// an unknown field gracefully on the tools that don't expect it (additions
// are non-breaking for the token-capable ones), and the worst case for
// older drivers is a schema-validation error that runAction surfaces as
// ActionResult.OK=false — which is the correct "retry without token" signal
// to the model. The plan's hardening section covers surfacing the
// capability check via `tools/list` as a future refinement.
func (b *Backend) attachElementToken(toolName string, element int, args map[string]any) {
	b.mu.Lock()
	token := b.snapTokens[element]
	b.mu.Unlock()
	if token == "" {
		return
	}
	args["element_token"] = token
}

// ---------------------------------------------------------------------------
// Response shaping — translate of tool.py:507-650 _capture_response,
// _format_elements, _image_dimensions_from_b64.
// ---------------------------------------------------------------------------

// minProviderImageDimension is the minimum image side providers accept.
// Translate of tool.py:468 _MIN_PROVIDER_IMAGE_DIMENSION.
const minProviderImageDimension = 8

// captureResponse builds the ToolResult for a capture. Translate of
// tool.py:507-650 `_capture_response`. The image arrives via NewMessages
// (plan Key Decision #2): a user-role message carrying [text, image] blocks.
// The text summary lives in ToolResult.Data. When the image is too small or
// absent, NewMessages is empty and the result is text-only.
func captureResponse(cap *CaptureResult, maxElements int) *tool.ToolResult {
	totalElements := len(cap.Elements)
	visible := cap.Elements
	if maxElements > 0 && maxElements < totalElements {
		visible = cap.Elements[:maxElements]
	}
	truncated := max(totalElements-len(visible), 0)

	var dimsW, dimsH int
	imageTooSmall := false
	if cap.PngB64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(cap.PngB64); err == nil {
			if w, h, ok := imageDimensionsFromBytes(raw); ok {
				dimsW, dimsH = w, h
				if w < minProviderImageDimension || h < minProviderImageDimension {
					imageTooSmall = true
				}
			}
		}
	}
	respW := dimsW
	respH := dimsH
	if respW == 0 {
		respW = cap.Width
	}
	if respH == 0 {
		respH = cap.Height
	}

	elementIndex := formatElements(visible)
	summaryLines := []string{
		fmt.Sprintf("capture mode=%s %dx%d", cap.Mode, respW, respH) +
			optSuffix(" app=", cap.App) +
			optQuotedSuffix(" window=", cap.WindowTitle),
		fmt.Sprintf("%d interactable element(s):", totalElements),
	}
	summaryLines = append(summaryLines, elementIndex...)
	if imageTooSmall {
		summaryLines = append(summaryLines,
			fmt.Sprintf("  (screenshot omitted: %dx%d is below the %dx%d provider minimum)",
				dimsW, dimsH, minProviderImageDimension, minProviderImageDimension))
	}

	// Decide which branch we're on up-front. The multimodal branch (image
	// present, non-ax mode, image not too small) carries the screenshot, NOT
	// the AX elements array — so a "response truncated to N of M elements"
	// note would be inaccurate there (tool.py:624-628). The AX-only / image-
	// missing branch actually carries the elements array, so the note applies.
	multimodal := cap.PngB64 != "" && cap.Mode != ModeAx && !imageTooSmall

	// Multimodal summary is frozen before the truncation note; the text
	// branch appends the note and rebuilds.
	summary := strings.Join(summaryLines, "\n")

	if truncated > 0 && !multimodal {
		summaryLines = append(summaryLines,
			fmt.Sprintf("  (response truncated to %d of %d elements; raise max_elements or pass app= to narrow)",
				len(visible), totalElements))
		summary = strings.Join(summaryLines, "\n")
	}

	res := &tool.ToolResult{Data: summary}

	if multimodal {
		mime := cap.ImageMimeType
		if mime == "" {
			if strings.HasPrefix(cap.PngB64, "/9j/") {
				mime = "image/jpeg"
			} else {
				mime = "image/png"
			}
		}
		res.NewMessages = []types.Message{{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock(summary),
				types.NewImageBlock(types.ImageSource{
					Type:      "base64",
					MediaType: mime,
					Data:      cap.PngB64,
				}),
			},
		}}
	}
	return res
}

// textResponse builds the JSON text result for a non-capture action.
// Translate of tool.py:431-440 `_text_response`. Used ONLY for successful
// ActionResult objects coming back from the backend — pre-dispatch validation
// rejections use errorResponse (matching Hermes's {"error": ...} envelope).
func textResponse(res *ActionResult) *tool.ToolResult {
	payload := map[string]any{"ok": res.OK, "action": res.Action}
	if res.Message != "" {
		payload["message"] = res.Message
	}
	if len(res.Meta) > 0 {
		payload["meta"] = res.Meta
	}
	b, _ := json.Marshal(payload)
	return &tool.ToolResult{Data: string(b)}
}

// errorResponse builds the {"error": ...} JSON result used for pre-dispatch
// validation/safety rejections. Translate of Hermes tool.py's
// `json.dumps({"error": ...})` returns from handle_computer_use (blocked
// type/key combo, backend unavailable) and _dispatch (bad mode, missing
// app/value, drag missing coords, unknown action).
func errorResponse(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// formatElements builds the human-readable element index lines, capped at
// maxLines. Translate of tool.py:654-664 `_format_elements`.
func formatElements(elements []UIElement) []string {
	const maxLines = 40
	out := make([]string, 0, len(elements))
	for _, e := range elements {
		if len(out) >= maxLines {
			break
		}
		label := strings.ReplaceAll(e.Label, "\n", " ")
		if len(label) > 60 {
			label = label[:60]
		}
		line := fmt.Sprintf("  #%d %s %q @ %v", e.Index, e.Role, label, e.Bounds)
		if e.App != "" {
			line += fmt.Sprintf(" [%s]", e.App)
		}
		out = append(out, line)
	}
	if len(elements) > maxLines {
		out = append(out, fmt.Sprintf("  ... +%d more (call capture with app= to narrow)", len(elements)-maxLines))
	}
	return out
}

// imageDimensionsFromBytes sniffs PNG/JPEG dimensions from raw image bytes
// without extra dependencies. Translate of cua_backend.py:330-387
// `_image_dimensions_from_bytes`. Returns ok=false when the format is
// unknown or the bytes are too short.
func imageDimensionsFromBytes(raw []byte) (int, int, bool) {
	// PNG: signature + IHDR width/height.
	if len(raw) >= 24 && string(raw[:8]) == "\x89PNG\r\n\x1a\n" {
		w := int(raw[16])<<24 | int(raw[17])<<16 | int(raw[18])<<8 | int(raw[19])
		h := int(raw[20])<<24 | int(raw[21])<<16 | int(raw[22])<<8 | int(raw[23])
		if w > 0 && h > 0 {
			return w, h, true
		}
	}
	// JPEG: scan for SOF markers carrying dimensions.
	if len(raw) > 4 && raw[0] == 0xFF && raw[1] == 0xD8 {
		i := 2
		n := len(raw)
		for i+9 < n {
			if raw[i] != 0xFF {
				i++
				continue
			}
			marker := raw[i+1]
			i += 2
			if marker == 0xD8 || marker == 0xD9 {
				continue
			}
			if marker >= 0xD0 && marker <= 0xD7 {
				continue
			}
			if marker == 0xDA {
				break
			}
			if i+2 > n {
				break
			}
			segLen := int(raw[i])<<8 | int(raw[i+1])
			if segLen < 2 || i+segLen > n {
				break
			}
			sofMarkers := map[byte]bool{
				0xC0: true, 0xC1: true, 0xC2: true, 0xC3: true,
				0xC5: true, 0xC6: true, 0xC7: true,
				0xC9: true, 0xCA: true, 0xCB: true,
				0xCD: true, 0xCE: true, 0xCF: true,
			}
			if sofMarkers[marker] && segLen >= 7 {
				h := int(raw[i+3])<<8 | int(raw[i+4])
				w := int(raw[i+5])<<8 | int(raw[i+6])
				if w > 0 && h > 0 {
					return w, h, true
				}
				break
			}
			i += segLen
		}
	}
	return 0, 0, false
}

// parseElementsFromStructured translates cua_backend.py:243-289
// `_parse_elements_from_structured`. Reads the canonical
// structuredContent.elements array (trycua/cua#1961), preserving real frames
// and the opaque element_token. Malformed entries are skipped rather than
// failing the whole walk.
func parseElementsFromStructured(rawElements []any) []UIElement {
	out := make([]UIElement, 0, len(rawElements))
	for _, raw := range rawElements {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		idx, ok := asInt(m["element_index"])
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		label, _ := m["label"].(string)
		var bounds [4]int
		if frame, ok := m["frame"].(map[string]any); ok {
			bounds[0], _ = asInt(frame["x"])
			bounds[1], _ = asInt(frame["y"])
			bounds[2], _ = asInt(frame["w"])
			bounds[3], _ = asInt(frame["h"])
		}
		token, _ := m["element_token"].(string)
		out = append(out, UIElement{
			Index:        idx,
			Role:         role,
			Label:        label,
			Bounds:       bounds,
			ElementToken: token,
		})
	}
	return out
}

// elementLineRE translates cua_backend.py:215-224 `_ELEMENT_LINE_RE` — the
// regex fallback for parsing elements out of get_window_state's AX tree
// markdown when structuredContent.elements is absent. Handles both the
// classic `"label"`-quoted and newer `id=Label` formats.
var elementLineRE = regexp.MustCompile(`(?m)^\s*(?:-\s+)?\[(\d+)\]\s+(\w+)(?:\s+"([^"]*)"|(?:\s+\(\d+\))?\s+id=([^\s\[\]]*))?`)

// parseElementsFromTree translates cua_backend.py:226-241
// `_parse_elements_from_tree`. Bounds are always (0,0,0,0) because the
// markdown surface doesn't carry them — that's the loss the structured path
// avoids.
func parseElementsFromTree(markdown string) []UIElement {
	out := []UIElement{}
	for _, m := range elementLineRE.FindAllStringSubmatch(markdown, -1) {
		idx, _ := strconv.Atoi(m[1])
		label := m[3]
		if label == "" {
			label = m[4]
		}
		out = append(out, UIElement{
			Index: idx,
			Role:  m[2],
			Label: label,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Helpers (small, file-local).
// ---------------------------------------------------------------------------

// actionErr is a convenience constructor for an ActionResult failure that
// is not a Go-level error (mirrors the Python `ActionResult(ok=False, ...)`
// returns that are not exceptions).
func actionErr(action, message string) *ActionResult {
	return &ActionResult{OK: false, Action: action, Message: message}
}

// parseWindows translates the list_windows structured payload to normalized
// windowInfo. Mirror of cua_backend.py:605-614.
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
		// is_on_screen is absent on Linux (cua-driver PARITY OPEN); since the
		// list_windows call passes on_screen_only=true, treat missing as on-screen.
		onScreen := true
		if v, ok := wmap["is_on_screen"].(bool); ok {
			onScreen = v
		}
		windows = append(windows, windowInfo{
			AppName:   app,
			PID:       pid,
			WindowID:  winID,
			OffScreen: !onScreen,
			Title:     title,
			ZIndex:    z,
		})
	}
	return windows
}

// imageFromResult pulls (png_b64, mime) out of a flattened tool result.
// Translate of cua_backend.py:610-650 `_image_from_tool_result`. Checks the
// image content-part array first, then structuredContent screenshot fields.
func imageFromResult(out map[string]any) (string, string) {
	if images, ok := out["images"].([]string); ok && len(images) > 0 && images[0] != "" {
		mime := ""
		if mimes, ok := out["image_mime_types"].([]string); ok && len(mimes) > 0 {
			mime = mimes[0]
		}
		return images[0], mime
	}
	if sc, ok := out["structuredContent"].(map[string]any); ok {
		for _, k := range []string{"screenshot_png_b64", "png_b64"} {
			if b64, ok := sc[k].(string); ok && b64 != "" {
				mime := ""
				for _, mk := range []string{"screenshot_mime_type", "mime_type"} {
					if v, ok := sc[mk].(string); ok {
						mime = v
						break
					}
				}
				return b64, mime
			}
		}
	}
	return "", ""
}

// parseElementsFromResult picks the structured elements array when present,
// else falls back to the tree regex. Translate of cua_backend.py:749-764.
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

// parseAppsList translates cua_backend.py:1033-1048 list_apps result parsing.
// Accepts list, dict-with-apps, or newline text with "name (pid N)" lines.
func parseAppsList(data any) []map[string]any {
	switch d := data.(type) {
	case []any:
		out := make([]map[string]any, 0, len(d))
		for _, v := range d {
			if m, ok := v.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		if apps, ok := d["apps"].([]any); ok {
			out := make([]map[string]any, 0, len(apps))
			for _, v := range apps {
				if m, ok := v.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
	case string:
		// Parse "name (pid N)" lines.
		re := regexp.MustCompile(`(.+?)\s+\(pid\s+(\d+)\)`)
		out := []map[string]any{}
		for line := range strings.SplitSeq(d, "\n") {
			if m := re.FindStringSubmatch(line); m != nil {
				name := strings.TrimSpace(m[1])
				pid, _ := strconv.Atoi(m[2])
				out = append(out, map[string]any{"name": name, "pid": pid})
			}
		}
		return out
	}
	return nil
}

// splitTreeText splits get_window_state text into (summary, tree). Translate
// of cua_backend.py:389-395 `_split_tree_text`.
func splitTreeText(full string) (string, string) {
	parts := strings.SplitN(full, "\n", 2)
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// extractWindowTitle pulls the window title out of the AX tree markdown.
// Translate of cua_backend.py:779-781 (the regex search after the tree split).
// macOS reports AXWindow "title"; Linux AT-SPI reports frame = "title".
var windowTitleRE = regexp.MustCompile(`AXWindow\s+"([^"]+)"`)
var frameTitleRE = regexp.MustCompile(`frame\s+=\s+"([^"]+)"`)

func extractWindowTitle(tree string) string {
	if m := windowTitleRE.FindStringSubmatch(tree); m != nil {
		return m[1]
	}
	if m := frameTitleRE.FindStringSubmatch(tree); m != nil {
		return m[1]
	}
	return ""
}

// parseKeyCombo parses a key string like 'cmd+s' into (key, modifiers).
// Translate of cua_backend.py:397-413 `_parse_key_combo`.
func parseKeyCombo(keys string) (string, []string) {
	modifierNames := map[string]bool{
		"cmd": true, "command": true, "shift": true,
		"option": true, "alt": true, "ctrl": true, "control": true, "fn": true,
	}
	aliases := map[string]string{
		"command": "cmd", "alt": "option", "control": "ctrl",
	}
	parts := strings.FieldsFunc(keys, func(r rune) bool { return r == '+' || r == '-' })
	var modifiers []string
	key := ""
	for _, raw := range parts {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		if norm, ok := aliases[p]; ok {
			p = norm
		}
		if modifierNames[p] {
			modifiers = append(modifiers, p)
		} else {
			key = p // last non-modifier wins
		}
	}
	return key, modifiers
}

// containsAny reports whether s contains any of subs (case-insensitive
// already assumed — caller lowercases). Translate of the inline
// `any(name in haystack for name in _DESKTOP_WINDOW_NAMES)` Python check.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// optSuffix / optQuotedSuffix format optional app/window suffixes on the
// capture summary header. Translate of tool.py:534-536.
func optSuffix(prefix, value string) string {
	if value == "" {
		return ""
	}
	return prefix + value
}

func optQuotedSuffix(prefix, value string) string {
	if value == "" {
		return ""
	}
	return prefix + strconv.Quote(value)
}

// asInt coerces a JSON-decoded numeric value to int. JSON numbers arrive as
// float64; this helper centralizes the cast and the success flag so callers
// can skip malformed fields.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// rawAny decodes a json.RawMessage into its generic Go value (nil if empty).
// Used to bridge Input.MaxElements (json.RawMessage) into coerceMaxElements
// (which takes any, mirroring Python's Any).
func rawAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}
