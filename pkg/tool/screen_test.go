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

// ---------------------------------------------------------------------------
// Event-sequence pinning table: each case drives one Write and asserts the
// exact (kind, content) event stream, exercising the CR/LF/CSI/OSC state
// machine boundaries — erased-line tracking, cursor-home equivalence classes,
// final-byte ranges, and OSC terminator scanning.
// ---------------------------------------------------------------------------

func TestScreen_EventSequences(t *testing.T) {
	t.Parallel()
	appendEvent := func(c string) ScreenEvent { return ScreenEvent{ScreenAppend, c} }
	replaceEvent := func(c string) ScreenEvent { return ScreenEvent{ScreenReplace, c} }

	tests := []struct {
		name  string
		input string
		want  []ScreenEvent
	}{
		// Invalid-UTF-8 skip must not swallow a literal U+FFFD rune.
		{"literal replacement char preserved", "\uFFFD\n", []ScreenEvent{appendEvent("\uFFFD")}},
		// 0x1F is below the printable threshold and must be dropped.
		{"US control byte skipped", "\x1fa\n", []ScreenEvent{appendEvent("a")}},
		// Emit guards: SGR-only lines are invisible, 1-char lines still emit.
		{"SGR-only line then CR emits nothing", "\x1b[31m\r", nil},
		{"SGR-only line then LF emits nothing", "\x1b[31m\n", nil},
		{"single-char line emits on CR", "a\r", []ScreenEvent{appendEvent("a")}},
		{"single-char line emits on LF", "a\n", []ScreenEvent{appendEvent("a")}},
		// Erased-line bookkeeping around ESC[1G + ESC[0K.
		{"erased empty line emits on LF", "x\x1b[1G\x1b[0K\n", []ScreenEvent{appendEvent("x"), replaceEvent(""), appendEvent("")}},
		{"printable clears lineErased", "f\x1b[1G\x1b[0Kx\x1b[1G\n", []ScreenEvent{appendEvent("f"), replaceEvent(""), appendEvent("x")}},
		{"content after erase is Append", "x\x1b[1G\x1b[0Ky\n", []ScreenEvent{appendEvent("x"), replaceEvent(""), appendEvent("y")}},
		{"LF clears crPending", "a\n\x1b[0K\n", []ScreenEvent{appendEvent("a")}},
		// Same resets, but with the flag actually set beforehand — removing
		// the assignment only diverges when the stale value is true.
		{"LF clears crPending set by 1G", "x\x1b[1Ga\n\x1b[0K\n", []ScreenEvent{appendEvent("x"), replaceEvent("a")}},
		{"LF clears lineErased", "a\x1b[1G\x1b[0Kb\n\n", []ScreenEvent{appendEvent("a"), replaceEvent(""), appendEvent("b")}},
		{"LF clears lineErased set by 0K", "a\x1b[1G\x1b[0K\n\n", []ScreenEvent{appendEvent("a"), replaceEvent(""), appendEvent("")}},
		// OSC bodies: backslash is data, only ESC-\ or BEL terminate, and a
		// trailing lone ESC at the buffer end must not index past the slice.
		{"OSC with backslash in body", "\x1b]0;ti\\tle\x07ok\n", []ScreenEvent{appendEvent("ok")}},
		{"OSC terminated by ST", "\x1b]0;t\x1b\\ok\n", []ScreenEvent{appendEvent("ok")}},
		{"OSC ends without terminator", "\x1b]0;end", nil},
		{"OSC ends with lone ESC", "\x1b]0;x\x1b", nil},
		{"ESC inside OSC body is not ST", "\x1b]0;a\x1b[31mred\n", nil},
		// Cursor movement equivalents of \r.
		{"ESC[1G acts as CR", "x\x1b[1Gy\n", []ScreenEvent{appendEvent("x"), replaceEvent("y")}},
		{"ESC[d acts as CR", "x\x1b[1dy\n", []ScreenEvent{appendEvent("x"), replaceEvent("y")}},
		{"ESC[H acts as CR", "x\x1b[Hy\n", []ScreenEvent{appendEvent("x"), replaceEvent("y")}},
		{"ESC[1;1H acts as CR", "x\x1b[1;1Hy\n", []ScreenEvent{appendEvent("x"), replaceEvent("y")}},
		{"ESC[2;1H not home", "x\x1b[2;1Hy\n", []ScreenEvent{appendEvent("xy")}},
		{"ESC[1:1H not home", "x\x1b[1:1Hy\n", []ScreenEvent{appendEvent("xy")}},
		{"ESC[1;2H not home", "x\x1b[1;2Hy\n", []ScreenEvent{appendEvent("xy")}},
		{"ESC[1;12H not home", "x\x1b[1;12Hy\n", []ScreenEvent{appendEvent("xy")}},
		{"erase after ESC[H", "x\x1b[H\x1b[0K\n", []ScreenEvent{appendEvent("x"), replaceEvent(""), appendEvent("")}},
		// ESC[0K erase only fires when a cursor-to-column preceded it.
		{"ESC[0K without pending CR ignored", "a\n\x1b[0K\n", []ScreenEvent{appendEvent("a")}},
		{"non-K final after ESC[1G ignored", "x\x1b[1G\x1b[2J\n", []ScreenEvent{appendEvent("x")}},
		// CSI byte classification boundaries.
		{"digit-zero stays a parameter", "\x1b[0mred\n", []ScreenEvent{appendEvent("\x1b[0mred")}},
		{"at-sign is a final byte", "\x1b[@x\n", []ScreenEvent{appendEvent("x")}},
		{"tilde is a final byte", "\x1b[~x\n", []ScreenEvent{appendEvent("x")}},
		{"DEL after ESC[ stays content", "\x1b[\x7fX\n", []ScreenEvent{appendEvent("\x7fX")}},
		{"multibyte rune after invalid final", "a\x1b[\xc3\xa9b\n", []ScreenEvent{appendEvent("aéb")}},
		{"CR after invalid final still a CR", "a\x1b[\rX\n", []ScreenEvent{appendEvent("a"), replaceEvent("X")}},
		{"params then invalid final byte", "\x1b[12\x10ok\n", []ScreenEvent{appendEvent("ok")}},
		{"space is an intermediate byte", "\x1b[\x20mX\n", []ScreenEvent{appendEvent("\x1b[ mX")}},
		{"slash is an intermediate byte", "\x1b[/mX\n", []ScreenEvent{appendEvent("\x1b[/mX")}},
		{"0x1F is not an intermediate byte", "\x1b[\x1fmX\n", []ScreenEvent{appendEvent("mX")}},
		// The classification loops stop at the first intermediate byte, so a
		// digit or '?' after '/' is an invalid final, not a parameter.
		{"digit after intermediate is an invalid final", "\x1b[/0hello\n", []ScreenEvent{appendEvent("0hello")}},
		{"digit after intermediate is not SGR body", "\x1b[/0mX\n", []ScreenEvent{appendEvent("0mX")}},
		{"? after intermediate is not a final", "\x1b[/?X\n", []ScreenEvent{appendEvent("?X")}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var events []ScreenEvent
			s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
			s.Write([]byte(tc.input))
			if len(events) != len(tc.want) {
				t.Fatalf("got %d events, want %d: %+v", len(events), len(tc.want), events)
			}
			for i, ev := range events {
				if ev != tc.want[i] {
					t.Errorf("event %d = {%d, %q}, want {%d, %q}", i, ev.Kind, ev.Content, tc.want[i].Kind, tc.want[i].Content)
				}
			}
		})
	}
}

