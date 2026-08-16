package tui

import (
	"fmt"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// ---------------------------------------------------------------------------
// Auto-rewind — ESC cancel when LLM produced no meaningful content
// Source: TS REPL.tsx:2996-3022 — restoreMessageSync
// ---------------------------------------------------------------------------

func (a *App) tryAutoRewind() bool {
	msgs := a.engine.Messages()
	lastUserIdx := utils.LastSelectableUserMessageIndex(msgs)
	if lastUserIdx < 0 {
		return false
	}

	if !utils.MessagesAfterAreOnlySynthetic(msgs, lastUserIdx) {
		return false
	}

	userText := utils.FirstTextBlockContent(msgs[lastUserIdx])

	result, err := a.engine.RewindTo(lastUserIdx)
	if err != nil {
		slog.Error("tui:auto_rewind:engine_failed", "err", err)
		return false
	}
	_ = result

	if a.repl.committedCount <= len(a.repl.messages) {
		a.repl.messages = a.repl.messages[:a.repl.committedCount]
	}
	if a.repl.committedCount > len(a.repl.messages) {
		a.repl.committedCount = len(a.repl.messages)
	}

	a.input.SetValue(userText)

	if a.history != nil {
		a.history.RemoveLast()
	}

	a.markViewportDirty()
	return true
}

// ---------------------------------------------------------------------------
// /rewind command — message picker dialog + file restoration
// ---------------------------------------------------------------------------

// handleRewind opens a message picker dialog to select a rewind target.
// If the selected rewind point has file changes, a second dialog lets the user
// choose between restoring code only, conversation only, or both.
// Source: TS /rewind → MessageSelector.tsx → getRestoreOptions
func (a *App) handleRewind(commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return commitCmd
	}

	msgs := a.engine.Messages()

	var options []DialogOption
	var indices []int
	for i, msg := range msgs {
		if !utils.IsSelectableUserMessage(msg) {
			continue
		}
		text := utils.FirstTextBlockContent(msg)
		if text == "" {
			continue
		}
		// Restored timestamps carry UTC location from the driver scan; render local wall clock — store UTC, convert on read.
		label := fmt.Sprintf("%s  %s", msg.Timestamp.Local().Format("15:04"), truncateRunes(text, 60))
		options = append(options, DialogOption{Label: label})
		indices = append(indices, i)
	}

	if len(options) == 0 {
		// Source: TS MessageSelector.tsx:325-327 — "Nothing to rewind to yet."
		a.activeDialog = NewDialog("Nothing to rewind to yet.", []DialogOption{
			{Label: "OK"},
		})
		a.activeDialog.width = a.width
		return commitCmd
	}

	a.activeDialog = NewDialog("Rewind to message:", options)
	a.activeDialog.width = a.width

	a.onDialogDone = func(d *Dialog) (tea.Model, tea.Cmd) {
		if d.Aborted() {
			return a, a.readEvents()
		}
		idx := indices[d.SelectedIndex()]

		// TS align: MessageSelector.tsx uses preselectedMessage.uuid directly
		// for fileHistoryGetDiffStats. No fallback, no backwards search.
		var hasFileChanges bool
		if a.fileHistory != nil && idx < len(msgs) {
			checkMsgID := msgs[idx].ID
			hasFileChanges = a.fileHistory.HasChangesAtMessage(checkMsgID)
			slog.Info("tui:rewind_filecheck", "idx", idx, "checkMsgID", checkMsgID, "hasFileChanges", hasFileChanges)
		}

		if !hasFileChanges {
			// No file changes — rewind messages only directly
			return a.executeRewind(idx, engine.RewindMessagesOnly, msgs)
		}

		// File changes exist — show scope selection dialog
		// Source: TS MessageSelector.tsx getRestoreOptions — canRestoreCode_0 check
		scopeOpts := []DialogOption{
			{Label: "Restore code and conversation"},
			{Label: "Restore conversation"},
			{Label: "Restore code"},
			{Label: "Cancel"},
		}

		a.activeDialog = NewDialog("What do you want to restore?", scopeOpts)
		a.activeDialog.width = a.width

		a.onDialogDone = func(d *Dialog) (tea.Model, tea.Cmd) {
			if d.Aborted() {
				return a, a.readEvents()
			}
			selected := d.SelectedIndex()
			switch selected {
			case 0: // Restore code and conversation
				return a.executeRewind(idx, engine.RewindAll, msgs)
			case 1: // Restore conversation only
				return a.executeRewind(idx, engine.RewindMessagesOnly, msgs)
			case 2: // Restore code only
				return a.executeRewind(idx, engine.RewindFilesOnly, msgs)
			default: // Cancel
				return a, a.readEvents()
			}
		}
		return a, a.readEvents()
	}
	return commitCmd
}

// executeRewind performs the actual rewind with the given scope.
// Source: TS MessageSelector.tsx onSelectRestoreOption
func (a *App) executeRewind(idx int, scope engine.RewindScope, originalMsgs []types.Message) (tea.Model, tea.Cmd) {
	result, err := a.engine.RewindToScoped(idx, scope)
	if err != nil {
		slog.Error("tui:rewind:engine_failed", "err", err)
		return a, a.readEvents()
	}

	if len(result.RestoredFiles) > 0 {
		slog.Info("tui:rewind:files_restored", "count", len(result.RestoredFiles), "files", result.RestoredFiles)
	}

	// Sync persistence for message-affecting scopes
	if scope == engine.RewindAll || scope == engine.RewindMessagesOnly {
		// Fork point capture now happens inside engine.RewindToScoped
		// Sync TUI lastPersistedIdx from engine after rewind
		// Reset TUI messages — rewind changes engine messages, rebuild from scratch
		a.repl.Reset()
		a.repl.messages = engineMessagesToViews(a.engine.Messages(), a.engine.AllTools())
		a.repl.committedCount = 0
		// Restore input text from the selected message for resubmission
		selectedText := utils.FirstTextBlockContent(originalMsgs[idx])
		a.input.SetValue(selectedText)
		// TS align: tokenCountWithEstimation is lazy, status bar naturally shows
		// reduced count. gbot stores it, so sync the status bar after rewind.
		ctxTokens := engine.TokenCountWithEstimation(a.engine.Messages())
		a.status.SetContext(ctxTokens, a.engine.ContextWindow())
	}

	a.markViewportDirty()
	slog.Info("tui:rewind", "to_index", idx, "scope", scope, "message_count", result.MessageCount)
	return a, a.readEvents()
}
