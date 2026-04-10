package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// dot is a bullet indicator rendered before tool calls.
var dot = "●"

// Pre-cached styles to avoid creating new lipgloss.Style on every render call.
var (
	styleDotError   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDotSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleDotDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	styleNameBold   = lipgloss.NewStyle().Bold(true)
	styleTimeDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	styleDim        = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	stylePrompt     = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
)

// ---------------------------------------------------------------------------
// ContentBlock types — interleaved text + tool rendering
// ---------------------------------------------------------------------------

// ContentBlockType distinguishes text vs tool content blocks.
type ContentBlockType int

const (
	BlockText ContentBlockType = iota
	BlockTool
)

// ContentBlock represents a single block in an assistant message.
type ContentBlock struct {
	Type     ContentBlockType
	Text     string       // for BlockText
	ToolCall ToolCallView // for BlockTool
}

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

// DeleteForward deletes the rune at the cursor position (forward delete).
func (i *Input) DeleteForward() {
	if i.cursor < len(i.value) {
		i.value = append(i.value[:i.cursor], i.value[i.cursor+1:]...)
	}
}

// PrevWord moves the cursor to the start of the previous word.
// Source: useTextInput.ts — Alt+B / Ctrl+Left
func (i *Input) PrevWord() {
	if i.cursor == 0 {
		return
	}
	pos := i.cursor - 1
	for pos > 0 && i.value[pos] == ' ' {
		pos--
	}
	for pos > 0 && i.value[pos-1] != ' ' {
		pos--
	}
	i.cursor = pos
}

// NextWord moves the cursor to the start of the next word.
// Source: useTextInput.ts — Alt+F / Ctrl+Right
func (i *Input) NextWord() {
	pos := i.cursor
	// Skip current word
	for pos < len(i.value) && i.value[pos] != ' ' {
		pos++
	}
	// Skip spaces
	for pos < len(i.value) && i.value[pos] == ' ' {
		pos++
	}
	i.cursor = pos
}

