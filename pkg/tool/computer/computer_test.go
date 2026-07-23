package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// newTestTool builds a Computer tool over a backend with an injectable fake
// dialer, plus the recorded dialer so tests can inspect connect args.
func newTestTool() (tool.Tool, *AndroidBackend, *dialRecorder) {
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	return New(b), b, rec
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestComputer_NameAndAliases(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	if tt.Name() != "Computer" {
		t.Errorf("Name = %q, want Computer", tt.Name())
	}
	aliases := tt.Aliases()
	if len(aliases) != 1 || aliases[0] != "computer" {
		t.Errorf("Aliases = %v, want [computer]", aliases)
	}
}

func TestComputer_PromptNonEmpty(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	if tt.Prompt() == "" {
		t.Error("Prompt is empty")
	}
	if !contains(tt.Prompt(), "connect") {
		t.Errorf("Prompt missing 'connect' keyword")
	}
}

func TestComputer_InputSchema(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	schema := tt.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema returned empty")
	}
	if !contains(string(schema), "device_info") {
		t.Errorf("InputSchema missing 'device_info' action")
	}
}

func TestComputer_IsReadOnly_PerAction(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	readOnly := []string{"screen", "screenshot", "device_info"}
	for _, a := range readOnly {
		input := mustMarshal(t, map[string]any{"action": a})
		if !tt.IsReadOnly(input) {
			t.Errorf("IsReadOnly(%s) = false, want true", a)
		}
	}
	notReadOnly := []string{"click", "type", "scroll", "connect", "disconnect"}
	for _, a := range notReadOnly {
		input := mustMarshal(t, map[string]any{"action": a})
		if tt.IsReadOnly(input) {
			t.Errorf("IsReadOnly(%s) = true, want false", a)
		}
	}
}

func TestComputer_IsDestructive_PerAction(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	destructive := []string{
		"click", "click_element", "open_menu", "open_menu_element",
		"type", "send_key", "scroll", "zoom", "open_app", "send_file",
	}
	for _, a := range destructive {
		input := mustMarshal(t, map[string]any{"action": a})
		if !tt.IsDestructive(input) {
			t.Errorf("IsDestructive(%s) = false, want true", a)
		}
	}
	// connect and disconnect must NOT be destructive — connect flows at session
	// start and must not trip the destructive-confirmation gate.
	nonDestructive := []string{"connect", "disconnect", "screen", "screenshot", "device_info"}
	for _, a := range nonDestructive {
		input := mustMarshal(t, map[string]any{"action": a})
		if tt.IsDestructive(input) {
			t.Errorf("IsDestructive(%s) = true, want false", a)
		}
	}
}

func TestComputer_IsConcurrencySafe_False(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	input := mustMarshal(t, map[string]any{"action": "screen"})
	if tt.IsConcurrencySafe(input) {
		t.Error("IsConcurrencySafe = true, want false (drives real device)")
	}
}

func TestComputer_InterruptBehavior(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Error("InterruptBehavior != InterruptCancel")
	}
}

func TestComputer_Description_Static(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	desc, err := tt.Description(mustMarshal(t, map[string]any{}))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc == "" {
		t.Error("Description empty for empty input")
	}
}

func TestComputer_Description_PerAction(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	desc, err := tt.Description(mustMarshal(t, map[string]any{"action": "screen"}))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if !contains(desc, "elements") {
		t.Errorf("Description(screen) = %q, want mention of 'elements'", desc)
	}
}

func TestComputer_Execute_MissingAction(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	_, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"host": "1.2.3.4"}), &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("Call returned nil for missing action, want error")
	}
}

func TestComputer_Execute_UnknownAction(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	_, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "frobnicate"}), &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("Call returned nil for unknown action, want error")
	}
}

func TestComputer_Execute_ConnectMissingHost(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "connect"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for missing host", er.Error)
	}
	if !contains(er.Error, "host") {
		t.Errorf("error = %q, want mention of 'host'", er.Error)
	}
}

