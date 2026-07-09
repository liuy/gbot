package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// Action constants — the 15 actions routed by the Computer tool. connect and
// disconnect are the only actions that bypass the connection gate (they must
// work when disconnected); every other action flows through the Backend
// methods, which call ensureConnected themselves.
const (
	ActionConnect         = "connect"
	ActionDisconnect      = "disconnect"
	ActionScreen          = "screen"
	ActionScreenshot      = "screenshot"
	ActionClick           = "click"
	ActionClickElement    = "click_element"
	ActionOpenMenu        = "open_menu"
	ActionOpenMenuElement = "open_menu_element"
	ActionType            = "type"
	ActionSendKey         = "send_key"
	ActionScroll          = "scroll"
	ActionZoom            = "zoom"
	ActionDeviceInfo      = "device_info"
	ActionOpenApp         = "open_app"
	ActionSendFile        = "send_file"
)

// readOnlyActions are pure perception: they read device/scratch state without
// mutating it. connect/disconnect are NOT read-only — they mutate connection
// state — but they are also not destructive (see destructiveActions) so
// connect flows freely at session start.
var readOnlyActions = map[string]bool{
	ActionScreen:     true,
	ActionScreenshot: true,
	ActionDeviceInfo: true,
}

// destructiveActions mutate device state. connect/disconnect deliberately
// absent: they change OUR connection state, not the device, so they must not
// trip the destructive-confirmation gate (the model needs connect to flow at
// session start).
var destructiveActions = map[string]bool{
	ActionClick:           true,
	ActionClickElement:    true,
	ActionOpenMenu:        true,
	ActionOpenMenuElement: true,
	ActionType:            true,
	ActionSendKey:         true,
	ActionScroll:          true,
	ActionZoom:            true,
	ActionOpenApp:         true,
	ActionSendFile:        true,
}

// validScrollDirections is the exact set the GBot app's scroll command accepts.
var validScrollDirections = map[string]bool{
	"up": true, "down": true, "left": true, "right": true,
}

// New builds the Computer tool bound to the given AndroidBackend. It takes
// the concrete type (not the Backend interface) because connect/disconnect
// need the lifecycle methods the interface intentionally omits.
func New(b *AndroidBackend) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "Computer",
		Aliases_:     []string{"computer"},
		InputSchema_: func() json.RawMessage { return inputSchema() },
		Description_: func(input json.RawMessage) (string, error) {
			in, err := parseInput(input)
			if err != nil {
				return "Computer tool: control an Android device via GBot.", nil
			}
			return summarizeAction(in), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, input, b)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			in, _ := parseInput(input)
			return readOnlyActions[strings.TrimSpace(strings.ToLower(in.Action))]
		},
		IsDestructive_: func(input json.RawMessage) bool {
			in, _ := parseInput(input)
			return destructiveActions[strings.TrimSpace(strings.ToLower(in.Action))]
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return false },
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 50000,
		Prompt_:            computerPrompt(),
		RenderResult_:      renderResult,
	})
}

// execute parses input and routes to dispatch. It normalizes the action
// (lowercase + trim) and rejects an empty action before any backend call.
func execute(ctx context.Context, raw json.RawMessage, b *AndroidBackend) (*tool.ToolResult, error) {
	in, err := parseInput(raw)
	if err != nil {
		return nil, err
	}
	in.Action = strings.TrimSpace(strings.ToLower(in.Action))
	if in.Action == "" {
		return nil, errors.New("computer: action is required")
	}
	return dispatch(ctx, b, in)
}

