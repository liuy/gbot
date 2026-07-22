package tool

import (
	"strings"
	"testing"
)

func TestScreen_NormalLines(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("hello\nworld\n"))

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	assertEvent(t, events[0], ScreenAppend, "hello")
	assertEvent(t, events[1], ScreenAppend, "world")
}

func TestScreen_CarriageReturnProgress(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("Downloading 10%\rDownloading 35%\rDownloading 80%\r\nDone!\n"))

	// "10%\r"  → Append("Downloading 10%"), lineEmitted=true
	// "35%\r"  → Replace("Downloading 35%")
	// "80%\r"  → Replace("Downloading 80%")
	// "\n"     → lineEmitted=true, skip emit. lineEmitted=false
	// "Done!\n"→ Append("Done!")
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "Downloading 10%")
	assertEvent(t, events[1], ScreenReplace, "Downloading 35%")
	assertEvent(t, events[2], ScreenReplace, "Downloading 80%")
	assertEvent(t, events[3], ScreenAppend, "Done!")
}

func TestScreen_MixedNewlineAndCR(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("line1\nprog 10%\rprog 90%\r\nline2\n"))

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	assertEvent(t, events[0], ScreenAppend, "line1")
	assertEvent(t, events[1], ScreenAppend, "prog 10%")
	assertEvent(t, events[2], ScreenReplace, "prog 90%")
	assertEvent(t, events[3], ScreenAppend, "line2")
}

