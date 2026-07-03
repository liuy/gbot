package bash

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// npm spinner uses ANSI cursor-move + erase-line, NOT \r.
// Sequence per frame: <char> ESC[1G ESC[0K
//   ESC[1G = move cursor to column 1
//   ESC[0K = erase from cursor to end of line
// Screen must treat this as a line replacement (like \r), so that
// ReplaceLastLine fires and downstream sees only the latest frame.
func TestScreen_NpmSpinner_ESC1G_ESC0K_TriggersReplace(t *testing.T) {
	t.Parallel()

	var events []tool.ScreenEvent
	screen := tool.NewScreen(func(ev tool.ScreenEvent) {
		events = append(events, ev)
	})

	// Real npm output captured from PTY: ⠙\x1b[1G\x1b[0K⠹\x1b[1G\x1b[0K⠸\x1b[1G\x1b[0K
	data := []byte("⠙\x1b[1G\x1b[0K⠹\x1b[1G\x1b[0K⠸\x1b[1G\x1b[0K")
	screen.Write(data)

	// Expect: 3 Append (each frame) + 3 Replace("") (ESC[0K erases each frame)
	appends := 0
	replaceClears := 0
	for _, ev := range events {
		switch ev.Kind {
		case tool.ScreenAppend:
			appends++
		case tool.ScreenReplace:
			if ev.Content == "" {
				replaceClears++
			}
		}
	}
	if appends != 3 {
		t.Errorf("ScreenAppend = %d, want 3 (one per frame)", appends)
	}
	if replaceClears != 3 {
		t.Errorf("ScreenReplace(clear) = %d, want 3 (ESC[0K per frame)", replaceClears)
	}

	// Last non-empty append must be only the last spinner char
	lastAppend := ""
	for _, ev := range events {
		if ev.Kind == tool.ScreenAppend && ev.Content != "" {
			lastAppend = ev.Content
		}
	}
	if strings.Contains(lastAppend, "⠙") || strings.Contains(lastAppend, "⠹") {
		t.Errorf("last append = %q, should be only ⠸", lastAppend)
	}
}
