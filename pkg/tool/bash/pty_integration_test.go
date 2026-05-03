package bash

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// PTY integration tests: real PTY with actual shell commands
// ---------------------------------------------------------------------------
// These tests verify the full Screen pipeline with a real PTY,
// exercising actual kernel PTY allocation and shell execution.
// Skipped on non-Linux systems or when PTY is unavailable.

func TestPTYIntegration_CarriageReturn(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	screen := tool.NewScreen(func(ev tool.ScreenEvent) {
		lines = append(lines, ev.Content)
	})

	exitCode, _, err := ptyCommand(
		context.Background(),
		"printf '10%%\\r50%%\\r100%%\\nDone!\\n'",
		"",
		os.Environ(),
		screen,
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "100%") {
		t.Errorf("output should contain '100%%', got %q", joined)
	}
	if !strings.Contains(joined, "Done!") {
		t.Errorf("output should contain 'Done!', got %q", joined)
	}
	// The replaced values should NOT appear as separate lines
	// "10%" was replaced by "50%" which was replaced by "100%"
	if strings.Count(joined, "10%") > 1 {
		t.Errorf("output should not have '10%%' as separate entry, got %q", joined)
	}
}

func TestPTYIntegration_AnsiColor(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	screen := tool.NewScreen(func(ev tool.ScreenEvent) {
		lines = append(lines, ev.Content)
	})

	exitCode, _, err := ptyCommand(
		context.Background(),
		"printf '\\033[31mred\\033[0m\\n\\033[32mgreen\\033[0m\\n'",
		"",
		os.Environ(),
		screen,
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "\x1b[31mred\x1b[0m") {
		t.Errorf("output should contain ANSI red, got %q", joined)
	}
	if !strings.Contains(joined, "\x1b[32mgreen\x1b[0m") {
		t.Errorf("output should contain ANSI green, got %q", joined)
	}
}

func TestPTYIntegration_UTF8Chinese(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	screen := tool.NewScreen(func(ev tool.ScreenEvent) {
		lines = append(lines, ev.Content)
	})

	exitCode, _, err := ptyCommand(
		context.Background(),
		"printf '你好\\n世界\\n'",
		"",
		os.Environ(),
		screen,
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}

	if len(lines) < 2 {
		t.Fatalf("lines = %v, want at least 2", lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "你好") {
		t.Errorf("output should contain '你好', got %q", joined)
	}
	if !strings.Contains(joined, "世界") {
		t.Errorf("output should contain '世界', got %q", joined)
	}
}
