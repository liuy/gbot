package tool

import "testing"

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
