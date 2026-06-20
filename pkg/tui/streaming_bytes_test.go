package tui

import (
	"testing"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		bytes int
		want  string
	}{
		{"zero", 0, "0B"},
		{"one byte", 1, "1B"},
		{"just below KB", 1023, "1023B"},
		{"exactly 1KB", 1024, "1.0KB"},
		{"1.5KB", 1536, "1.5KB"},
		{"10KB", 10240, "10.0KB"},
		{"just below MB", 1024*1024 - 1, "1024.0KB"},
		{"exactly 1MB", 1024 * 1024, "1.0MB"},
		{"2.5MB", 2*1024*1024 + 512*1024, "2.5MB"},
		{"large file 10MB", 10 * 1024 * 1024, "10.0MB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
