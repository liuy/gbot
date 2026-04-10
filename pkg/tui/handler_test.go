package tui

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// P2-1: Dropped event counter
// ---------------------------------------------------------------------------

func TestTUIHandler_DroppedCounter_Zero(t *testing.T) {
	h := NewTUIHandler()
	if h.Dropped() != 0 {
		t.Errorf("new handler should have 0 dropped, got %d", h.Dropped())
	}
}

func TestTUIHandler_DroppedCounter_WhenBufferFull(t *testing.T) {
	h := NewTUIHandler()
	// Fill the 256-buffer
	for i := 0; i < 256; i++ {
		h.appCh <- streamChunkMsg{Text: "fill"}
	}

	// Next event should be dropped
	h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "dropped"})

	if h.Dropped() != 1 {
		t.Errorf("expected 1 dropped, got %d", h.Dropped())
	}

	// And another
	h.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "also dropped"})
	if h.Dropped() != 2 {
		t.Errorf("expected 2 dropped, got %d", h.Dropped())
	}
}

// ---------------------------------------------------------------------------
// P2-3: EventStreamStart and EventMessage handling in convertEventToMsg
// ---------------------------------------------------------------------------

func TestConvertEventToMsg_StreamStart(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventStreamStart})
	if msg == nil {
		t.Fatal("EventStreamStart should not return nil")
	}
	_, ok := msg.(streamStartMsg)
	if !ok {
		t.Errorf("expected streamStartMsg, got %T", msg)
	}
}

func TestConvertEventToMsg_EventMessage_WithMessage(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type: types.EventMessage,
		Message: &types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock("hello"),
			},
		},
	})
	if msg == nil {
		t.Fatal("EventMessage with non-nil Message should not return nil")
	}
	sm, ok := msg.(streamMessageMsg)
	if !ok {
		t.Fatalf("expected streamMessageMsg, got %T", msg)
	}
	if sm.Role != string(types.RoleUser) {
		t.Errorf("expected role %q, got %q", types.RoleUser, sm.Role)
	}
}

func TestConvertEventToMsg_EventMessage_NilMessage(t *testing.T) {
	h := NewTUIHandler()
	msg := h.convertEventToMsg(types.QueryEvent{
		Type:    types.EventMessage,
		Message: nil,
	})
	// nil Message should still return nil — nothing to display
	if msg != nil {
		t.Errorf("EventMessage with nil Message should return nil, got %T", msg)
	}
}