func TestComputer_Execute_ConnectDefaultsPort8765(t *testing.T) {
	t.Parallel()
	tt, b, rec := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "connect",
		"host":   "1.2.3.4",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !b.IsConnected() {
		t.Error("not connected after connect")
	}
	lc := rec.lastConnect()
	if lc.host != "1.2.3.4" {
		t.Errorf("connect host = %q, want 1.2.3.4", lc.host)
	}
	if lc.port != 8765 {
		t.Errorf("connect port = %d, want 8765 (default)", lc.port)
	}
	cr, ok := res.Data.(*ConnectResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ConnectResult", res.Data)
	}
	if cr.Host != "1.2.3.4" {
		t.Errorf("result host = %q, want 1.2.3.4", cr.Host)
	}
	if cr.Port != 8765 {
		t.Errorf("result port = %d, want 8765", cr.Port)
	}
}

func TestComputer_Execute_ConnectExplicitPort(t *testing.T) {
	t.Parallel()
	tt, _, rec := newTestTool()
	_, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "connect",
		"host":   "1.2.3.4",
		"port":   9999,
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	lc := rec.lastConnect()
	if lc.port != 9999 {
		t.Errorf("connect port = %d, want 9999", lc.port)
	}
}

func TestComputer_Execute_Disconnect(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	// Connect first so disconnect has something to close.
	_, _ = tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "connect", "host": "h",
	}), &tool.ToolUseContext{})
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "disconnect"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if b.IsConnected() {
		t.Error("still connected after disconnect")
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "disconnect" {
		t.Errorf("action = %q, want disconnect", ar.Action)
	}
	if !ar.OK {
		t.Error("ok = false, want true")
	}
}

func TestComputer_Execute_DisconnectWhenNeverConnected(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "disconnect"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if !ar.OK {
		t.Error("ok = false, want true (idempotent disconnect)")
	}
}

func TestComputer_Execute_ScreenBeforeConnect(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "screen"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "not connected; call connect first") {
		t.Errorf("error = %q, want 'not connected; call connect first'", er.Error)
	}
}

