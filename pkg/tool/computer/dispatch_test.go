package computer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// testBackend is a cuaBackend stub for dispatch tests. It records every call
// and returns canned results, so dispatch routing is testable without a live
// cua-driver binary.
type testBackend struct {
	calls         []string
	captureCalls  []captureCall
	captureResult *CaptureResult
	clickResult   *ActionResult
	err           error
	// Per-action overrideable result (when non-nil, used instead of clickResult).
	perAction map[string]*ActionResult
}

type captureCall struct {
	mode        string
	app         string
	maxElements int
}

func (t *testBackend) capture(_ context.Context, mode, app string, max int) (*CaptureResult, error) {
	t.calls = append(t.calls, "capture")
	t.captureCalls = append(t.captureCalls, captureCall{mode: mode, app: app, maxElements: max})
	if t.err != nil {
		return nil, t.err
	}
	if t.captureResult != nil {
		return t.captureResult, nil
	}
	return &CaptureResult{Mode: mode, App: app}, nil
}

func (t *testBackend) click(_ context.Context, in Input) (*ActionResult, error) {
	t.calls = append(t.calls, "click:"+in.Action)
	return t.actionResult("click", in), nil
}

func (t *testBackend) drag(_ context.Context, in Input) (*ActionResult, error) {
	t.calls = append(t.calls, "drag")
	return t.actionResult("drag", in), nil
}

func (t *testBackend) scroll(_ context.Context, in Input) (*ActionResult, error) {
	t.calls = append(t.calls, "scroll")
	return t.actionResult("scroll", in), nil
}

func (t *testBackend) typeText(_ context.Context, in Input) (*ActionResult, error) {
	t.calls = append(t.calls, "type")
	return t.actionResult("type_text", in), nil
}

func (t *testBackend) key(_ context.Context, in Input) (*ActionResult, error) {
	t.calls = append(t.calls, "key")
	return t.actionResult("key", in), nil
}

func (t *testBackend) setValue(_ context.Context, in Input) (*ActionResult, error) {
	t.calls = append(t.calls, "set_value")
	return t.actionResult("set_value", in), nil
}

func (t *testBackend) wait(_ context.Context, _ Input) (*ActionResult, error) {
	t.calls = append(t.calls, "wait")
	return &ActionResult{OK: true, Action: "wait", Message: "waited 1.00s"}, nil
}

func (t *testBackend) listApps(_ context.Context) (*ActionResult, error) {
	t.calls = append(t.calls, "list_apps")
	return &ActionResult{OK: true, Action: "list_apps", Meta: map[string]any{"apps": []any{}, "count": 0}}, nil
}

func (t *testBackend) focusApp(_ context.Context, in Input) (*ActionResult, error) {
	t.calls = append(t.calls, "focus_app")
	return t.actionResult("focus_app", in), nil
}

// actionResult returns the canned result for an action, preferring perAction
// override, then clickResult, then a default OK ActionResult.
func (t *testBackend) actionResult(action string, _ Input) *ActionResult {
	if t.err != nil {
		return &ActionResult{OK: false, Action: action, Message: t.err.Error()}
	}
	if res, ok := t.perAction[action]; ok {
		return res
	}
	if t.clickResult != nil {
		clone := *t.clickResult
		clone.Action = action
		return &clone
	}
	return &ActionResult{OK: true, Action: action}
}

// TestDispatchRoutesEachAction verifies each action routes to the matching
// backend method with the right action name.
func TestDispatchRoutesEachAction(t *testing.T) {
	cases := []struct {
		action   string
		wantCall string
		extra    string // extra JSON to embed in the input
	}{
		{"capture", "capture", ``},
		{"click", "click:click", `,"element":1`},
		{"double_click", "click:double_click", `,"element":1`},
		{"right_click", "click:right_click", `,"element":1`},
		{"middle_click", "click:middle_click", `,"element":1`},
		{"drag", "drag", `,"from_element":1,"to_element":2`},
		{"scroll", "scroll", `,"direction":"down"`},
		{"type", "type", `,"text":"hi"`},
		{"key", "key", `,"keys":"return"`},
		{"set_value", "set_value", `,"element":1,"value":"x"`},
		{"wait", "wait", ``},
		{"list_apps", "list_apps", ``},
		{"focus_app", "focus_app", `,"app":"Safari"`},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			tb := &testBackend{}
			raw := json.RawMessage(`{"action":"` + tc.action + `"` + tc.extra + `}`)
			in, err := parseInput(raw)
			if err != nil {
				t.Fatalf("parseInput: %v", err)
			}
			if _, err := dispatch(context.Background(), tb, in); err != nil {
				t.Fatalf("dispatch(%s): %v", tc.action, err)
			}
			if len(tb.calls) != 1 {
				t.Fatalf("dispatch(%s) calls = %v, want exactly 1", tc.action, tb.calls)
			}
			if tb.calls[0] != tc.wantCall {
				t.Errorf("dispatch(%s) routed to %q, want %q", tc.action, tb.calls[0], tc.wantCall)
			}
		})
	}
}

