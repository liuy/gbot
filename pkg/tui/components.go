package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Input — source: components/Input.tsx
// ---------------------------------------------------------------------------

// Input is a single-line text input component.
// Source: components/Input.tsx → bubbletea textarea replacement.
// Cursor is a rune index (not byte offset) to support multibyte characters.
type Input struct {
	value       []rune
	cursor      int // rune index
	focused     bool
	placeholder string
	width       int
}

// NewInput creates a new Input component.
func NewInput() *Input {
	return &Input{
		placeholder: "Type a message...",
		focused:     true,
	}
}

// Focus focuses the input.
func (i *Input) Focus() {
	i.focused = true
}

// Blur blurs the input.
func (i *Input) Blur() {
	i.focused = false
}

// Focused returns whether the input is focused.
func (i *Input) Focused() bool {
	return i.focused
}

// Value returns the current input value as a string.
func (i *Input) Value() string {
	return string(i.value)
}

// SetValue sets the input value.
func (i *Input) SetValue(v string) {
	i.value = []rune(v)
	i.cursor = len(i.value)
}

// Reset clears the input.
func (i *Input) Reset() {
	i.value = nil
	i.cursor = 0
}

// SetWidth sets the input width.
func (i *Input) SetWidth(w int) {
	i.width = w
}

// InsertChar inserts a character at the cursor position.
func (i *Input) InsertChar(ch rune) {
	if i.cursor > len(i.value) {
		i.cursor = len(i.value)
	}
	i.value = append(i.value[:i.cursor], append([]rune{ch}, i.value[i.cursor:]...)...)
	i.cursor++
}

// Backspace deletes the rune before the cursor.
func (i *Input) Backspace() {
	if i.cursor > 0 {
		i.value = append(i.value[:i.cursor-1], i.value[i.cursor:]...)
		i.cursor--
	}
}

// DeleteWord deletes the word before the cursor.
func (i *Input) DeleteWord() {
	if i.cursor == 0 {
		return
	}
	// Skip trailing spaces
	pos := i.cursor - 1
	for pos > 0 && i.value[pos] == ' ' {
		pos--
	}
	// Skip word characters
	for pos > 0 && i.value[pos-1] != ' ' {
		pos--
	}
	i.value = append(i.value[:pos], i.value[i.cursor:]...)
	i.cursor = pos
}

// CursorLeft moves the cursor left one rune.
func (i *Input) CursorLeft() {
	if i.cursor > 0 {
		i.cursor--
	}
}

// CursorRight moves the cursor right one rune.
func (i *Input) CursorRight() {
	if i.cursor < len(i.value) {
		i.cursor++
	}
}

// Home moves cursor to start.
func (i *Input) Home() {
	i.cursor = 0
}

// End moves cursor to end.
func (i *Input) End() {
	i.cursor = len(i.value)
}

// View renders the input.
func (i *Input) View() string {
	promptStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	inputStyle := lipgloss.NewStyle()

	prompt := promptStyle.Render("> ")
	text := string(i.value)
	if text == "" && !i.focused {
		text = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(i.placeholder)
	}

	// Build the display with cursor
	if i.focused {
		before := string(i.value[:min(i.cursor, len(i.value))])
		var cursorChar string
		if i.cursor < len(i.value) {
			cursorChar = string(i.value[i.cursor])
		} else {
			cursorChar = " "
		}
		after := ""
		if i.cursor+1 < len(i.value) {
			after = string(i.value[i.cursor+1:])
		}

		cursorStyle := lipgloss.NewStyle().Background(lipgloss.Color("15")).Foreground(lipgloss.Color("0"))
		display := before + cursorStyle.Render(cursorChar) + after
		return prompt + inputStyle.Render(display)
	}

	return prompt + inputStyle.Render(text)
}

// ---------------------------------------------------------------------------
// StatusBar — source: components/StatusBar.tsx
// ---------------------------------------------------------------------------

// StatusBar shows model info, token usage, and streaming status.
// Source: components/StatusBar.tsx → bubbletea status bar.
type StatusBar struct {
	model       string
	streaming   bool
	inputTokens int
	outTokens   int
	width       int
	err         string
}

// NewStatusBar creates a new status bar.
func NewStatusBar() StatusBar {
	return StatusBar{}
}

