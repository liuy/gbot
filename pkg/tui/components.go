package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/liuy/gbot/pkg/quota"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
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
	styleNameBold   = lipgloss.NewStyle().Bold(true)
	styleTimeDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	styleDim        = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
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
	BlockUser     // mid-turn queued user input, visual only
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

// promptLiteral is the prompt character shown before the input.
const promptLiteral = "❯ "

var (
	// renderedPrompt is the styled prompt, computed once at init.
	renderedPrompt = stylePrompt.Render(promptLiteral)
	// renderedPromptWidth is the display width of the styled prompt.
	renderedPromptWidth = lipgloss.Width(renderedPrompt)
	// mainCursorStyle is the inverted block cursor for the main input field.
	mainCursorStyle = lipgloss.NewStyle().Background(lipgloss.Color("15")).Foreground(lipgloss.Color("0"))
)

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

// Value returns the current input value as a string.
func (i *Input) Value() string {
	return string(i.value)
}

// SetValue sets the input value.
func (i *Input) SetValue(v string) {
	i.value = []rune(v)
	i.cursor = len(i.value)
}

// SetCursor sets the cursor position, clamped to [0, len(value)].
func (i *Input) SetCursor(pos int) {
	if pos < 0 {
		pos = 0
	}
	if pos > len(i.value) {
		pos = len(i.value)
	}
	i.cursor = pos
}