// TestDispatchUnknownAction verifies unknown actions return an error.
func TestDispatchUnknownAction(t *testing.T) {
	tb := &testBackend{}
	// Build input directly (parseInput would reject it, but dispatch should
	// still guard).
	in := Input{Action: "fly"}
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch(unknown): unexpected error: %v", err)
	}
	// Unknown action returns an {"error": ...} JSON payload in Data
	// (Hermes _dispatch returns json.dumps({"error": ...})), not a Go error.
	data, ok := res.Data.(string)
	if !ok {
		t.Fatalf("Data type = %T, want string", res.Data)
	}
	if !strings.Contains(data, `"error"`) || !strings.Contains(data, "unknown action") {
		t.Errorf("data %q missing {\"error\": ... unknown action}", data)
	}
}

// TestDispatchCaptureAfter verifies capture_after triggers a follow-up
// capture call after the action.
func TestDispatchCaptureAfter(t *testing.T) {
	tb := &testBackend{
		captureResult: &CaptureResult{Mode: ModeSom, Width: 100, Height: 100},
		clickResult:   &ActionResult{OK: true, Action: "click"},
	}
	raw := json.RawMessage(`{"action":"click","element":1,"capture_after":true}`)
	in, _ := parseInput(raw)
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// click + capture.
	if len(tb.calls) != 2 {
		t.Fatalf("calls = %v, want 2 (click + capture)", tb.calls)
	}
	if tb.calls[0] != "click:click" || tb.calls[1] != "capture" {
		t.Errorf("calls = %v, want [click:click capture]", tb.calls)
	}
	// Follow-up capture populates NewMessages with [text, image] when there
	// is an image. Here there's no image, so NewMessages should be empty.
	if len(res.NewMessages) != 0 {
		t.Errorf("NewMessages length = %d, want 0 (no image)", len(res.NewMessages))
	}
}

// TestDispatchCaptureAfterFailed verifies a failed action does NOT trigger
// follow-up capture (tool.py:165-170).
func TestDispatchCaptureAfterFailed(t *testing.T) {
	tb := &testBackend{
		clickResult: &ActionResult{OK: false, Action: "click", Message: "boom"},
	}
	raw := json.RawMessage(`{"action":"click","element":1,"capture_after":true}`)
	in, _ := parseInput(raw)
	_, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Only the click call, no follow-up capture.
	if len(tb.calls) != 1 {
		t.Errorf("calls = %v, want 1 (failed action skips follow-up capture)", tb.calls)
	}
}

