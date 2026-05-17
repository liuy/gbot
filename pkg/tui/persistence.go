package tui

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// persistTurn persists uncommitted engine messages to the short-term store.
// Delegates to engine.PersistNewMessages() when engine has a store.
// Called synchronously from the Bubble Tea Update loop on successful query end.
//
// Transaction semantics: only called when err == nil in queryEndMsg handler.
// Ctrl+C and error paths do NOT call persistTurn, ensuring no partial state is stored.
func (a *App) persistTurn() {
	a.engine.PersistNewMessages()
	a.syncPersistState()
}

// syncPersistState syncs TUI's lastPersistedIdx from engine after engine-driven persist.
func (a *App) syncPersistState() {
	a.lastPersistedIdx = a.engine.LastPersistedIdx()
	a.forkParentUUID = "" // engine clears this on successful persist
}

// loadAndConvertMessages loads store messages and converts them to engine format.
func loadAndConvertMessages(store *short.Store, sessionID string) ([]types.Message, error) {
	storeMsgs, err := store.LoadChainMessages(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	msgSlice := make([]short.TranscriptMessage, len(storeMsgs))
	for i, m := range storeMsgs {
		msgSlice[i] = *m
	}
	return StoreMessagesToEngine(msgSlice)
}

// extractUserTitle extracts the first user message text as a session title.
func extractUserTitle(msgs []types.Message) string {
	for _, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		for _, block := range m.Content {
			if block.Type != types.ContentTypeText {
				continue
			}
			text := strings.ReplaceAll(block.Text, "\n", " ")
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			if strings.HasPrefix(text, "<") {
				continue
			}
			if len(text) > 200 {
				text = strings.TrimSpace(text[:200]) + "…"
			}
			return text
		}
	}
	return ""
}
