package tui

// ---------------------------------------------------------------------------
// KillRing — Source: utils/Cursor.ts kill ring implementation
// ---------------------------------------------------------------------------

const killRingMaxSize = 10

// KillRing is a circular buffer of killed text, supporting Emacs-style kill/yank.
// Source: utils/Cursor.ts lines 16-111
type KillRing struct {
	entries       []string
	lastActionKill bool
}

// NewKillRing creates a new KillRing.
func NewKillRing() *KillRing {
	return &KillRing{
		entries: make([]string, 0, killRingMaxSize),
	}
}

// Push adds text to the kill ring.
// direction: "append" concatenates after the last entry, "prepend" concatenates before.
// If the previous action was also a kill, the text accumulates with the existing entry.
// Source: Cursor.ts pushToKillRing
func (k *KillRing) Push(text string, direction string) {
	if text == "" {
		return
	}

	if k.lastActionKill && len(k.entries) > 0 {
		// Accumulate with existing entry
		switch direction {
		case "append":
			k.entries[0] += text
		case "prepend":
			k.entries[0] = text + k.entries[0]
		default:
			k.entries = append([]string{text}, k.entries...)
		}
	} else {
		// New entry at front
		k.entries = append([]string{text}, k.entries...)
	}

	// Cap at max size
	if len(k.entries) > killRingMaxSize {
		k.entries = k.entries[:killRingMaxSize]
	}

	k.lastActionKill = true
}

// Top returns the most recent kill ring entry, or empty string.
// Source: Cursor.ts getLastKill
func (k *KillRing) Top() string {
	if len(k.entries) == 0 {
		return ""
	}
	return k.entries[0]
}

// Pop removes and returns the most recent entry.
func (k *KillRing) Pop() string {
	if len(k.entries) == 0 {
		return ""
	}
	top := k.entries[0]
	k.entries = k.entries[1:]
	return top
}

// ResetAccumulation marks that the next kill should start a new entry.
// Source: Cursor.ts resetKillAccumulation
func (k *KillRing) ResetAccumulation() {
	k.lastActionKill = false
}

// Clear empties the kill ring.
// Source: Cursor.ts clearKillRing
func (k *KillRing) Clear() {
	k.entries = k.entries[:0]
	k.lastActionKill = false
}

// Len returns the number of entries.
func (k *KillRing) Len() int {
	return len(k.entries)
}