// Cursor returns the current cursor position.
func (i *Input) Cursor() int {
	return i.cursor
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
// All lines wrap at the same width — prompt/indent is handled by the caller.
func (i *Input) wrapLines() []wrappedLine {
	if len(i.value) == 0 {
		return []wrappedLine{{runes: i.value, startOffset: 0}}
	}
	if i.width <= 0 {
		return i.splitOnNewlines()
	}

	avail := max(i.width, 1)

	var lines []wrappedLine
	var current []rune
	currentLen := 0
	lineStart := 0

	for idx, r := range i.value {
		if r == '\n' {
			lines = append(lines, wrappedLine{runes: current, startOffset: lineStart})
			current = nil
			currentLen = 0
			lineStart = idx + 1
			continue
		}
		rw := runeDisplayWidth(r)
		if currentLen+rw > avail && currentLen > 0 {
			lines = append(lines, wrappedLine{runes: current, startOffset: lineStart})
			current = nil
			currentLen = 0
			lineStart = idx
		}
		current = append(current, r)
		currentLen += rw
	}

	// Always append the final line, even if empty after trailing \n.
	lines = append(lines, wrappedLine{runes: current, startOffset: lineStart})

	return lines
}

// splitOnNewlines splits the input value on hard newlines without word wrapping.
// Used when width is unknown (before WindowSizeMsg arrives).
func (i *Input) splitOnNewlines() []wrappedLine {
	var lines []wrappedLine
	start := 0
	for idx, r := range i.value {
		if r == '\n' {
			lines = append(lines, wrappedLine{runes: i.value[start:idx], startOffset: start})
			start = idx + 1
		}
	}
	lines = append(lines, wrappedLine{runes: i.value[start:], startOffset: start})
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

// InsertNewline inserts a newline character at the cursor position.
func (i *Input) InsertNewline() {
	i.InsertChar('\n')
}

// InsertString inserts a string at the cursor position.
// More efficient than calling InsertChar in a loop for multi-char strings.
func (i *Input) InsertString(s string) {
	runes := []rune(s)
	if len(runes) == 0 {
		return
	}
	i.value = append(i.value[:i.cursor], append(runes, i.value[i.cursor:]...)...)
	i.cursor += len(runes)
}

// Backspace deletes the rune before the cursor.
func (i *Input) Backspace() {
	if i.cursor > 0 {
		i.value = append(i.value[:i.cursor-1], i.value[i.cursor:]...)
		i.cursor--
	}
}

// pasteRefInputRe matches [Pasted text #N] or [Pasted text #N +L lines]
// in the input rune buffer for atomic backspace deletion.
var pasteRefInputRe = regexp.MustCompile(`^\[Pasted text #\d+(?: \+\d+ lines)?\]$`)

// BackspaceToken attempts to delete a paste reference token before the cursor.
// If the cursor is right after a [Pasted text #N ...] token, deletes it atomically.
// Otherwise falls back to single-rune backspace.
func (i *Input) BackspaceToken() {
	if i.cursor == 0 || i.value[i.cursor-1] != ']' {
		i.Backspace()
		return
	}
	start := -1
	for pos := i.cursor - 2; pos >= 0; pos-- {
		r := i.value[pos]
		if r == '[' {
			start = pos
			break
		}
		if r == '\n' {
			break
		}
	}
	if start >= 0 {
		candidate := string(i.value[start:i.cursor])
		if pasteRefInputRe.MatchString(candidate) {
			i.value = append(i.value[:start], i.value[i.cursor:]...)
			i.cursor = start
			return
		}
	}
	i.Backspace()
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

// View renders the input text with cursor highlighting.
// Returns pure text content — no prompt prefix or continuation indent.
// The caller (App) is responsible for prepending the prompt to the first line
// and indent to continuation lines.
func (i *Input) View() string {
	if len(i.value) == 0 {
		if !i.focused {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render(i.placeholder)
		}
		return mainCursorStyle.Render(" ")
	}

	lines := i.wrapLines()
	if len(lines) <= 1 {
		return i.renderLineSingle(lines)
	}

	// Multi-line wrapped rendering
	cl := i.cursorLine(lines)

	var sb strings.Builder
	for li, line := range lines {
		var rendered string
		if i.focused && li == cl {
			cursorInLine := i.cursor - line.startOffset
			beforeRunes := line.runes[:min(cursorInLine, len(line.runes))]
			var cursorRune string
			if cursorInLine < len(line.runes) {
				cursorRune = string(line.runes[cursorInLine])
			} else {
				cursorRune = " "
			}
			afterRunes := line.runes[min(cursorInLine+1, len(line.runes)):]
			rendered = string(beforeRunes) + mainCursorStyle.Render(cursorRune) + string(afterRunes)
		} else {
			rendered = string(line.runes)
		}

		if li == 0 {
			sb.WriteString(rendered)
		} else {
			sb.WriteString("\n" + rendered)
		}
	}
	return sb.String()
}

// renderLineSingle renders a single-line input with cursor (no wrapping).
func (i *Input) renderLineSingle(lines []wrappedLine) string {
	if len(lines) == 0 || len(lines[0].runes) == 0 {
		return mainCursorStyle.Render(" ")
	}
	if !i.focused {
		return string(i.value)
	}

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
	return before + mainCursorStyle.Render(cursorChar) + after
}

// ---------------------------------------------------------------------------
// StatusBar — source: components/StatusBar.tsx
// ---------------------------------------------------------------------------

// Pre-cached lipgloss styles for StatusBar rendering — avoids per-frame allocations.
var (
	statusGreenStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("120;200;120"))
	statusDimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("100;100;100"))
	statusYellowCtx  = lipgloss.NewStyle().Foreground(lipgloss.Color("230;200;50"))
	statusRedCtx     = lipgloss.NewStyle().Foreground(lipgloss.Color("230;70;70"))
	statusSep        = statusDimStyle.Render(" • ")
)

// StatusBar shows model info, quota, context usage, and tool count below the input.
// Design: "model • 85%/2h30m • 84K/200K • 13 tools" — green model, quota/context
// default→yellow(≥80% used)→red(≥90% used), gray separators.
type StatusBar struct {
	model        string
	streaming    bool
	usage        types.Usage
	width        int
	err          string
	info         string
	contextUsed  int // current context input tokens
	contextTotal int // model context window size
	toolCount    int
	quota        *quota.Info // nil = hidden (no provider with quota endpoint)
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

// IsStreaming reports whether the status bar is currently showing the
// streaming indicator. Read by switchEngine when binding to a target
// engine whose ReplState is already streaming.
func (s *StatusBar) IsStreaming() bool {
	return s.streaming
}

// SetUsage updates token counters.
func (s *StatusBar) SetUsage(u types.Usage) {
	s.usage = u
}

// SetWidth sets the bar width.
func (s *StatusBar) SetWidth(w int) {
	s.width = w
}

// SetError sets an error message.
func (s *StatusBar) SetError(msg string) {
	s.err = msg
}

// SetInfo sets an info message.
func (s *StatusBar) SetInfo(msg string) {
	s.info = msg
}

// SetContext sets the context window usage for the status bar.
func (s *StatusBar) SetContext(used, total int) {
	s.contextUsed = used
	s.contextTotal = total
}

// SetToolCount sets the number of registered tools.
func (s *StatusBar) SetToolCount(n int) {
	s.toolCount = n
}

// SetQuota sets the provider quota info (5h window). Pass nil to hide.
func (s *StatusBar) SetQuota(q *quota.Info) {
	s.quota = q
}

// View renders the status bar below the input field.
// Format: " model • 85%/2h30m • usedK/totalK • N tools"
// Model: green. Quota: green→yellow(<20% left)→red(<10% left). Separators: gray.
func (s StatusBar) View() string {
	modelStr := s.model
	if modelStr == "" {
		modelStr = "gbot"
	}
	left := statusGreenStyle.Render(modelStr)

	parts := []string{left}

	if s.quota != nil && !s.quota.ResetAt.IsZero() {
		q := quotaStyle(s.quota.Remaining()).Render(formatQuota(s.quota))
		parts = append(parts, q)
	}

	ctxStr := formatContextSize(s.contextUsed, s.contextTotal)
	mid := ctxStyle(s.contextUsed, s.contextTotal).Render(ctxStr)
	parts = append(parts, mid)

	if s.toolCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tools", s.toolCount))
	}
	return " " + strings.Join(parts, statusSep) + " "
}

// formatQuota renders quota as "remaining%/countdown".
// Examples: "85%/2h30m", "30%/45m", "8%/3m".
func formatQuota(q *quota.Info) string {
	rem := q.Remaining()
	left := time.Until(q.ResetAt)
	return fmt.Sprintf("%d%%/%s", rem, formatCountdown(left))
}

// formatCountdown compactly renders a duration:
// "2d", "2h30m", "45m", "3m", "0m" if negative.
func formatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	hours := int(d.Hours())
	if hours >= 24 {
		days := hours / 24
		return fmt.Sprintf("%dd", days)
	}
	if hours >= 1 {
		mins := int(d.Minutes()) - hours*60
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	mins := int(d.Minutes())
	if mins < 1 {
		return "0m"
	}
	return fmt.Sprintf("%dm", mins)
}

// quotaStyle picks the color for the quota segment based on remaining %.
// ≥20% left = green, ≥10% left = yellow, <10% left = red.
func quotaStyle(remaining int) lipgloss.Style {
	switch {
	case remaining < 10:
		return statusRedCtx
	case remaining < 20:
		return statusYellowCtx
	default:
		return statusGreenStyle
	}
}

// ctxColor returns the lipgloss color for the context usage based on percentage.
func ctxColor(used, total int) lipgloss.Color {
	if total <= 0 {
		return ""
	}
	pct := used * 100 / total
	if pct >= 90 {
		return lipgloss.Color("230;70;70")
	}
	if pct >= 80 {
		return lipgloss.Color("230;200;50")
	}
	return ""
}

// ctxStyle returns a pre-cached lipgloss style for the context color.
// Avoids allocating new Style objects on every View() call.
func ctxStyle(used, total int) lipgloss.Style {
	switch string(ctxColor(used, total)) {
	case "230;200;50":
		return statusYellowCtx
	case "230;70;70":
		return statusRedCtx
	}
	return lipgloss.NewStyle()
}

// formatContextSize formats context usage as "usedK/totalK" or "used/total".
func formatContextSize(used, total int) string {
	return fmt.Sprintf("%s/%s", types.FormatTokenCount(used), types.FormatTokenCount(total))
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
		frames: []string{" ", "·", "•", "●", "⬤", "●", "•", "·"},
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
	Role        string         // "user", "assistant", "system"
	Blocks      []ContentBlock // interleaved content: text and tool blocks in order
	ExpandTools bool           // when true, show full tool output instead of collapsed
}

// AgentLogEntry records one tool call from a sub-agent for live progress display.
type AgentLogEntry struct {
	AgentType string // "General", "Explore", "Planner", etc.
	Depth     int    // nesting depth
	ToolName  string // "Read", "Grep", "Bash", etc.
	Summary   string // tool summary text
	IsError   bool   // true if tool_end reported error
	Done      bool   // false = running, true = completed
}

// ToolCallView renders a tool invocation within a message.
type ToolCallView struct {
	ID            string
	Name          string // raw tool name (e.g., "Bash", "Grep")
	Summary       string // context-aware display name (e.g., "Listing 1 directory", "Found 5 matches")
	Input         string
	Output        string
	IsError       bool
	Done          bool
	Elapsed       time.Duration
	AgentLogs     []AgentLogEntry     // sub-agent tool call progress (nil for non-Agent tools)
	ToolCount     int                 // total sub-agent tool calls (for summary line when done)
	TokensIn      int                 // sub-agent input tokens (for summary line when done)
	TokensOut     int                 // sub-agent output tokens (for summary line when done)
	ContextSize   int                 // sub-agent latest context size (InputTokens + CacheRead + CacheCreation + OutputTokens)
	ContextWindow int                 // sub-agent context window size (set once at tool start)
	Blocks        []ContentBlock      // nested blocks for agent's sub-events (text/tool/thinking)
	AgentType     string              // agent type name (e.g., "Explore", "Planner")
	SearchRead    tool.SearchReadKind // classification for collapse behavior
}

// ThinkingView renders a thinking block within a message.
type ThinkingView struct {
	Text     string        // accumulated thinking text
	Duration time.Duration // set on ThinkingEnd
	Done     bool          // false during streaming, true after ThinkingEnd
}

// View renders the message with word wrapping at the given width.
// When expand is true, tool output is shown fully instead of collapsed.
func (m MessageView) View(width int, expand bool, toolDot string, streaming bool, noHint bool, maxOutputLines int) string {
	if width < 10 {
		width = 10
	}

	var sb strings.Builder
	availWidth := max(
		// minimal margin
		width-2, 10)

	// Render using Blocks (interleaved text+tool, per TS).
	if len(m.Blocks) > 0 {
		isUser := m.Role == "user"

		// Detect groups of consecutive search/read tools for collapsed rendering.
		gl := buildGroupLookup(m.Blocks)
		// Find the last group index (matches TS isActiveCollapsedGroup:
		// last group in a streaming message is "active" even if all tools done).
		lastGroupIdx := -1
		for _, g := range gl.byFirstIdx {
			if g.indices[0] > lastGroupIdx {
				lastGroupIdx = g.indices[0]
			}
		}
		isStreaming := streaming

		for i, blk := range m.Blocks {
			// Check if this block is the first of a group.
			if g, ok := gl.byFirstIdx[i]; ok && !expand {
				// Active = anyRunning || (streaming && last group && no content after).
				// Matches TS: hasAnyToolInProgress || (isLoading && !hasContentAfter)
				isActive := g.anyRunning
				if !isActive && isStreaming && i == lastGroupIdx {
					hasContentAfter := false
					for j := i + 1; j < len(m.Blocks); j++ {
						if gl.consumed[j] {
							continue
						}
						if m.Blocks[j].Type == BlockText && m.Blocks[j].Text != "" {
							hasContentAfter = true
							break
						}
						if m.Blocks[j].Type == BlockTool || m.Blocks[j].Type == BlockStats {
							hasContentAfter = true
							break
						}
					}
					isActive = !hasContentAfter
				}

				writeGroupSummary(&sb, "", g, isActive, toolDot, noHint)
				sb.WriteString("\n\n")
				continue
			}
			if gl.consumed[i] && !expand {
				// Subsequent block in a collapsed group — skip.
				continue
			}

			switch blk.Type {
			case BlockText:
				if blk.Text != "" {
					wrapped := wordWrap(Render(blk.Text), availWidth)
					if isUser {
						wrapped = prefixUserLine(wrapped)
					}
					sb.WriteString(wrapped)
					sb.WriteString("\n\n")
				}
			case BlockTool:
				blk.renderToolCall(&sb, availWidth, expand, toolDot, noHint, maxOutputLines, 0, isStreaming)
				sb.WriteString("\n\n")
			case BlockThinking:
				blk.renderThinkingBlock(&sb, availWidth, expand, toolDot, noHint, 0)
				sb.WriteString("\n\n")
			case BlockStats:
				sb.WriteString(blk.Text)
				sb.WriteString("\n\n")
			case BlockUser:
				sb.WriteString(prefixUserLine(blk.Text))
				sb.WriteString("\n\n")
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

var resultPrefixWidth = lipgloss.Width(resultPrefix)

// withResultPrefix prepends "| " to the first line and matching spaces to
// continuation lines, using lipgloss layout primitives for ANSI-safe width.
func withResultPrefix(content string) string {
	if content == "" {
		return ""
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, resultPrefix, content)
}

// renderStatsText returns a styled stats string for an agent tool.
// Empty string if no stats to show.
// agentTypeName returns the display agent type, checking both AgentType field
// and AgentLogs. Returns empty string if not an agent or type is internal ("fork").
func agentTypeName(tc *ToolCallView) string {
	if tc.AgentType != "" && tc.AgentType != "fork" {
		return tc.AgentType
	}
	if len(tc.AgentLogs) > 0 && tc.AgentLogs[0].AgentType != "" && tc.AgentLogs[0].AgentType != "fork" {
		return tc.AgentLogs[0].AgentType
	}
	return ""
}

func renderStatsText(tcv *ToolCallView) string {
	var parts []string
	if tcv.TokensIn > 0 || tcv.TokensOut > 0 {
		parts = append(parts, fmt.Sprintf("↑%s ↓%s", types.FormatTokenCount(tcv.TokensIn), types.FormatTokenCount(tcv.TokensOut)))
	}
	if tcv.ContextSize > 0 && tcv.ContextWindow > 0 {
		parts = append(parts, formatContextSize(tcv.ContextSize, tcv.ContextWindow))
	}
	if tcv.ToolCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tool%s", tcv.ToolCount, pluralS(tcv.ToolCount)))
	}
	if len(parts) > 0 {
		return styleDim.Render(strings.Join(parts, " · "))
	}
	return ""
}

// renderToolCall renders a tool block using ● dot indicator.
// When expand is true, full tool output is shown; otherwise output is collapsed.
// depth controls indentation: depth=0 is top-level, depth=N adds N*2 spaces.
//
// Rendering rules (same at every depth):
//
//	● ToolName(summary) (time)       ← line 1: dot + name, NO pipe
//	| output line 1                  ← line 2: pipe + first output
//	  output line 2                  ← line 3+: spaces (pipe width) + continuation
//
// Nested tools inside an Agent's Blocks render at depth+1, adding 2 more spaces
// before ALL lines (header and output alike).
func (blk ContentBlock) renderToolCall(sb *strings.Builder, availWidth int, expand bool, toolDot string, noHint bool, maxOutputLines int, depth int, streaming bool) {
	if blk.Type != BlockTool {
		return
	}
	tc := blk.ToolCall
	indent := strings.Repeat("  ", depth)

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
		dotStr = " "
	}

	if !tc.Done {
		// Running state: spinner dot + bold name + summary + "running..."
		var runningDot string
		if toolDot != "" {
			runningDot = toolDot
		} else {
			runningDot = " "
		}
		agentName := tc.Name
		if agentTypeName(&tc) != "" {
			agentName = tc.Name + " " + agentTypeName(&tc)
		}
		header := runningDot + " " + styleNameBold.Render(agentName)
		if tc.Summary != "" {
			header += fmt.Sprintf("(%s)", highlightSummary(tc.Name, tc.Summary))
		}
		// content_block_start initializes input as "{}" (2B); non-streaming providers
		// (e.g. MiMo) never send input_json_delta, so require >100B to confirm real streaming input.
		if (tc.Name == "Write" || tc.Name == "Edit") && len(tc.Input) > 100 {
			header += fmt.Sprintf(" (%s)", toolresult.FormatFileSize(len(tc.Input)))
		} else {
			header += " " + styleDim.Render("running...")
		}
		sb.WriteString(indent + wordWrapIndent(header, availWidth, indent+strings.Repeat(" ", 2)))
		// Render running sub-blocks if present
		if len(tc.Blocks) > 0 {
			subIndent := strings.Repeat("  ", depth+1)
			gl := buildGroupLookup(tc.Blocks)
			// Find the last group index for isActive streaming logic.
			lastGroupIdx := -1
			for _, g := range gl.byFirstIdx {
				if g.indices[0] > lastGroupIdx {
					lastGroupIdx = g.indices[0]
				}
			}
			for i, sub := range tc.Blocks {
				if g, ok := gl.byFirstIdx[i]; ok && !expand {
					isActive := g.anyRunning
					if !isActive && streaming && i == lastGroupIdx {
						hasContentAfter := false
						for j := i + 1; j < len(tc.Blocks); j++ {
							if gl.consumed[j] {
								continue
							}
							if tc.Blocks[j].Type == BlockText && tc.Blocks[j].Text != "" {
								hasContentAfter = true
								break
							}
							if tc.Blocks[j].Type == BlockTool || tc.Blocks[j].Type == BlockStats {
								hasContentAfter = true
								break
							}
						}
						isActive = !hasContentAfter
					}
					sb.WriteString("\n" + subIndent)
					writeGroupSummary(sb, "", g, isActive, toolDot, true)
					continue
				}
				if gl.consumed[i] && !expand {
					continue
				}
				switch sub.Type {
				case BlockTool:
					sb.WriteString("\n")
					sub.renderToolCall(sb, availWidth, expand, toolDot, noHint, maxOutputLines, depth+1, streaming)
				case BlockText:
					if sub.Text != "" {
						sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, subIndent, formatToolContent(Render(sub.Text), false, expand, availWidth-len(subIndent), noHint, maxOutputLines, lipgloss.NewStyle())))
					}
				case BlockThinking:
					sb.WriteString("\n")
					sub.renderThinkingBlock(sb, availWidth, expand, toolDot, noHint, depth+1)
				}
			}
		} else if len(tc.AgentLogs) > 0 {
			sb.WriteString(renderAgentLogs(&tc, availWidth))
		}
		// Show streaming output while tool is still running
		if tc.Output != "" {
			toolExpand := expand || tc.Name == "Write" || tc.Name == "Edit"
			output := formatToolOutput(tc.Output, tc.IsError, toolExpand, availWidth-resultPrefixWidth, noHint, maxOutputLines, lipgloss.NewStyle())
			sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, indent, output))
		}
		return
	}

	// Done state — build header
	var hdr strings.Builder
	agentName := tc.Name
	if agentTypeName(&tc) != "" {
		agentName = tc.Name + " " + agentTypeName(&tc)
	}
	hdr.WriteString(dotStr)
	hdr.WriteByte(' ')
	hdr.WriteString(styleNameBold.Render(agentName))
	if tc.Summary != "" {
		fmt.Fprintf(&hdr, "(%s)", highlightSummary(tc.Name, tc.Summary))
	}
	if tc.Elapsed > 0 {
		hdr.WriteString(styleTimeDim.Render(" (" + formatDuration(tc.Elapsed) + ")"))
	}
	sb.WriteString(indent + wordWrapIndent(hdr.String(), availWidth, indent+strings.Repeat(" ", 2)))

	if len(tc.Blocks) > 0 {
		// Agent with nested blocks: stats line + recursive sub-blocks
		statsText := renderStatsText(&tc)
		if statsText != "" {
			sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, indent, formatToolOutput(statsText, false, true, availWidth-resultPrefixWidth, true, 0, lipgloss.NewStyle())))
		}
		subIndent := strings.Repeat("  ", depth+1)
		// Detect groups of consecutive search/read tools for collapsed rendering.
		gl := buildGroupLookup(tc.Blocks)

		for i, sub := range tc.Blocks {
			if g, ok := gl.byFirstIdx[i]; ok && !expand {
				isActive := g.anyRunning
				sb.WriteString("\n" + subIndent)
				writeGroupSummary(sb, "", g, isActive, "", noHint)
				continue
			}
			if gl.consumed[i] && !expand {
				continue
			}
			switch sub.Type {
			case BlockTool:
				sb.WriteString("\n")
				sub.renderToolCall(sb, availWidth, expand, toolDot, noHint, maxOutputLines, depth+1, streaming)
			case BlockText:
				if sub.Text != "" {
					sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, subIndent, formatToolContent(Render(sub.Text), false, expand, availWidth-len(subIndent), noHint, maxOutputLines, lipgloss.NewStyle())))
				}
			case BlockThinking:
				sb.WriteString("\n")
				sub.renderThinkingBlock(sb, availWidth, expand, toolDot, noHint, depth+1)
			}
		}
	} else {
		// Write/Edit: always show full diff hunks (never collapse).
		// Source: TS FileEditToolUpdatedMessage / FileWriteToolCreatedMessage —
		// structured diffs are shown in full, not truncated.
		toolExpand := expand || tc.Name == "Write" || tc.Name == "Edit"
		if tc.ToolCount > 0 {
			if len(tc.AgentLogs) > 0 {
				sb.WriteString(renderAgentLogs(&tc, availWidth))
			}
			if tc.Output != "" {
				output := formatToolOutput(tc.Output, tc.IsError, toolExpand, availWidth-resultPrefixWidth, noHint, maxOutputLines, lipgloss.NewStyle())
				sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, indent, output))
			}
		} else if tc.Output != "" {
			// Search/read tools: collapsed view shows summary + ctrl+o.
			if tc.SearchRead.IsCollapsible() && !toolExpand {
				summary := styleDim.Render(collapseSummary(tc.Output, tc.SearchRead) + " … ctrl+o to expand")
				sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, indent, "| "+summary))
			} else {
				output := formatToolOutput(tc.Output, tc.IsError, toolExpand, availWidth-resultPrefixWidth, noHint, maxOutputLines, lipgloss.NewStyle())
				sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, indent, output))
			}
		}
	}
}

