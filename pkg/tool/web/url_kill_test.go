package web

import "testing"

func TestUKIsURLScheme(t *testing.T) {
	// Short hostnames keep the scheme path authoritative; the bare-domain
	// fallback alone would reject "a.b" (1-char TLD).
	if !IsURL("https://a.b") {
		t.Error("IsURL(https://a.b) = false, want true")
	}
	if !IsURL("http://a.b") {
		t.Error("IsURL(http://a.b) = false, want true")
	}
}

func TestUKIsURLLeadingDot(t *testing.T) {
	// A dot at index 0 still starts the TLD scan (loop starts at i=0).
	if !IsURL(".com") {
		t.Error("IsURL(.com) = false, want true")
	}
	if IsURL("example.1ab") {
		t.Error("IsURL(example.1ab) = true, want false (digit breaks TLD)")
	}
}

func TestUKIsASCIILetter(t *testing.T) {
	tests := []struct {
		c    byte
		want bool
	}{
		{'a', true},
		{'b', true},
		{'z', true},
		{'A', true},
		{'Y', true},
		{'Z', true},
		{' ', false},
		{'0', false},
		{'@', false},
		{'`', false},
		{'{', false},
		{'}', false},
		{'[', false},
	}
	for _, tt := range tests {
		if got := isASCIILetter(tt.c); got != tt.want {
			t.Errorf("isASCIILetter(%q) = %v, want %v", tt.c, got, tt.want)
		}
	}
}
