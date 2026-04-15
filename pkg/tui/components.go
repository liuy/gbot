package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// dot is a bullet indicator rendered before tool calls.
var dot = "●"

// thinkingStar is the symbol for thinking blocks.
var thinkingStar = "✦"

// Pre-cached styles to avoid creating new lipgloss.Style on every render call.
var (
	styleDotError   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDotSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleDotDim = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	styleNameBold   = lipgloss.NewStyle().Bold(true)
	styleTimeDim = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
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
	BlockThinking // thinking block with collapsible content
	BlockStats    // TUI-only stats line embedded in assistant message
)

// ContentBlock represents a single block in an assistant message.
type ContentBlock struct {
	Type     ContentBlockType
	Text     string       // for BlockText
	ToolCall ToolCallView // for BlockTool
	Thinking ThinkingView // for BlockThinking
}

// ---------------------------------------------------------------------------
// Input — source: components/Input.tsx
// ---------------------------------------------------------------------------

// promptDisplayWidth is the display width of "❯ " (❯ = 1 cell + space = 1 cell).
const promptDisplayWidth = 2

// wrappedLine represents one visual line after wrapping the input value.
type wrappedLine struct {
	runes       []rune // runes on this visual line
	startOffset int    // rune index into original value where this line starts
}

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

// wrapLines wraps the input value into visual lines based on display width.
// Source: Cursor.ts — MeasuredText.measureWrappedText() (simplified rune-based version).
func (i *Input) wrapLines() []wrappedLine {
	if i.width <= 0 || len(i.value) == 0 {
		return []wrappedLine{{runes: i.value, startOffset: 0}}
	}

	// First line has less room due to prompt prefix
	availFirst := i.width - promptDisplayWidth
	if availFirst < 1 {
		availFirst = 1
	}
	availRest := i.width
	if availRest < 1 {
		availRest = 1
	}

	var lines []wrappedLine
	var current []rune
	currentLen := 0
	lineStart := 0
	avail := availFirst

	for idx, r := range i.value {
		rw := runeDisplayWidth(r)
		if currentLen+rw > avail && currentLen > 0 {
			lines = append(lines, wrappedLine{runes: current, startOffset: lineStart})
			current = nil
			currentLen = 0
			lineStart = idx
			avail = availRest
		}
		current = append(current, r)
		currentLen += rw
	}

	if len(current) > 0 || len(lines) == 0 {
		lines = append(lines, wrappedLine{runes: current, startOffset: lineStart})
	}

	return lines
}

// cursorLine returns the index of the wrapped line containing the cursor.
func (i *Input) cursorLine(lines []wrappedLine) int {
	for idx, line := range lines {
		end := line.startOffset + len(line.runes)
		if i.cursor <= end {
			return idx
		}
	}
	return len(lines) - 1
}

// HasWrappedLines returns true if the current value wraps to multiple visual lines.
func (i *Input) HasWrappedLines() bool {
	lines := i.wrapLines()
	return len(lines) > 1
}

// CursorUp moves the cursor up one wrapped line.
// Source: Cursor.ts — Cursor.up()
// Returns true if cursor moved, false if already on first line.
func (i *Input) CursorUp() bool {
	lines := i.wrapLines()
	if len(lines) <= 1 {
		return false
	}
	cl := i.cursorLine(lines)
	if cl == 0 {
		return false
	}
	// Find current column (display width from line start to cursor)
	prevLine := lines[cl-1]
	curLine := lines[cl]
	colInLine := 0
	for _, r := range curLine.runes[:i.cursor-curLine.startOffset] {
		colInLine += runeDisplayWidth(r)
	}
	// Move to same column on previous line, clamped to line end
	newOffset := prevLine.startOffset
	colAccum := 0
	for idx, r := range prevLine.runes {
		rw := runeDisplayWidth(r)
		if colAccum+rw > colInLine {
			break
		}
		colAccum += rw
		newOffset = prevLine.startOffset + idx + 1
	}
	i.cursor = newOffset
	return true
}