// DeleteWordForward deletes from cursor to start of next word.
// Source: useTextInput.ts — Alt+D (killWord)
func (i *Input) DeleteWordForward() string {
	pos := i.cursor
	for pos < len(i.value) && i.value[pos] != ' ' {
		pos++
	}
	for pos < len(i.value) && i.value[pos] == ' ' {
		pos++
	}
	deleted := string(i.value[i.cursor:pos])
	i.value = append(i.value[:i.cursor], i.value[pos:]...)
	return deleted
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
	promptStyle := stylePrompt
	inputStyle := lipgloss.NewStyle()

	prompt := promptStyle.Render("❯ ")
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
type MessageView struct {
	Role        string // "user", "assistant", "system"
	Blocks      []ContentBlock // interleaved content: text and tool blocks in order
	ExpandTools bool   // when true, show full tool output instead of collapsed
}

// ToolCallView renders a tool invocation within a message.
type ToolCallView struct {
	ID      string
	Name    string // raw tool name (e.g., "Bash", "Grep")
	Summary string // context-aware display name (e.g., "Listing 1 directory", "Found 5 matches")
	Input   string
	Output  string
	IsError bool
	Done    bool
	Elapsed time.Duration
}

// View renders the message with word wrapping at the given width.
// When expand is true, tool output is shown fully instead of collapsed.
func (m MessageView) View(width int, expand bool) string {
	if width < 10 {
		width = 10
	}

	var sb strings.Builder
	availWidth := width - 2 // minimal margin
	if availWidth < 10 {
		availWidth = 10
	}

	// Render using Blocks (interleaved text+tool, per TS).
	if len(m.Blocks) > 0 {
		isUser := m.Role == "user"
		for i, blk := range m.Blocks {
			switch blk.Type {
			case BlockText:
				if blk.Text != "" {
					wrapped := wordWrap(Render(blk.Text), availWidth)
					if isUser {
						wrapped = prefixUserLine(wrapped, availWidth)
					}
					sb.WriteString(wrapped)
					sb.WriteString("\n")
				}
			case BlockTool:
				blk.renderToolCall(&sb, availWidth, expand)
				sb.WriteString("\n")
				// Blank line between completed tool and following text block
				if blk.ToolCall.Done && i+1 < len(m.Blocks) && m.Blocks[i+1].Type == BlockText {
					sb.WriteString("\n")
				}
			}
		}
		return sb.String()
	}
	return ""
}

// resultPrefix is the indentation prefix for tool output lines.
const resultPrefix = "\u23bf "

// prefixLine returns the prefix for line index i: first line gets resultPrefix,
// subsequent lines get spaces of equal width for alignment.
func prefixLine(i int, text string) string {
	if i == 0 {
		return resultPrefix + text
	}
	return "  " + text
}

// renderToolCall renders a tool block using ● dot indicator.
// When expand is true, full tool output is shown; otherwise output is collapsed.
// Format matches TS: ● ToolName(summary)
//
//	⎿  output line 1
//	⎿  output line 2
//	⎿  … +N lines (ctrl+o to expand)
func (blk ContentBlock) renderToolCall(sb *strings.Builder, availWidth int, expand bool) {
	if blk.Type != BlockTool {
		return
	}
	tc := blk.ToolCall

	// Determine dot color per TS ToolUseLoader.tsx:
	// When Done: isError→red(9), else→green(10)
	// When !Done: dim(8) — "running"
	var dotStr string
	if tc.Done {
		if tc.IsError {
			dotStr = styleDotError.Render(dot)
		} else {
			dotStr = styleDotSuccess.Render(dot)
		}
	} else {
		dotStr = styleDotDim.Render(dot)
	}

	// Header: ● ToolName(summary)
	toolName := styleNameBold.Render(tc.Name)

	if !tc.Done {
		// Running state: dim name, & suffix, no summary
		fmt.Fprintf(sb, "%s %s&", dotStr, styleDim.Render(tc.Name))
		return
	}

	// Done state
	if tc.IsError {
		fmt.Fprintf(sb, "%s %s", dotStr, toolName)
		if tc.Summary != "" {
			fmt.Fprintf(sb, "(%s)", tc.Summary)
		}
		if tc.Output != "" {
			sb.WriteString("\n" + formatToolOutput(tc.Output, true, expand, availWidth-2))
		}
	} else {
		fmt.Fprintf(sb, "%s %s", dotStr, toolName)
		if tc.Summary != "" {
			fmt.Fprintf(sb, "(%s)", tc.Summary)
		}
		if tc.Elapsed > 0 {
			sb.WriteString(styleTimeDim.Render(" (" + formatDuration(tc.Elapsed) + ")"))
		}
		if tc.Output != "" {
			sb.WriteString("\n" + formatToolOutput(tc.Output, false, expand, availWidth-2))
		}
	}
}

// formatToolOutput formats tool output with ⎿ prefix and line collapse.
// Normal: show first 3 lines, then "… +N lines (ctrl+o to expand)".
// Error: show first 10 lines, red, "… +N lines (ctrl+o to see all)".
func formatToolOutput(output string, isError bool, expand bool, availWidth int) string {
	if output == "" {
		return ""
	}
	// Trim trailing newlines to avoid empty prefixed lines
	output = strings.TrimRight(output, "\n")
	lines := strings.Split(output, "\n")
	maxLines := 3
	if isError {
		maxLines = 10
	}

	// Show all lines if expanded or few enough lines
	if expand || len(lines) <= maxLines+1 {
		var sb strings.Builder
		for i, line := range lines {
			sb.WriteString(prefixLine(i, wordWrap(line, availWidth)) + "\n")
		}
		return strings.TrimRight(sb.String(), "\n")
	}

	// Collapse: show first maxLines lines + hint
	shown := lines[:maxLines]
	hidden := len(lines) - maxLines

	var hint string
	if isError {
		hint = styleDim.Render(fmt.Sprintf("… +%d lines (ctrl+o to see all)", hidden))
	} else {
		hint = styleDim.Render(fmt.Sprintf("… +%d lines (ctrl+o to expand)", hidden))
	}

	var sb strings.Builder
	for i, line := range shown {
		sb.WriteString(prefixLine(i, wordWrap(line, availWidth)) + "\n")
	}
	sb.WriteString(prefixLine(len(shown), hint))
	return sb.String()
}

// firstMeaningfulLine extracts the first non-empty line from text.
func firstMeaningfulLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// wordWrap wraps text to the given width, breaking at word boundaries.
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

// prefixUserLine adds ❯ prefix to the first line and aligns continuation lines.
// Lines are split by \n from wordWrap output.
func prefixUserLine(text string, width int) string {
	prompt := stylePrompt.Render("❯ ")
	promptLen := 2 // width of ❯ in display cells (1 cell)
	lines := strings.Split(text, "\n")
	// First line: prepend prompt
	lines[0] = prompt + lines[0]
	// Continuation lines: indent to align with text after prompt
	indent := strings.Repeat(" ", promptLen)
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// consumeAnsiEscape consumes a complete ANSI escape sequence from the start of s.
func consumeAnsiEscape(s string) string {
	if len(s) < 2 || s[0] != '\x1b' {
		return s[:1]
	}

	switch s[1] {
	case '[':
		j := 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7E) {
			j++
		}
		if j < len(s) {
			j++
		}
		return s[:j]
	case ']':
		j := 2
		for j < len(s) {
			if s[j] == '\x07' {
				j++
				break
			}
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				j += 2
				break
			}
			j++
		}
		return s[:j]
	default:
		return s[:2]
	}
}

// runeDisplayWidth returns the display width of a rune.
func runeDisplayWidth(r rune) int {
	if r >= 0x20 && r <= 0x7E {
		return 1
	}
	if r < 0x80 {
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F:
		return 2
	case r >= 0x2E80 && r <= 0x303E:
		return 2
	case r >= 0x3040 && r <= 0x9FFF:
		return 2
	case r >= 0xAC00 && r <= 0xD7AF:
		return 2
	case r >= 0xF900 && r <= 0xFAFF:
		return 2
	case r >= 0xFE30 && r <= 0xFE6F:
		return 2
	case r >= 0xFF01 && r <= 0xFF60:
		return 2
	case r >= 0xFFE0 && r <= 0xFFE6:
		return 2
	case r >= 0x20000 && r <= 0x2FFFD:
		return 2
	case r >= 0x30000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var result strings.Builder
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
		result.WriteRune(ch)
	}
	return result.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
