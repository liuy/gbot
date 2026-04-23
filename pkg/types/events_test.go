package types

import (
	"testing"
)

// ---------------------------------------------------------------------------
// EventDispatcher interface — compile-time + runtime verification
// ---------------------------------------------------------------------------

// mockDispatcher satisfies EventDispatcher for testing.
type mockDispatcher struct {
	events []QueryEvent
}

func (d *mockDispatcher) Dispatch(event QueryEvent) {
	d.events = append(d.events, event)
}

// TestEventDispatcher_Interface verifies the interface is satisfiable.
func TestEventDispatcher_Interface(t *testing.T) {
	var d EventDispatcher = &mockDispatcher{}

	d.Dispatch(QueryEvent{
		Type: EventQueryStart,
		Text: "test",
	})

	md := d.(*mockDispatcher)
	if len(md.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(md.events))
	}
	if md.events[0].Type != EventQueryStart {
		t.Errorf("expected EventQueryStart, got %s", md.events[0].Type)
	}
	if md.events[0].Text != "test" {
		t.Errorf("expected text 'test', got %q", md.events[0].Text)
	}
}

// TestEventDispatcher_NilCheck verifies nil interface check.
func TestEventDispatcher_NilCheck(t *testing.T) {
	var d EventDispatcher
	// A nil interface should not satisfy the "is set" check
	if d != nil {
		t.Error("expected nil EventDispatcher")
	}
	_ = d // use variable to avoid unused warning
}