// CursorDown moves the cursor down one wrapped line.
// Source: Cursor.ts — Cursor.down()
// Returns true if cursor moved, false if already on last line.
func (i *Input) CursorDown() bool {
	lines := i.wrapLines()
	if len(lines) <= 1 {
		return false
	}
	cl := i.cursorLine(lines)
	if cl >= len(lines)-1 {
		return false
	}
	// Find current column
	curLine := lines[cl]
	nextLine := lines[cl+1]
	colInLine := 0
	for _, r := range curLine.runes[:i.cursor-curLine.startOffset] {
		colInLine += runeDisplayWidth(r)
	}
	// Move to same column on next line, clamped to line end
	newOffset := nextLine.startOffset
	colAccum := 0
	for idx, r := range nextLine.runes {
		rw := runeDisplayWidth(r)
		if colAccum+rw > colInLine {
			break
		}
		colAccum += rw
		newOffset = nextLine.startOffset + idx + 1
	}
	i.cursor = newOffset
	return true
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

// View renders the input with wrapping support for long text.
// Source: Cursor.ts — Cursor.render() (simplified rune-based version).
func (i *Input) View() string {
	promptStyle := stylePrompt
	prompt := promptStyle.Render("❯ ")
	indent := strings.Repeat(" ", promptDisplayWidth)

	// Empty value: show placeholder or cursor-only
	if len(i.value) == 0 {
		if !i.focused {
			ph := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render(i.placeholder)
			return prompt + ph
		}
		cursorStyle := lipgloss.NewStyle().Background(lipgloss.Color("15")).Foreground(lipgloss.Color("0"))
		return prompt + cursorStyle.Render(" ")
	}

	// No wrapping needed (width not set or single line)
	lines := i.wrapLines()
	if len(lines) <= 1 {
		return prompt + i.renderLineSingle(lines)
	}

	// Multi-line wrapped rendering
	cl := i.cursorLine(lines)
	cursorStyle := lipgloss.NewStyle().Background(lipgloss.Color("15")).Foreground(lipgloss.Color("0"))

	var sb strings.Builder
	for li, line := range lines {
		var rendered string
		if i.focused && li == cl {
			// Render this line with cursor
			cursorInLine := i.cursor - line.startOffset
			beforeRunes := line.runes[:min(cursorInLine, len(line.runes))]
			var cursorRune string
			if cursorInLine < len(line.runes) {
				cursorRune = string(line.runes[cursorInLine])
			} else {
				cursorRune = " "
			}
			afterRunes := line.runes[min(cursorInLine+1, len(line.runes)):]
			rendered = string(beforeRunes) + cursorStyle.Render(cursorRune) + string(afterRunes)
		} else {
			rendered = string(line.runes)
		}

		if li == 0 {
			sb.WriteString(prompt + rendered)
		} else {
			sb.WriteString("\n" + indent + rendered)
		}
	}
	return sb.String()
}

// renderLineSingle renders a single-line input with cursor (no wrapping).
func (i *Input) renderLineSingle(lines []wrappedLine) string {
	if len(lines) == 0 || len(lines[0].runes) == 0 {
		cursorStyle := lipgloss.NewStyle().Background(lipgloss.Color("15")).Foreground(lipgloss.Color("0"))
		return cursorStyle.Render(" ")
	}
	if !i.focused {
		return string(i.value)
	}

	cursorStyle := lipgloss.NewStyle().Background(lipgloss.Color("15")).Foreground(lipgloss.Color("0"))
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
	return before + cursorStyle.Render(cursorChar) + after
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
		frames: []string{"·", "•", "●", "⬤", "●", "•"},
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

// ThinkingView renders a thinking block within a message.
type ThinkingView struct {
	Text     string        // accumulated thinking text
	Duration time.Duration // set on ThinkingEnd
	Done     bool          // false during streaming, true after ThinkingEnd
}

// View renders the message with word wrapping at the given width.
// When expand is true, tool output is shown fully instead of collapsed.
func (m MessageView) View(width int, expand bool, toolDot string, noHint bool, maxOutputLines int) string {
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
				blk.renderToolCall(&sb, availWidth, expand, toolDot, noHint, maxOutputLines)
				sb.WriteString("\n")
				// Blank line between completed tool and following text block
				if blk.ToolCall.Done && i+1 < len(m.Blocks) && m.Blocks[i+1].Type == BlockText {
					sb.WriteString("\n")
				}
			case BlockThinking:
				blk.renderThinkingBlock(&sb, availWidth, expand, toolDot, noHint)
				sb.WriteString("\n")
			case BlockStats:
				sb.WriteString(blk.Text)
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}
	return ""
}

// resultPrefix is the indentation prefix for tool output lines.
// Using ASCII "|" to guarantee consistent display width across all terminals
// (CJK terminals render ⎿ as 2 cells, breaking alignment).
const resultPrefix = "| "

// prefixLine returns the prefix for line index i: first line gets resultPrefix,
// subsequent lines get spaces of equal width for alignment.
func prefixLine(i int, text string) string {
	if i == 0 {
		return resultPrefix + text
	}
	// Continuation lines: match the display width of resultPrefix using spaces
	prefixWidth := 0
	for _, r := range resultPrefix {
		prefixWidth += runeDisplayWidth(r)
	}
	return strings.Repeat(" ", prefixWidth) + text
}

// renderToolCall renders a tool block using ● dot indicator.
// When expand is true, full tool output is shown; otherwise output is collapsed.
// Format matches TS: ● ToolName(summary)
//
//	⎿  output line 1
//	⎿  output line 2
//	⎿  … +N lines (ctrl+o to expand)
func (blk ContentBlock) renderToolCall(sb *strings.Builder, availWidth int, expand bool, toolDot string, noHint bool, maxOutputLines int) {
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

	// Header: ● ToolName(summary) (elapsed)
	toolName := styleNameBold.Render(tc.Name)

	if !tc.Done {
		// Running state: spinner dot + bold name + summary + "running..."
		var runningDot string
		if toolDot != "" {
			runningDot = toolDot
		} else {
			runningDot = styleDotDim.Render(dot)
		}
		header := runningDot + " " + styleNameBold.Render(tc.Name)
		if tc.Summary != "" {
			header += fmt.Sprintf("(%s)", tc.Summary)
		}
		header += " " + styleDim.Render("running...")
		sb.WriteString(wordWrap(header, availWidth))
		return
	}

	// Done state — build header then wrap
	var hdr strings.Builder
	hdr.WriteString(dotStr)
	hdr.WriteByte(' ')
	hdr.WriteString(toolName)
	if tc.Summary != "" {
		fmt.Fprintf(&hdr, "(%s)", tc.Summary)
	}
	if tc.Elapsed > 0 {
		hdr.WriteString(styleTimeDim.Render(" (" + formatDuration(tc.Elapsed) + ")"))
	}
	sb.WriteString(wordWrap(hdr.String(), availWidth))

	if tc.Output != "" {
		isErr := tc.IsError
		sb.WriteString("\n" + formatToolOutput(tc.Output, isErr, expand, availWidth-2, noHint, maxOutputLines))
	}
}

// formatToolOutput formats tool output with ⎿ prefix and line collapse.
// Collapsed: show first 3 lines + hint (or 10 for errors).
// Expanded: show all lines, or last maxOutputLines if height-limited.
// maxOutputLines=0 means unlimited.
func formatToolOutput(output string, isError bool, expand bool, availWidth int, noHint bool, maxOutputLines int) string {
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

	// Show all lines if expanded (with optional height limit) or few enough lines
	if expand || len(lines) <= maxLines+1 {
		if expand && maxOutputLines > 0 && len(lines) > maxOutputLines {
			// Height-limited expanded: show last maxOutputLines + truncation notice
			shown := lines[len(lines)-maxOutputLines:]
			hidden := len(lines) - maxOutputLines
			var sb strings.Builder
			sb.WriteString(prefixLine(0, styleDim.Render(fmt.Sprintf("... %d lines truncated ...", hidden))) + "\n")
			for i, line := range shown {
				for j, wl := range strings.Split(wordWrap(line, availWidth), "\n") {
					sb.WriteString(prefixLine(i+j+1, wl) + "\n")
				}
			}
			return strings.TrimRight(sb.String(), "\n")
		}
		var sb strings.Builder
		lineIdx := 0
		for _, line := range lines {
			for _, wl := range strings.Split(wordWrap(line, availWidth), "\n") {
				sb.WriteString(prefixLine(lineIdx, wl) + "\n")
				lineIdx++
			}
		}
		return strings.TrimRight(sb.String(), "\n")
	}
	// Collapse: show first maxLines lines + hint
	shown := lines[:maxLines]
	hidden := len(lines) - maxLines

	var hint string
	if noHint {
		hint = styleDim.Render(fmt.Sprintf("… +%d lines", hidden))
	} else if isError {
		hint = styleDim.Render(fmt.Sprintf("… +%d lines (ctrl+o to see all)", hidden))
	} else {
		hint = styleDim.Render(fmt.Sprintf("… +%d lines (ctrl+o to expand)", hidden))
	}

	var sb strings.Builder
	lineIdx := 0
	for _, line := range shown {
		for _, wl := range strings.Split(wordWrap(line, availWidth), "\n") {
			sb.WriteString(prefixLine(lineIdx, wl) + "\n")
			lineIdx++
		}
	}
	sb.WriteString(prefixLine(len(shown), hint))
	return sb.String()
}


// Pre-cached styles for thinking blocks.
var (
	styleThinkingStar     = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
	styleThinkingDimGold  = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))
	styleThinkingContent  = lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Italic(true)
)

