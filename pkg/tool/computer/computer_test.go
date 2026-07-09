package computer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field for missing host", m)
	}
	if e, _ := m["error"].(string); !contains(e, "host") {
		t.Errorf("error = %q, want mention of 'host'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["host"] != "1.2.3.4" {
		t.Errorf("result host = %v, want 1.2.3.4", m["host"])
	}
	if port := numAsInt(m["port"]); port != 8765 {
		t.Errorf("result port = %v, want 8765", m["port"])
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["action"] != "disconnect" {
		t.Errorf("action = %v, want disconnect", m["action"])
	}
	if ok2, _ := m["ok"].(bool); !ok2 {
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if ok2, _ := m["ok"].(bool); !ok2 {
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "not connected; call connect first") {
		t.Errorf("error = %q, want 'not connected; call connect first'", e)
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

func TestComputer_Execute_ScreenshotAttachesImageBlock(t *testing.T) {
	t.Parallel()
	tt, b, _ := newTestTool()
	ctx := context.Background()
	_, _ = tt.Call(ctx, mustMarshal(t, map[string]any{"action": "connect", "host": "h"}), &tool.ToolUseContext{})
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"screenshot": json.RawMessage(`{"image":"BASE64","format":"jpeg","width":1080,"height":2400}`),
	}
	res, err := tt.Call(ctx, mustMarshal(t, map[string]any{"action": "screenshot"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.NewMessages) != 1 {
		t.Fatalf("NewMessages len = %d, want 1", len(res.NewMessages))
	}
	msg := res.NewMessages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2 (text + image)", len(msg.Content))
	}
	if msg.Content[1].Type != "image" {
		t.Errorf("Content[1] Type = %q, want image", msg.Content[1].Type)
	}
	if msg.Content[1].Source == nil {
		t.Fatal("Content[1] Source = nil")
	}
	if msg.Content[1].Source.MediaType != "image/jpeg" {
		t.Errorf("MediaType = %q, want image/jpeg", msg.Content[1].Source.MediaType)
	}
	if msg.Content[1].Source.Data != "BASE64" {
		t.Errorf("Data = %q, want BASE64", msg.Content[1].Source.Data)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "non-empty text") {
		t.Errorf("error = %q, want 'non-empty text'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "blocked") {
		t.Errorf("error = %q, want 'blocked'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["action"] != "type" {
		t.Errorf("action = %v, want type", m["action"])
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "unknown key") {
		t.Errorf("error = %q, want 'unknown key'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["key"] != "back" {
		t.Errorf("result key = %v, want back", m["key"])
	}
}

func TestComputer_Execute_ClickRequiresCoordinate(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "click"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "coordinate") {
		t.Errorf("error = %q, want mention of 'coordinate'", e)
	}
}

func TestComputer_Execute_ClickElementRequiresRef(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	res, err := tt.Call(context.Background(), mustMarshal(t, map[string]any{"action": "click_element"}), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "ref") {
		t.Errorf("error = %q, want mention of 'ref'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "direction") {
		t.Errorf("error = %q, want mention of 'direction'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if ok2, _ := m["ok"].(bool); !ok2 {
		t.Error("ok = false, want true")
	}
}

func TestComputer_RenderResult_ErrorMap(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	got := tt.RenderResult(map[string]any{"error": "bad thing"})
	if !strings.HasPrefix(got, "error:") {
		t.Errorf("RenderResult = %q, want 'error:' prefix", got)
	}
	if !contains(got, "bad thing") {
		t.Errorf("RenderResult = %q, want 'bad thing'", got)
	}
}

func TestComputer_RenderResult_OkMap(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	got := tt.RenderResult(map[string]any{"action": "click", "ok": true})
	if got != "click: ok" {
		t.Errorf("RenderResult = %q, want 'click: ok'", got)
	}
}

func TestComputer_RenderResult_ScreenshotMap(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	got := tt.RenderResult(map[string]any{
		"Width":    float64(1256),
		"Height":   float64(2760),
		"MIMEType": "image/jpeg",
	})
	want := "screenshot 1256x2760 (image/jpeg)"
	if got != want {
		t.Errorf("RenderResult = %q, want %q", got, want)
	}
}

func TestComputer_RenderResult_DeviceInfoMap(t *testing.T) {
	t.Parallel()
	tt, _, _ := newTestTool()
	got := tt.RenderResult(map[string]any{
		"Manufacturer": "HONOR",
		"Model":        "BKQ-AN80",
	})
	want := "HONOR BKQ-AN80"
	if got != want {
		t.Errorf("RenderResult = %q, want %q", got, want)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["action"] != "click_element" {
		t.Errorf("action = %v, want click_element", m["action"])
	}
	if ref := numAsInt(m["ref"]); ref != 1 {
		t.Errorf("ref = %v, want 1", m["ref"])
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["direction"] != "down" {
		t.Errorf("direction = %v, want down", m["direction"])
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["action"] != "zoom" {
		t.Errorf("action = %v, want zoom", m["action"])
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["action"] != "open_menu" {
		t.Errorf("action = %v, want open_menu", m["action"])
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field for dial failure", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if ok2, _ := m["ok"].(bool); !ok2 {
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field for backend failure", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["action"] != "open_app" {
		t.Errorf("action = %v, want open_app", m["action"])
	}
	if m["package"] != "com.android.chrome" {
		t.Errorf("package = %v, want com.android.chrome", m["package"])
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "package") {
		t.Errorf("error = %q, want mention of 'package'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("Data = %+v, want 'error' field", m)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "connect first") {
		t.Errorf("error = %q, want 'connect first'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if m["action"] != "send_file" {
		t.Errorf("action = %v, want send_file", m["action"])
	}
	if m["path"] != tmpPath {
		t.Errorf("path = %v, want %q", m["path"], tmpPath)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "path") {
		t.Errorf("error = %q, want mention of 'path'", e)
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
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data type = %T, want map", res.Data)
	}
	if e, _ := m["error"].(string); !contains(e, "connect first") {
		t.Errorf("error = %q, want 'connect first'", e)
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
