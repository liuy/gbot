package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/types"
)

// dialogOption is one selectable choice in the permission dialog.
type dialogOption struct {
	label    string
	decision types.PermissionUserDecision
}

// PermissionDialog is a modal overlay that asks the user to confirm or deny
// a tool invocation that matched an ask rule.
type PermissionDialog struct {
	toolName   string
	message    string
	detail     string // extracted command or file path (修正 9)
	ruleDetail string // matched rule description
	agentType  string // non-empty for sub-agent asks
	responseCh chan types.PermissionUserDecision

	cursor  int
	options []dialogOption
	done    bool
}

// NewPermissionDialog creates a permission dialog from the ask event.
func NewPermissionDialog(event *types.PermissionAskEvent, detail string) *PermissionDialog {
	d := &PermissionDialog{
		toolName:   event.ToolName,
		message:    event.Message,
		detail:     detail,
		ruleDetail: event.RuleDetail,
		agentType:  event.AgentType,
		responseCh: event.ResponseCh,
		options: []dialogOption{
			{label: "Allow (this time)", decision: types.UserDecisionAllow},
			{label: "Deny", decision: types.UserDecisionDeny},
			{label: "Allow always (remember for this session)", decision: types.UserDecisionAllowAlways},
		},
	}
	return d
}

// extractDetail derives a human-readable detail string from the tool input.
func extractDetail(toolName string, input json.RawMessage) string {
	switch toolName {
	case "Bash":
		return permission.ExtractBashCommand(input)
	default:
		return permission.ExtractFilePath(input)
	}
}

// HandleKey processes a key event. Returns true if the key was consumed.
// When a selection is made, writes the decision to responseCh and sets done=true.
func (d *PermissionDialog) HandleKey(key tea.KeyMsg) bool {
	switch key.String() {
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		} else {
			d.cursor = len(d.options) - 1
		}
		return true
	case "down", "j":
		if d.cursor < len(d.options)-1 {
			d.cursor++
		} else {
			d.cursor = 0
		}
		return true
	case "enter":
		d.sendDecision(d.options[d.cursor].decision)
		d.done = true
		return true
	case "esc":
		d.sendDecision(types.UserDecisionDeny)
		d.done = true
		return true
	case "y":
		d.sendDecision(types.UserDecisionAllow)
		d.done = true
		return true
	case "n":
		d.sendDecision(types.UserDecisionDeny)
		d.done = true
		return true
	case "a":
		d.sendDecision(types.UserDecisionAllowAlways)
		d.done = true
		return true
	default:
		// 修正 5: intercept ALL keys to prevent them reaching handleKey/handleCtrlC
		return true
	}
}

// sendDecision writes the user's decision to responseCh.
// Non-blocking: if engine already timed out and stopped reading, the write is dropped.
func (d *PermissionDialog) sendDecision(decision types.PermissionUserDecision) {
	select {
	case d.responseCh <- decision:
	default:
	}
}

// Done returns true after the user has made a selection.
func (d *PermissionDialog) Done() bool {
	return d.done
}

// Cached dialog styles — allocated once, not per View() call.
var (
	dialogBorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)
	dialogTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))
	dialogLabelStyle  = lipgloss.NewStyle().Faint(true)
	dialogHighlight   = lipgloss.NewStyle().
			Background(lipgloss.Color("63")).
			Foreground(lipgloss.Color("230")).
			Padding(0, 1)
	dialogNormal = lipgloss.NewStyle().Padding(0, 1)
	dialogHint   = lipgloss.NewStyle().Faint(true)
)

// View renders the permission dialog.
func (d *PermissionDialog) View() string {

	var b strings.Builder

	// Title
	b.WriteString(dialogTitleStyle.Render("Permission Required"))
	b.WriteString("\n\n")

	// Agent type badge (修正 6)
	if d.agentType != "" {
		b.WriteString(dialogLabelStyle.Render(fmt.Sprintf("Agent: %s", d.agentType)))
		b.WriteString("\n")
	}

	// Tool name
	b.WriteString(dialogLabelStyle.Render("Tool: "))
	b.WriteString(d.toolName)
	b.WriteString("\n")

	// Detail (command or file path) — 修正 9
	if d.detail != "" {
		b.WriteString(dialogLabelStyle.Render("Command: "))
		b.WriteString(d.detail)
		b.WriteString("\n")
	}

	// Rule detail
	if d.ruleDetail != "" {
		b.WriteString(dialogLabelStyle.Render("Rule: "))
		b.WriteString(d.ruleDetail)
		b.WriteString("\n")
	}

	// Message (always shown)
	if d.message != "" {
		b.WriteString(dialogLabelStyle.Render("Reason: "))
		b.WriteString(d.message)
		b.WriteString("\n")
	}

	b.WriteString("\n")

	// Options
	for i, opt := range d.options {
		row := "  " + opt.label
		if i == d.cursor {
			b.WriteString(dialogHighlight.Render(row))
		} else {
			b.WriteString(dialogNormal.Render(row))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dialogHint.Render("y allow · n deny · a always · ↑/k up · ↓/j down · Enter confirm · Esc deny"))

	return dialogBorderStyle.Render(b.String())
}
