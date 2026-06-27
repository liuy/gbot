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

// CaptureResult is the result of a screen capture call (snapshot).
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
// backend — platform-agnostic dispatch surface. *CuaBackend (mac/windows)
// and *X11Backend (linux) both satisfy it. A testBackend stub records calls
// and returns canned results so dispatch is testable without a live driver.
// ---------------------------------------------------------------------------

type backend interface {
	ensureStarted(ctx context.Context) error
	list(ctx context.Context) (*ActionResult, error)
	snapshot(ctx context.Context, in Input) (*CaptureResult, error)
	click(ctx context.Context, in Input) (*ActionResult, error)
	typeText(ctx context.Context, in Input) (*ActionResult, error)
	key(ctx context.Context, in Input) (*ActionResult, error)
	scroll(ctx context.Context, in Input) (*ActionResult, error)
	drag(ctx context.Context, in Input) (*ActionResult, error)
}

// dispatch routes a parsed Input to the matching backend action. Every action
// except `list` requires an explicit `window` (window_id).
func dispatch(ctx context.Context, b backend, in Input) (*tool.ToolResult, error) {
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
		// R3: element-based click removed — coordinate is the only targeting mode.
		if _, _, ok := parseCoordinate(in.Coordinate); !ok {
			return &tool.ToolResult{Data: errorResponse("click requires coordinate=[x,y]")}, nil
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
	}

	return &tool.ToolResult{Data: errorResponse(fmt.Sprintf("unknown action %q", in.Action))}, nil
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
// Helpers shared by both backends.
// ---------------------------------------------------------------------------

// actionErr is a convenience constructor for an ActionResult failure that is
// not a Go-level error.
func actionErr(action, message string) *ActionResult {
	return &ActionResult{OK: false, Action: action, Message: message}
}

// windowInfo is a normalized list entry. Both CuaBackend (cua-driver
// structured payload) and X11Backend (EWMH property walk) produce these.
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

// desktopWindowNames is matched case-insensitively as a substring against a
// window's title to classify it as a desktop/shell surface in list output.
// Shared by both backends' list classification.
var desktopWindowNames = []string{
	"progman", "workerw", "program manager", // Windows desktop
	"shell_traywnd", "taskbar", // Windows taskbar
	"finder", "desktop", "dock", // macOS desktop / shell
}

// formatWindowLine renders a single window list line. Shared by both
// backends so list output is identical regardless of platform.
func formatWindowLine(w windowInfo) string {
	wtype := "app"
	hay := strings.ToLower(w.Title + " " + w.AppName)
	if containsAny(hay, desktopWindowNames) {
		wtype = "desktop"
	} else if strings.Contains(hay, "panel") {
		wtype = "panel"
	}
	return fmt.Sprintf(
		"window_id=%d pid=%d title=%q bounds=[%d,%d,%d,%d] type=%s",
		w.WindowID, w.PID, w.Title, w.X, w.Y, w.Width, w.Height, wtype,
	)
}

// parseWindows and parseElementsFromResult live in dispatch.go (!linux): they
// consume the cua-driver structured payload shape, which the X11Backend path
// never produces.

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

// parseElementsFromResult lives in dispatch.go (!linux) — it consumes the
// cua-driver get_window_state structured payload shape (X11Backend has no
// elements array to parse).

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
// C8: win/super/meta/windows are recognized as modifiers (previously dropped,
// which silently mis-parsed combos like win+r / super+r / meta+r).
func parseKeyCombo(keys string) (string, []string) {
	modifierNames := map[string]bool{
		"cmd": true, "command": true, "shift": true,
		"option": true, "alt": true, "ctrl": true, "control": true, "fn": true,
		"win": true, "super": true, "meta": true, "windows": true,
	}
	aliases := map[string]string{
		"command": "cmd", "alt": "option", "control": "ctrl",
	}
	parts := strings.FieldsFunc(keys, func(r rune) bool { return r == '+' || r == '-' })
	var modifiers []string
	key := ""
	for _, raw := range parts {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		pl := strings.ToLower(p)
		if norm, ok := aliases[pl]; ok {
			p = norm
		}
		if modifierNames[strings.ToLower(p)] {
			modifiers = append(modifiers, p)
		} else {
			key = p // last non-modifier wins — preserve case for xdotool
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

// summarizeAction is the tool-card summary string for each action. Shared by
// both platforms' New() Description_ callback.
func summarizeAction(in Input) string {
	wid := ""
	if in.Window != nil {
		wid = fmt.Sprintf(" window=%d", *in.Window)
	}
	switch in.Action {
	case ActionList:
		return "list windows"
	case ActionSnapshot:
		suffix := ""
		if in.Mode != "" && in.Mode != ModeSom {
			suffix = " mode=" + in.Mode
		}
		return fmt.Sprintf("snapshot%s%s", wid, suffix)
	case ActionClick:
		// R3: element-based click removed — coordinate-only targeting.
		target := ""
		if x, y, ok := parseCoordinate(in.Coordinate); ok {
			target = fmt.Sprintf(" at (%d,%d)", x, y)
		}
		extra := ""
		if in.Button != "" && in.Button != ButtonLeft {
			extra += " " + in.Button
		}
		if in.Count != nil && *in.Count > 1 {
			extra += fmt.Sprintf(" x%d", *in.Count)
		}
		return fmt.Sprintf("click%s%s%s", wid, target, extra)
	case ActionType:
		text := in.Text
		suffix := ""
		if len(text) > 60 {
			text, suffix = text[:60], "..."
		}
		return fmt.Sprintf("type%s %q%s", wid, text, suffix)
	case ActionKey:
		return fmt.Sprintf("key%s %q", wid, in.Keys)
	case ActionScroll:
		dir := in.Direction
		if dir == "" {
			dir = "?"
		}
		amount := 3
		if in.Amount != nil {
			amount = *in.Amount
		}
		return fmt.Sprintf("scroll%s %s x%d", wid, dir, amount)
	case ActionDrag:
		src, dst := "?", "?"
		if x, y, ok := parseCoordinate(in.FromCoordinate); ok {
			src = fmt.Sprintf("(%d,%d)", x, y)
		}
		if x, y, ok := parseCoordinate(in.ToCoordinate); ok {
			dst = fmt.Sprintf("(%d,%d)", x, y)
		}
		return fmt.Sprintf("drag%s %s→%s", wid, src, dst)
		// ActionZoom/ActionWait cases removed (R2/R1): both actions dropped.
	}
	return in.Action
}

// captureSummary is the RenderResult string for a *CaptureResult. Mirrors
// the summary header captureResponse builds (mode + size + app + window),
// minus the element index (the full summary is in the ToolResult.Data).
func captureSummary(cap *CaptureResult) string {
	if cap == nil {
		return ""
	}
	header := fmt.Sprintf("capture mode=%s %dx%d", cap.Mode, cap.Width, cap.Height)
	if cap.App != "" {
		header += " app=" + cap.App
	}
	if cap.WindowTitle != "" {
		header += fmt.Sprintf(" window=%q", cap.WindowTitle)
	}
	return header
}