// renderThinkingBlock renders a thinking block using ✦ symbol.
// During streaming (Done=false): animated star + "Thinking..." + real-time content.
// After done (Done=true): static ✦ + duration + collapsed/expanded content.
// Title line matches tool block style (bold name + dim details).
// Content uses italic to distinguish from tool output.
func (blk ContentBlock) renderThinkingBlock(sb *strings.Builder, availWidth int, expand bool, toolDot string, noHint bool) {
	if blk.Type != BlockThinking {
		return
	}
	tv := blk.Thinking

	if !tv.Done {
		// Streaming state: blink ✦ via toolDot mechanism (same as tool running dot)
		var star string
		if toolDot != "" {
			// Visible blink frame: bright bold ✦
			star = styleThinkingStar.Render(thinkingStar)
		} else {
			// Invisible blink frame: dim ✦
			star = styleThinkingDimGold.Render(thinkingStar)
		}
		header := star + " " + styleNameBold.Render("Thinking") + styleDim.Render("...")
		sb.WriteString(wordWrap(header, availWidth))

		// Show streaming content (italic to distinguish from tool output)
		if tv.Text != "" {
			formatted := formatToolOutput(tv.Text, false, true, availWidth-2, noHint, 0)
			sb.WriteString("\n" + styleThinkingContent.Render(formatted))
		}
		return
	}

	// Done state: static gold bold ✦ Thought for X
	star := styleThinkingStar.Render(thinkingStar)
	var hdr strings.Builder
	hdr.WriteString(star)
	hdr.WriteByte(' ')
	hdr.WriteString(styleNameBold.Render("Thought"))
	if tv.Duration > 0 {
		hdr.WriteString(styleNameBold.Render(" for "))
		hdr.WriteString(styleDim.Render(formatDuration(tv.Duration)))
	}
	sb.WriteString(wordWrap(hdr.String(), availWidth))

	// Show content with collapse/expand (italic to distinguish from tool output)
	if tv.Text != "" {
		formatted := formatToolOutput(tv.Text, false, expand, availWidth-2, noHint, 0)
		sb.WriteString("\n" + styleThinkingContent.Render(formatted))
	}
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
// Uses go-runewidth for accurate terminal column width matching bubbletea/lipgloss.
func runeDisplayWidth(r rune) int {
	return runewidth.RuneWidth(r)
}
// isEmojiPresentation returns true for emoji that render as colorful by default.
// VS16 (U+FE0F) is redundant for these and causes visible artifacts on some
// terminals, breaking table alignment.
//
// Coverage:
//   - BMP: only codepoints with Unicode Emoji_Presentation=Yes
//   - SMP (U+1F000+): all emoji characters (modern terminals render
//     them colorful regardless of the Emoji_Presentation property)
//
// Source: unicode.org/Public/UCD/latest/ucd/emoji/emoji-data.txt
func isEmojiPresentation(r rune) bool {
	// SMP emoji (1F000+) — modern terminals render all as colorful
	if r >= 0x1F000 && r <= 0x1FAFF {
		return true
	}
	// BMP emoji with Emoji_Presentation=Yes
	switch {
	case
		r >= 0x231A && r <= 0x231B,
		r >= 0x23E9 && r <= 0x23EC,
		r == 0x23F0,
		r == 0x23F3,
		r >= 0x23F8 && r <= 0x23FA,
		r >= 0x25FD && r <= 0x25FE,
		r >= 0x2614 && r <= 0x2615,
		r >= 0x2648 && r <= 0x2653,
		r == 0x267F,
		r == 0x2693,
		r == 0x26A1,
		r >= 0x26AA && r <= 0x26AB,
		r >= 0x26BD && r <= 0x26BE,
		r >= 0x26C4 && r <= 0x26C5,
		r == 0x26CE,
		r == 0x26D4,
		r == 0x26EA,
		r >= 0x26F2 && r <= 0x26F3,
		r == 0x26F5,
		r == 0x26FA,
		r == 0x26FD,
		r == 0x2705,
		r >= 0x270A && r <= 0x270B,
		r == 0x2728,
		r == 0x274C,
		r == 0x274E,
		r >= 0x2753 && r <= 0x2755,
		r == 0x2757,
		r >= 0x2795 && r <= 0x2797,
		r == 0x27B0,
		r == 0x27BF,
		r >= 0x2B1B && r <= 0x2B1C,
		r == 0x2B50,
		r == 0x2B55:
		return true
	}
	return false
}

// stripRedundantVS16 removes VS16 (U+FE0F) from emoji that already have
// Emoji_Presentation=Yes (default colorful). These emoji don't need VS16,
// and some terminals render the redundant VS16 as a visible glyph,
// breaking table alignment and other layout.
//
// Emoji with Emoji_Presentation=No (default text presentation) keep VS16
// because they need it to switch to colorful rendering.
func stripRedundantVS16(s string) string {
	// Fast path: no VS16 in string
	if !strings.ContainsRune(s, '\uFE0F') {
		return s
	}
	runes := []rune(s)
	var out []rune
	for i, r := range runes {
		if r == '\uFE0F' && i > 0 && isEmojiPresentation(runes[i-1]) {
			continue // skip redundant VS16
		}
		out = append(out, r)
	}
	return string(out)
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