func TestComputer_Execute_ScreenRenders(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":{
			"className":"root","children":[
				{"className":"android.widget.Button","isClickable":true,"text":"OK","bounds":{"left":0,"top":0,"right":10,"bottom":20}}
			]
		}}`),
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screen"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	sr, ok := res.Data.(*ScreenResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ScreenResult", res.Data)
	}
	if len(sr.Elements) != 1 {
		t.Fatalf("elements = %d, want 1", len(sr.Elements))
	}
	// renderResult should produce the numbered list.
	rendered := tt.RenderResult(res.Data)
	if !contains(rendered, "#1 Button") {
		t.Errorf("RenderResult = %q, want #1 Button", rendered)
	}
}

func TestComputer_FormatWireBlocks_Screenshot(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	// Use a real base64-encoded JPEG so the resize pipeline in doScreenshot
	// can decode it. A 4x4 solid-color JPEG is well under every cap, so the
	// resizer returns it byte-identical and MediaType stays image/jpeg.
	smallImg := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			smallImg.SetRGBA(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, smallImg, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(jbuf.Bytes())
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"screenshot": json.RawMessage(`{"image":"` + b64 + `","format":"jpeg","width":4,"height":4}`),
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screenshot"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.NewMessages) != 0 {
		t.Fatalf("NewMessages len = %d, want 0 (image moved to FormatWireBlocks)", len(res.NewMessages))
	}

	wb, ok := tt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Computer tool should implement ToolWithWireBlocks")
	}
	blocks := wb.FormatWireBlocks(res.Data)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Errorf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	if blocks[0].Text != "Screenshot captured (4x4)." {
		t.Errorf("blocks[0].Text = %q, want %q", blocks[0].Text, "Screenshot captured (4x4).")
	}
	if blocks[1].Type != types.ContentTypeImage {
		t.Errorf("blocks[1].Type = %q, want %q", blocks[1].Type, types.ContentTypeImage)
	}
	if blocks[1].Source == nil {
		t.Fatal("blocks[1].Source = nil")
	}
	if blocks[1].Source.MediaType != "image/jpeg" {
		t.Errorf("blocks[1].Source.MediaType = %q, want image/jpeg", blocks[1].Source.MediaType)
	}
	decoded, err := base64.StdEncoding.DecodeString(blocks[1].Source.Data)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if cfg.Width != 4 || cfg.Height != 4 {
		t.Errorf("decoded dims = %dx%d, want 4x4", cfg.Width, cfg.Height)
	}
}

func TestComputer_DecodeResult_ScreenshotArrayForm(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	// Build the wire format inline (cannot use pkg/engine's marshalBlocks —
	// it is unexported and pkg/engine imports pkg/tool, so importing it
	// back from package computer would form a cycle).
	raw, err := json.Marshal([]types.ContentBlock{
		types.NewTextBlock("Screenshot captured (1080x2400)."),
		types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "abc"}),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	shot, ok := v.(*Screenshot)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *Screenshot", v)
	}
	if shot.Width != 1080 || shot.Height != 2400 {
		t.Errorf("dims = %dx%d, want 1080x2400", shot.Width, shot.Height)
	}
	if shot.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want %q", shot.MIMEType, "image/jpeg")
	}
	if shot.DataB64 != "abc" {
		t.Errorf("DataB64 = %q, want %q", shot.DataB64, "abc")
	}
}

func TestComputer_DecodeResult_NoDimsGraceful(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	// Text block does not match the dimension regex — dims stay 0/0 but
	// MIMEType and DataB64 are still populated from the image block.
	raw, err := json.Marshal([]types.ContentBlock{
		types.NewTextBlock("no dimensions here"),
		types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "xyz"}),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	shot, ok := v.(*Screenshot)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *Screenshot", v)
	}
	if shot.Width != 0 || shot.Height != 0 {
		t.Errorf("dims = %dx%d, want 0x0 (regex didn't match)", shot.Width, shot.Height)
	}
	if shot.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want %q", shot.MIMEType, "image/jpeg")
	}
	if shot.DataB64 != "xyz" {
		t.Errorf("DataB64 = %q, want %q", shot.DataB64, "xyz")
	}
}

func TestComputer_Screenshot_DataB64_IsResized(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})

	// Oversized JPEG: 2500x2500 forces the resizer to shrink it below the
	// 2000x2000 threshold. The returned Screenshot.DataB64 must carry the
	// RESIZED bytes (not the original), proving doScreenshot stores resized
	// data back into the Screenshot struct. Kept small to avoid bloating
	// `make check` runtime.
	bigImg := image.NewRGBA(image.Rect(0, 0, 2500, 2500))
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, bigImg, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	origBytes := jbuf.Bytes()
	b64 := base64.StdEncoding.EncodeToString(origBytes)
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"screenshot": json.RawMessage(`{"image":"` + b64 + `","format":"jpeg","width":2500,"height":2500}`),
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screenshot"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	shot, ok := res.Data.(*Screenshot)
	if !ok {
		t.Fatalf("res.Data type = %T, want *Screenshot", res.Data)
	}
	resizedBytes, err := base64.StdEncoding.DecodeString(shot.DataB64)
	if err != nil {
		t.Fatalf("decode DataB64: %v", err)
	}
	if len(resizedBytes) >= len(origBytes) {
		t.Fatalf("resized bytes len = %d, original = %d; expected resized to be strictly smaller", len(resizedBytes), len(origBytes))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(resizedBytes))
	if err != nil {
		t.Fatalf("decode resized: %v", err)
	}
	if cfg.Width >= 8000 || cfg.Height >= 8000 {
		t.Errorf("resized dims = %dx%d, want both strictly < 8000", cfg.Width, cfg.Height)
	}
}

func TestComputer_Execute_TypeEmptyText(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	fc := b.client.(*fakeCaller)
	callsBefore := fc.callCount()
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "type", "text": ""}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "non-empty text") {
		t.Errorf("error = %q, want 'non-empty text'", er.Error)
	}
	if fc.callCount() != callsBefore {
		t.Errorf("wire calls changed from %d to %d (empty type must not reach wire)", callsBefore, fc.callCount())
	}
}

func TestComputer_Execute_TypeBlockedText(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	fc := b.client.(*fakeCaller)
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "type",
		"text":   "sudo rm -rf /",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "blocked") {
		t.Errorf("error = %q, want 'blocked'", er.Error)
	}
	// set_text must not have been issued.
	last := fc.lastCall()
	if last.Command == "set_text" {
		t.Error("set_text was issued for blocked text, want rejection before wire")
	}
}

func TestComputer_Execute_TypeSuccess(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	fc := b.client.(*fakeCaller)
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "type", "text": "hello",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "set_text" {
		t.Errorf("command = %q, want set_text", last.Command)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "type" {
		t.Errorf("action = %q, want type", ar.Action)
	}
}

func TestComputer_Execute_SendKeyUnknown(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	fc := b.client.(*fakeCaller)
	callsBefore := fc.callCount()
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "send_key", "key": "escape",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "unknown key") {
		t.Errorf("error = %q, want 'unknown key'", er.Error)
	}
	if fc.callCount() != callsBefore {
		t.Errorf("wire calls changed from %d to %d (unknown key must not reach wire)", callsBefore, fc.callCount())
	}
}

func TestComputer_Execute_SendKeyValid(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	fc := b.client.(*fakeCaller)
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "send_key", "key": "BACK",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "press_key" {
		t.Errorf("command = %q, want press_key", last.Command)
	}
	if last.Params["key"] != "back" {
		t.Errorf("key param = %v, want back (lowercased)", last.Params["key"])
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "send_key" {
		t.Errorf("action = %q, want send_key", ar.Action)
	}
	if !ar.OK {
		t.Error("ok = false, want true")
	}
}

func TestComputer_Execute_ClickRequiresCoordinate(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "click"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "coordinate") {
		t.Errorf("error = %q, want mention of 'coordinate'", er.Error)
	}
}

func TestComputer_Execute_ClickElementRequiresRef(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "click_element"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "ref") {
		t.Errorf("error = %q, want mention of 'ref'", er.Error)
	}
}

func TestComputer_Execute_ScrollBadDirection(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "scroll", "direction": "diagonal",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "direction") {
		t.Errorf("error = %q, want mention of 'direction'", er.Error)
	}
}

func TestComputer_Execute_DeviceInfo(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"get_device_info": json.RawMessage(`{"manufacturer":"Google","model":"Pixel 8","sdk":34,"release":"14","screenWidth":1080,"screenHeight":2400,"density":2.625,"densityDpi":420}`),
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "device_info"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	info, ok := res.Data.(*DeviceInfo)
	if !ok {
		t.Fatalf("Data type = %T, want *DeviceInfo", res.Data)
	}
	if info.Model != "Pixel 8" {
		t.Errorf("Model = %q, want Pixel 8", info.Model)
	}
	rendered := tt.RenderResult(res.Data)
	if !contains(rendered, "Pixel 8") {
		t.Errorf("RenderResult = %q, want 'Pixel 8'", rendered)
	}
}

func TestComputer_Execute_ClickWithCoordinate(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	fc := b.client.(*fakeCaller)
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "click", "coordinate": []int{100, 200},
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "tap" {
		t.Errorf("command = %q, want tap", last.Command)
	}
	if x := numAsInt(last.Params["x"]); x != 100 {
		t.Errorf("tap x = %v, want 100", last.Params["x"])
	}
	if y := numAsInt(last.Params["y"]); y != 200 {
		t.Errorf("tap y = %v, want 200", last.Params["y"])
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if !ar.OK {
		t.Error("ok = false, want true")
	}
}

func TestComputer_RenderResult_ErrorResult(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	got := tt.RenderResult(&ErrorResult{Error: "bad thing"})
	if !strings.HasPrefix(got, "error:") {
		t.Errorf("RenderResult = %q, want 'error:' prefix", got)
	}
	if !contains(got, "bad thing") {
		t.Errorf("RenderResult = %q, want 'bad thing'", got)
	}
}

func TestComputer_RenderResult_OkAction(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	got := tt.RenderResult(&ActionResult{Action: "click", OK: true})
	if got != "click: ok" {
		t.Errorf("RenderResult = %q, want 'click: ok'", got)
	}
}

func TestComputer_RenderResult_ScreenshotType(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	shot := &Screenshot{MIMEType: "image/jpeg", Width: 1080, Height: 2400}
	got := tt.RenderResult(shot)
	if !contains(got, "1080x2400") {
		t.Errorf("RenderResult = %q, want '1080x2400'", got)
	}
	if !contains(got, "image/jpeg") {
		t.Errorf("RenderResult = %q, want 'image/jpeg'", got)
	}
}

func TestComputer_Execute_InvalidJSON(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	_, err := tt.Call(context.Background(), json.RawMessage(`{bad`), &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("Call returned nil for invalid JSON, want error")
	}
}

func TestComputer_SummarizeAction_AllBranches(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	cases := []struct {
		action string
		want   string
	}{
		{"connect", "connect to"},
		{"disconnect", "disconnect from device"},
		{"screen", "elements"},
		{"screenshot", "screenshot"},
		{"click", "tap coordinate"},
		{"click_element", "tap element ref"},
		{"open_menu", "long-press coordinate"},
		{"open_menu_element", "long-press element ref"},
		{"type", "type text"},
		{"send_key", "send key"},
		{"scroll", "scroll"},
		{"zoom", "pinch zoom"},
		{"device_info", "device info"},
		{"open_app", "open app"},
		{"unknown", "computer action"},
	}
	for _, c := range cases {
		desc, err := tt.Description(mustMarshal(t, map[string]any{"action": c.action}))
		if err != nil {
			t.Fatalf("Description(%s): %v", c.action, err)
		}
		if !contains(desc, c.want) {
			t.Errorf("Description(%s) = %q, want it to contain %q", c.action, desc, c.want)
		}
	}
}

func TestComputer_Description_InvalidJSON(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// Invalid JSON must fall back to the static description, not error.
	desc, err := tt.Description(json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatalf("Description invalid JSON should not error: %v", err)
	}
	if desc == "" {
		t.Error("Description invalid JSON returned empty")
	}
}

func TestComputer_Execute_ClickElementSuccess(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":{
			"className":"root","children":[
				{"className":"android.widget.Button","isClickable":true,"bounds":{"left":0,"top":0,"right":100,"bottom":100}}
			]
		}}`),
	}
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screen"}), &tool.ToolUseContext{})
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "click_element", "ref": 1,
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "click_element" {
		t.Errorf("action = %q, want click_element", ar.Action)
	}
	if !ar.OK {
		t.Error("ok = false, want true")
	}
}

