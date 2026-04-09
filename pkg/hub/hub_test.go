package hub

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// mockHandler records received events.
type mockHandler struct {
	events []Event
	mu     sync.Mutex
}

func (m *mockHandler) Handle(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *mockHandler) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// panickingHandler panics on every Handle call.
type panickingHandler struct{}

func (p *panickingHandler) Handle(_ Event) {
	panic("boom")
}

func TestNewHub(t *testing.T) {
	h := NewHub()
	if h == nil {
		t.Fatal("NewHub returned nil")
	}
}

func TestSubscribeAndDispatch(t *testing.T) {
	h := NewHub()
	m := &mockHandler{}
	h.Subscribe(m)

	evt := Event{Type: types.EventTextDelta, Text: "hello"}
	h.Dispatch(evt)

	received := m.Events()
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	if received[0].Text != "hello" {
		t.Errorf("expected text 'hello', got '%s'", received[0].Text)
	}
}

func TestMultipleHandlers(t *testing.T) {
	h := NewHub()
	m1 := &mockHandler{}
	m2 := &mockHandler{}
	h.Subscribe(m1)
	h.Subscribe(m2)

	evt := Event{Type: types.EventTextDelta, Text: "hi"}
	h.Dispatch(evt)

	if len(m1.Events()) != 1 {
		t.Errorf("handler1: expected 1 event, got %d", len(m1.Events()))
	}
	if len(m2.Events()) != 1 {
		t.Errorf("handler2: expected 1 event, got %d", len(m2.Events()))
	}
}

func TestUnsubscribe(t *testing.T) {
	h := NewHub()
	m1 := &mockHandler{}
	m2 := &mockHandler{}

	unsub1 := h.Subscribe(m1)
	h.Subscribe(m2)

	evt := Event{Type: types.EventTextDelta, Text: "first"}
	h.Dispatch(evt)

	unsub1()

	evt2 := Event{Type: types.EventTextDelta, Text: "second"}
	h.Dispatch(evt2)

	if len(m1.Events()) != 1 {
		t.Errorf("handler1 after unsub: expected 1 event, got %d", len(m1.Events()))
	}
	if len(m2.Events()) != 2 {
		t.Errorf("handler2: expected 2 events, got %d", len(m2.Events()))
	}
}

func TestUnsubscribeIdempotent(t *testing.T) {
	h := NewHub()
	m := &mockHandler{}
	unsub := h.Subscribe(m)

	unsub() // first call removes
	unsub() // second call is no-op

	h.Dispatch(Event{Type: types.EventTextDelta, Text: "test"})
	if len(m.Events()) != 0 {
		t.Errorf("expected 0 events after double unsub, got %d", len(m.Events()))
	}
}

func TestDispatchNoHandlers(t *testing.T) {
	h := NewHub()
	// Should not panic
	h.Dispatch(Event{Type: types.EventTextDelta, Text: "nobody"})
}

func TestDispatchPanickingHandler(t *testing.T) {
	h := NewHub()
	p := &panickingHandler{}
	m := &mockHandler{}

	// panicking handler subscribed first
	h.Subscribe(p)
	h.Subscribe(m)

	// Dispatch should not panic, m should still receive the event
	h.Dispatch(Event{Type: types.EventTextDelta, Text: "survive"})

	if len(m.Events()) != 1 {
		t.Errorf("expected handler to receive event despite panic, got %d", len(m.Events()))
	}
}

func TestClose(t *testing.T) {
	h := NewHub()
	m := &mockHandler{}
	h.Subscribe(m)

	h.Close()

	h.Dispatch(Event{Type: types.EventTextDelta, Text: "after close"})
	if len(m.Events()) != 0 {
		t.Errorf("expected 0 events after Close, got %d", len(m.Events()))
	}
}

func TestConcurrentDispatch(t *testing.T) {
	h := NewHub()
	var count atomic.Int32
	m := &struct {
		EventHandler
	}{
		EventHandler: EventHandlerFunc(func(_ Event) {
			count.Add(1)
		}),
	}
	h.Subscribe(m)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Dispatch(Event{Type: types.EventTextDelta, Text: "concurrent"})
		}()
	}
	wg.Wait()

	if count.Load() != 100 {
		t.Errorf("expected 100 events, got %d", count.Load())
	}
}

func TestAllEventTypes(t *testing.T) {
	h := NewHub()
	m := &mockHandler{}
	h.Subscribe(m)

	events := []Event{
		{Type: types.EventStreamStart},
		{Type: types.EventTextDelta, Text: "delta"},
		{Type: types.EventToolUseStart, ToolUse: &types.ToolUseEvent{ID: "1", Name: "bash"}},
		{Type: types.EventToolResult, ToolResult: &types.ToolResultEvent{ToolUseID: "1"}},
		{Type: types.EventError, Error: errTest},
		{Type: types.EventComplete},
	}

	for _, evt := range events {
		h.Dispatch(evt)
	}

	received := m.Events()
	if len(received) != len(events) {
		t.Fatalf("expected %d events, got %d", len(events), len(received))
	}
	for i, r := range received {
		if r.Type != events[i].Type {
			t.Errorf("event %d: expected type %s, got %s", i, events[i].Type, r.Type)
		}
	}
}

// EventHandlerFunc is an adapter for functions to implement EventHandler.
type EventHandlerFunc func(Event)

func (f EventHandlerFunc) Handle(e Event) { f(e) }

var errTest = func() error {
	return &testError{"test error"}
}()

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestHub_RaceStress(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Start 5 handlers that subscribe/unsubscribe in a loop
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			m := &mockHandler{}
			for {
				select {
				case <-stop:
					return
				default:
					unsub := h.Subscribe(m)
					h.Dispatch(Event{Type: types.EventTextDelta, Text: fmt.Sprintf("handler-%d", id)})
					unsub()
				}
			}
		}(i)
	}

	// Start 10 dispatchers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h.Dispatch(Event{Type: types.EventTextDelta, Text: fmt.Sprintf("disp-%d-%d", id, j)})
			}
		}(i)
	}

	// Let it run then stop
	time.Sleep(100 * time.Millisecond)
	close(stop)

	// Give goroutines time to finish their last iteration
	time.Sleep(50 * time.Millisecond)
	wg.Wait()
}
