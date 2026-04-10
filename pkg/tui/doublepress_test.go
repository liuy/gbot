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
	d.lastTime = time.Now().Add(-1 * time.Second)
	d.mu.Unlock()

	if d.Press("ctrl-c") {
		t.Error("expired double press should not return true")
	}
}

func TestDoublePress_IsPending(t *testing.T) {
	d := NewDoublePress()
	if d.IsPending() {
		t.Error("should not be pending initially")
	}
	d.Press("ctrl-c")
	if !d.IsPending() {
		t.Error("should be pending after first press")
	}
	d.Press("ctrl-c") // double press clears pending
	if d.IsPending() {
		t.Error("should not be pending after double press")
	}
}

func TestDoublePress_KeyName(t *testing.T) {
	d := NewDoublePress()
	if d.KeyName() != "" {
		t.Errorf("KeyName() = %q, want empty", d.KeyName())
	}
	d.Press("ctrl-x")
	if d.KeyName() != "ctrl-x" {
		t.Errorf("KeyName() = %q, want %q", d.KeyName(), "ctrl-x")
	}
}

func TestDoublePress_Reset(t *testing.T) {
	d := NewDoublePress()
	d.Press("ctrl-c")
	if !d.IsPending() {
		t.Fatal("should be pending after press")
	}
	d.Reset()
	if d.IsPending() {
		t.Error("should not be pending after reset")
	}
	// Second press after reset should NOT be double press
	if d.Press("ctrl-c") {
		t.Error("should not detect double press after reset")
	}
}