func TestComputer_Execute_ScrollSuccess(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "scroll", "direction": "down",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "scroll" {
		t.Errorf("action = %q, want scroll", ar.Action)
	}
	// Confirm the scroll command reached the wire with direction down.
	last := b.client.(*fakeCaller).lastCall()
	if last.Command != "scroll" {
		t.Errorf("wire command = %q, want scroll", last.Command)
	}
	if last.Params["direction"] != "down" {
		t.Errorf("wire direction = %v, want down", last.Params["direction"])
	}
}

func TestComputer_Execute_ZoomSuccess(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "zoom", "coordinate": []int{540, 1200}, "scale": 2.0,
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "zoom" {
		t.Errorf("action = %q, want zoom", ar.Action)
	}
}

func TestComputer_Execute_OpenMenuSuccess(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "open_menu", "coordinate": []int{10, 20},
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "open_menu" {
		t.Errorf("action = %q, want open_menu", ar.Action)
	}
}

func TestComputer_Execute_ConnectBackendError(t *testing.T) {
	t.Parallel()
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	tt := New(b)
	rec.failNext = context.DeadlineExceeded
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "connect", "host": "h",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for dial failure", er.Error)
	}
}

func TestComputer_PortOr(t *testing.T) {
	t.Parallel()
	p := 9000
	if got := portOr(&p, 8765); got != 9000 {
		t.Errorf("portOr(&9000) = %d, want 9000", got)
	}
	if got := portOr(nil, 8765); got != 8765 {
		t.Errorf("portOr(nil) = %d, want 8765", got)
	}
}

