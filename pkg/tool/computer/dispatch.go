package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Result types.
// ---------------------------------------------------------------------------

// UIElement is one interactable element on the current screen.
type UIElement struct {
	Index        int    // 1-based SOM index
	Role         string // AX role (AXButton, AXTextField, ...)
	Label        string // AXTitle / AXDescription / AXValue snippet
	Bounds       [4]int // x, y, w, h (logical px)
	App          string // owning bundle ID or app name
	PID          int    // owning process PID
	WindowID     int    // SkyLight / CG / X11 window ID
	ElementToken string // opaque per-snapshot token
}

// CaptureResult is the result of a screen capture call (snapshot or zoom).
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
type ActionResult struct {
	OK      bool
	Action  string
	Message string
	Meta    map[string]any
}

// ---------------------------------------------------------------------------
// cuaBackend — narrow dispatch surface. *Backend satisfies it; a testBackend
// stub records calls and returns canned results so dispatch is testable
// without a live cua-driver.
// ---------------------------------------------------------------------------

type cuaBackend interface {
	list(ctx context.Context) (*ActionResult, error)
	snapshot(ctx context.Context, in Input) (*CaptureResult, error)
	click(ctx context.Context, in Input) (*ActionResult, error)
	typeText(ctx context.Context, in Input) (*ActionResult, error)
	key(ctx context.Context, in Input) (*ActionResult, error)
	scroll(ctx context.Context, in Input) (*ActionResult, error)
	drag(ctx context.Context, in Input) (*ActionResult, error)
	zoom(ctx context.Context, in Input) (*CaptureResult, error)
	wait(ctx context.Context, in Input) (*ActionResult, error)
}

// dispatch routes a parsed Input to the matching backend action. Every action
// except `list` and `wait` requires an explicit `window` (window_id).
func dispatch(ctx context.Context, b cuaBackend, in Input) (*tool.ToolResult, error) {
	switch in.Action {
	case ActionList:
		res, err := b.list(ctx)
		if err != nil {
			return nil, err
		}
		return listResponse(res), nil

	case ActionSnapshot:
		if in.Window == nil {
			return &tool.ToolResult{Data: errorResponse("snapshot requires window= (window_id from list/snapshot)")}, nil
		}
		mode := in.Mode
		if mode == "" {
			mode = ModeSom
		}
		if mode != ModeSom && mode != ModeVision && mode != ModeAx {
			return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("bad mode %q; use som|vision|ax", mode))}, nil
		}
		cap, err := b.snapshot(ctx, in)
		if err != nil {
			return nil, err
		}
		return captureResponse(cap, coerceMaxElements(rawAny(in.MaxElements))), nil

	case ActionClick:
		if in.Window == nil {
			return &tool.ToolResult{Data: errorResponse("click requires window= (window_id from list/snapshot)")}, nil
		}
		hasTarget := in.Element != nil
		if _, _, ok := parseCoordinate(in.Coordinate); ok {
			hasTarget = true
		}
		if !hasTarget {
			return &tool.ToolResult{Data: errorResponse("click requires element= or coordinate=")}, nil
		}
		res, err := b.click(ctx, in)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil

	case ActionType:
		if in.Window == nil {
			return &tool.ToolResult{Data: errorResponse("type requires window= (window_id from list/snapshot)")}, nil
		}
		res, err := b.typeText(ctx, in)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil

	case ActionKey:
		if in.Window == nil {
			return &tool.ToolResult{Data: errorResponse("key requires window= (window_id from list/snapshot)")}, nil
		}
		res, err := b.key(ctx, in)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil

	case ActionScroll:
		if in.Window == nil {
			return &tool.ToolResult{Data: errorResponse("scroll requires window= (window_id from list/snapshot)")}, nil
		}
		res, err := b.scroll(ctx, in)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil

	case ActionDrag:
		if in.Window == nil {
			return &tool.ToolResult{Data: errorResponse("drag requires window= (window_id from list/snapshot)")}, nil
		}
		_, _, fromOK := parseCoordinate(in.FromCoordinate)
		_, _, toOK := parseCoordinate(in.ToCoordinate)
		if !fromOK || !toOK {
			return &tool.ToolResult{Data: errorResponse("drag requires from_coordinate and to_coordinate")}, nil
		}
		res, err := b.drag(ctx, in)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil

	case ActionZoom:
		if in.Window == nil {
			return &tool.ToolResult{Data: errorResponse("zoom requires window= (window_id from list/snapshot)")}, nil
		}
		if _, _, _, _, ok := parseRegion(in.Region); !ok {
			return &tool.ToolResult{Data: errorResponse("zoom requires region=[x1,y1,x2,y2]")}, nil
		}
		cap, err := b.zoom(ctx, in)
		if err != nil {
			return nil, err
		}
		return zoomResponse(cap), nil

	case ActionWait:
		res, err := b.wait(ctx, in)
		if err != nil {
			return nil, err
		}
		return textResponse(res), nil
	}

	return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("unknown action %q", in.Action))}, nil
}

