package bash

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
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

	exitCode, _, err := runPTYCommand(
		context.Background(),
		"printf '10%%\\r50%%\\r100%%\\nDone!\\n'",
		"",
		os.Environ(),
		screen,
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
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

	exitCode, _, err := runPTYCommand(
		context.Background(),
		"printf '\\033[31mred\\033[0m\\n\\033[32mgreen\\033[0m\\n'",
		"",
		os.Environ(),
		screen,
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
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

	exitCode, _, err := runPTYCommand(
		context.Background(),
		"printf '你好\\n世界\\n'",
		"",
		os.Environ(),
		screen,
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
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

// ---------------------------------------------------------------------------
// Fix 1: Drain must unblock when ctx is cancelled while blocked on respCh
// ---------------------------------------------------------------------------

func TestPTYIntegration_Drain_UnblocksOnCtxCancel(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	ctx, cancel := context.WithCancel(context.Background())

	screen := tool.NewScreen(func(ev tool.ScreenEvent) {})
	output := NewStreamingOutput(nil)

	emitCalled := make(chan struct{}, 1)
	neverRespondCh := make(chan types.AskResponse) // unbuffered, never written to

	emitAskInput := func(tail string, masked bool) chan types.AskResponse {
		select {
		case emitCalled <- struct{}{}:
		default:
		}
		return neverRespondCh
	}

	session, err := openPTYSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	session.Output = output

	// Cancel ctx after emitAskInput fires
	go func() {
		select {
		case <-emitCalled:
			// Brief pause to ensure Drain is blocked on <-respCh before we cancel
			<-time.After(300 * time.Millisecond)
			cancel()
		case <-time.After(10 * time.Second):
			cancel() // safety
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = session.Run(ctx, `printf 'Password: ' && sleep 60`, "", os.Environ(), screen, 10*time.Second, emitAskInput)
	}()

	select {
	case <-done:
		// Drain returned — ctx cancellation unblocked it
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not unblock after ctx cancellation — deadlock suspected")
	}
}

// Fix 3: emitAskInput returning nil (expired deadline) should not deadlock Drain
func TestPTYIntegration_Drain_NilChannelSkipsPrompt(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	screen := tool.NewScreen(func(ev tool.ScreenEvent) {})
	output := NewStreamingOutput(nil)

	emitCount := 0
	emitAskInput := func(tail string, masked bool) chan types.AskResponse {
		emitCount++
		return nil // simulates expired deadline
	}

	session, err := openPTYSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	session.Output = output

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = session.Run(
			context.Background(),
			`printf 'Password: ' && sleep 1`,
			"",
			os.Environ(),
			screen,
			10*time.Second,
			emitAskInput,
		)
	}()

	select {
	case <-done:
		// Drain should have skipped the nil channel and continued
		if emitCount == 0 {
			t.Error("emitAskInput was never called — test did not exercise the code path")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Drain blocked on nil channel — deadlock suspected")
	}
}

// ---------------------------------------------------------------------------
// Integration: runPTYCommand must detect interactive prompts via local line tracking
// ---------------------------------------------------------------------------

func TestPTYIntegration_InteractivePromptDetected(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	askCalled := make(chan string, 1)
	emitAskInput := func(tail string, masked bool) chan types.AskResponse {
		select {
		case askCalled <- tail:
		default:
		}
		ch := make(chan types.AskResponse, 1)
		ch <- types.AskResponse{Text: "testpass", Aborted: false}
		return ch
	}

	screen := tool.NewScreen(func(ev tool.ScreenEvent) {})

	done := make(chan struct{})
	go func() {
		defer close(done)
		exitCode, _, err := runPTYCommand(
			context.Background(),
			"printf 'Password: ' && sleep 2",
			"",
			os.Environ(),
			screen,
			10*time.Second,
			emitAskInput,
		)
		if err != nil {
			t.Errorf("runPTYCommand error: %v", err)
		}
		if exitCode != 0 {
			t.Errorf("exitCode = %d, want 0", exitCode)
		}
	}()

	select {
	case tail := <-askCalled:
		if !strings.Contains(tail, "Password") {
			t.Errorf("expected tail to contain 'Password', got %q", tail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("emitAskInput was never called — interactive prompt detection not working")
	}
	<-done
}

// Test actual sudo prompt format: "[sudo] password for user:" 
func TestPTYIntegration_SudoPromptDetected(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	askCalled := make(chan string, 1)
	emitAskInput := func(tail string, masked bool) chan types.AskResponse {
		select {
		case askCalled <- tail:
		default:
		}
		ch := make(chan types.AskResponse, 1)
		ch <- types.AskResponse{Text: "testpass", Aborted: false}
		return ch
	}

	screen := tool.NewScreen(func(ev tool.ScreenEvent) {})

	done := make(chan struct{})
	go func() {
		defer close(done)
		exitCode, _, err := runPTYCommand(
			context.Background(),
			"printf '[sudo] password for yliu: ' && sleep 2",
			"",
			os.Environ(),
			screen,
			10*time.Second,
			emitAskInput,
		)
		if err != nil {
			t.Errorf("runPTYCommand error: %v", err)
		}
		if exitCode != 0 {
			t.Errorf("exitCode = %d, want 0", exitCode)
		}
	}()

	select {
	case tail := <-askCalled:
		if !strings.Contains(tail, "password") {
			t.Errorf("expected tail to contain 'password', got %q", tail)
		}
		if !isPasswordPrompt(tail) {
			t.Errorf("isPasswordPrompt(%q) = false, want true", tail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("emitAskInput was never called — sudo prompt detection not working")
	}
	<-done
}