// SetModel sets the displayed model name.
func (s *StatusBar) SetModel(m string) {
	s.model = m
}

// SetStreaming sets the streaming indicator.
func (s *StatusBar) SetStreaming(v bool) {
	s.streaming = v
}

// SetUsage updates token counters.
func (s *StatusBar) SetUsage(in, out int) {
	s.inputTokens = in
	s.outTokens = out
}

// SetWidth sets the bar width.
func (s *StatusBar) SetWidth(w int) {
	s.width = w
}

// SetError sets an error message.
func (s *StatusBar) SetError(msg string) {
	s.err = msg
}

// View renders the status bar.
func (s StatusBar) View() string {
	barStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1)

	modelStr := s.model
	if modelStr == "" {
		modelStr = "gbot"
	}

	left := fmt.Sprintf(" %s", modelStr)

	if s.streaming {
		spinnerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
		left += spinnerStyle.Render(" [working...]")
	}

	if s.err != "" {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
		left += errStyle.Render(fmt.Sprintf(" err: %s", s.err))
	}

	right := fmt.Sprintf("in:%d out:%d ", s.inputTokens, s.outTokens)

	// Pad to fill width
	content := left + right
	avail := s.width - len(stripAnsi(content))
	if avail > 0 {
		content = left + strings.Repeat(" ", avail) + right
	}

	return barStyle.Render(content)
}

// ---------------------------------------------------------------------------
// Spinner — source: components/Spinner.tsx
// ---------------------------------------------------------------------------

// Spinner is a simple animated spinner.
// Source: components/Spinner.tsx → simplified bubbletea spinner.
type Spinner struct {
	frames []string
	idx    int
	active bool
}

// NewSpinner creates a new Spinner.
func NewSpinner() Spinner {
	return Spinner{
		frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		idx:    0,
		active: false,
	}
}

// Tick advances the spinner one frame.
func (s *Spinner) Tick() {
	if !s.active {
		return
	}
	s.idx = (s.idx + 1) % len(s.frames)
}

// Start activates the spinner.
func (s *Spinner) Start() {
	s.active = true
}

// Stop deactivates the spinner.
func (s *Spinner) Stop() {
	s.active = false
	s.idx = 0
}

// Active returns whether the spinner is active.
func (s *Spinner) Active() bool {
	return s.active
}