// Flush-boundary battery: partial-escape recovery, idempotence, and the
// state resets that keep subsequent writes starting clean.
func TestScreen_FlushBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("bare ESC flush emits nothing", func(t *testing.T) {
		t.Parallel()
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte{'\x1b'})
		s.Flush()
		if len(events) != 0 {
			t.Fatalf("want 0 events, got %+v", events)
		}
	})

	t.Run("bare ESC flush then write starts clean", func(t *testing.T) {
		t.Parallel()
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte{'\x1b'})
		s.Flush()
		s.Write([]byte("ok\n"))
		if len(events) != 1 || events[0].Content != "ok" {
			t.Fatalf("want [Append ok], got %+v", events)
		}
	})

	t.Run("partial CSI header recovered by flush", func(t *testing.T) {
		t.Parallel()
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte("\x1b["))
		s.Flush()
		s.Write([]byte("hi\n"))
		want := []ScreenEvent{{ScreenAppend, "\x1b["}, {ScreenReplace, "hi"}}
		if len(events) != len(want) {
			t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
		}
		for i := range want {
			if events[i] != want[i] {
				t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
			}
		}
	})

	t.Run("flush empty line emits nothing", func(t *testing.T) {
		t.Parallel()
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte("\n"))
		s.Flush()
		if len(events) != 0 {
			t.Fatalf("want 0 events, got %+v", events)
		}
	})

	t.Run("flush single-char line emits", func(t *testing.T) {
		t.Parallel()
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte("a"))
		s.Flush()
		if len(events) != 1 || events[0] != (ScreenEvent{ScreenAppend, "a"}) {
			t.Fatalf("want [Append a], got %+v", events)
		}
	})

	t.Run("double flush emits once", func(t *testing.T) {
		t.Parallel()
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte("a"))
		s.Flush()
		s.Flush()
		if len(events) != 1 || events[0] != (ScreenEvent{ScreenAppend, "a"}) {
			t.Fatalf("want [Append a], got %+v", events)
		}
	})

	t.Run("flush clears line for next write", func(t *testing.T) {
		t.Parallel()
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte("a"))
		s.Flush()
		s.Write([]byte("b\n"))
		want := []ScreenEvent{{ScreenAppend, "a"}, {ScreenReplace, "b"}}
		if len(events) != len(want) {
			t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
		}
		for i := range want {
			if events[i] != want[i] {
				t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
			}
		}
	})
}
