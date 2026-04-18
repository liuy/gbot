package tool

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStripANSI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no ansi", "hello world", "hello world"},
		{"SGR color", "\x1b[31mred\x1b[0m text", "red text"},
		{"OSC BEL", "\x1b]0;title\x07body", "body"},
		{"OSC ST", "\x1b]0;title\x1b\\body", "body"},
		{"bare ESC", "before\x1bafter", "beforeafter"},
		{"empty string", "", ""},
		{"fast path", "plain text no escapes", "plain text no escapes"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := StripANSI(tc.in)
			if got != tc.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		maxRunes int
		want     string
	}{
		{name: "short string no truncation", input: "hello", maxRunes: 10, want: "hello"},
		{name: "exact fit no truncation", input: "hello", maxRunes: 5, want: "hello"},
		{name: "empty string", input: "", maxRunes: 0, want: ""},
		{name: "empty string nonzero max", input: "", maxRunes: 5, want: ""},
		{name: "zero maxRunes truncates all", input: "abc", maxRunes: 0, want: "..."},
		{name: "truncation appends ellipsis", input: "hello world", maxRunes: 5, want: "hello..."},
		{name: "truncation single char", input: "abcdef", maxRunes: 1, want: "a..."},
		{name: "unicode CJK characters", input: "你好世界朋友", maxRunes: 2, want: "你好..."},
		{name: "unicode mixed with ascii", input: "Hi世界", maxRunes: 3, want: "Hi世..."},
		{name: "single rune over max", input: "Ω", maxRunes: 0, want: "..."},
		{name: "single rune at max", input: "Ω", maxRunes: 1, want: "Ω"},
		{name: "multi-byte rune truncated mid-string", input: "aaΩbb", maxRunes: 3, want: "aaΩ..."},
		{name: "long repeated string", input: strings.Repeat("x", 1000), maxRunes: 10, want: "xxxxxxxxxx..."},
		{name: "newline counted as single rune", input: "a\nb\nc", maxRunes: 3, want: "a\nb..."},
		{name: "tab counted as single rune", input: "a\tb", maxRunes: 2, want: "a\t..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateRunes(tc.input, tc.maxRunes)
			if got != tc.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tc.input, tc.maxRunes, got, tc.want)
			}
		})
	}
}

func TestTruncateRunes_DoesNotModifyOriginal(t *testing.T) {
	t.Parallel()

	// Verify that when no truncation is needed, the original string is
	// returned exactly (not a copy with different contents).
	input := "hello"
	got := TruncateRunes(input, 10)
	if got != input {
		t.Errorf("TruncateRunes(%q, 10) = %q, want exact same string %q", input, got, input)
	}
}

func TestTruncateRunes_TruncationLength(t *testing.T) {
	t.Parallel()

	// Verify the truncated result has exactly maxRunes runes + 3 for "..."
	input := "abcdefghijklmnopqrstuvwxyz"
	maxRunes := 5
	got := TruncateRunes(input, maxRunes)

	// Must end with "..."
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated result must end with '...', got %q", got)
	}
	// The prefix before "..." must be exactly maxRunes runes
	prefix := strings.TrimSuffix(got, "...")
	if runeCount := len([]rune(prefix)); runeCount != maxRunes {
		t.Errorf("prefix has %d runes, want %d; prefix=%q", runeCount, maxRunes, prefix)
	}
	if prefix != "abcde" {
		t.Errorf("prefix = %q, want %q", prefix, "abcde")
	}
}

func TestTruncateRunes_UnicodeTruncationExactRuneBoundary(t *testing.T) {
	t.Parallel()

	// Ensure truncation splits at rune boundaries, never mid-byte
	input := "日本語テスト" // 5 CJK runes, each 3 bytes
	got := TruncateRunes(input, 3)
	if got != "日本語..." {
		t.Errorf("TruncateRunes(%q, 3) = %q, want %q", input, got, "日本語...")
	}

	// Verify the result is valid UTF-8 (no mid-rune corruption)
	for i, r := range got {
		if r == utf8.RuneError {
			t.Errorf("found RuneError at position %d in result %q", i, got)
		}
	}
}