func TestComputer_RefOr(t *testing.T) {
	t.Parallel()
	r := 5
	if got := refOr(&r); got != 5 {
		t.Errorf("refOr(&5) = %d, want 5", got)
	}
	if got := refOr(nil); got != 0 {
		t.Errorf("refOr(nil) = %d, want 0", got)
	}
}

func TestComputer_RenderResult_NilSafe(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// nil data must not panic — falls to the default JSON branch.
	got := tt.RenderResult(nil)
	if got != "null" {
		t.Errorf("RenderResult(nil) = %q, want 'null'", got)
	}
}

func TestComputer_RenderResult_UnknownType(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// A non-handled type falls to the default JSON marshal branch.
	got := tt.RenderResult(42)
	if got != "42" {
		t.Errorf("RenderResult(42) = %q, want '42'", got)
	}
}

func TestComputer_RenderResult_JSONRawMessage_Screenshot(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// Array form: text block with dims + image block.
	raw := json.RawMessage(`[{"type":"text","text":"Screenshot captured (1256x2760)."},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"abc"}}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	got := tt.RenderResult(v)
	want := "screenshot 1256x2760 (image/jpeg)"
	if got != want {
		t.Errorf("RenderResult(decoded screenshot) = %q, want %q", got, want)
	}
}

func TestComputer_RenderResult_JSONRawMessage_Action(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// Array form: text block with double-wrapped JSON content.
	innerJSON, _ := json.Marshal(map[string]any{"action": "click", "ok": true})
	doubleWrapped, _ := json.Marshal(string(innerJSON))
	raw := json.RawMessage(`[{"type":"text","text":` + string(doubleWrapped) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	got := tt.RenderResult(v)
	if got != "click: ok" {
		t.Errorf("RenderResult(decoded action) = %q, want 'click: ok'", got)
	}
}

func TestComputer_Execute_DisconnectBackendError(t *testing.T) {
	t.Parallel()
	// Disconnect never errors in the current implementation, but the
	// doDisconnect error branch must still be exercised by a test that
	// disconnects successfully — this confirms the ok path.
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "disconnect"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if !ar.OK {
		t.Error("ok = false, want true")
	}
}

func TestComputer_Execute_ScreenBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"get_ui_tree": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screen"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_ScreenshotBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"screenshot": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screenshot"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_DeviceInfoBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"get_device_info": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "device_info"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_ClickElementBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":{"className":"root","children":[{"className":"B","isClickable":true,"bounds":{"left":0,"top":0,"right":10,"bottom":10}}]}}`),
	}
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screen"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"tap": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "click_element", "ref": 1}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_ScrollBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"scroll": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "scroll", "direction": "up"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_TypeBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"set_text": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "type", "text": "hi"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_SendKeyBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"press_key": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "send_key", "key": "home"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_ClickBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"tap": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "click", "coordinate": []int{1, 2}}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_OpenAppSuccess(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "open_app", "package": "com.android.chrome",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "open_app" {
		t.Errorf("action = %q, want open_app", ar.Action)
	}
	last := b.client.(*fakeCaller).lastCall()
	if last.Command != "open_app" {
		t.Errorf("wire command = %q, want open_app", last.Command)
	}
	if last.Params["package"] != "com.android.chrome" {
		t.Errorf("wire package = %v, want com.android.chrome", last.Params["package"])
	}
}