// dispatch performs per-action validation, invokes the backend, and shapes the
// response. connect/disconnect route BEFORE the connection gate (they must
// work when disconnected). For type and send_key, declarative validation runs
// BEFORE ensureConnected so a malformed request fails fast with no wire
// traffic and no connection requirement.
func dispatch(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	switch in.Action {
	case ActionConnect:
		return doConnect(ctx, b, in)
	case ActionDisconnect:
		return doDisconnect(ctx, b, in)
	case ActionScreen:
		return doScreen(ctx, b, in)
	case ActionScreenshot:
		return doScreenshot(ctx, b, in)
	case ActionClick, ActionOpenMenu, ActionZoom:
		return doCoordinateAction(ctx, b, in)
	case ActionClickElement, ActionOpenMenuElement:
		return doRefAction(ctx, b, in)
	case ActionScroll:
		return doScroll(ctx, b, in)
	case ActionType:
		return doType(ctx, b, in)
	case ActionSendKey:
		return doSendKey(ctx, b, in)
	case ActionDeviceInfo:
		return doDeviceInfo(ctx, b, in)
	case ActionOpenApp:
		return doOpenApp(ctx, b, in)
	case ActionSendFile:
		return doSendFile(ctx, b, in)
	default:
		return nil, fmt.Errorf("computer: unknown action %q", in.Action)
	}
}

// doConnect handles the connect action. host is required; port defaults to
// DefaultWSPort when absent so the model can send just host.
func doConnect(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	if strings.TrimSpace(in.Host) == "" {
		return errorResponse("connect requires a host"), nil
	}
	port := DefaultWSPort
	if in.Port != nil {
		port = *in.Port
	}
	if err := b.Connect(ctx, in.Host, port, in.Password); err != nil {
		return errorResponse(err.Error()), nil
	}
	return okResponse(map[string]any{"action": ActionConnect, "host": in.Host, "port": port}), nil
}

// doDisconnect is idempotent — it returns ok regardless of prior state.
func doDisconnect(_ context.Context, b *AndroidBackend, _ Input) (*tool.ToolResult, error) {
	if err := b.Disconnect(); err != nil {
		return errorResponse(err.Error()), nil
	}
	return okResponse(map[string]any{"action": ActionDisconnect}), nil
}

// doScreen renders the numbered element list for the model.
func doScreen(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	maxDepth := 0
	if in.MaxDepth != nil {
		maxDepth = *in.MaxDepth
	}
	res, err := b.Screen(ctx, maxDepth)
	if err != nil {
		return notConnectedOrError(err), nil
	}
	return &tool.ToolResult{Data: res}, nil
}

// doScreenshot captures a JPEG and attaches a multimodal image block so the
// model sees the picture directly in the conversation.
func doScreenshot(ctx context.Context, b *AndroidBackend, _ Input) (*tool.ToolResult, error) {
	shot, err := b.Screenshot(ctx)
	if err != nil {
		return notConnectedOrError(err), nil
	}
	return &tool.ToolResult{
		Data: shot,
		NewMessages: []types.Message{{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock("Screenshot captured."),
				types.NewImageBlock(types.ImageSource{
					Type:      "base64",
					MediaType: shot.MIMEType,
					Data:      shot.DataB64,
				}),
			},
			Flags: types.FlagMeta,
		}},
	}, nil
}

// doCoordinateAction handles click/open_menu/zoom, which all require a
// [x, y] coordinate.
func doCoordinateAction(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	x, y, ok := parseCoordinate(in.Coordinate)
	if !ok {
		return errorResponse(fmt.Sprintf("%s requires a coordinate [x, y]", in.Action)), nil
	}
	var err error
	switch in.Action {
	case ActionClick:
		err = b.Click(ctx, x, y)
	case ActionOpenMenu:
		err = b.OpenMenu(ctx, x, y)
	case ActionZoom:
		scale := 1.0
		if in.Scale != nil {
			scale = *in.Scale
		}
		err = b.Zoom(ctx, x, y, scale)
	}
	if err != nil {
		return notConnectedOrError(err), nil
	}
	return okResponse(map[string]any{"action": in.Action, "x": x, "y": y}), nil
}

