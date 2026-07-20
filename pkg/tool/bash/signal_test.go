package bash

import (
	"testing"
)

func TestGetTerminalSize(t *testing.T) {
	t.Parallel()

	rows, cols, err := GetTerminalSize()
	if err != nil {
		t.Errorf("GetTerminalSize() error: %v", err)
	}
	if rows < 1 {
		t.Errorf("GetTerminalSize() rows = %d, want positive", rows)
	}
	if cols < 1 {
		t.Errorf("GetTerminalSize() cols = %d, want positive", cols)
	}
}

func TestGetTerminalSizeFd_Fallback(t *testing.T) {
	t.Parallel()

	// Non-existent fd → term.GetSize fails → fallback 24x80
	rows, cols, err := getTerminalSizeFd(999)
	if err != nil {
		t.Errorf("getTerminalSizeFd() error: %v", err)
	}
	if rows != 24 || cols != 80 {
		t.Errorf("getTerminalSizeFd() = %d, %d, want 24, 80 (fallback)", rows, cols)
	}
}
