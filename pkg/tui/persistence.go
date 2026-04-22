package tui

import (
	"fmt"
	"log/slog"
	"strings"

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

	// Convert []*short.TranscriptMessage for AppendMessages
	ptrs := make([]*short.TranscriptMessage, len(storeMsgs))
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

	// Auto-title: extract first user prompt as session title (TS: saveCustomTitle)
	// Only runs on the first persist to avoid overwriting user-set titles.
	if a.lastPersistedIdx == 0 && len(storeMsgs) > 0 {
		title := extractUserTitle(uncommitted)
		if title != "" {
			// Only set if no custom title exists (e.g. /switch -n "my title")
			if ses, err := a.store.GetSession(a.sessionID); err == nil && ses.Title == "" {
				if err := a.store.UpdateSessionTitle(a.sessionID, title); err != nil {
					slog.Error("persistTurn: auto-title", "error", err)
				}
				slog.Info("persistTurn: auto-titled session", "title", title)
			}
		}
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
	msgSlice := make([]short.TranscriptMessage, len(storeMsgs))
	for i, m := range storeMsgs {
		msgSlice[i] = *m
	}
	return StoreMessagesToEngine(msgSlice)
}

// extractUserTitle extracts the first user message text as a session title.
// Skips tool_result and other non-text content. Truncates to 200 chars.
// TS aligned: extractFirstPromptFromHead() (sessionStoragePortable.ts:135-201)
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
			// Skip XML-like tags (system messages, command-name, etc.)
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