// renderAgentLogs renders sub-agent tool call progress using formatToolOutput.
// Shows last 5 entries + overflow + stats, all with "| " prefix.
func renderAgentLogs(tcv *ToolCallView, availWidth int) string {
	if len(tcv.AgentLogs) == 0 {
		return ""
	}

	maxVisible := 5
	entries := tcv.AgentLogs
	overflow := 0
	if len(entries) > maxVisible {
		overflow = len(entries) - maxVisible
		entries = entries[len(entries)-maxVisible:]
	}

	var lines []string
	for _, e := range entries {
		if e.ToolName == "Thinking" {
			lines = append(lines, styleDim.Render("Thinking..."))
		} else {
			text := styleDim.Render(e.ToolName)
			if e.Summary != "" {
				text += styleDim.Render(fmt.Sprintf("(%s)", truncateSummary(e.Summary, 30)))
			}
			if !e.Done {
				text += styleDim.Italic(true).Render("...")
			}
			lines = append(lines, text)
		}
	}

	if overflow > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("... +%d more", overflow)))
	}

	// Stats line: tokens · context · tools (matches main agent order)
	if tcv.ToolCount > 0 || tcv.TokensIn > 0 || tcv.TokensOut > 0 {
		var parts []string
		if tcv.TokensIn > 0 || tcv.TokensOut > 0 {
			parts = append(parts, fmt.Sprintf("↑%s ↓%s", types.FormatTokenCount(tcv.TokensIn), types.FormatTokenCount(tcv.TokensOut)))
		}
		if tcv.ContextSize > 0 && tcv.ContextWindow > 0 {
			parts = append(parts, formatContextSize(tcv.ContextSize, tcv.ContextWindow))
		}
		if tcv.ToolCount > 0 {
			parts = append(parts, fmt.Sprintf("%d tool%s", tcv.ToolCount, pluralS(tcv.ToolCount)))
		}
		if len(parts) > 0 {
			lines = append(lines, styleDim.Render(strings.Join(parts, " · ")))
		}
	}

	content := strings.Join(lines, "\n")
	return "\n" + formatToolOutput(content, false, true, availWidth-resultPrefixWidth, true, 0, lipgloss.NewStyle())
}

