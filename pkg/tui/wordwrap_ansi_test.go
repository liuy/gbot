package tui

import (
	"strings"
	"testing"
)

func TestWordWrap_AnsiCodesNotSplit(t *testing.T) {
	t.Parallel()
	input := "\x1b[1mbold text\x1b[22m rest"
	result := wordWrap(input, 10)
	if !strings.Contains(result, "\x1b[1m") {
		t.Errorf("bold start code should be intact, got: %q", result)
	}
	if !strings.Contains(result, "\x1b[22m") {
		t.Errorf("bold end code should be intact, got: %q", result)
	}
}

func TestWordWrap_AnsiCodes_PreservesColor(t *testing.T) {
	t.Parallel()
	input := "\x1b[38;5;148mconst\x1b[0m\x1b[38;5;231m.\x1b[0m\x1b[38;5;148mPrintln\x1b[0m"
	result := wordWrap(input, 20)
	for _, code := range []string{"\x1b[38;5;148m", "\x1b[0m", "\x1b[38;5;231m"} {
		count := strings.Count(result, code)
		expected := strings.Count(input, code)
		if count != expected {
			t.Errorf("ANSI code %q count=%d, expected=%d, result=%q", code, count, expected, result)
		}
	}
}