func TestComputer_Execute_OpenAppEmptyPackage(t *testing.T) {
	t.Parallel()
	tt, _, rec := newTestTool()
	// Validation runs pre-connect, so no connect is needed and no wire
	// traffic should occur.
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "open_app",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "package") {
		t.Errorf("error = %q, want mention of 'package'", er.Error)
	}
	if rec.clientCount() != 0 {
		t.Errorf("clients dialed = %d, want 0 (validation must run pre-connect)", rec.clientCount())
	}
}

func TestComputer_Execute_OpenAppBackendError(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).errs = map[string]error{
		"open_app": context.DeadlineExceeded,
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "open_app", "package": "com.android.chrome",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if er.Error == "" {
		t.Errorf("Error = %q, want non-empty for backend failure", er.Error)
	}
}

func TestComputer_Execute_OpenAppNotConnected(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// A valid package but no connect: ensureConnected fires and surfaces the
	// canonical not-connected error.
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "open_app", "package": "com.android.chrome",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "connect first") {
		t.Errorf("error = %q, want 'connect first'", er.Error)
	}
}

func TestComputer_Execute_SendFileSuccess(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	fc := b.client.(*fakeCaller)
	tmpPath := writeSendFileTemp(t, 512)
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{
		"action": "send_file", "path": tmpPath,
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ar, ok := res.Data.(*ActionResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ActionResult", res.Data)
	}
	if ar.Action != "send_file" {
		t.Errorf("action = %q, want send_file", ar.Action)
	}
	if !ar.OK {
		t.Error("ok = false, want true")
	}
	// The first wire call must be receive_file_begin with the basename param.
	if got := len(fc.calls); got != 2 {
		t.Fatalf("wire calls = %d, want 2 (begin + end)", got)
	}
	if fc.calls[0].Command != "receive_file_begin" {
		t.Errorf("calls[0] = %q, want receive_file_begin", fc.calls[0].Command)
	}
	if fc.calls[0].Params["path"] != filepath.Base(tmpPath) {
		t.Errorf("begin path = %v, want %q", fc.calls[0].Params["path"], filepath.Base(tmpPath))
	}
}

func TestComputer_Execute_SendFileEmptyPath(t *testing.T) {
	t.Parallel()
	tt, _, rec := newTestTool()
	// Validation runs pre-connect: no connect needed, no wire traffic.
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "send_file",
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "path") {
		t.Errorf("error = %q, want mention of 'path'", er.Error)
	}
	if rec.clientCount() != 0 {
		t.Errorf("clients dialed = %d, want 0 (validation must run pre-connect)", rec.clientCount())
	}
}

