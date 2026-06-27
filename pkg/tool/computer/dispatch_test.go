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
	calls          []string
	snapshotCalls  int
	snapshotResult *CaptureResult
	zoomResult     *CaptureResult
	clickResult    *ActionResult
	err            error
}

func (t *testBackend) list(_ context.Context) (*ActionResult, error) {
	t.calls = append(t.calls, "list")
	if t.err != nil {
		return nil, t.err
	}
	return &ActionResult{OK: true, Action: "list", Meta: map[string]any{"count": 2}}, nil
}

func (t *testBackend) snapshot(_ context.Context, in Input) (*CaptureResult, error) {
	t.calls = append(t.calls, "snapshot")
	t.snapshotCalls++
	if t.err != nil {
		return nil, t.err
	}
	if t.snapshotResult != nil {
		return t.snapshotResult, nil
	}
	mode := in.Mode
	if mode == "" {
		mode = ModeSom
	}
	return &CaptureResult{Mode: mode}, nil
}

func (t *testBackend) click(_ context.Context, _ Input) (*ActionResult, error) {
	t.calls = append(t.calls, "click")
	return t.actionResult("click"), nil
}

func (t *testBackend) typeText(_ context.Context, _ Input) (*ActionResult, error) {
	t.calls = append(t.calls, "type")
	return t.actionResult("type_text"), nil
}

func (t *testBackend) key(_ context.Context, _ Input) (*ActionResult, error) {
	t.calls = append(t.calls, "key")
	return t.actionResult("key"), nil
}

func (t *testBackend) scroll(_ context.Context, _ Input) (*ActionResult, error) {
	t.calls = append(t.calls, "scroll")
	return t.actionResult("scroll"), nil
}

func (t *testBackend) drag(_ context.Context, _ Input) (*ActionResult, error) {
	t.calls = append(t.calls, "drag")
	return t.actionResult("drag"), nil
}

func (t *testBackend) zoom(_ context.Context, _ Input) (*CaptureResult, error) {
	t.calls = append(t.calls, "zoom")
	if t.err != nil {
		return nil, t.err
	}
	if t.zoomResult != nil {
		return t.zoomResult, nil
	}
	return &CaptureResult{Mode: ModeZoom}, nil
}

func (t *testBackend) wait(_ context.Context, _ Input) (*ActionResult, error) {
	t.calls = append(t.calls, "wait")
	return &ActionResult{OK: true, Action: "wait", Message: "waited 1.00s"}, nil
}

// actionResult returns the canned result for an action.
func (t *testBackend) actionResult(action string) *ActionResult {
	if t.err != nil {
		return &ActionResult{OK: false, Action: action, Message: t.err.Error()}
	}
	if t.clickResult != nil {
		clone := *t.clickResult
		clone.Action = action
		return &clone
	}
	return &ActionResult{OK: true, Action: action}
}

// TestDispatchRoutesEachAction verifies each action routes to the matching
// backend method.
func TestDispatchRoutesEachAction(t *testing.T) {
	cases := []struct {
		action   string
		wantCall string
		extra    string // extra JSON to embed in the input
	}{
		{"list", "list", ``},
		{"snapshot", "snapshot", `,"window":42`},
		{"click", "click", `,"window":42,"element":1`},
		{"type", "type", `,"window":42,"text":"hi"`},
		{"key", "key", `,"window":42,"keys":"return"`},
		{"scroll", "scroll", `,"window":42,"direction":"down"`},
		{"drag", "drag", `,"window":42,"from_coordinate":[1,2],"to_coordinate":[3,4]`},
		{"zoom", "zoom", `,"window":42,"region":[10,20,30,40]`},
		{"wait", "wait", ``},
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
	in := Input{Action: "fly"}
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch(unknown): unexpected error: %v", err)
	}
	data, ok := res.Data.(string)
	if !ok {
		t.Fatalf("Data type = %T, want string", res.Data)
	}
	if !strings.Contains(data, `"error"`) || !strings.Contains(data, "unknown action") {
		t.Errorf("data %q missing {\"error\": ... unknown action}", data)
	}
}

