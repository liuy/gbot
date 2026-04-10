package tui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ---------------------------------------------------------------------------
// History — source: history.ts prompt history persistence
// ---------------------------------------------------------------------------

// historyEntry is the JSONL on-disk format, matching TS LogEntry.
type historyEntry struct {
	Display   string `json:"display"`
	Timestamp int64  `json:"timestamp"`
}

// History stores command history for Up/Down navigation.
// Source: history.ts → promptHistory + history.jsonl persistence
type History struct {
	items    []string
	index    int
	maxSize  int
	navMode  bool
	filePath string // path to history.jsonl; empty means no persistence
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
		maxSize:  100,
		index:    -1,
		filePath: filePath,
	}
	if filePath != "" {
		h.load()
	}
	return h
}

// Add appends a command to history and persists it to disk.
func (h *History) Add(cmd string) {
	if cmd == "" {
		return
	}
	// Don't add duplicates at the end
	if len(h.items) > 0 && h.items[len(h.items)-1] == cmd {
		return
	}
	h.items = append(h.items, cmd)
	h.index = len(h.items) - 1
	h.navMode = false

	// Cap at max size
	if len(h.items) > h.maxSize {
		h.items = h.items[1:]
		h.index--
	}

	h.save(cmd)
}

// Up returns the previous command in history, starting from current input.
func (h *History) Up(current string) (string, bool) {
	if len(h.items) == 0 {
		return current, false
	}

	// If not in nav mode, start from the end
	if !h.navMode {
		h.navMode = true
		// If current matches last item, go one back
		if len(h.items) > 0 && h.items[len(h.items)-1] == current {
			h.index = len(h.items) - 2
		} else {
			h.index = len(h.items) - 1
		}
	} else {
		h.index--
	}

	if h.index < 0 {
		h.index = 0
	}

	return h.items[h.index], true
}

// Down returns the next command in history.
func (h *History) Down() (string, bool) {
	if len(h.items) == 0 {
		return "", false
	}

	h.index++

	if h.index >= len(h.items) {
		h.index = len(h.items) - 1
	}

	return h.items[h.index], true
}

// ResetNav exits navigation mode.
func (h *History) ResetNav() {
	h.navMode = false
}

// Len returns the number of history entries.
func (h *History) Len() int {
	return len(h.items)
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
	defer func() { _ = f.Close() }()

	if _, err := f.Write(line); err != nil {
			return
		}
		if _, err := f.Write([]byte("\n")); err != nil {
			return
		}
}

// load reads all entries from the JSONL history file into items.
func (h *History) load() {
	if h.filePath == "" {
		return
	}

	f, err := os.Open(h.filePath)
	if err != nil {
		return // file doesn't exist yet — that's fine
	}
	defer func() { _ = f.Close() }()

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

	if len(h.items) > 0 {
		h.index = len(h.items) - 1
	}
}

// timestampMillis returns current time as Unix milliseconds.
var timestampMillis = func() int64 {
	return time.Now().UnixMilli()
}
