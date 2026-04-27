package tui

import (
	"encoding/json"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/types"
)

// NewPermissionDialog creates a Dialog for a permission ask event.
// The dialog shows tool details and offers Allow/Deny/AllowAlways options
// with y/n/a shortcuts.
func NewPermissionDialog(event *types.PermissionAskEvent, detail string) *Dialog {
	var details []DialogDetail

	if event.AgentType != "" {
		details = append(details, DialogDetail{Label: "Agent:", Value: event.AgentType})
	}
	details = append(details, DialogDetail{Label: "Tool:", Value: event.ToolName})

	if detail != "" {
		details = append(details, DialogDetail{Label: "Command:", Value: detail})
	}
	if event.RuleDetail != "" {
		details = append(details, DialogDetail{Label: "Rule:", Value: event.RuleDetail})
	}
	if event.Message != "" {
		details = append(details, DialogDetail{Label: "Reason:", Value: event.Message})
	}

	return NewDialog("Permission Required", []DialogOption{
		{Label: "Allow (this time)", Shortcut: "y"},
		{Label: "Deny", Shortcut: "n"},
		{Label: "Allow always (remember for this session)", Shortcut: "a"},
	}, details...)
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

// sendDecision writes the user's decision to the response channel.
// Non-blocking: if engine already timed out and stopped reading, the write is dropped.
func sendDecision(ch chan types.PermissionUserDecision, decision types.PermissionUserDecision) {
	select {
	case ch <- decision:
	default:
	}
}

// permissionDecisions maps dialog option index to PermissionUserDecision.
var permissionDecisions = []types.PermissionUserDecision{
	types.UserDecisionAllow,
	types.UserDecisionDeny,
	types.UserDecisionAllowAlways,
}

// dialogDonePermission handles the completion of a permission dialog.
// It sends the appropriate decision to the response channel.
func dialogDonePermission(d *Dialog, ch chan types.PermissionUserDecision) {
	if d.Aborted() {
		sendDecision(ch, types.UserDecisionDeny)
		return
	}
	idx := d.SelectedIndex()
	if idx >= 0 && idx < len(permissionDecisions) {
		sendDecision(ch, permissionDecisions[idx])
	} else {
		sendDecision(ch, types.UserDecisionDeny)
	}
}
