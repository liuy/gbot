package attachment

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// mockTaskList — test helper
// ---------------------------------------------------------------------------

type mockTaskList struct {
	tasks []TaskItem
	err   error
}

func (m *mockTaskList) ListPending() ([]TaskItem, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.tasks, nil
}

// ---------------------------------------------------------------------------
// mockProvider — test helper for ReminderEngine tests
// ---------------------------------------------------------------------------

type mockProvider struct {
	key        string
	shouldFire bool
	rendered   []types.Message
	fireCalled bool
}

func (m *mockProvider) Key() string { return m.key }

func (m *mockProvider) ShouldFire(_ ReminderContext) bool {
	m.fireCalled = true
	return m.shouldFire
}

func (m *mockProvider) Render(_ ReminderContext) []types.Message {
	return m.rendered
}

// ---------------------------------------------------------------------------
// WrapInSystemReminder
// ---------------------------------------------------------------------------

func TestWrapInSystemReminder(t *testing.T) {
	got := WrapInSystemReminder("hello")
	want := "<system-reminder>\nhello\n</system-reminder>"
	if got != want {
		t.Errorf("WrapInSystemReminder(%q) = %q, want %q", "hello", got, want)
	}
}

func TestWrapInSystemReminder_Empty(t *testing.T) {
	got := WrapInSystemReminder("")
	want := "<system-reminder>\n\n</system-reminder>"
	if got != want {
		t.Errorf("WrapInSystemReminder(\"\") = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// NewMetaUserMessage
// ---------------------------------------------------------------------------

func TestNewMetaUserMessage(t *testing.T) {
	msg := NewMetaUserMessage("test content")
	if msg.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, types.RoleUser)
	}
	if msg.Flags&types.FlagMeta == 0 {
		t.Error("FlagMeta not set")
	}
	if len(msg.Content) != 1 {
		t.Fatalf("Content blocks = %d, want 1", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeText {
		t.Errorf("Content[0].Type = %q, want %q", msg.Content[0].Type, types.ContentTypeText)
	}
	if msg.Content[0].Text != "test content" {
		t.Errorf("Content[0].Text = %q, want %q", msg.Content[0].Text, "test content")
	}
}

// ---------------------------------------------------------------------------
// ReminderEngine
// ---------------------------------------------------------------------------

func TestReminderEngine_Collect_NoProviders(t *testing.T) {
	e := NewReminderEngine()
	msgs := e.Collect(ReminderContext{})
	if len(msgs) != 0 {
		t.Errorf("Collect with no providers = %d messages, want 0", len(msgs))
	}
}

func TestReminderEngine_Collect_NoneFire(t *testing.T) {
	p := &mockProvider{key: "test", shouldFire: false}
	e := NewReminderEngine(p)
	msgs := e.Collect(ReminderContext{})
	if len(msgs) != 0 {
		t.Errorf("Collect when none fire = %d messages, want 0", len(msgs))
	}
	if !p.fireCalled {
		t.Error("ShouldFire was not called")
	}
}

func TestReminderEngine_Collect_OneFires(t *testing.T) {
	rendered := []types.Message{
		NewMetaUserMessage("reminder 1"),
		NewMetaUserMessage("reminder 2"),
	}
	p := &mockProvider{key: "test", shouldFire: true, rendered: rendered}
	e := NewReminderEngine(p)
	msgs := e.Collect(ReminderContext{})
	if len(msgs) != 2 {
		t.Fatalf("Collect = %d messages, want 2", len(msgs))
	}
	if msgs[0].Content[0].Text != "reminder 1" {
		t.Errorf("msgs[0] = %q, want %q", msgs[0].Content[0].Text, "reminder 1")
	}
	if msgs[1].Content[0].Text != "reminder 2" {
		t.Errorf("msgs[1] = %q, want %q", msgs[1].Content[0].Text, "reminder 2")
	}
}

func TestReminderEngine_Collect_MultipleProviders_Order(t *testing.T) {
	p1 := &mockProvider{
		key:        "first",
		shouldFire: true,
		rendered:   []types.Message{NewMetaUserMessage("from-first")},
	}
	p2 := &mockProvider{
		key:        "second",
		shouldFire: true,
		rendered:   []types.Message{NewMetaUserMessage("from-second")},
	}
	e := NewReminderEngine(p1, p2)
	msgs := e.Collect(ReminderContext{})
	if len(msgs) != 2 {
		t.Fatalf("Collect = %d messages, want 2", len(msgs))
	}
	if msgs[0].Content[0].Text != "from-first" {
		t.Errorf("msgs[0] = %q, want %q", msgs[0].Content[0].Text, "from-first")
	}
	if msgs[1].Content[0].Text != "from-second" {
		t.Errorf("msgs[1] = %q, want %q", msgs[1].Content[0].Text, "from-second")
	}
}

func TestReminderEngine_Collect_Mixed(t *testing.T) {
	p1 := &mockProvider{
		key:        "fires",
		shouldFire: true,
		rendered:   []types.Message{NewMetaUserMessage("yes")},
	}
	p2 := &mockProvider{
		key:        "skipped",
		shouldFire: false,
	}
	e := NewReminderEngine(p1, p2)
	msgs := e.Collect(ReminderContext{})
	if len(msgs) != 1 {
		t.Fatalf("Collect = %d messages, want 1", len(msgs))
	}
	if msgs[0].Content[0].Text != "yes" {
		t.Errorf("msgs[0] = %q, want %q", msgs[0].Content[0].Text, "yes")
	}
}

func TestReminderEngine_Providers(t *testing.T) {
	p1 := &mockProvider{key: "a"}
	p2 := &mockProvider{key: "b"}
	e := NewReminderEngine(p1, p2)
	provs := e.Providers()
	if len(provs) != 2 {
		t.Fatalf("Providers() = %d, want 2", len(provs))
	}
	if provs[0].Key() != "a" || provs[1].Key() != "b" {
		t.Errorf("Providers() keys = %q, %q, want a, b", provs[0].Key(), provs[1].Key())
	}
}

// ---------------------------------------------------------------------------
// FormatTaskList
// ---------------------------------------------------------------------------

func TestFormatTaskList_Empty(t *testing.T) {
	if got := FormatTaskList(nil); got != "" {
		t.Errorf("FormatTaskList(nil) = %q, want empty", got)
	}
}

func TestFormatTaskList_Items(t *testing.T) {
	items := []TaskItem{
		{ID: "1", Status: "in_progress", Subject: "Fix bug"},
		{ID: "2", Status: "pending", Subject: "Add feature", Description: "new API"},
	}
	got := FormatTaskList(items)
	want := "#1. [in_progress] Fix bug\n#2. [pending] Add feature: new API\n"
	if got != want {
		t.Errorf("FormatTaskList() = %q, want %q", got, want)
	}
}

func TestFormatTaskList_NoDescription(t *testing.T) {
	items := []TaskItem{
		{ID: "3", Status: "completed", Subject: "Done"},
	}
	got := FormatTaskList(items)
	if strings.Contains(got, ": ") {
		t.Errorf("FormatTaskList() should not have ': ' for items without description, got %q", got)
	}
	if !strings.Contains(got, "#3. [completed] Done\n") {
		t.Errorf("FormatTaskList() = %q, want line starting with #3", got)
	}
}
