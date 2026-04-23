// Package types defines shared types for the gbot engine and TUI.
package types

// ---------------------------------------------------------------------------
// EventDispatcher — interface for routing events from Engine to consumers.
// Source: engine/EventDispatcher (moved here for dependency inversion)
// ---------------------------------------------------------------------------

// EventDispatcher is the interface for routing events from Engine to consumers.
// Engine depends on this abstraction rather than a concrete Hub type,
// following the Dependency Inversion Principle.
// *hub.Hub satisfies this interface.
type EventDispatcher interface {
	Dispatch(event QueryEvent)
}