// TestDispatchWindowRequired verifies every action except list/wait errors
// without a window parameter.
func TestDispatchWindowRequired(t *testing.T) {
	cases := []struct {
		action string
		extra  string
	}{
		{"snapshot", ``},
		{"click", `,"element":1`},
		{"type", `,"text":"hi"`},
		{"key", `,"keys":"return"`},
		{"scroll", `,"direction":"down"`},
		{"drag", `,"from_coordinate":[1,2],"to_coordinate":[3,4]`},
		{"zoom", `,"region":[10,20,30,40]`},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			tb := &testBackend{}
			raw := json.RawMessage(`{"action":"` + tc.action + `"` + tc.extra + `}`)
			in, err := parseInput(raw)
			if err != nil {
				t.Fatalf("parseInput: %v", err)
			}
			res, err := dispatch(context.Background(), tb, in)
			if err != nil {
				t.Fatalf("dispatch(%s): unexpected Go error: %v", tc.action, err)
			}
			if len(tb.calls) != 0 {
				t.Fatalf("dispatch(%s) without window routed to backend: %v", tc.action, tb.calls)
			}
			data, ok := res.Data.(string)
			if !ok {
				t.Fatalf("Data type = %T, want string", res.Data)
			}
			if !strings.Contains(data, `"error"`) {
				t.Errorf("data %q missing \"error\" key", data)
			}
			if !strings.Contains(data, "requires window=") {
				t.Errorf("data %q missing 'requires window=' hint", data)
			}
		})
	}
}

// TestDispatchListWaitNoWindow verifies list and wait succeed WITHOUT a window.
func TestDispatchListWaitNoWindow(t *testing.T) {
	for _, action := range []string{"list", "wait"} {
		t.Run(action, func(t *testing.T) {
			tb := &testBackend{}
			in, _ := parseInput(json.RawMessage(`{"action":"` + action + `"}`))
			res, err := dispatch(context.Background(), tb, in)
			if err != nil {
				t.Fatalf("dispatch(%s): %v", action, err)
			}
			if len(tb.calls) != 1 {
				t.Fatalf("dispatch(%s) calls = %v, want 1", action, tb.calls)
			}
			data, ok := res.Data.(string)
			if !ok {
				t.Fatalf("Data type = %T, want string", res.Data)
			}
			if strings.Contains(data, `"error"`) {
				t.Errorf("dispatch(%s) returned error: %s", action, data)
			}
		})
	}
}