// pluralS returns "s" if n != 1.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// truncateSummary truncates a tool summary to maxLen characters.
func truncateSummary(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// formatToolContent produces pure styled content from tool output — no prefix.
// Handles tab expansion, line collapse/expand, word wrap, and per-line styling.
func formatToolContent(output string, isError bool, expand bool, availWidth int, noHint bool, maxOutputLines int, contentStyle lipgloss.Style) string {
	if output == "" {
		return ""
	}
	output = strings.ReplaceAll(output, "\t", "    ")
	output = strings.Trim(output, "\n")
	lines := strings.Split(output, "\n")
	maxLines := 3
	if isError {
		maxLines = 10
	}

	styleLine := func(text string) string {
		if contentStyle.String() != "" {
			return contentStyle.Render(text)
		}
		return text
	}

	if expand || len(lines) <= maxLines+1 {
		if expand && maxOutputLines > 0 && len(lines) > maxOutputLines {
			shown := lines[len(lines)-maxOutputLines:]
			hidden := len(lines) - maxOutputLines
			cl := make([]string, 0, 1+len(shown))
			cl = append(cl, styleDim.Render(fmt.Sprintf("... %d lines truncated ...", hidden)))
			for _, line := range shown {
				for wl := range strings.SplitSeq(wordWrap(line, availWidth), "\n") {
					cl = append(cl, styleLine(wl))
				}
			}
			return strings.Join(cl, "\n")
		}
		cl := make([]string, 0, len(lines))
		for _, line := range lines {
			for wl := range strings.SplitSeq(wordWrap(line, availWidth), "\n") {
				cl = append(cl, styleLine(wl))
			}
		}
		return strings.Join(cl, "\n")
	}

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

	cl := make([]string, 0, len(shown)+1)
	for _, line := range shown {
		for wl := range strings.SplitSeq(wordWrap(line, availWidth), "\n") {
			cl = append(cl, styleLine(wl))
		}
	}
	cl = append(cl, hint)
	return strings.Join(cl, "\n")
}

// formatToolOutput formats tool output with ⎿ prefix and line collapse.
// Collapsed: show first 3 lines + hint (or 10 for errors).
// Expanded: show all lines, or last maxOutputLines if height-limited.
// maxOutputLines=0 means unlimited.
func formatToolOutput(output string, isError bool, expand bool, availWidth int, noHint bool, maxOutputLines int, contentStyle lipgloss.Style) string {
	content := formatToolContent(output, isError, expand, availWidth, noHint, maxOutputLines, contentStyle)
	return withResultPrefix(applyDiffBackground(content, availWidth))
}

// Pre-cached styles for thinking blocks.
var (
	styleThinkingStar    = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
	styleThinkingContent = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
)

// renderThinkingBlock renders a thinking block using ✦ symbol.
// During streaming (Done=false): animated star + "Thinking..." + real-time content.
// After done (Done=true): static ✦ + duration + collapsed/expanded content.
// Title line matches tool block style (bold name + dim details).
// Content uses italic to distinguish from tool output.
func (blk ContentBlock) renderThinkingBlock(sb *strings.Builder, availWidth int, expand bool, toolDot string, noHint bool, depth int) {
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
			// Invisible blink frame: hide ✦ (space for alignment)
			star = " "
		}
		indent := strings.Repeat("  ", depth)
		header := star + " " + styleNameBold.Render("Thinking") + styleDim.Render("...")
		sb.WriteString(indent + wordWrap(header, availWidth))

		// Show streaming content (italic to distinguish from tool output)
		if tv.Text != "" {
			content := formatToolContent(tv.Text, false, true, availWidth-resultPrefixWidth, noHint, 0, styleThinkingContent)
			sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, indent, withResultPrefix(content)))
		}
		return
	}

	indent := strings.Repeat("  ", depth)
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
	sb.WriteString(indent + wordWrapIndent(hdr.String(), availWidth, indent+strings.Repeat(" ", 2)))

	// Show content with collapse/expand (italic to distinguish from tool output)
	if tv.Text != "" {
		content := formatToolContent(tv.Text, false, expand, availWidth-resultPrefixWidth, noHint, 0, styleThinkingContent)
		sb.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, indent, withResultPrefix(content)))
	}
}

