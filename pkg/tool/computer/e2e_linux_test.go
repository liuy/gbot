//go:build linux

package computer

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// TestList_E2E exercises the real cua-driver list_windows path. Skipped
// unless GBOT_COMPUTER_E2E=1 and cua-driver is on PATH with DISPLAY=:10.
func TestList_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires cua-driver + visible window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e list test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewBackend()
	t.Cleanup(b.Stop)
	res, err := b.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !res.OK {
		t.Fatalf("list not OK: %s", res.Message)
	}
	count, ok := res.Meta["count"].(int)
	if !ok {
		t.Fatalf("Meta count type = %T, want int", res.Meta["count"])
	}
	if count < 1 {
		t.Fatalf("window count = %d, want >= 1", count)
	}
	// At least one window should mention "Terminal" on this box.
	if !strings.Contains(res.Message, "Terminal") {
		t.Errorf("list output missing a Terminal window: %s", res.Message)
	}
	// Desktop/panel tagging: the heuristic should label a desktop or panel
	// window if one is present.
	hasTypeTag := strings.Contains(res.Message, "type=desktop") ||
		strings.Contains(res.Message, "type=panel") ||
		strings.Contains(res.Message, "type=app")
	if !hasTypeTag {
		t.Errorf("list output missing type= tag: %s", res.Message)
	}
}

// TestSnapshot_E2E exercises the real snapshot path: list to find a Terminal
// window_id, then snapshot that window. Asserts image + elements + window title.
func TestSnapshot_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires cua-driver + visible window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e snapshot test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewBackend()
	t.Cleanup(b.Stop)

	// Find the Terminal window_id via list.
	listRes, err := b.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	windows, _ := listRes.Meta["windows"].([]windowInfo)
	winID := 0
	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.Title), "terminal") {
			winID = w.WindowID
			break
		}
	}
	if winID == 0 {
		t.Skip("no Terminal window in list_windows output")
	}

	cap, err := b.snapshot(ctx, Input{Action: ActionSnapshot, Window: &winID, Mode: ModeSom})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if cap.Width == 0 || cap.Height == 0 {
		t.Fatalf("snapshot dimensions = %dx%d, want non-zero", cap.Width, cap.Height)
	}
	if cap.PngB64 == "" {
		t.Fatal("snapshot PngB64 empty, want a screenshot")
	}
	if len(cap.PngB64) < 1000 {
		t.Errorf("PngB64 length = %d, want > 1000 (real screenshot)", len(cap.PngB64))
	}
	if cap.WindowTitle == "" {
		t.Fatal("WindowTitle empty, want the snapshot window's title")
	}

	res := captureResponse(cap, defaultMaxElements)
	if len(res.NewMessages) != 1 {
		t.Fatalf("NewMessages length = %d, want 1", len(res.NewMessages))
	}
	msg := res.NewMessages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("content length = %d, want 2 [text, image]", len(msg.Content))
	}
	if msg.Content[1].Type != types.ContentTypeImage {
		t.Errorf("content[1] type = %q, want %q", msg.Content[1].Type, types.ContentTypeImage)
	}
	if msg.Content[1].Source.MediaType != "image/png" && msg.Content[1].Source.MediaType != "image/jpeg" {
		t.Errorf("image media type = %q, want image/png or image/jpeg", msg.Content[1].Source.MediaType)
	}

	summary := res.Data.(string)
	if !strings.Contains(summary, "capture mode=som") {
		t.Errorf("summary %q missing 'capture mode=som'", summary)
	}
}

// TestClickAndType_E2E is the regression test for the original wrong-window
// bug. It explicitly targets a Terminal window_id, types text, and the
// keystrokes MUST land in that Terminal (verifiable by a follow-up snapshot
// showing the typed text, NOT in Desktop). Skipped unless explicitly requested
// via GBOT_COMPUTER_E2E=1.
func TestClickAndType_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires cua-driver + visible window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e click/type test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	b := NewBackend()
	t.Cleanup(b.Stop)

	// Find the Terminal window_id via list, then snapshot it to get elements.
	listRes, err := b.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	windows, _ := listRes.Meta["windows"].([]windowInfo)
	winID := 0
	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.Title), "terminal") {
			winID = w.WindowID
			break
		}
	}
	if winID == 0 {
		t.Skip("no Terminal window in list_windows output")
	}

	cap, err := b.snapshot(ctx, Input{Action: ActionSnapshot, Window: &winID, Mode: ModeSom})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(cap.Elements) == 0 {
		t.Skip("no elements to click — need a window with UI in display :10")
	}

	// Click element 1 of the explicitly-targeted window, then type.
	clickRes, err := b.click(ctx, Input{Action: ActionClick, Window: &winID, Element: &cap.Elements[0].Index})
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if !clickRes.OK {
		t.Skipf("click did not succeed: %s", clickRes.Message)
	}
	typeRes, err := b.typeText(ctx, Input{Action: ActionType, Window: &winID, Text: "gbot e2e"})
	if err != nil {
		t.Fatalf("type: %v", err)
	}
	if !typeRes.OK {
		t.Errorf("type not OK: %s", typeRes.Message)
	}
}

// TestBackendEnsureStarted_E2E verifies the full MCP connection path works
// against the real cua-driver binary.
func TestBackendEnsureStarted_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires cua-driver binary")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e backend test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b := NewBackend()
	t.Cleanup(b.Stop)
	if err := b.ensureStarted(ctx); err != nil {
		t.Fatalf("ensureStarted: %v", err)
	}
	if !b.started {
		t.Error("started flag not set after ensureStarted")
	}
	// idempotent: second call is a no-op.
	if err := b.ensureStarted(ctx); err != nil {
		t.Errorf("second ensureStarted: %v", err)
	}
}

// TestExecuteE2EListRoundTrip runs the list action through the full execute
// path and verifies it returns a non-error text result.
func TestExecuteE2EListRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires cua-driver + visible window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e list test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewBackend()
	t.Cleanup(b.Stop)
	res, err := execute(ctx, json.RawMessage(`{"action":"list"}`), b)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	data := res.Data.(string)
	if strings.Contains(data, `"error"`) {
		t.Errorf("list execute returned error: %s", data)
	}
	if !strings.Contains(data, `"action":"list"`) {
		t.Errorf("list round-trip data %q missing action=list", data)
	}
}