func TestComputer_Execute_SendFileNotConnected(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// A valid path but no connect: ensureConnected surfaces the canonical
	// not-connected error. Use a real temp path so the validation gate passes
	// and we reach ensureConnected.
	tmpPath := writeSendFileTemp(t, 64)
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{
		"action": "send_file", "path": tmpPath,
	}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	er, ok := res.Data.(*ErrorResult)
	if !ok {
		t.Fatalf("Data type = %T, want *ErrorResult", res.Data)
	}
	if !contains(er.Error, "connect first") {
		t.Errorf("error = %q, want 'connect first'", er.Error)
	}
}

// TestComputer_SummarizeAction_SendFilePath is a dedicated sub-test because the
// shared SummarizeAction table marshals only {action:...}, but the send_file
// branch reads in.Path. Asserting both "send file" and "x.apk" proves the path
// reached the branch (it would fail if the branch dropped Path or returned a
// static literal).
func TestComputer_SummarizeAction_SendFilePath(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	desc, err := tt.Description(mustMarshal(t, map[string]any{
		"action": "send_file", "path": "x.apk",
	}))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if !contains(desc, "send file") {
		t.Errorf("Description = %q, want it to contain 'send file'", desc)
	}
	if !contains(desc, "x.apk") {
		t.Errorf("Description = %q, want it to contain 'x.apk' (in.Path)", desc)
	}
}