// wordWrap wraps text to the given width, breaking at word boundaries.
// Tabs are expanded to 4 spaces so runewidth correctly accounts for their
// display width (runewidth.RuneWidth('\t') returns 0, which would cause
// under-wrapping and ghost content on re-render).
func wordWrap(text string, width int) string {
	return wordWrapIndent(text, width, "")
}

// highlightSummary applies syntax highlighting to the summary field of tool
// calls where the summary is code (Bash commands, Repl code). For tools whose
// summary is a path, only the basename is shown to keep headers short.
func highlightSummary(toolName, summary string) string {
	switch toolName {
	case "Bash":
		return tool.HighlightCode(summary, "bash")
	case "Repl":
		return tool.HighlightCode(summary, "javascript")
	default:
		return summary
	}
}

// wordWrapIndent wraps text to width, indenting continuation lines by contIndent.
// Used for tool call headers where the first line has a "● " prefix and
// continuation lines must align with the content after it.
func wordWrapIndent(text string, width int, contIndent string) string {
	if width <= 0 {
		return text
	}
	text = strings.ReplaceAll(text, "\t", "    ")

	var lines []string
	var currentLine strings.Builder
	currentLen := 0
	var activeANSI strings.Builder // track active SGR codes to re-emit after wrap

	i := 0
	for i < len(text) {
		// Handle newlines
		if text[i] == '\n' {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLen = 0
			activeANSI.Reset()
			if contIndent != "" {
				currentLine.WriteString(contIndent)
				currentLen = lipgloss.Width(contIndent)
			}
			i++
			continue
		}

		// Handle ANSI escape sequences — consume entirely, no width counted
		if text[i] == '\x1b' {
			seq := consumeAnsiEscape(text[i:])
			currentLine.WriteString(seq)
			// Track SGR codes for re-emission after wrap
			if strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" {
					activeANSI.Reset()
				} else {
					activeANSI.WriteString(seq)
				}
			}
			i += len(seq)
			continue
		}

		// Visible character
		r, size := utf8.DecodeRuneInString(text[i:])
		rw := runeDisplayWidth(r)

		if currentLen+rw > width && currentLen > 0 {
			// Close color on old line, re-open on new line
			if activeANSI.Len() > 0 {
				currentLine.WriteString("\x1b[0m")
			}
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLen = 0
			if contIndent != "" {
				currentLine.WriteString(contIndent)
				currentLen = lipgloss.Width(contIndent)
			}
			if activeANSI.Len() > 0 {
				currentLine.WriteString(activeANSI.String())
			}
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
func prefixUserLine(text string) string {
	lines := strings.Split(text, "\n")
	lines[0] = renderedPrompt + lines[0]
	indent := strings.Repeat(" ", renderedPromptWidth)
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
// Task list panel rendering
// ---------------------------------------------------------------------------

// renderTaskList builds a compact task summary panel.
// Aligned with TS TaskListV2.tsx rendering.
//
// Format:
//
//	☐ 1 Fix auth bug
//	▶ 2 Write tests             (agent-1)
//	☑ 3 Implement API           [blocked by 2]
const maxTaskPanelItems = 10

// taskMaxDisplay computes dynamic max visible tasks based on terminal height.
// Source: TS TaskListV2.tsx:48 — maxDisplay = min(10, max(3, rows - 14))
func taskMaxDisplay(termRows int) int {
	return min(maxTaskPanelItems, max(3, termRows-14))
}

// truncateRunes truncates s to maxRunes, appending "..." if truncated.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func (a *App) renderTaskList() string {
	if a.taskListFn == nil {
		return ""
	}
	tasks := a.taskListFn()
	if len(tasks) == 0 {
		return ""
	}

	// Cap to dynamic max, show overflow summary.
	// Source: TS TaskListV2.tsx:136-185 — hiddenSummary
	maxDisplay := taskMaxDisplay(a.height)
	var hidden []TaskSummary
	if len(tasks) > maxDisplay {
		hidden = tasks[maxDisplay:]
		tasks = tasks[:maxDisplay]
	}

	var b strings.Builder
	for _, t := range tasks {
		isCompleted := t.Status == "completed"
		isBlocked := len(t.BlockedBy) > 0

		// Icon + color (Source: TaskListV2.tsx getTaskIcon)
		var icon string
		var iconStyle lipgloss.Style
		switch t.Status {
		case "in_progress":
			icon = "[▶]"
			iconStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // same blue as input prompt ❯
		case "completed":
			icon = "[✓]"
			iconStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // emerald
		default: // pending
			icon = "[ ]"
			iconStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("246")) // cool gray
		}

		// Subject style (Source: TaskListV2.tsx TaskItem)
		subjectStyle := lipgloss.NewStyle()
		if t.Status == "in_progress" {
			subjectStyle = subjectStyle.Bold(true)
		}
		if isCompleted {
			subjectStyle = subjectStyle.Strikethrough(true)
		}
		if isBlocked {
			subjectStyle = subjectStyle.Faint(true)
		}

		line := fmt.Sprintf(" %s %s", iconStyle.Render(icon), subjectStyle.Render(t.Subject))
		if t.Owner != "" {
			line += lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf(" (@%s)", t.Owner))
		}
		if isBlocked {
			// Show blocker subjects with adaptive truncation (max 15 runes each).
			subjects := make([]string, len(t.BlockedBy))
			for i, s := range t.BlockedBy {
				subjects[i] = truncateRunes(s, 15)
			}
			line += lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf(" [blocked by %s]", strings.Join(subjects, ", ")))
		}

		b.WriteString(line)
		b.WriteByte('\n')
	}

	// Overflow summary (Source: TS TaskListV2.tsx:170-185 — hiddenSummary)
	if len(hidden) > 0 {
		var parts []string
		var hInProgress, hPending, hCompleted int
		for _, h := range hidden {
			switch h.Status {
			case "in_progress":
				hInProgress++
			case "completed":
				hCompleted++
			default:
				hPending++
			}
		}
		if hInProgress > 0 {
			parts = append(parts, fmt.Sprintf("%d in progress", hInProgress))
		}
		if hPending > 0 {
			parts = append(parts, fmt.Sprintf("%d pending", hPending))
		}
		if hCompleted > 0 {
			parts = append(parts, fmt.Sprintf("%d completed", hCompleted))
		}
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf(" … +%s", strings.Join(parts, ", "))))
		b.WriteByte('\n')
	}

	return strings.TrimRight(b.String(), "\n")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// toolGroup holds consecutive search/read tool blocks for group rendering.
