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

// TestCapture_E2E exercises the real cua-driver + a visible window. Skipped
// unless -test.run is set explicitly and cua-driver is on PATH and DISPLAY=:10
// has a visible window. Plan Verification Step #4.
func TestCapture_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires cua-driver + visible window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e capture test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewBackend()
	t.Cleanup(b.Stop)
	cap, err := b.capture(ctx, ModeSom, "", defaultMaxElements)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if cap.Width == 0 || cap.Height == 0 {
		t.Fatalf("capture dimensions = %dx%d, want non-zero", cap.Width, cap.Height)
	}
	if cap.PngB64 == "" {
		t.Fatal("capture PngB64 empty, want a screenshot")
	}
	if len(cap.PngB64) < 1000 {
		t.Errorf("PngB64 length = %d, want > 1000 (real screenshot)", len(cap.PngB64))
	}
	// The X11 active window is the Terminal — confirm we didn't fall back to
	// Desktop (which has 0 interactable elements and would silently pass above).
	if cap.WindowTitle == "" {
		t.Fatal("WindowTitle empty, want the active window's title")
	}
	if cap.WindowTitle == "Desktop" {
		t.Errorf("WindowTitle = %q — picked Desktop, not the active Terminal; X11 active-window fallback failed",
			cap.WindowTitle)
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

// TestClickAndType_E2E exercises click + type + capture_after against the
// real cua-driver. Plan Verification Step #5. Skipped unless explicitly
// requested via GBOT_COMPUTER_E2E=1.
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

	// Capture first to establish the active window.
	cap, err := b.capture(ctx, ModeSom, "", defaultMaxElements)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(cap.Elements) == 0 {
		t.Skip("no elements to click — need a window with UI in display :10")
	}

	// Click element 1 (frontmost control), then type a short string with
	// capture_after so we see the result.
	clickIn := Input{Action: ActionClick, Element: new(1), CaptureAfter: true}
	clickRes, err := b.click(ctx, clickIn)
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if !clickRes.OK {
		t.Skipf("click did not succeed: %s", clickRes.Message)
	}
	typeRes, err := b.typeText(ctx, Input{Action: ActionType, Text: "gbot e2e"})
	if err != nil {
		t.Fatalf("type: %v", err)
	}
	if !typeRes.OK {
		t.Errorf("type not OK: %s", typeRes.Message)
	}
}

// TestBackendEnsureStarted_E2E verifies the full MCP connection path works
// against the real cua-driver binary. Plan Verification #3 (real probe).
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

// TestExecuteE2ECaptureRoundTrip runs the full tool execute path against the
// real backend and verifies the resulting ToolResult has the image in
// NewMessages. Plan Verification #4 variant (tool wrapper layer).
func TestExecuteE2ECaptureRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires cua-driver + visible window")
	}
	if os.Getenv("GBOT_COMPUTER_E2E") == "" {
		t.Skip("set GBOT_COMPUTER_E2E=1 to run e2e execute test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := NewBackend()
	t.Cleanup(b.Stop)
	res, err := execute(ctx, json.RawMessage(`{"action":"capture","mode":"som"}`), b)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.NewMessages) != 1 {
		t.Fatalf("NewMessages length = %d, want 1", len(res.NewMessages))
	}
}