// doRefAction handles click_element/open_menu_element, which require a ref
// from the most recent screen call.
func doRefAction(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	if in.Ref == nil {
		return errorResponse(fmt.Sprintf("%s requires a ref", in.Action)), nil
	}
	var err error
	switch in.Action {
	case ActionClickElement:
		err = b.ClickElement(ctx, *in.Ref)
	case ActionOpenMenuElement:
		err = b.OpenMenuElement(ctx, *in.Ref)
	}
	if err != nil {
		return notConnectedOrError(err), nil
	}
	return okResponse(map[string]any{"action": in.Action, "ref": *in.Ref}), nil
}

// doScroll validates the direction (allowlist) before calling the backend.
func doScroll(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	dir := strings.TrimSpace(strings.ToLower(in.Direction))
	if !validScrollDirections[dir] {
		return errorResponse("scroll requires direction (up|down|left|right)"), nil
	}
	if err := b.Scroll(ctx, dir); err != nil {
		return notConnectedOrError(err), nil
	}
	return okResponse(map[string]any{"action": ActionScroll, "direction": dir}), nil
}

// doType rejects empty text BEFORE any connect check — typing nothing is
// always a model mistake — and then runs the blocked-type safety gate, also
// pre-ensureConnected, so dangerous text never reaches the wire.
func doType(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	if strings.TrimSpace(in.Text) == "" {
		return errorResponse("type requires non-empty text"), nil
	}
	if pat := isBlockedType(in.Text); pat != "" {
		return errorResponse(fmt.Sprintf("type blocked by safety pattern: %s", pat)), nil
	}
	mode := strings.TrimSpace(strings.ToLower(in.Mode))
	if mode != "" && mode != "replace" && mode != "append" {
		return errorResponse("type mode must be \"replace\" or \"append\""), nil
	}
	if err := b.Type(ctx, in.Text, mode); err != nil {
		return notConnectedOrError(err), nil
	}
	return okResponse(map[string]any{"action": ActionType}), nil
}

// doSendKey runs the allowlist check BEFORE ensureConnected so an unknown key
// fails fast with no wire traffic and no connection requirement. The backend
// does NOT re-validate — the check is single-sited here.
func doSendKey(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	if !validAndroidKey(in.Key) {
		return errorResponse(fmt.Sprintf("send_key: unknown key %q", in.Key)), nil
	}
	key := strings.ToLower(strings.TrimSpace(in.Key))
	if err := b.SendKey(ctx, key); err != nil {
		return notConnectedOrError(err), nil
	}
	return okResponse(map[string]any{"action": ActionSendKey, "key": key}), nil
}

// doDeviceInfo returns the device metadata summary.
func doDeviceInfo(ctx context.Context, b *AndroidBackend, _ Input) (*tool.ToolResult, error) {
	info, err := b.DeviceInfo(ctx)
	if err != nil {
		return notConnectedOrError(err), nil
	}
	return &tool.ToolResult{Data: info}, nil
}

// doOpenApp validates non-empty package BEFORE ensureConnected (mirrors
// doType/doSendKey's pre-connect declarative validation) so a malformed
// request fails with no wire traffic.
func doOpenApp(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	if strings.TrimSpace(in.Package) == "" {
		return errorResponse("open_app requires a package name"), nil
	}
	if err := b.OpenApp(ctx, in.Package); err != nil {
		return notConnectedOrError(err), nil
	}
	return okResponse(map[string]any{"action": ActionOpenApp, "package": in.Package}), nil
}

// doSendFile validates non-empty path BEFORE ensureConnected (mirrors
// doType/doSendKey/doOpenApp's pre-connect declarative validation) so a
// malformed request fails with no wire traffic.
func doSendFile(ctx context.Context, b *AndroidBackend, in Input) (*tool.ToolResult, error) {
	if strings.TrimSpace(in.Path) == "" {
		return errorResponse("send_file requires a path"), nil
	}
	if err := b.SendFile(ctx, in.Path); err != nil {
		return notConnectedOrError(err), nil
	}
	return okResponse(map[string]any{"action": ActionSendFile, "path": in.Path}), nil
}