// groupLookup holds the pre-computed mapping from block index to group,
// and the set of indices consumed by groups (to skip in the main loop).
type groupLookup struct {
	byFirstIdx map[int]*toolGroup
	consumed   map[int]bool
}

// buildGroupLookup runs detectToolGroups and builds the lookup maps.
func buildGroupLookup(blocks []ContentBlock) groupLookup {
	groups := detectToolGroups(blocks)
	gl := groupLookup{
		byFirstIdx: make(map[int]*toolGroup, len(groups)),
		consumed:   make(map[int]bool),
	}
	for gi := range groups {
		g := &groups[gi]
		gl.byFirstIdx[g.indices[0]] = g
		for _, idx := range g.indices {
			gl.consumed[idx] = true
		}
	}
	return gl
}

// writeGroupSummary renders one collapsed group summary line into sb.
// prefix is the line prefix (e.g. subIndent or "").
// toolDot is the current blink frame (empty string on blink-off).
// noHint suppresses the "ctrl+o to expand" hint.
func writeGroupSummary(sb *strings.Builder, prefix string, g *toolGroup, isActive bool, toolDot string, noHint bool) {
	var dotStr string
	if isActive {
		if toolDot != "" {
			dotStr = toolDot
		} else {
			dotStr = " "
		}
	} else {
		dotStr = styleDotSuccess.Render(dot)
	}
	summary := groupSummaryText(*g, isActive)
	hint := ""
	if !noHint {
		hint = " … ctrl+o to expand"
	}
	sb.WriteString(prefix + dotStr + " " + styleDim.Render(summary+hint))
}