// View renders the current spinner frame.
func (s Spinner) View() string {
	if !s.active {
		return ""
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	return style.Render(s.frames[s.idx])
}

// ---------------------------------------------------------------------------
// MessageView — source: components/MessageView.tsx
// ---------------------------------------------------------------------------

// MessageView renders a single conversation message.
// Source: components/MessageView.tsx → bubbletea message rendering.
type MessageView struct {
	Role      string // "user", "assistant", "system"
	Content   string
	ToolCalls []ToolCallView
}

// ToolCallView renders a tool invocation within a message.
// Source: components/ToolCallView.tsx.
type ToolCallView struct {
	Name    string
	Input   string
	Output  string
	IsError bool
	Done    bool
}

// View renders the message with word wrapping at the given width.
func (m MessageView) View(width int) string {
	if width < 10 {
		width = 10
	}

	var sb strings.Builder

	var prefix string
	var content string
	switch m.Role {
	case "user":
		userStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
		prefix = userStyle.Render("You: ")
		content = m.Content
	case "assistant":
		asstStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
		prefix = asstStyle.Render("gbot: ")
		content = Render(m.Content)
	default:
		prefix = ""
		content = m.Content
	}

	// Account for prefix length in available width
	availWidth := width - 6 // prefix + padding margin
	if availWidth < 10 {
		availWidth = 10
	}

	// Word-wrap the content
	wrapped := wordWrap(content, availWidth)
	sb.WriteString(prefix)
	sb.WriteString(wrapped)

	// Render tool calls
	for _, tc := range m.ToolCalls {
		sb.WriteString("\n")
		toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

		if tc.Done {
			if tc.IsError {
				sb.WriteString(errStyle.Render(fmt.Sprintf("  [%s] ERROR", tc.Name)))
				if tc.Output != "" {
					sb.WriteString("\n  " + wordWrap(tc.Output, availWidth-2))
				}
			} else {
				sb.WriteString(toolStyle.Render(fmt.Sprintf("  [%s] done", tc.Name)))
				if tc.Output != "" && len(tc.Output) < 200 {
					sb.WriteString("\n  " + wordWrap(tc.Output, availWidth-2))
				}
			}
		} else {
			sb.WriteString(toolStyle.Render(fmt.Sprintf("  [%s] running...", tc.Name)))
			if tc.Input != "" && len(tc.Input) < 200 {
				sb.WriteString("\n  " + wordWrap(tc.Input, availWidth-2))
			}
		}
	}

	sb.WriteString("\n")
	return sb.String()
}

// wordWrap wraps text to the given width, breaking at word boundaries.
// For CJK characters, allows breaking between any characters since CJK
// doesn't use spaces as word boundaries.
// ANSI escape sequences are preserved intact — never split across lines.
func wordWrap(text string, width int) string {
	if width <= 0 {
		return text
	}

	var lines []string
	var currentLine strings.Builder
	currentLen := 0

	i := 0
	for i < len(text) {
		// Handle newlines
		if text[i] == '\n' {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLen = 0
			i++
			continue
		}

		// Handle ANSI escape sequences — consume entirely, no width counted
		if text[i] == '\x1b' {
			seq := consumeAnsiEscape(text[i:])
			currentLine.WriteString(seq)
			i += len(seq)
			continue
		}

		// Visible character
		r, size := utf8.DecodeRuneInString(text[i:])
		rw := runeDisplayWidth(r)

		if currentLen+rw > width && currentLen > 0 {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLen = 0
		}

		currentLine.WriteRune(r)
		currentLen += rw
		i += size
	}

	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return strings.Join(lines, "\n")
}

// consumeAnsiEscape consumes a complete ANSI escape sequence from the start of s.
// Supports CSI (\x1b[...final), OSC (\x1b]...\x07 or \x1b]...\x1b\\), and
// other 2-byte sequences (\x1b + one char).
func consumeAnsiEscape(s string) string {
	if len(s) < 2 || s[0] != '\x1b' {
		return s[:1]
	}

	switch s[1] {
	case '[':
		// CSI sequence: \x1b[ <params> <intermediate> <final>
		// Final byte is 0x40..0x7E
		j := 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7E) {
			j++
		}
		if j < len(s) {
			j++ // include final byte
		}
		return s[:j]
	case ']':
		// OSC sequence: \x1b] ... (BEL \x07 or ST \x1b\\)
		j := 2
		for j < len(s) {
			if s[j] == '\x07' {
				j++ // include BEL
				break
			}
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				j += 2 // include ST (\x1b\\)
				break
			}
			j++
		}
		return s[:j]
	default:
		// Other escape: \x1b + one char (e.g. \x1b\\)
		return s[:2]
	}
}

// runeDisplayWidth returns the display width of a rune.
// CJK characters are typically 2 columns wide.
func runeDisplayWidth(r rune) int {
	// Fast path for ASCII
	if r >= 0x20 && r <= 0x7E {
		return 1
	}
	if r < 0x80 {
		return 0 // control chars
	}
	// CJK ranges — these are double-width in most terminals
	switch {
	case r >= 0x1100 && r <= 0x115F: // Hangul Jamo
		return 2
	case r >= 0x2E80 && r <= 0x303E: // CJK Misc
		return 2
	case r >= 0x3040 && r <= 0x9FFF: // Hiragana, Katakana, CJK Unified
		return 2
	case r >= 0xAC00 && r <= 0xD7AF: // Hangul Syllables
		return 2
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility
		return 2
	case r >= 0xFE30 && r <= 0xFE6F: // CJK Forms
		return 2
	case r >= 0xFF01 && r <= 0xFF60: // Fullwidth Forms
		return 2
	case r >= 0xFFE0 && r <= 0xFFE6: // Fullwidth Signs
		return 2
	case r >= 0x20000 && r <= 0x2FFFD: // CJK Extension B+
		return 2
	case r >= 0x30000 && r <= 0x3FFFD: // CJK Extension G+
		return 2
	}
	return 1
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stripAnsi removes ANSI escape sequences from a string (simplified).
func stripAnsi(s string) string {
	// Simple approach: count only printable chars
	var n int
	inEscape := false
	for _, ch := range s {
		if ch == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
				inEscape = false
			}
			continue
		}
		n++
	}
	return strings.Repeat("x", n)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
