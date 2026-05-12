package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/liuy/gbot/pkg/types"
)

type tickMsg time.Time

// InputDialog is a bubbletea Model for interactive text input during PTY commands.
// Displays a prompt with text input, optional password masking, and a countdown timer.
// Enter submits, Esc aborts. Countdown zero auto-aborts.
type InputDialog struct {
	prompt   string
	masked   bool
	value    strings.Builder
	cursor   int
	deadline time.Time
	done     bool
	result   chan types.AskResponse
}

var (
	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("63")).
				Padding(1, 2)
	inputCursorStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("63")).
				Foreground(lipgloss.Color("230"))
	inputCountdownStyle = lipgloss.NewStyle().Faint(true)
	inputBullet         = "•"
)

// NewInputDialog creates an InputDialog for the given prompt.
// The deadline determines the countdown timer; when it expires, the dialog auto-aborts.
func NewInputDialog(prompt string, masked bool, deadline time.Time, resultCh chan types.AskResponse) *InputDialog {
	return &InputDialog{
		prompt:   prompt,
		masked:   masked,
		deadline: deadline,
		result:   resultCh,
	}
}

// Init starts the countdown ticker.
func (d *InputDialog) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles key events and tick messages.
func (d *InputDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if d.done {
		return d, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			d.submit()
			return d, nil
		case tea.KeyEsc:
			d.abort()
			return d, nil
		case tea.KeyCtrlC:
			d.abort()
			return d, tea.Quit
		case tea.KeyBackspace:
			d.deleteBackward()
			return d, nil
		case tea.KeyDelete:
			d.deleteForward()
			return d, nil
		case tea.KeyLeft:
			if d.cursor > 0 {
				d.cursor--
			}
			return d, nil
		case tea.KeyRight:
			if d.cursor < d.value.Len() {
				d.cursor++
			}
			return d, nil
		case tea.KeyRunes:
			d.insertText(msg.String())
			return d, nil
		}

	case tickMsg:
		if time.Until(d.deadline) <= 0 {
			d.abortTimeout()
			return d, nil
		}
		// Continue ticking
		return d, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg(t)
		})
	}

	return d, nil
}

func (d *InputDialog) View() string {
	var b strings.Builder

	b.WriteString(dialogTitleStyle.Render("Input Required"))
	b.WriteString("\n\n")

	b.WriteString(d.prompt)
	b.WriteString("\n\n")

	b.WriteString(d.renderInput())
	b.WriteString("\n\n")

	remaining := time.Until(d.deadline)
	secs := max(int(remaining.Seconds()), 0)
	b.WriteString(inputCountdownStyle.Render(fmt.Sprintf("Timeout in %ds", secs)))
	b.WriteString("\n")

	hint := "Enter to submit · Esc to abort"
	b.WriteString(dialogHint.Render(hint))

	return inputBorderStyle.Render(b.String())
}

func (d *InputDialog) submit() {
	d.done = true
	sendDecision(d.result, types.AskResponse{Text: d.value.String()})
}

func (d *InputDialog) abort() {
	d.done = true
	sendDecision(d.result, types.AskResponse{Aborted: true})
}

func (d *InputDialog) abortTimeout() {
	d.done = true
	sendDecision(d.result, types.AskResponse{Aborted: true, Timeout: true})
}

func (d *InputDialog) insertText(text string) {
	runes := []rune(d.value.String())
	pos := d.cursor
	d.value.Reset()
	d.value.WriteString(string(runes[:pos]))
	d.value.WriteString(text)
	d.value.WriteString(string(runes[pos:]))
	d.cursor += utf8.RuneCountInString(text)
}

// deleteBackward deletes the character before the cursor.
func (d *InputDialog) deleteBackward() {
	if d.cursor == 0 {
		return
	}
	runes := []rune(d.value.String())
	d.value.Reset()
	d.value.WriteString(string(runes[:d.cursor-1]))
	d.value.WriteString(string(runes[d.cursor:]))
	d.cursor--
}

// deleteForward deletes the character at the cursor.
func (d *InputDialog) deleteForward() {
	runes := []rune(d.value.String())
	if d.cursor >= len(runes) {
		return
	}
	d.value.Reset()
	d.value.WriteString(string(runes[:d.cursor]))
	d.value.WriteString(string(runes[d.cursor+1:]))
}

// renderInput renders the text input field with cursor.
func (d *InputDialog) renderInput() string {
	text := d.value.String()
	runes := []rune(text)
	displayRunes := runes

	if d.masked {
		displayRunes = make([]rune, len(runes))
		bullet := []rune(inputBullet)[0]
		for i := range displayRunes {
			displayRunes[i] = bullet
		}
	}

	before := string(displayRunes[:d.cursor])
	at := " "
	after := ""

	if d.cursor < len(displayRunes) {
		at = string(displayRunes[d.cursor])
		after = string(displayRunes[d.cursor+1:])
	}

	return before + inputCursorStyle.Render(at) + after
}