// Simplified from TS GroupAccumulator (collapseReadSearch.ts:270-330):
// memory, MCP, git, hook fields deferred — gbot doesn't have these yet.
type toolGroup struct {
	indices     []int // indices into m.Blocks for the grouped tool blocks
	searchCount int
	readCount   int // operation count (unique file paths deferred)
	listCount   int
	anyRunning  bool // true if any tool in group has Done==false
}

// detectToolGroups scans blocks for consecutive collapsible search/read tools.
// Returns groups with >= 2 tools only; single-tool groups are skipped so they
// render as Phase 1 per-tool collapse.
// Matches TS collapseReadSearchGroups() (collapseReadSearch.ts:771-930).
func detectToolGroups(blocks []ContentBlock) []toolGroup {
	var groups []toolGroup
	var current toolGroup

	flush := func() {
		if len(current.indices) >= 2 {
			groups = append(groups, current)
		}
		current = toolGroup{}
	}

	for i, blk := range blocks {
		switch blk.Type {
		case BlockTool:
			tc := blk.ToolCall
			if tc.SearchRead.IsCollapsible() {
				current.indices = append(current.indices, i)
				if tc.SearchRead.IsSearch {
					current.searchCount++
				}
				if tc.SearchRead.IsRead {
					current.readCount++
				}
				if tc.SearchRead.IsList {
					current.listCount++
				}
				if !tc.Done {
					current.anyRunning = true
				}
			} else {
				// Non-collapsible tool breaks the group.
				flush()
			}
		case BlockText:
			if blk.Text != "" {
				// Non-empty text breaks the group.
				flush()
			}
			// Empty text doesn't break.
		case BlockThinking:
			// Thinking doesn't break the group — matches TS shouldSkipMessage().
		case BlockStats:
			flush()
		case BlockUser:
			flush()
		}
	}
	flush()
	return groups
}