func TestScreen_SGRPreserved(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("\x1b[31mred text\x1b[0m\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	want := "\x1b[31mred text\x1b[0m"
	if events[0].Content != want {
		t.Errorf("content = %q, want %q", events[0].Content, want)
	}
}

func TestScreen_MultiParamSGR(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("\x1b[1;32mbold green\x1b[0m\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	want := "\x1b[1;32mbold green\x1b[0m"
	if events[0].Content != want {
		t.Errorf("content = %q, want %q", events[0].Content, want)
	}
}

func TestScreen_CSINonSGRDiscarded(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// \x1b[2J = clear screen, \x1b[H = cursor home, \x1b[K = clear to EOL
	s.Write([]byte("\x1b[2J\x1b[Hhello\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Content != "hello" {
		t.Errorf("content = %q, want %q", events[0].Content, "hello")
	}
	assertEvent(t, events[0], ScreenAppend, "hello")
}

func TestScreen_OSCDiscarded(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// OSC with BEL terminator
	s.Write([]byte("\x1b]0;window title\x07hello\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "hello")
}

func TestScreen_OSCWithSTTerminator(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// OSC with ST terminator (ESC \)
	s.Write([]byte("\x1b]0;title\x1b\\world\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "world")
}

func TestScreen_EmptyInput(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte{})

	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestScreen_FlushPartialLine(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("partial"))
	s.Flush()

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "partial")
}

func TestScreen_FlushNotReEmitted(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("line\n"))
	s.Flush()
	s.Flush()

	if len(events) != 1 {
		t.Fatalf("expected 1 event (no double-flush), got %d", len(events))
	}
}

func TestScreen_CRLF(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// \r\n: \r emits line (Append, lineEmitted=false), \n skips (lineEmitted=true)
	s.Write([]byte("hello\r\nworld\r\n"))

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	assertEvent(t, events[0], ScreenAppend, "hello")
	assertEvent(t, events[1], ScreenAppend, "world")
}

func TestScreen_NilCallback(t *testing.T) {
	s := NewScreen(nil)
	s.Write([]byte("hello\n")) // should not panic
	s.Flush()
}

func TestScreen_UTF8Chinese(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("你好\n世界\n"))

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "你好")
	assertEvent(t, events[1], ScreenAppend, "世界")
}

func TestScreen_UTF8WithCarriageReturn(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("进度50%\r进度100%\n"))

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "进度50%")
	assertEvent(t, events[1], ScreenReplace, "进度100%")
}

func TestScreen_UTF8Emoji(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("✅ done\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "✅ done")
}

func TestScreen_AnsiSplitAcrossWrites(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Split \x1b[31m across two Write calls
	s.Write([]byte("\x1b[31"))
	s.Write([]byte("mhello\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}
	want := "\x1b[31mhello"
	if events[0].Content != want {
		t.Errorf("content = %q, want %q", events[0].Content, want)
	}
}

func TestScreen_EmptyCarriageReturn(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// \r\r\r with no content between — should produce 0 events
	s.Write([]byte("\r\r\r"))

	if len(events) != 0 {
		t.Fatalf("expected 0 events for empty \\r, got %d: %+v", len(events), events)
	}
}

func TestScreen_IncompleteEscapeOnFlush(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Incomplete SGR — missing final byte → buffered, then recovered by Flush
	s.Write([]byte("\x1b[31"))
	s.Flush()

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if !strings.Contains(events[0].Content, "\x1b[31") {
		t.Errorf("content = %q, should contain partial SGR sequence", events[0].Content)
	}
}

func TestScreen_TabPassesThrough(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	s.Write([]byte("col1\tcol2\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "col1\tcol2")
}

func TestScreen_LargeInput(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Simulate rapid progress bar updates
	var input []byte
	for range 100 {
		input = append(input, []byte("progress...")...)
		input = append(input, '\r')
	}
	input = append(input, []byte("done!\n")...)

	s.Write(input)

	// Should produce 101 events: 100 Replace + 1 Append (the final "done!")
	// First "progress..." is Append, subsequent are Replace
	if len(events) != 101 {
		t.Fatalf("expected 101 events, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "progress...")
	for i := range 99 {
		if events[i+1].Kind != ScreenReplace {
			t.Errorf("event %d: kind = %v, want Replace", i, events[i].Kind)
		}
	}
	assertEvent(t, events[100], ScreenReplace, "done!")
}

func TestScreen_InvalidUTF8LeadByte(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// 0x80 is a continuation byte without a lead byte → invalid UTF-8, skipped
	s.Write([]byte{0x80, 'o', 'k', '\n'})

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "ok")
}

func TestScreen_ControlCharactersSkipped(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// SOH (0x01) and BEL (0x07) are control chars < 0x20, not \r or \n → skipped
	s.Write([]byte{0x01, 'a', 0x07, 'b', '\n'})

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "ab")
}

func TestScreen_BareEscapeSequence(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// ESC followed by a letter (not [ or ]) → bare ESC, consumed and discarded
	s.Write([]byte{'\x1b', 'X', 'h', 'i', '\n'})

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "hi")
}

func TestScreen_IncompleteEscapeJustESC(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Just ESC byte → incomplete, buffer remaining. Next write completes it as CSI SGR.
	s.Write([]byte{'\x1b'})
	s.Write([]byte{'[', '3', '1', 'm', 'r', 'e', 'd', '\n'})

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	want := "\x1b[31mred"
	if events[0].Content != want {
		t.Errorf("content = %q, want %q", events[0].Content, want)
	}
}

func TestScreen_CSIIntermediateBytes(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// CSI with intermediate byte: \x1b[?25h (show cursor) — ? is parameter, but
	// let's use an intermediate byte range 0x20-0x2F after parameters
	// \x1b[?25l = hide cursor: ? is 0x3F (parameter), 'l' is 0x6C (final)
	// Test with space (0x20) as intermediate: \x1b[?25\x20l
	// Actually let's just use a sequence with intermediate bytes
	s.Write([]byte("\x1b[?\x20lhel\x1b[?25llo\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}
	assertEvent(t, events[0], ScreenAppend, "hello")
}

func TestScreen_CSIInvalidFinalByte(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// CSI with final byte outside 0x40-0x7E (e.g., 0x30 = '0' digit)
	// \x1b[0 followed by '0' as "final" → invalid, consumed up to that point
	// The '0' at position after parameters is not a valid final byte (< 0x40)
	// But actually in parseCSI, after parameter bytes (0x30-0x3F), if the next
	// byte is also in parameter range, it keeps going. We need a byte that is
	// not parameter, not intermediate, and not a valid final byte.
	// After parameters, byte < 0x20 (control char) would work.
	s.Write([]byte("\x1b[" + string([]byte{0x10}) + "ok\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "ok")
}

func TestScreen_OSCIncomplete(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// OSC without terminator → incomplete, buffered across writes
	s.Write([]byte("\x1b]0;unfinished"))
	// The parseOSC returns -1, parseEscape returns -1, Write buffers remaining
	// Now send terminator + content in next write
	s.Write([]byte("\x07hello\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEvent(t, events[0], ScreenAppend, "hello")
}

func TestScreen_FlushNonSGREscapeRecovery(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Write incomplete escape that is NOT SGR (e.g., partial OSC)
	// ESC ] starts OSC, not SGR
	s.Write([]byte("\x1b]0;title"))
	// Now escapeBuf contains the partial. Flush should handle non-SGR escape.
	s.Flush()

	// The incomplete OSC escape is NOT SGR (buf[1] is ']', not '['),
	// so the SGR check fails and the else branch runs.
	// But line is still empty, so no event emitted.
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestScreen_FlushAfterLFNoReEmit(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Write a complete line with \n → lineEmitted becomes true
	s.Write([]byte("done\n"))
	if len(events) != 1 {
		t.Fatalf("expected 1 event after write, got %d", len(events))
	}

	// Flush should NOT re-emit because lineEmitted=true after \n
	s.Flush()
	if len(events) != 1 {
		t.Fatalf("expected 1 event total (no re-emit on flush), got %d", len(events))
	}
}

func TestScreen_IncompleteCSIAcrossWrites(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Start a CSI sequence, don't finish it
	s.Write([]byte("\x1b[31"))
	// Now send the rest
	s.Write([]byte("mcolor\n"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	want := "\x1b[31mcolor"
	if events[0].Content != want {
		t.Errorf("content = %q, want %q", events[0].Content, want)
	}
}

// TestScreen_SGROnlyLineDoesNotEmit reproduces a Windows ConPTY bug where
// bash emits \x1b[m (SGR reset) followed by \r\n before actual output.
// The SGR-only line should NOT be emitted as a separate event — it produces
// a spurious empty line in the tool output.
func TestScreen_SGROnlyLineDoesNotEmit(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// ConPTY sends: SGR reset, CR, LF, then actual output
	s.Write([]byte("\x1b[m\r\n/c/Users/PC/Desktop/gbot\r\n"))

	// Should get exactly 1 event: the pwd output. NOT the \x1b[m line.
	if len(events) != 1 {
		t.Fatalf("expected 1 event (pwd output only), got %d: %+v", len(events), events)
	}
	want := "/c/Users/PC/Desktop/gbot"
	if events[0].Content != want {
		t.Errorf("content = %q, want %q", events[0].Content, want)
	}
}

// TestScreen_CursorHomeActsAsCR reproduces a Windows winget bug where
// progress bar updates use \x1b[H (cursor home) to redraw the same line.
// Screen should treat \x1b[H like \r: emit current line, then reset for
// overwrite. Without this, each progress update accumulates as a new line.
func TestScreen_CursorHomeActsAsCR(t *testing.T) {
	var events []ScreenEvent
	s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })

	// Simulate winget-style progress updates:
	// \x1b[H redraws the line in place (same semantics as \r)
	s.Write([]byte("\x1b[H 25%\x1b[H 50%\x1b[H 75%\n"))

	// Should get 3 events: 25% (append), 50% (replace), 75% (replace).
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}
	// First emit is an append.
	if events[0].Kind != ScreenAppend || events[0].Content != " 25%" {
		t.Errorf("events[0] = {%d, %q}, want {Append, \" 25%%\"}", events[0].Kind, events[0].Content)
	}
	// Subsequent emits are replaces (same line overwritten).
	if events[1].Kind != ScreenReplace || events[1].Content != " 50%" {
		t.Errorf("events[1] = {%d, %q}, want {Replace, \" 50%%\"}", events[1].Kind, events[1].Content)
	}
	if events[2].Kind != ScreenReplace || events[2].Content != " 75%" {
		t.Errorf("events[2] = {%d, %q}, want {Replace, \" 75%%\"}", events[2].Kind, events[2].Content)
	}
}

func assertEvent(t *testing.T, ev ScreenEvent, wantKind ScreenEventKind, wantContent string) {
	t.Helper()
	if ev.Kind != wantKind {
		t.Errorf("kind = %v, want %v (content=%q)", ev.Kind, wantKind, ev.Content)
	}
	if ev.Content != wantContent {
		t.Errorf("content = %q, want %q", ev.Content, wantContent)
	}
}