func TestComputer_DecodeResult_ScreenshotRoundTrip(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	original := &Screenshot{MIMEType: "image/jpeg", Width: 100, Height: 200, DataB64: "abc"}
	// Array form: text block + image block (FormatWireBlocks output shape).
	raw, err := json.Marshal([]types.ContentBlock{
		types.NewTextBlock("Screenshot captured (100x200)."),
		types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "abc"}),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*Screenshot)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *Screenshot", v)
	}
	if got.MIMEType != "image/jpeg" || got.Width != 100 || got.Height != 200 || got.DataB64 != "abc" {
		t.Errorf("DecodeResult round-trip lost fields: %+v", got)
	}
	renderedStream := tt.RenderResult(original)
	renderedHistory := tt.RenderResult(v)
	if renderedStream != renderedHistory {
		t.Errorf("round-trip mismatch: stream=%q history=%q", renderedStream, renderedHistory)
	}
}

func TestComputer_DecodeResult_ScreenResultRoundTrip(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	// Ref has json:"-" so it is excluded from persisted JSON by design.
	original := &ScreenResult{Width: 1080, Height: 2400, Elements: []ElementRef{{Text: "button"}}}
	// Wrap as array form: text block containing JSON-string-wrapped struct.
	innerJSON, _ := json.Marshal(original)
	doubleWrapped, _ := json.Marshal(string(innerJSON))
	raw := json.RawMessage(`[{"type":"text","text":` + string(doubleWrapped) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*ScreenResult)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *ScreenResult", v)
	}
	if got.Width != 1080 || got.Height != 2400 || len(got.Elements) != 1 || got.Elements[0].Text != "button" {
		t.Errorf("DecodeResult round-trip lost fields: %+v", got)
	}
	renderedStream := tt.RenderResult(original)
	renderedHistory := tt.RenderResult(v)
	if renderedStream != renderedHistory {
		t.Errorf("round-trip mismatch: stream=%q history=%q", renderedStream, renderedHistory)
	}
}

func TestComputer_DecodeResult_DeviceInfoRoundTrip(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	original := &DeviceInfo{Manufacturer: "Google", Model: "Pixel 8", SDK: 34}
	innerJSON, _ := json.Marshal(original)
	doubleWrapped, _ := json.Marshal(string(innerJSON))
	raw := json.RawMessage(`[{"type":"text","text":` + string(doubleWrapped) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	got, ok := v.(*DeviceInfo)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *DeviceInfo", v)
	}
	if got.Manufacturer != "Google" || got.Model != "Pixel 8" || got.SDK != 34 {
		t.Errorf("DecodeResult round-trip lost fields: %+v", got)
	}
	renderedStream := tt.RenderResult(original)
	renderedHistory := tt.RenderResult(v)
	if renderedStream != renderedHistory {
		t.Errorf("round-trip mismatch: stream=%q history=%q", renderedStream, renderedHistory)
	}
}

func TestComputer_DecodeResult_ActionResult(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	innerJSON, _ := json.Marshal(map[string]any{"action": "click", "ok": true})
	doubleWrapped, _ := json.Marshal(string(innerJSON))
	raw := json.RawMessage(`[{"type":"text","text":` + string(doubleWrapped) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	ar, ok := v.(*ActionResult)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *ActionResult", v)
	}
	if ar.Action != "click" {
		t.Errorf("action = %q, want click", ar.Action)
	}
	if !ar.OK {
		t.Error("ok = false, want true")
	}
	rendered := tt.RenderResult(v)
	if rendered != "click: ok" {
		t.Errorf("RenderResult(action result) = %q, want %q", rendered, "click: ok")
	}
}

func TestComputer_DecodeResult_MalformedJSON(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{bad`))
	if err == nil {
		t.Error("DecodeResult(malformed) should return error")
	}
}