// groupSummaryText generates a summary for a tool group.
// Translates TS getSearchReadSummaryText() (collapseReadSearch.ts:930-1085).
func groupSummaryText(g toolGroup, isActive bool) string {
	var parts []string

	add := func(verb, body string) {
		if len(parts) == 0 {
			parts = append(parts, strings.ToUpper(verb[:1])+verb[1:]+" "+body)
		} else {
			parts = append(parts, verb+" "+body)
		}
	}

	if g.searchCount > 0 {
		p := "pattern"
		if g.searchCount != 1 {
			p = "patterns"
		}
		if isActive {
			add("searching for", fmt.Sprintf("%d %s", g.searchCount, p))
		} else {
			add("searched for", fmt.Sprintf("%d %s", g.searchCount, p))
		}
	}
	if g.readCount > 0 {
		f := "file"
		if g.readCount != 1 {
			f = "files"
		}
		if isActive {
			add("reading", fmt.Sprintf("%d %s", g.readCount, f))
		} else {
			add("read", fmt.Sprintf("%d %s", g.readCount, f))
		}
	}
	if g.listCount > 0 {
		d := "directory"
		if g.listCount != 1 {
			d = "directories"
		}
		if isActive {
			add("listing", fmt.Sprintf("%d %s", g.listCount, d))
		} else {
			add("listed", fmt.Sprintf("%d %s", g.listCount, d))
		}
	}

	text := strings.Join(parts, ", ")
	if isActive {
		text += "…"
	}
	return text
}

// collapseSummary generates a one-line summary from full tool output
// based on the search/read/list classification.
func collapseSummary(output string, srk tool.SearchReadKind) string {
	lines := strings.Count(output, "\n")
	if output != "" && !strings.HasSuffix(output, "\n") {
		lines++
	}

	switch {
	case srk.IsSearch:
		if lines == 0 {
			return "No matches"
		}
		return fmt.Sprintf("Found %d %s", lines, tool.PluralWord(lines, "matches"))
	case srk.IsRead:
		if lines == 0 {
			return "Read 0 lines"
		}
		return fmt.Sprintf("Read %d %s", lines, tool.PluralWord(lines, "lines"))
	case srk.IsList:
		if lines == 0 {
			return "Empty listing"
		}
		return fmt.Sprintf("Listed %d %s", lines, tool.PluralWord(lines, "entries"))
	default:
		if lines <= 1 {
			return output
		}
		return fmt.Sprintf("%d lines", lines)
	}
}
