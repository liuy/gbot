package tui

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// DoublePress — Source: hooks/useDoublePress.ts
// ---------------------------------------------------------------------------

// doublePressTimeout is the window for detecting a double press.
// Source: useDoublePress.ts DOUBLE_PRESS_TIMEOUT_MS = 800
const doublePressTimeout = 800 * time.Millisecond

// DoublePress detects two consecutive key presses within a timeout window.
// Source: hooks/useDoublePress.ts
type DoublePress struct {
	mu       sync.Mutex
	lastTime time.Time
	pending  bool
	keyName  string
}

// NewDoublePress creates a new DoublePress detector.
func NewDoublePress() *DoublePress {
	return &DoublePress{}
}

// Press records a key press. Returns true if this is the second press within
// the timeout window (i.e., a double press was detected).
// Source: useDoublePress.ts — tracks timestamps, fires onDoublePress on second press
func (d *DoublePress) Press(keyName string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	if d.pending && d.keyName == keyName && now.Sub(d.lastTime) <= doublePressTimeout {
		// Double press detected
		d.pending = false
		d.lastTime = time.Time{}
		return true
	}

	// First press — start timeout window
	d.pending = true
	d.keyName = keyName
	d.lastTime = now
	return false
}

// IsPending returns whether a first press is waiting for the second.
func (d *DoublePress) IsPending() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending
}

// KeyName returns the name of the key that is pending.
func (d *DoublePress) KeyName() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.keyName
}

// Reset clears the double press state.
func (d *DoublePress) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = false
	d.lastTime = time.Time{}
}
