package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/types"
)

func newTestDialog() *PermissionDialog {
	ch := make(chan types.PermissionUserDecision, 1)
	return NewPermissionDialog(&types.PermissionAskEvent{
		ToolName:   "Bash",
		Message:    "permission required",
		RuleDetail: "Bash(rm -rf *) from project",
		ResponseCh: ch,
	}, "rm -rf /tmp/test")
}

func TestPermissionDialog_ViewContainsInfo(t *testing.T) {
	d := newTestDialog()
	view := d.View()

	if !strings.Contains(view, "Bash") {
		t.Error("view should contain tool name")
	}
	if !strings.Contains(view, "rm -rf /tmp/test") {
		t.Error("view should contain detail (command)")
	}
	if !strings.Contains(view, "Bash(rm -rf *) from project") {
		t.Error("view should contain rule detail")
	}
	if !strings.Contains(view, "permission required") {
		t.Error("view should contain message")
	}
	if !strings.Contains(view, "Permission Required") {
		t.Error("view should contain title")
	}
}

func TestPermissionDialog_KeyY_Allow(t *testing.T) {
	d := newTestDialog()
	if d.Done() {
		t.Error("should not be done before key press")
	}

	handled := d.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if !handled {
		t.Error("y key should be handled")
	}
	if !d.Done() {
		t.Error("should be done after y key")
	}

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionAllow {
			t.Errorf("got %q, want allow", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_KeyN_Deny(t *testing.T) {
	d := newTestDialog()
	d.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionDeny {
			t.Errorf("got %q, want deny", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_KeyA_AllowAlways(t *testing.T) {
	d := newTestDialog()
	d.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionAllowAlways {
			t.Errorf("got %q, want allow_always", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_Esc_Deny(t *testing.T) {
	d := newTestDialog()
	d.HandleKey(tea.KeyMsg{Type: tea.KeyEsc})

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionDeny {
			t.Errorf("got %q, want deny", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_EnterSelectsCursor(t *testing.T) {
	d := newTestDialog()
	// cursor starts at 0 = Allow
	d.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionAllow {
			t.Errorf("got %q, want allow (cursor=0)", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_ArrowDownThenEnter(t *testing.T) {
	d := newTestDialog()
	d.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // cursor=1 = Deny
	d.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionDeny {
			t.Errorf("got %q, want deny (cursor=1)", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_ArrowDownTwiceThenEnter(t *testing.T) {
	d := newTestDialog()
	d.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // cursor=1
	d.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // cursor=2 = Allow always
	d.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionAllowAlways {
			t.Errorf("got %q, want allow_always (cursor=2)", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_ArrowUpWraps(t *testing.T) {
	d := newTestDialog()
	// cursor starts at 0, up wraps to last
	d.HandleKey(tea.KeyMsg{Type: tea.KeyUp})
	d.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case dec := <-d.responseCh:
		if dec != types.UserDecisionAllowAlways {
			t.Errorf("got %q, want allow_always (wrapped to last)", dec)
		}
	default:
		t.Fatal("expected decision on responseCh")
	}
}

func TestPermissionDialog_DoneBeforeSelection(t *testing.T) {
	d := newTestDialog()
	if d.Done() {
		t.Error("should not be done before any key press")
	}
}

func TestPermissionDialog_UnknownKeyIntercepted(t *testing.T) {
	d := newTestDialog()
	handled := d.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if !handled {
		t.Error("unknown keys should still be intercepted")
	}
	if d.Done() {
		t.Error("unknown key should not complete dialog")
	}
}

func TestPermissionDialog_AgentType(t *testing.T) {
	ch := make(chan types.PermissionUserDecision, 1)
	d := NewPermissionDialog(&types.PermissionAskEvent{
		ToolName:   "Bash",
		Message:    "test",
		AgentType:  "Explore",
		ResponseCh: ch,
	}, "ls")

	view := d.View()
	if !strings.Contains(view, "Agent: Explore") {
		t.Error("view should contain agent type when set")
	}
}

func TestPermissionDialog_NoRuleDetail(t *testing.T) {
	ch := make(chan types.PermissionUserDecision, 1)
	d := NewPermissionDialog(&types.PermissionAskEvent{
		ToolName:   "Bash",
		Message:    "test",
		RuleDetail: "",
		ResponseCh: ch,
	}, "ls")

	view := d.View()
	if strings.Contains(view, "Rule:") {
		t.Error("view should not show Rule line when RuleDetail is empty")
	}
}

func TestPermissionDialog_NoDetail(t *testing.T) {
	ch := make(chan types.PermissionUserDecision, 1)
	d := NewPermissionDialog(&types.PermissionAskEvent{
		ToolName:   "Bash",
		Message:    "test",
		ResponseCh: ch,
	}, "")

	view := d.View()
	if strings.Contains(view, "Command:") {
		t.Error("view should not show Command line when detail is empty")
	}
}

func TestExtractDetailBashValidCommand(t *testing.T) {
	input := json.RawMessage(`{"command":"git status","timeout":5000}`)
	got := extractDetail("Bash", input)
	if got != "git status" {
		t.Errorf("got %q, want %q", got, "git status")
	}
}

func TestExtractDetailBashInvalidJSON(t *testing.T) {
	input := json.RawMessage(`{invalid json`)
	got := extractDetail("Bash", input)
	if got != "" {
		t.Errorf("got %q, want empty string for invalid JSON", got)
	}
}

func TestExtractDetailWriteFilePath(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/tmp/test.go","content":"package main"}`)
	got := extractDetail("Write", input)
	if got != "/tmp/test.go" {
		t.Errorf("got %q, want %q", got, "/tmp/test.go")
	}
}

func TestExtractDetailEditFilePath(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/home/user/config.json","old_string":"foo","new_string":"bar"}`)
	got := extractDetail("Edit", input)
	if got != "/home/user/config.json" {
		t.Errorf("got %q, want %q", got, "/home/user/config.json")
	}
}

func TestExtractDetailEmptyToolName(t *testing.T) {
	input := json.RawMessage(`{}`)
	got := extractDetail("", input)
	if got != "" {
		t.Errorf("got %q, want empty string when tool name is empty", got)
	}
}
