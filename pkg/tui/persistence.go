package tui

import (
	"fmt"
	"log/slog"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// persistTurn persists uncommitted engine messages to the short-term store.
// Called synchronously from the Bubble Tea Update loop on successful query end.
//
// Transaction semantics: only called when err == nil in queryEndMsg handler.
// Ctrl+C and error paths do NOT call persistTurn, ensuring no partial state is stored.
func (a *App) persistTurn() {
	if a.store == nil || a.sessionID == "" {
		return
	}

	engMsgs := a.engine.Messages() // returns copy under RLock
	if len(engMsgs) <= a.lastPersistedIdx {
		return // nothing new to persist
	}

	uncommitted := engMsgs[a.lastPersistedIdx:]
	storeMsgs, err := EngineMessagesToStore(uncommitted)
	if err != nil {
		slog.Error("persistTurn: convert messages", "error", err)
		return
	}

	// Convert []*short.Message for AppendMessages
	ptrs := make([]*short.Message, len(storeMsgs))
	for i := range storeMsgs {
		ptrs[i] = &storeMsgs[i]
	}

	if err := a.store.AppendMessages(a.sessionID, ptrs); err != nil {
		slog.Error("persistTurn: append messages", "error", err)
		return
	}

	if err := a.store.UpdateSessionTimestamp(a.sessionID); err != nil {
		slog.Error("persistTurn: update session timestamp", "error", err)
		// non-fatal: messages were persisted, timestamp is best-effort
	}

	a.lastPersistedIdx = len(engMsgs)
	slog.Info("persistTurn: persisted messages",
		"count", len(ptrs),
		"total", a.lastPersistedIdx,
		"session", fmt.Sprintf("%.8s", a.sessionID),
	)
}

// loadAndConvertMessages loads store messages and converts them to engine format.
// Deduplicates the load→dereference→convert pattern used in auto-resume, picker, and fork.
func loadAndConvertMessages(store *short.Store, sessionID string) ([]types.Message, error) {
	storeMsgs, err := store.LoadMessages(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	msgSlice := make([]short.Message, len(storeMsgs))
	for i, m := range storeMsgs {
		msgSlice[i] = *m
	}
	return StoreMessagesToEngine(msgSlice)
}
