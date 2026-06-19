package tool

import (
	"strings"
	"testing"
)

func TestHighlightCode_Go(t *testing.T) {
	t.Parallel()
	result := HighlightCode("fmt.Println(\"hello\")", "go")
	if !strings.Contains(result, "fmt") || !strings.Contains(result, "Println") {
		t.Errorf("expected highlighted code, got: %q", result)
	}
}

func TestHighlightCode_UnknownLang(t *testing.T) {
	t.Parallel()
	result := HighlightCode("some code here", "unknownlang123")
	if !strings.Contains(result, "some code here") {
		t.Errorf("expected fallback plain text, got: %q", result)
	}
}

func TestHighlightCode_EmptyCode(t *testing.T) {
	t.Parallel()
	result := HighlightCode("", "go")
	if result != "" {
		t.Errorf("expected empty, got: %q", result)
	}
}

func TestHighlightCode_FilePath(t *testing.T) {
	t.Parallel()
	result := HighlightCode("func main() {}", "main.go")
	if !strings.Contains(result, "func") || !strings.Contains(result, "main") {
		t.Errorf("expected highlighted Go code from file path, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// stripColorFromLeadingWhitespace
// ---------------------------------------------------------------------------

func TestStripColorFromLeadingWhitespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "multiline string indent not colored",
			in:   "fmt.Sprint(\x1b[38;5;186m\"hello \\\n\x1b[38;5;186m    world\"\x1b[0m)",
			want: "fmt.Sprint(\x1b[38;5;186m\"hello \\\n    \x1b[38;5;186mworld\"\x1b[0m)",
		},
		{
			name: "no leading whitespace unchanged",
			in:   "fmt.Sprint(\x1b[38;5;186m\"hello\"\x1b[0m)",
			want: "fmt.Sprint(\x1b[38;5;186m\"hello\"\x1b[0m)",
		},
		{
			name: "plain text unchanged",
			in:   "hello\nworld",
			want: "hello\nworld",
		},
		{
			name: "colored indent at start of first line",
			in:   "\x1b[38;5;186m    indented string\"\x1b[0m",
			want: "    \x1b[38;5;186mindented string\"\x1b[0m",
		},
		{
			name: "tab indent not colored",
			in:   "\x1b[38;5;186m\tindented\"\x1b[0m",
			want: "\t\x1b[38;5;186mindented\"\x1b[0m",
		},
		{
			name: "mixed spaces and tabs",
			in:   "foo\n\x1b[38;5;186m  \t  text\x1b[0m",
			want: "foo\n  \t  \x1b[38;5;186mtext\x1b[0m",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stripColorFromLeadingWhitespace(tc.in)
			if got != tc.want {
				t.Errorf("got:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestHighlightCode_CacheHit verifies repeated calls with same (code, lang)
// return identical output and hit the cache (verified via Len()).
func TestHighlightCode_CacheHit(t *testing.T) {
	// Clear cache state for deterministic Len assertion.
	highlightCache.Purge()

	got1 := HighlightCode(`fmt.Println("x")`, "go")
	got2 := HighlightCode(`fmt.Println("x")`, "go")
	if got1 != got2 {
		t.Errorf("cache returned different results for same input:\n  first:  %q\n  second: %q", got1, got2)
	}
	if n := highlightCache.Len(); n != 1 {
		t.Errorf("cache Len = %d after one unique call, want 1", n)
	}

	// Different input grows cache.
	HighlightCode(`echo hi`, "bash")
	if n := highlightCache.Len(); n != 2 {
		t.Errorf("cache Len = %d after two unique calls, want 2", n)
	}
}

// TestHighlightCode_CacheKeySeparatesByLanguage verifies (code, lang) pairs
// are cached independently — same code with different language must not collide.
func TestHighlightCode_CacheKeySeparatesByLanguage(t *testing.T) {
	highlightCache.Purge()
	const code = `x = 1`
	HighlightCode(code, "go")
	HighlightCode(code, "python")
	if n := highlightCache.Len(); n != 2 {
		t.Errorf("cache Len = %d, want 2 (same code, different lang should be distinct)", n)
	}
}
