package engine

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// PersistNewMessages persists uncommitted engine messages to the short-term store.
// Called after each successful query (outside the for-loop, before EventQueryEnd).
// Silently returns if store is nil or sessionID is empty (sub-agents, headless mode).
//
// IMPORTANT: This method acquires e.mu.Lock(). Under the lock, use direct field
// access (e.messages, e.ContextTokens) — do NOT call e.Messages() or
// e.GetContextTokens() which acquire their own locks and will deadlock.
func (e *Engine) PersistNewMessages() {
	if e.store == nil || e.sessionID == "" {
		// Headless/sub-agent configs legitimately run without a store —
		// Info, not Warn, so normal deployments don't log noise per query.
		slog.Info("PersistNewMessages: skip persistence",
			"store_nil", e.store == nil,
			"sessionID", e.sessionID,
			"subagent", e.isSubagent,
		)
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.messages) <= e.lastPersistedIdx {
		slog.Info("PersistNewMessages: nothing new",
			"messages", len(e.messages),
			"lastPersistedIdx", e.lastPersistedIdx,
			"session", fmt.Sprintf("%.8s", e.sessionID),
		)
		return // nothing new to persist
	}

	uncommitted := e.messages[e.lastPersistedIdx:]

	// Attachment messages are ephemeral context for the current LLM call —
	// they should never be persisted as-is. Exception: prompt-mode
	// attachments are real user input queued mid-stream; persisting them as
	// plain user messages keeps restart-replay coherent (the assistant
	// answer would otherwise reference a question that never existed in DB).
	var persistable []types.Message
	for i := range uncommitted {
		msg := uncommitted[i]
		if msg.MessageType != types.MessageTypeAttachment {
			persistable = append(persistable, msg)
			continue
		}
		if msg.Attachment != nil && msg.Attachment.Mode == types.ItemModePrompt && !msg.Attachment.IsMeta {
			asUser := msg
			asUser.MessageType = types.MessageTypeUser
			asUser.Attachment = nil
			persistable = append(persistable, asUser)
		}
		// Non-prompt attachments (job notifications, reminders) stay ephemeral.
	}

	storeMsgs, err := short.EngineMessagesToStore(persistable)
	if err != nil {
		slog.Error("PersistNewMessages: convert messages", "error", err)
		return
	}

	// Use fork-aware persist when rewind has set a fork point
	if e.forkParentUUID != "" {
		err = e.store.AppendMessagesWithForkPoint(e.sessionID, storeMsgs, e.forkParentUUID)
	} else {
		err = e.store.AppendMessages(e.sessionID, storeMsgs)
	}
	if err != nil {
		slog.Error("PersistNewMessages: append messages", "error", err)
		return
	}
	e.forkParentUUID = "" // clear fork point after successful persist

	if err := e.store.UpdateSessionTimestamp(e.sessionID); err != nil {
		slog.Error("PersistNewMessages: update session timestamp", "error", err)
	}

	// Read ContextTokens directly — do NOT call e.GetContextTokens() (deadlock under lock)
	if e.ContextTokens > 0 {
		if err := e.store.UpdateContextTokens(e.sessionID, e.ContextTokens); err != nil {
			slog.Error("PersistNewMessages: update context tokens", "error", err)
		}
	}

	// Auto-title: extract first user prompt as session title
	// Only runs on the first persist to avoid overwriting user-set titles.
	if e.lastPersistedIdx == 0 && len(storeMsgs) > 0 {
		title := extractUserTitle(uncommitted)
		if title != "" {
			if ses, err := e.store.GetSession(e.sessionID); err == nil && ses.Title == "" {
				if err := e.store.UpdateSessionTitle(e.sessionID, title); err != nil {
					slog.Error("PersistNewMessages: auto-title", "error", err)
				}
				slog.Info("PersistNewMessages: auto-titled session", "title", title)
			}
		}
	}

	e.lastPersistedIdx = len(e.messages)
	slog.Info("PersistNewMessages: persisted messages",
		"count", len(storeMsgs),
		"total", e.lastPersistedIdx,
		"session", fmt.Sprintf("%.8s", e.sessionID),
	)
}

// LoadMessages loads store messages and converts them to engine format.
// Sets lastPersistedIdx to the number of loaded messages.
func (e *Engine) LoadMessages(sessionID string) ([]types.Message, error) {
	storeMsgs, err := e.store.LoadChainMessages(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	if len(storeMsgs) == 0 {
		return nil, nil
	}
	engineMsgs, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		return nil, fmt.Errorf("convert messages: %w", err)
	}
	return engineMsgs, nil
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