// ---------------------------------------------------------------------------
// Backend action methods — each maps action args to the matching cua-driver
// tool call. Each resolves pid from window_id via resolvePID (no implicit
// active-target state).
// ---------------------------------------------------------------------------

// list enumerates on-screen windows via list_windows, warms winCache, and
// returns a text summary. The `type` field is heuristic-derived (cua-driver's
// Linux list_windows exposes no window-type or app_name field; only title).
func (b *Backend) list(ctx context.Context) (*ActionResult, error) {
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
		wtype := "app"
		hay := strings.ToLower(w.Title + " " + w.AppName)
		if containsAny(hay, desktopWindowNames) {
			wtype = "desktop"
		} else if strings.Contains(hay, "panel") {
			wtype = "panel"
		}
		lines = append(lines, fmt.Sprintf(
			"window_id=%d pid=%d title=%q bounds=[%d,%d,%d,%d] type=%s",
			w.WindowID, w.PID, w.Title, w.X, w.Y, w.Width, w.Height, wtype,
		))
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
func (b *Backend) snapshot(ctx context.Context, in Input) (*CaptureResult, error) {
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
		elements = parseElementsFromResult(res)
		b.mu.Lock()
		b.snapTokens = map[int]string{}
		for _, e := range elements {
			if e.ElementToken != "" {
				b.snapTokens[e.Index] = e.ElementToken
			}
		}
		b.mu.Unlock()
	}
	if sc, ok := res["structuredContent"].(map[string]any); ok {
		if sid, ok := sc["snapshot_id"].(string); ok {
			b.mu.Lock()
			b.snapshotID = sid
			b.mu.Unlock()
		}
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
func (b *Backend) click(ctx context.Context, in Input) (*ActionResult, error) {
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
	if in.Element != nil {
		args["element_index"] = *in.Element
		b.attachElementToken("click", *in.Element, args)
	} else if x, y, ok := parseCoordinate(in.Coordinate); ok {
		args["x"] = x
		args["y"] = y
	} else {
		return actionErr("click", "click requires element= or coordinate=."), nil
	}
	return b.runAction(ctx, "click", args)
}

// typeText types a string into the window.
func (b *Backend) typeText(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	pid, err := b.resolvePID(ctx, *in.Window)
	if err != nil {
		return nil, err
	}
	return b.runAction(ctx, "type_text", map[string]any{
		"pid":       pid,
		"window_id": *in.Window,
		"text":      in.Text,
	})
}

// key presses a key or hotkey combo in the window.
func (b *Backend) key(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	pid, err := b.resolvePID(ctx, *in.Window)
	if err != nil {
		return nil, err
	}
	keyName, modifiers := parseKeyCombo(in.Keys)
	if keyName == "" {
		return actionErr("key", fmt.Sprintf("Could not parse key from '%s'.", in.Keys)), nil
	}
	if len(modifiers) > 0 {
		return b.runAction(ctx, "hotkey", map[string]any{
			"pid":       pid,
			"window_id": *in.Window,
			"keys":      append(modifiers, keyName),
		})
	}
	return b.runAction(ctx, "press_key", map[string]any{
		"pid":       pid,
		"window_id": *in.Window,
		"key":       keyName,
	})
}

// scroll scrolls the window. Default direction=down, default amount=3 clamped
// to [1,50].
func (b *Backend) scroll(ctx context.Context, in Input) (*ActionResult, error) {
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
	if in.Element != nil {
		args["element_index"] = *in.Element
		b.attachElementToken("scroll", *in.Element, args)
	} else if x, y, ok := parseCoordinate(in.Coordinate); ok {
		args["x"] = x
		args["y"] = y
	}
	return b.runAction(ctx, "scroll", args)
}

// drag drags from from_coordinate to to_coordinate. Coordinate-only (element-
// based drag is dropped per the explicit-window design).
func (b *Backend) drag(ctx context.Context, in Input) (*ActionResult, error) {
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

// zoom captures a high-detail sub-region screenshot. zoom itself needs no pid,
// but we resolve+cache it so the model's subsequent from_zoom=true click/type
// can resolve the window.
func (b *Backend) zoom(ctx context.Context, in Input) (*CaptureResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	pid, err := b.resolvePID(ctx, *in.Window)
	if err != nil {
		return nil, err
	}
	x1, y1, x2, y2, _ := parseRegion(in.Region)
	out, err := b.call(ctx, "zoom", map[string]any{
		"window_id": *in.Window,
		"x1":        x1,
		"y1":        y1,
		"x2":        x2,
		"y2":        y2,
	})
	if err != nil {
		return nil, fmt.Errorf("zoom: %w", err)
	}
	res := extractResult(out)
	pngB64, mime := imageFromResult(res)

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
		Mode:          ModeZoom,
		Width:         width,
		Height:        height,
		PngB64:        pngB64,
		PngBytesLen:   pngBytesLen,
		ImageMimeType: mime,
	}, nil
}

// wait is a pure sleep, no backend call. Clamp [0,30].
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

// runAction is the shared call+result-shape path for all mutating actions.
// It injects the session id and flattens the MCP result into an ActionResult.
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

// attachElementToken attaches the cached element_token for an element index
// when one is available (refreshed by the last snapshot of this window).
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
// Response shaping.
// ---------------------------------------------------------------------------

// minProviderImageDimension is the minimum image side providers accept.
const minProviderImageDimension = 8

// listResponse builds the ToolResult for a list action (text only, no image).
func listResponse(res *ActionResult) *tool.ToolResult {
	payload := map[string]any{"ok": res.OK, "action": res.Action}
	if res.Message != "" {
		payload["windows"] = res.Message
	}
	if c, ok := res.Meta["count"]; ok {
		payload["count"] = c
	}
	b, _ := json.Marshal(payload)
	return &tool.ToolResult{Data: string(b)}
}

// captureResponse builds the ToolResult for a snapshot. The image arrives via
// NewMessages: a user-role message carrying [text, image] blocks. The text
// summary lives in ToolResult.Data. When the image is too small or absent,
// NewMessages is empty and the result is text-only.
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

	// The multimodal branch (image present, non-ax mode, image not too small)
	// carries the screenshot, NOT the AX elements array — so a "response
	// truncated to N of M elements" note would be inaccurate there. The AX-only
	// / image-missing branch actually carries the elements array, so the note
	// applies.
	multimodal := cap.PngB64 != "" && cap.Mode != ModeAx && !imageTooSmall

	summary := strings.Join(summaryLines, "\n")

	if truncated > 0 && !multimodal {
		summaryLines = append(summaryLines,
			fmt.Sprintf("  (response truncated to %d of %d elements; raise max_elements to see more)",
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

// zoomResponse builds the ToolResult for a zoom action — a thin variant of
// captureResponse: same [text, image] NewMessages block, but the header labels
// it "zoom region".
func zoomResponse(cap *CaptureResult) *tool.ToolResult {
	var dimsW, dimsH int
	if cap.PngB64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(cap.PngB64); err == nil {
			if w, h, ok := imageDimensionsFromBytes(raw); ok {
				dimsW, dimsH = w, h
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
	summary := fmt.Sprintf("zoom region %dx%d", respW, respH)
	res := &tool.ToolResult{Data: summary}
	if cap.PngB64 == "" {
		return res
	}
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
	return res
}

// textResponse builds the JSON text result for a non-capture action. Used
// ONLY for successful ActionResult objects coming back from the backend —
// pre-dispatch validation rejections use errorResponse.
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
// validation/safety rejections.
func errorResponse(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// formatElements builds the human-readable element index lines, capped at
// maxLines.
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
		out = append(out, fmt.Sprintf("  ... +%d more (raise max_elements to see more)", len(elements)-maxLines))
	}
	return out
}

// imageDimensionsFromBytes sniffs PNG/JPEG dimensions from raw image bytes
// without extra dependencies. Returns ok=false when the format is unknown or
// the bytes are too short.
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

// parseElementsFromStructured reads the canonical structuredContent.elements
// array, preserving real frames and the opaque element_token. Malformed
// entries are skipped rather than failing the whole walk.
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

// elementLineRE is the regex fallback for parsing elements out of
// get_window_state's AX tree markdown when structuredContent.elements is
// absent. Handles both the classic "label"-quoted and newer id=Label formats.
var elementLineRE = regexp.MustCompile(`(?m)^\s*(?:-\s+)?\[(\d+)\]\s+(\w+)(?:\s+"([^"]*)"|(?:\s+\(\d+\))?\s+id=([^\s\[\]]*))?`)

// parseElementsFromTree parses elements from the AX tree markdown. Bounds are
// always (0,0,0,0) because the markdown surface doesn't carry them — that's
// the loss the structured path avoids.
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

// actionErr is a convenience constructor for an ActionResult failure that is
// not a Go-level error.
func actionErr(action, message string) *ActionResult {
	return &ActionResult{OK: false, Action: action, Message: message}
}

// windowInfo is a normalized list_windows entry.
type windowInfo struct {
	AppName   string
	PID       int
	WindowID  int
	OffScreen bool
	Title     string
	ZIndex    int
	X, Y      int
	Width     int
	Height    int
}

// parseWindows translates the list_windows structured payload to normalized
// windowInfo.
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

// imageFromResult pulls (png_b64, mime) out of a flattened tool result. Checks
// the image content-part array first, then structuredContent screenshot fields.
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
// else falls back to the tree regex.
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

// splitTreeText splits get_window_state text into (summary, tree).
func splitTreeText(full string) (string, string) {
	parts := strings.SplitN(full, "\n", 2)
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// extractWindowTitle pulls the window title out of the AX tree markdown.
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
// already assumed — caller lowercases).
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// optSuffix / optQuotedSuffix format optional app/window suffixes on the
// capture summary header.
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
// (which takes any).
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