// notConnectedOrError maps errNotConnected onto the single canonical
// "not connected; call connect first" tool error and propagates all other
// errors with their concrete text.
func notConnectedOrError(err error) *tool.ToolResult {
	if errors.Is(err, errNotConnected) {
		return errorResponse("not connected; call connect first")
	}
	return errorResponse(err.Error())
}

// okResponse wraps an action-result map as a successful ToolResult.
func okResponse(data map[string]any) *tool.ToolResult {
	data["ok"] = true
	return &tool.ToolResult{Data: data}
}

// errorResponse wraps an error message as a ToolResult whose data carries an
// "error" field. Returning a non-error ToolResult (rather than a Go error)
// lets the model read the message and recover.
func errorResponse(msg string) *tool.ToolResult {
	return &tool.ToolResult{Data: map[string]any{"error": msg}}
}

// summarizeAction produces a short action-aware description for the tool card.
func summarizeAction(in Input) string {
	switch strings.TrimSpace(strings.ToLower(in.Action)) {
	case ActionConnect:
		return fmt.Sprintf("connect to %s:%d", in.Host, portOr(in.Port, DefaultWSPort))
	case ActionDisconnect:
		return "disconnect from device"
	case ActionScreen:
		return "list on-screen elements (refs)"
	case ActionScreenshot:
		return "capture a screenshot"
	case ActionClick:
		return "tap coordinate"
	case ActionClickElement:
		return fmt.Sprintf("tap element ref %d", refOr(in.Ref))
	case ActionOpenMenu:
		return "long-press coordinate"
	case ActionOpenMenuElement:
		return fmt.Sprintf("long-press element ref %d", refOr(in.Ref))
	case ActionType:
		return "type text"
	case ActionSendKey:
		return fmt.Sprintf("send key %q", in.Key)
	case ActionScroll:
		return fmt.Sprintf("scroll %s", in.Direction)
	case ActionZoom:
		return "pinch zoom"
	case ActionDeviceInfo:
		return "device info"
	case ActionOpenApp:
		return fmt.Sprintf("open app %q", in.Package)
	case ActionSendFile:
		return fmt.Sprintf("send file %q", in.Path)
	default:
		return "computer action"
	}
}

// portOr returns *p when non-nil, else def.
func portOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// refOr returns *r when non-nil, else 0 (for display only).
func refOr(r *int) int {
	if r == nil {
		return 0
	}
	return *r
}

// renderResult shapes a ToolResult data value for TUI display. It handles the
// concrete result types the dispatch paths produce: *ScreenResult (numbered
// element list), *Screenshot (caption + dimensions), *DeviceInfo (one-line
// summary), and action-result maps (ok/error JSON).
func renderResult(data any) string {
	switch v := data.(type) {
	case *ScreenResult:
		return renderScreenResult(v)
	case *Screenshot:
		return fmt.Sprintf("screenshot %dx%d (%s)", v.Width, v.Height, v.MIMEType)
	case *DeviceInfo:
		return renderDeviceInfo(v)
	case map[string]any:
		if _, ok := v["MIMEType"]; ok {
			if w, _ := v["Width"].(float64); w > 0 {
				h, _ := v["Height"].(float64)
				mime, _ := v["MIMEType"].(string)
				return fmt.Sprintf("screenshot %dx%d (%s)", int(w), int(h), mime)
			}
		}
		if elements, ok := v["Elements"].([]any); ok {
			return fmt.Sprintf("screen %d elements", len(elements))
		}
		if _, ok := v["Manufacturer"]; ok {
			manu, _ := v["Manufacturer"].(string)
			model, _ := v["Model"].(string)
			return fmt.Sprintf("%s %s", manu, model)
		}
		if errMsg, ok := v["error"].(string); ok {
			return "error: " + errMsg
		}
		if action, ok := v["action"].(string); ok {
			if ok2, _ := v["ok"].(bool); ok2 {
				return action + ": ok"
			}
			return action
		}
		b, _ := json.Marshal(v)
		return string(b)
	default:
		b, _ := json.Marshal(data)
		return string(b)
	}
}
