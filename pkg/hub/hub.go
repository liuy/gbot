// Package hub provides callback-based synchronous event routing.
//
// Hub sits between Engine and UI clients (TUI, WebSocket, Discord, etc).
// Engine goroutine calls Dispatch synchronously for each event;
// Hub iterates all registered handlers and calls Handle in the same call stack.
// Hub has no goroutines and no channels — it is purely a routing layer.
package hub

import (
	"sync"

	"github.com/liuy/gbot/pkg/types"
)

// Event is the event type dispatched by Engine through Hub.
type Event = types.QueryEvent

// EventHandler is implemented by UI clients to receive Engine events.
type EventHandler interface {
	Handle(event Event)
}

// Hub routes events from Engine to all subscribed handlers.
// It is safe for concurrent use (Dispatch and Subscribe are mutex-protected).
type Hub struct {
	mu       sync.Mutex
	handlers []EventHandler
}

// NewHub creates a new Hub with no handlers.
func NewHub() *Hub {
	return &Hub{}
}

// Subscribe adds a handler and returns an unsubscribe function.
// Calling the returned function removes the handler from future Dispatch calls.
func (h *Hub) Subscribe(handler EventHandler) func() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.handlers = append(h.handlers, handler)
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for i, hd := range h.handlers {
			if hd == handler {
				h.handlers = append(h.handlers[:i], h.handlers[i+1:]...)
				return
			}
		}
	}
}

// Dispatch sends an event to all registered handlers synchronously.
// Handlers are called in subscription order.
// If a handler panics, Dispatch recovers and continues to remaining handlers.
func (h *Hub) Dispatch(event Event) {
	h.mu.Lock()
	handlers := make([]EventHandler, len(h.handlers))
	copy(handlers, h.handlers)
	h.mu.Unlock()

	for _, handler := range handlers {
		func() {
			defer func() {
				_ = recover()
			}()
			handler.Handle(event)
		}()
	}
}

// Close removes all handlers.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers = nil
}
