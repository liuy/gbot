package bash

import "testing"

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"needs truncation", "hello world", 8, "hello..."},
		{"empty string", "", 5, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		maxSize int
		want    string
	}{
		{"small output", "hello", 10, "hello"},
		{"exact size", "hello", 5, "hello"},
		{"needs truncation", "hello world", 5, "hello\n... [output truncated]"},
		{"empty output", "", 5, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateOutput(tc.input, tc.maxSize)
			if got != tc.want {
				t.Errorf("truncateOutput(%q, %d) = %q, want %q", tc.input, tc.maxSize, got, tc.want)
			}
		})
	}
}