// TestCaptureResponseWithImage verifies a capture with an image populates
// NewMessages with exactly one message containing exactly [text, image] blocks.
func TestCaptureResponseWithImage(t *testing.T) {
	// A real 8x8 PNG so imageDimensionsFromBytes returns 8x8 — not too small.
	png := makeMinimalPNG(8, 8)
	tb := &testBackend{
		captureResult: &CaptureResult{
			Mode:   ModeSom,
			Width:  8,
			Height: 8,
			PngB64: png,
			Elements: []UIElement{
				{Index: 1, Role: "AXButton", Label: "OK"},
			},
		},
	}
	raw := json.RawMessage(`{"action":"capture"}`)
	in, _ := parseInput(raw)
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if len(res.NewMessages) != 1 {
		t.Fatalf("NewMessages length = %d, want exactly 1", len(res.NewMessages))
	}
	msg := res.NewMessages[0]
	if msg.Role != types.RoleUser {
		t.Errorf("message role = %q, want %q", msg.Role, types.RoleUser)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("message content length = %d, want exactly 2 [text, image]", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeText {
		t.Errorf("content[0] type = %q, want %q", msg.Content[0].Type, types.ContentTypeText)
	}
	if msg.Content[1].Type != types.ContentTypeImage {
		t.Errorf("content[1] type = %q, want %q", msg.Content[1].Type, types.ContentTypeImage)
	}
	if msg.Content[1].Source == nil {
		t.Fatal("content[1] image source is nil")
	}
	if msg.Content[1].Source.MediaType != "image/png" {
		t.Errorf("image media type = %q, want image/png", msg.Content[1].Source.MediaType)
	}
	if msg.Content[1].Source.Type != "base64" {
		t.Errorf("image source type = %q, want base64", msg.Content[1].Source.Type)
	}
	if msg.Content[1].Source.Data != png {
		t.Errorf("image data mismatch (got %d chars, want %d chars)", len(msg.Content[1].Source.Data), len(png))
	}

	// Data carries the summary text.
	summary, ok := res.Data.(string)
	if !ok {
		t.Fatalf("Data type = %T, want string", res.Data)
	}
	if !strings.Contains(summary, "capture mode=som 8x8") {
		t.Errorf("summary %q missing 'capture mode=som 8x8'", summary)
	}
	if !strings.Contains(summary, "1 interactable element(s):") {
		t.Errorf("summary %q missing element count line", summary)
	}
	if !strings.Contains(summary, "#1 AXButton") {
		t.Errorf("summary %q missing element index line", summary)
	}
}

// TestCaptureResponseNoImage verifies that a capture without an image does
// NOT populate NewMessages (the text summary lives only in Data).
func TestCaptureResponseNoImage(t *testing.T) {
	tb := &testBackend{
		captureResult: &CaptureResult{
			Mode:     ModeAx,
			Elements: []UIElement{{Index: 1, Role: "AXButton"}},
		},
	}
	raw := json.RawMessage(`{"action":"capture","mode":"ax"}`)
	in, _ := parseInput(raw)
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(res.NewMessages) != 0 {
		t.Errorf("NewMessages length = %d, want 0 for ax mode (no image)", len(res.NewMessages))
	}
}

// TestCaptureResponseImageTooSmall verifies images below 8x8 are dropped
// from NewMessages (tool.py:530-534).
func TestCaptureResponseImageTooSmall(t *testing.T) {
	png := makeMinimalPNG(4, 4) // < 8x8 threshold
	tb := &testBackend{
		captureResult: &CaptureResult{Mode: ModeSom, PngB64: png},
	}
	raw := json.RawMessage(`{"action":"capture"}`)
	in, _ := parseInput(raw)
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(res.NewMessages) != 0 {
		t.Errorf("NewMessages length = %d, want 0 (image too small)", len(res.NewMessages))
	}
	summary := res.Data.(string)
	if !strings.Contains(summary, "screenshot omitted: 4x4 is below") {
		t.Errorf("summary %q missing too-small notice", summary)
	}
}

// TestCaptureResponseTruncates verifies the elements array is capped at
// max_elements and a truncation notice is added (tool.py:518-522, 638-643).
func TestCaptureResponseTruncates(t *testing.T) {
	elements := make([]UIElement, 50)
	for i := range elements {
		elements[i] = UIElement{Index: i + 1, Role: "AXButton", Label: "B"}
	}
	tb := &testBackend{
		captureResult: &CaptureResult{Mode: ModeAx, Elements: elements},
	}
	raw := json.RawMessage(`{"action":"capture","mode":"ax","max_elements":10}`)
	in, _ := parseInput(raw)
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	summary := res.Data.(string)
	if !strings.Contains(summary, "50 interactable element(s):") {
		t.Errorf("summary %q missing total count", summary)
	}
	if !strings.Contains(summary, "response truncated to 10 of 50 elements") {
		t.Errorf("summary %q missing truncation notice", summary)
	}
	// Only 10 element index lines should appear (lines starting with "  #N ").
	lines := strings.Split(summary, "\n")
	elementLines := 0
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			elementLines++
		}
	}
	if elementLines != 10 {
		t.Errorf("element index lines = %d, want 10 (truncated)", elementLines)
	}
}

// TestCaptureResponseTruncationWithImage verifies the multimodal branch
// (image present, mode != ax) does NOT carry the "response truncated to N of M
// elements" note. Hermes (_capture_response, tool.py:624-628) skips it there
// because the image carries the screenshot, not the AX elements array — a
// truncation note would be inaccurate.
func TestCaptureResponseTruncationWithImage(t *testing.T) {
	png := makeMinimalPNG(8, 8) // valid, not-too-small image
	elements := make([]UIElement, 50)
	for i := range elements {
		elements[i] = UIElement{Index: i + 1, Role: "AXButton", Label: "B"}
	}
	tb := &testBackend{
		captureResult: &CaptureResult{Mode: ModeSom, PngB64: png, Elements: elements},
	}
	raw := json.RawMessage(`{"action":"capture","mode":"som","max_elements":3}`)
	in, _ := parseInput(raw)
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Multimodal branch must populate NewMessages with the image.
	if len(res.NewMessages) != 1 {
		t.Fatalf("NewMessages length = %d, want 1 (image present)", len(res.NewMessages))
	}
	summary := res.Data.(string)
	if strings.Contains(summary, "response truncated") {
		t.Errorf("multimodal summary %q unexpectedly contains truncation note", summary)
	}
}

