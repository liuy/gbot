package tui

import (
	"strings"
	"testing"
)

// TestWordWrap_ColorResetAndReEmit verifies that when wordWrap splits a colored
// span mid-token, it emits reset at the wrap point and re-emits the active color
// at the start of the new line.
//
// Given inline code `NewTaskGet` rendered as:
//
//	\x1b[38;5;15m\x1b[48;5;62mNewTaskGet\x1b[0m
//
// When wrapped at "NewT|askGet":
//   - Line N must end with \x1b[0m (close the open color)
//   - Line N+1 must start with \x1b[38;5;15m\x1b[48;5;62m (re-open the color)
//   - Line N+1 must still contain "askGet\x1b[0m" (rest of token + original reset)
func TestWordWrap_ColorResetAndReEmit(t *testing.T) {
	// Text with inline code that will be wrapped mid-token at width 60
	input := "There is a file that did not appear in the glob results earlier. " +
		"Let me read it (it must exist since the grep found `NewTaskGet`), " +
		"and also find where the production TaskList is initialized."

	rendered := Render(input)
	wrapped := wordWrap(rendered, 60)
	lines := strings.Split(wrapped, "\n")

	// Find the line that ends with the wrapped token (should have "NewT" + reset)
	found := false
	for i := 0; i < len(lines)-1; i++ {
		line := lines[i]
		if strings.Contains(line, "NewT") && strings.Contains(line, "\x1b[48;5;62m") {
			// Line must end with reset after the wrapped portion
			if !strings.HasSuffix(line, "\x1b[0m") {
				t.Errorf("line %d: wrapped mid-color but does not end with \\x1b[0m:\n  %q",
					i, strings.ReplaceAll(line, "\x1b", "\\x1b"))
			}
			// Next line must re-emit the inline code color
			next := lines[i+1]
			if !strings.HasPrefix(next, "\x1b[38;5;15m\x1b[48;5;62m") {
				t.Errorf("line %d: continuation line does not re-emit color codes:\n  %q",
					i+1, strings.ReplaceAll(next, "\x1b", "\\x1b"))
			}
			// Next line must contain the rest of the token
			if !strings.Contains(next, "askGet\x1b[0m") {
				t.Errorf("line %d: continuation line missing 'askGet' + reset:\n  %q",
					i+1, strings.ReplaceAll(next, "\x1b", "\\x1b"))
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("did not find a line wrapping mid-token 'NewTaskGet' — test setup needs adjustment")
	}
}

// TestWordWrap_CodeBlock_ColorResetAndReEmit verifies the same for chroma-highlighted
// code blocks where a long identifier gets wrapped.
func TestWordWrap_CodeBlock_ColorResetAndReEmit(t *testing.T) {
	// Long function name that will wrap at narrow width
	code := "```go\nprocessNewTaskGetResultsFromDatabase(ctx)\n```"
	rendered := Render(code)

	for _, width := range []int{25, 30, 40} {
		wrapped := wordWrap(rendered, width)
		lines := strings.Split(wrapped, "\n")

		for i := 0; i < len(lines)-1; i++ {
			if hasOpenColorAtEOL(lines[i]) {
				t.Errorf("width=%d line %d has open color at EOL:\n  %q",
					width, i, strings.ReplaceAll(lines[i], "\x1b", "\\x1b"))
			}
		}
	}
}

// hasOpenColorAtEOL checks whether a line has unclosed SGR color codes at the end.
func hasOpenColorAtEOL(line string) bool {
	active := 0
	j := 0
	for j < len(line) {
		if line[j] == '\x1b' {
			idx := strings.IndexByte(line[j:], 'm')
			if idx >= 0 && j+2 <= len(line) {
				code := line[j+2 : j+idx]
				if code == "0" {
					active = 0
				} else {
					active++
				}
				j = j + idx + 1
				continue
			}
		}
		j++
	}
	return active > 0
}
