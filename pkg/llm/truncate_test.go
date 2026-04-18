package llm

import (
	"strings"
	"testing"
)

func TestTruncateForLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{name: "short string no truncation", input: "hello", maxLen: 10, want: "hello"},
		{name: "exact fit no truncation", input: "hello", maxLen: 5, want: "hello"},
		{name: "empty string", input: "", maxLen: 0, want: ""},
		{name: "empty string nonzero max", input: "", maxLen: 5, want: ""},
		{name: "zero maxLen truncates all", input: "abc", maxLen: 0, want: "..."},
		{name: "truncation appends ellipsis", input: "hello world", maxLen: 5, want: "hello..."},
		{name: "truncation single byte", input: "abcdef", maxLen: 1, want: "a..."},
		{name: "long repeated string", input: strings.Repeat("x", 1000), maxLen: 10, want: "xxxxxxxxxx..."},
		{name: "single char over max", input: "a", maxLen: 0, want: "..."},
		{name: "single char at max", input: "a", maxLen: 1, want: "a"},
		{name: "newlines in input", input: "line1\nline2\nline3", maxLen: 6, want: "line1\n..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateForLog(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateForLog(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestTruncateForLog_TruncationByteLength(t *testing.T) {
	t.Parallel()

	// Verify the truncated prefix is exactly maxLen bytes (not runes)
	input := "abcdefghijklmnopqrstuvwxyz"
	maxLen := 5
	got := truncateForLog(input, maxLen)

	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated result must end with '...', got %q", got)
	}
	prefix := strings.TrimSuffix(got, "...")
	if len(prefix) != maxLen {
		t.Errorf("prefix has %d bytes, want %d; prefix=%q", len(prefix), maxLen, prefix)
	}
	if prefix != "abcde" {
		t.Errorf("prefix = %q, want %q", prefix, "abcde")
	}
}

func TestTruncateForLog_DoesNotModifyOriginal(t *testing.T) {
	t.Parallel()

	// When no truncation happens, result must be identical to input
	input := "hello"
	got := truncateForLog(input, 10)
	if got != input {
		t.Errorf("truncateForLog(%q, 10) = %q, want exact same string %q", input, got, input)
	}
}

func TestTruncateForLog_MultiByteCharsByteTruncation(t *testing.T) {
	t.Parallel()

	// truncateForLog truncates by byte offset, not rune offset.
	// Input: "你好世界" = 12 bytes (3 bytes per CJK rune).
	// maxLen=6 should keep the first 2 CJK runes (6 bytes) + "..."
	input := "你好世界"
	got := truncateForLog(input, 6)
	if got != "你好..." {
		t.Errorf("truncateForLog(%q, 6) = %q, want %q", input, got, "你好...")
	}
}

func TestTruncateForLog_MultiByteCharsMidRune(t *testing.T) {
	t.Parallel()

	// If maxLen falls in the middle of a multi-byte rune, the function
	// slices at the byte boundary which produces invalid UTF-8.
	// This test documents that behavior — the function operates on bytes.
	input := "你好世界" // 12 bytes total
	maxLen := 4       // falls in the middle of the second rune (你=3 bytes, 好=3 bytes)
	got := truncateForLog(input, maxLen)

	// The first 3 bytes are "你", the 4th byte is the first byte of "好"
	// This produces invalid UTF-8 — that is the documented behavior.
	prefix := got[:maxLen]
	if len(prefix) != maxLen {
		t.Errorf("prefix length = %d, want %d", len(prefix), maxLen)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated result must end with '...', got %q", got)
	}
}