// TestResolveTargetWindowX11UniformZIndex reproduces the X11/xrdp bug where
// every window reports z_index=0. Without the active-window fallback, Desktop
// would be picked (it's first in list_windows output). With the X11
// _NET_ACTIVE_WINDOW fallback, the real active app window is selected.
func TestResolveTargetWindowX11UniformZIndex(t *testing.T) {
	// Mirrors what list_windows returns on this xrdp box: all z_index=0,
	// Desktop first, Terminal in the middle, panels at the end.
	// WindowID 2 (Terminal) is what _NET_ACTIVE_WINDOW reports.
	windows := []windowInfo{
		{AppName: "desktop", PID: 100, WindowID: 1, Title: "Desktop", ZIndex: 0},
		{AppName: "xfce4-terminal", PID: 200, WindowID: 2, Title: "Terminal - yliu@host", ZIndex: 0},
		{AppName: "xfce4-panel", PID: 300, WindowID: 3, Title: "xfce4-panel", ZIndex: 0},
	}
	got, err := resolveTargetWindow(windows, "", 2)
	if err != nil {
		t.Fatalf("resolveTargetWindow: %v", err)
	}
	if got.WindowID != 2 {
		t.Errorf("picked WindowID=%d (%q), want 2 (Terminal); uniform z must use activeWinID",
			got.WindowID, got.Title)
	}
}

// TestResolveTargetWindowUniformZNoActive verifies that when z_index is
// uniform AND no active window is known (activeWinID=0), the first visible
// window is returned (same as Hermes' fallback behavior).
func TestResolveTargetWindowUniformZNoActive(t *testing.T) {
	windows := []windowInfo{
		{AppName: "desktop", PID: 100, WindowID: 1, Title: "Desktop", ZIndex: 0},
		{AppName: "terminal", PID: 200, WindowID: 2, Title: "Terminal", ZIndex: 0},
	}
	got, err := resolveTargetWindow(windows, "", 0)
	if err != nil {
		t.Fatalf("resolveTargetWindow: %v", err)
	}
	if got.WindowID != 1 {
		t.Errorf("picked WindowID=%d, want 1 (first visible when no active window)", got.WindowID)
	}
}

// TestResolveTargetWindowDistinctZUsesZOrder verifies that when z_index
// values differ (macOS/Windows), the uniform-z fallback is skipped and the
// first visible window in the (already z-sorted) list wins.
func TestResolveTargetWindowDistinctZUsesZOrder(t *testing.T) {
	// Already sorted by z_index ascending (done in capture before calling
	// resolveTargetWindow): Terminal (z=1) is frontmost.
	windows := []windowInfo{
		{AppName: "terminal", PID: 200, WindowID: 2, Title: "Terminal", ZIndex: 1},
		{AppName: "editor", PID: 300, WindowID: 3, Title: "editor", ZIndex: 3},
		{AppName: "desktop", PID: 100, WindowID: 1, Title: "Desktop", ZIndex: 5},
	}
	got, err := resolveTargetWindow(windows, "", 0)
	if err != nil {
		t.Fatalf("resolveTargetWindow: %v", err)
	}
	if got.WindowID != 2 {
		t.Errorf("picked WindowID=%d, want 2 (first after z-sort, distinct z)", got.WindowID)
	}
}

// TestResolveTargetWindowActiveNotInList verifies that when the X11 active
// window id doesn't match any window in list_windows (e.g. the WM reports a
// window cua-driver filtered out), we fall through to first-visible.
func TestResolveTargetWindowActiveNotInList(t *testing.T) {
	windows := []windowInfo{
		{AppName: "desktop", PID: 100, WindowID: 1, Title: "Desktop", ZIndex: 0},
		{AppName: "terminal", PID: 200, WindowID: 2, Title: "Terminal", ZIndex: 0},
	}
	// activeWinID=999 doesn't match any window — must not error, picks first.
	got, err := resolveTargetWindow(windows, "", 999)
	if err != nil {
		t.Fatalf("resolveTargetWindow: %v", err)
	}
	if got.WindowID != 1 {
		t.Errorf("picked WindowID=%d, want 1 (active not in list, first visible)", got.WindowID)
	}
}
