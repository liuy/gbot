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

// TestX11_E2E_List exercises the real X11Backend list path. Skipped unless
// GBOT_COMPUTER_E2E=1 and a visible window is present.
func TestX11_E2E_List(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires a visible X window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e list test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewX11Backend()
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
	hasTypeTag := strings.Contains(res.Message, "type=desktop") ||
		strings.Contains(res.Message, "type=panel") ||
		strings.Contains(res.Message, "type=app")
	if !hasTypeTag {
		t.Errorf("list output missing type= tag: %s", res.Message)
	}
}

// TestX11_E2E_Snapshot exercises list → snapshot against a visible window.
func TestX11_E2E_Snapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires a visible X window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e snapshot test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewX11Backend()
	t.Cleanup(b.Stop)

	listRes, err := b.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	windows, _ := listRes.Meta["windows"].([]windowInfo)
	if len(windows) == 0 {
		t.Skip("no windows in list output")
	}
	winID := windows[0].WindowID

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
}

// TestX11_E2E_ClickAndType clicks a window coordinate then types text.
func TestX11_E2E_ClickAndType(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires a visible X window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e click/type test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	b := NewX11Backend()
	t.Cleanup(b.Stop)

	listRes, err := b.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	windows, _ := listRes.Meta["windows"].([]windowInfo)
	if len(windows) == 0 {
		t.Skip("no windows in list output")
	}
	winID := windows[0].WindowID

	clickRes, err := b.click(ctx, Input{Action: ActionClick, Window: &winID, Coordinate: json.RawMessage(`[100,100]`)})
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

// TestX11_E2E_EnsureStarted verifies the X11 connection path works against a
// real display.
func TestX11_E2E_EnsureStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires a display")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e backend test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b := NewX11Backend()
	t.Cleanup(b.Stop)
	if err := b.ensureStarted(ctx); err != nil {
		t.Fatalf("ensureStarted: %v", err)
	}
	if !b.started {
		t.Error("started flag not set after ensureStarted")
	}
	if err := b.ensureStarted(ctx); err != nil {
		t.Errorf("second ensureStarted: %v", err)
	}
}

// TestX11_E2E_ExecuteListRoundTrip runs list through the full execute path.
func TestX11_E2E_ExecuteListRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires a visible X window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e list test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewX11Backend()
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
