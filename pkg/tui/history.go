package tui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// History — source: history.ts prompt history persistence
// Navigation: source: TS useArrowKeyHistory.tsx
// ---------------------------------------------------------------------------

// historyEntry is the JSONL on-disk format, matching TS LogEntry.
type historyEntry struct {
	Display   string `json:"display"`
	Timestamp int64  `json:"timestamp"`
}

// HistCursor tells the caller where to place the cursor after a history navigation.
type HistCursor int

const (
	CursorNone HistCursor = iota // no cursor movement (no-op)
	CursorHome                   // move cursor to start of text
	CursorEnd                    // move cursor to end of text
)

// HistResult is returned by Up/Down, containing the text and cursor action.
type HistResult struct {
	Text   string
	Cursor HistCursor
}

// History stores command history for Up/Down navigation.
//
// State model (aligned with TS useArrowKeyHistory.tsx):
//   - historyIndex: 0 = at draft (initial), 1+ = navigating history
//     (1 = newest entry, N = oldest entry)
//   - savedDraft: user's current input, saved on first Up press
//     (only saved when input is non-empty, matching TS setLastShownHistoryEntry)
type History struct {
	items        []string
	historyIndex int    // 0=draft, 1+=navigating (1=newest, N=oldest)
	savedDraft   string // draft saved on first Up; "" if input was empty
	maxSize      int
	filePath     string // path to history.jsonl; empty means no persistence
}

// NewHistory creates a new History with optional file persistence.
// If filePath is non-empty, existing entries are loaded from the JSONL file.
func NewHistory(filePath string) *History {
	// Validate: reject relative paths
	if filePath != "" && !filepath.IsAbs(filePath) {
		filePath = "" // disable persistence for relative paths
	}
	h := &History{
		items:    make([]string, 0, 100),
		maxSize:  200,
		filePath: filePath,
	}
	if filePath != "" {
		h.load()
	}
	return h
}

// Add appends a command to history and persists it to disk.
// Resets navigation state (historyIndex=0, savedDraft=""), matching TS resetHistory.
func (h *History) Add(cmd string) {
	if cmd == "" {
		return
	}
	// Don't add duplicates at the end
	if len(h.items) > 0 && h.items[len(h.items)-1] == cmd {
		return
	}
	h.items = append(h.items, cmd)
	h.historyIndex = 0
	h.savedDraft = ""

	// Cap at max size
	if len(h.items) > h.maxSize {
		h.items = h.items[1:]
	}

	h.save(cmd)
}

// Up navigates toward older history entries.
//
// TS useArrowKeyHistory.tsx onHistoryUp algorithm:
//  1. targetIndex = historyIndex (capture)
//  2. historyIndex++ (increment immediately for rapid keypresses)
//  3. If targetIndex === 0: save draft (only if non-empty input)
//  4. If targetIndex >= cache.length: rollback (decrement), return
//  5. Show cache[targetIndex] with cursor to START
//
// Go mapping: cache[i] = items[len(items)-1-i]
// (TS cache is newest-first; Go items is oldest-first)
func (h *History) Up(current string) HistResult {
	if len(h.items) == 0 {
		return HistResult{Text: current, Cursor: CursorNone}
	}

	targetIndex := h.historyIndex
	h.historyIndex++

	// Source: TS line 131-142 — save draft on first Up press
	if targetIndex == 0 {
		if strings.TrimSpace(current) != "" {
			h.savedDraft = current
		} else {
			h.savedDraft = ""
		}
	}

	// Source: TS line 166-171 — rollback if past oldest entry
	if targetIndex >= len(h.items) {
		h.historyIndex--
		return HistResult{Cursor: CursorNone}
	}

	// Show entry: items[len-1-targetIndex]
	// Source: TS line 174 — updateInput(historyCache.current[targetIndex], true)
	item := h.items[len(h.items)-1-targetIndex]
	return HistResult{Text: item, Cursor: CursorHome}
}

// Down navigates toward newer history entries or restores the draft.
//
// TS useArrowKeyHistory.tsx onHistoryDown algorithm:
//  1. currentIndex = historyIndex
//  2. If currentIndex > 1: historyIndex--, show cache[currentIndex-2], cursor END
//  3. If currentIndex === 1: historyIndex=0, restore draft (or clear), cursor END
//  4. If currentIndex <= 0: no-op, return false
func (h *History) Down() HistResult {
	currentIndex := h.historyIndex

	if currentIndex > 1 {
		// Source: TS line 188-189 — go to newer entry
		h.historyIndex--
		item := h.items[len(h.items)-currentIndex+1]
		return HistResult{Text: item, Cursor: CursorEnd}
	}

	if currentIndex == 1 {
		// Source: TS line 191-206 — back to draft
		h.historyIndex = 0
		if h.savedDraft != "" {
			// Source: TS line 193-200 — restore saved draft
			return HistResult{Text: h.savedDraft, Cursor: CursorEnd}
		}
		// Source: TS line 202-204 — no draft saved, clear input
		return HistResult{Text: "", Cursor: CursorEnd}
	}

	// currentIndex <= 0: no-op
	// Source: TS line 206 — return currentIndex <= 0
	return HistResult{Cursor: CursorNone}
}

// ResetNav exits navigation mode and clears the draft.
// Source: TS resetHistory — sets historyIndex=0, lastShownHistoryEntry=undefined.
func (h *History) ResetNav() {
	h.historyIndex = 0
	h.savedDraft = ""
}

// RemoveLast removes the most recent history entry.
// Used by auto-rewind to remove the entry added by the cancelled query.
func (h *History) RemoveLast() {
	if len(h.items) == 0 {
		return
	}
	h.items = h.items[:len(h.items)-1]
	h.historyIndex = 0
	h.savedDraft = ""
	// Note: on-disk JSONL is append-only. The stale entry remains on disk.
	// This matches TS behavior where removeLastFromHistory either pops from
	// pending buffer (pre-flush) or adds to a skip-set (post-flush).
	// For simplicity, we only trim the in-memory slice.
}

// ---------------------------------------------------------------------------
// Persistence — JSONL append/load matching TS history.ts
// ---------------------------------------------------------------------------

// save appends a single entry to the history JSONL file.
func (h *History) save(cmd string) {
	if h.filePath == "" {
		return
	}
	entry := historyEntry{
		Display:   cmd,
		Timestamp: timestampMillis(),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}

	// Ensure directory exists
	dir := filepath.Dir(h.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}

	f, err := os.OpenFile(h.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		return
	}
}

// load reads all entries from the JSONL history file into items.
// Caller guarantees filePath is non-empty (NewHistory guards this).
func (h *History) load() {
	f, err := os.Open(h.filePath)
	if err != nil {
		return // file doesn't exist yet — that's fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry historyEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip malformed lines
		}
		if entry.Display == "" {
			continue
		}
		// Don't add duplicates
		if len(h.items) > 0 && h.items[len(h.items)-1] == entry.Display {
			continue
		}
		h.items = append(h.items, entry.Display)
	}

	// Cap at max size
	if len(h.items) > h.maxSize {
		h.items = h.items[len(h.items)-h.maxSize:]
	}
}

// timestampMillis returns current time as Unix milliseconds.
var timestampMillis = func() int64 {
	return time.Now().UnixMilli()
}
