package tui

import (
	"testing"
	"time"
)

func TestDoublePress_SinglePress(t *testing.T) {
	t.Parallel()

	d := NewDoublePress()
	if d.Press("ctrl-c") {
		t.Error("first press should not return true")
	}
}

func TestDoublePress_DoublePress(t *testing.T) {
	d := NewDoublePress() // not parallel — timing sensitive
	d.Press("ctrl-c")
	if !d.Press("ctrl-c") {
		t.Error("second press within window should return true")
	}
}

func TestDoublePress_DifferentKey(t *testing.T) {
	d := NewDoublePress()
	d.Press("ctrl-c")
	if d.Press("ctrl-d") {
		t.Error("different key should not count as double press")
	}
}

func TestDoublePress_Expired(t *testing.T) {
	d := NewDoublePress()
	d.Press("ctrl-c")
	// Simulate timeout by manually setting lastTime
	d.mu.Lock()
	d.lastTime = time.Now().Add(-1 * time.Second)  // REAL-TIME: expiry simulation
	d.mu.Unlock()

	if d.Press("ctrl-c") {
		t.Error("expired double press should not return true")
	}
}