// TestDispatchClickMissingTarget verifies click without element/coordinate
// errors.
func TestDispatchClickMissingTarget(t *testing.T) {
	tb := &testBackend{}
	in, _ := parseInput(json.RawMessage(`{"action":"click","window":42}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(tb.calls) != 0 {
		t.Fatalf("calls = %v, want 0 (no target)", tb.calls)
	}
	data := res.Data.(string)
	if !strings.Contains(data, "requires element= or coordinate=") {
		t.Errorf("data %q missing target hint", data)
	}
}

// TestDispatchDragMissingCoords verifies drag without both coords errors.
func TestDispatchDragMissingCoords(t *testing.T) {
	tb := &testBackend{}
	// only from_coordinate, no to_coordinate
	in, _ := parseInput(json.RawMessage(`{"action":"drag","window":42,"from_coordinate":[1,2]}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(tb.calls) != 0 {
		t.Fatalf("calls = %v, want 0 (missing to_coordinate)", tb.calls)
	}
	data := res.Data.(string)
	if !strings.Contains(data, "requires from_coordinate and to_coordinate") {
		t.Errorf("data %q missing coords hint", data)
	}
}

// TestDispatchZoomMissingRegion verifies zoom without region errors.
func TestDispatchZoomMissingRegion(t *testing.T) {
	tb := &testBackend{}
	in, _ := parseInput(json.RawMessage(`{"action":"zoom","window":42}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(tb.calls) != 0 {
		t.Fatalf("calls = %v, want 0 (no region)", tb.calls)
	}
	data := res.Data.(string)
	if !strings.Contains(data, "requires region=") {
		t.Errorf("data %q missing region hint", data)
	}
}

// TestDispatchSnapshotBadMode verifies an invalid mode is rejected.
func TestDispatchSnapshotBadMode(t *testing.T) {
	tb := &testBackend{}
	in, _ := parseInput(json.RawMessage(`{"action":"snapshot","window":42,"mode":"bogus"}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(tb.calls) != 0 {
		t.Fatalf("calls = %v, want 0 (bad mode)", tb.calls)
	}
	data := res.Data.(string)
	if !strings.Contains(data, "bad mode") {
		t.Errorf("data %q missing 'bad mode'", data)
	}
}

// TestSnapshotResponseWithImage verifies a snapshot with an image populates
// NewMessages with exactly one message containing exactly [text, image] blocks.
func TestSnapshotResponseWithImage(t *testing.T) {
	png := makeMinimalPNG(8, 8)
	tb := &testBackend{
		snapshotResult: &CaptureResult{
			Mode:   ModeSom,
			Width:  8,
			Height: 8,
			PngB64: png,
			Elements: []UIElement{
				{Index: 1, Role: "AXButton", Label: "OK"},
			},
		},
	}
	in, _ := parseInput(json.RawMessage(`{"action":"snapshot","window":42}`))
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

// TestSnapshotResponseNoImage verifies that a snapshot without an image does
// NOT populate NewMessages (the text summary lives only in Data).
func TestSnapshotResponseNoImage(t *testing.T) {
	tb := &testBackend{
		snapshotResult: &CaptureResult{
			Mode:     ModeAx,
			Elements: []UIElement{{Index: 1, Role: "AXButton"}},
		},
	}
	in, _ := parseInput(json.RawMessage(`{"action":"snapshot","window":42,"mode":"ax"}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(res.NewMessages) != 0 {
		t.Errorf("NewMessages length = %d, want 0 for ax mode (no image)", len(res.NewMessages))
	}
}

// TestSnapshotResponseImageTooSmall verifies images below 8x8 are dropped
// from NewMessages.
func TestSnapshotResponseImageTooSmall(t *testing.T) {
	png := makeMinimalPNG(4, 4) // < 8x8 threshold
	tb := &testBackend{
		snapshotResult: &CaptureResult{Mode: ModeSom, PngB64: png},
	}
	in, _ := parseInput(json.RawMessage(`{"action":"snapshot","window":42}`))
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

// TestSnapshotResponseTruncates verifies the elements array is capped at
// max_elements and a truncation notice is added.
func TestSnapshotResponseTruncates(t *testing.T) {
	elements := make([]UIElement, 50)
	for i := range elements {
		elements[i] = UIElement{Index: i + 1, Role: "AXButton", Label: "B"}
	}
	tb := &testBackend{
		snapshotResult: &CaptureResult{Mode: ModeAx, Elements: elements},
	}
	in, _ := parseInput(json.RawMessage(`{"action":"snapshot","window":42,"mode":"ax","max_elements":10}`))
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

// TestSnapshotResponseTruncationWithImage verifies the multimodal branch
// (image present, mode != ax) does NOT carry the truncation note.
func TestSnapshotResponseTruncationWithImage(t *testing.T) {
	png := makeMinimalPNG(8, 8) // valid, not-too-small image
	elements := make([]UIElement, 50)
	for i := range elements {
		elements[i] = UIElement{Index: i + 1, Role: "AXButton", Label: "B"}
	}
	tb := &testBackend{
		snapshotResult: &CaptureResult{Mode: ModeSom, PngB64: png, Elements: elements},
	}
	in, _ := parseInput(json.RawMessage(`{"action":"snapshot","window":42,"mode":"som","max_elements":3}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(res.NewMessages) != 1 {
		t.Fatalf("NewMessages length = %d, want 1 (image present)", len(res.NewMessages))
	}
	summary := res.Data.(string)
	if strings.Contains(summary, "response truncated") {
		t.Errorf("multimodal summary %q unexpectedly contains truncation note", summary)
	}
}

// TestDispatchZoomResponse verifies zoom returns an image in NewMessages with
// a "zoom region" header.
func TestDispatchZoomResponse(t *testing.T) {
	png := makeMinimalPNG(8, 8)
	tb := &testBackend{
		zoomResult: &CaptureResult{Mode: ModeZoom, Width: 8, Height: 8, PngB64: png},
	}
	in, _ := parseInput(json.RawMessage(`{"action":"zoom","window":42,"region":[10,20,30,40]}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(res.NewMessages) != 1 {
		t.Fatalf("NewMessages length = %d, want 1 (zoom image)", len(res.NewMessages))
	}
	msg := res.NewMessages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("content length = %d, want 2 [text, image]", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeText {
		t.Errorf("content[0] type = %q, want text", msg.Content[0].Type)
	}
	if msg.Content[1].Type != types.ContentTypeImage {
		t.Errorf("content[1] type = %q, want image", msg.Content[1].Type)
	}
	summary := res.Data.(string)
	if !strings.Contains(summary, "zoom region") {
		t.Errorf("summary %q missing 'zoom region' header", summary)
	}
}

// TestDispatchZoomNoImage verifies zoom without an image returns text-only.
func TestDispatchZoomNoImage(t *testing.T) {
	tb := &testBackend{
		zoomResult: &CaptureResult{Mode: ModeZoom},
	}
	in, _ := parseInput(json.RawMessage(`{"action":"zoom","window":42,"region":[10,20,30,40]}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(res.NewMessages) != 0 {
		t.Errorf("NewMessages length = %d, want 0 (no image)", len(res.NewMessages))
	}
	summary := res.Data.(string)
	if !strings.Contains(summary, "zoom region") {
		t.Errorf("summary %q missing 'zoom region' header", summary)
	}
}

// TestDispatchListResponse verifies list returns a text result with count.
func TestDispatchListResponse(t *testing.T) {
	tb := &testBackend{}
	in, _ := parseInput(json.RawMessage(`{"action":"list"}`))
	res, err := dispatch(context.Background(), tb, in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data := res.Data.(string)
	if !strings.Contains(data, `"action":"list"`) {
		t.Errorf("data %q missing action=list", data)
	}
	if !strings.Contains(data, `"count":2`) {
		t.Errorf("data %q missing count=2", data)
	}
}
