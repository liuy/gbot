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

	// Incomplete SGR at end of stream
	s.Write([]byte("\x1b[31m"))
	s.Flush()

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	// The partial SGR should be included in the flushed content
	if !strings.Contains(events[0].Content, "\x1b[31m") {
		t.Errorf("content = %q, should contain SGR sequence", events[0].Content)
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

func assertEvent(t *testing.T, ev ScreenEvent, wantKind ScreenEventKind, wantContent string) {
	t.Helper()
	if ev.Kind != wantKind {
		t.Errorf("kind = %v, want %v (content=%q)", ev.Kind, wantKind, ev.Content)
	}
	if ev.Content != wantContent {
		t.Errorf("content = %q, want %q", ev.Content, wantContent)
	}
}
