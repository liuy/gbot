package tool

import (
	"fmt"
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

// TestHighlightCode_CacheCapacityEvictsAt512 pins the LRU capacity: filling
// with 513 distinct keys must leave exactly 512 (the oldest evicted). Drives
// the cache directly — re-tokenizing 513 codes through HighlightCode costs
// seconds, while the capacity contract only involves the LRU itself.
func TestHighlightCode_CacheCapacityEvictsAt512(t *testing.T) {
	highlightCache.Purge()
	for i := range 513 {
		highlightCache.Add(fmt.Sprintf("k%d", i), "v")
	}
	if n := highlightCache.Len(); n != 512 {
		t.Errorf("cache Len = %d after 513 distinct keys, want 512 (LRU capacity)", n)
	}
}

// TestHighlightCode_LexerSelection drives the three-way lexer choice: a known
// language name must pick the real lexer, not the plain-text fallback; a file
// path must be matched by extension. With the fallback in play both results
// would be identical, so inequality is the discriminator.
func TestHighlightCode_LexerSelection(t *testing.T) {
	t.Parallel()
	const code = `if x == 1 {}`
	fallback := HighlightCode(code, "zz-not-a-language")
	if got := HighlightCode(code, "go"); got == fallback {
		t.Errorf(`HighlightCode(code, "go") matched plain-text fallback output`)
	}
	if got := HighlightCode(code, "snippet.go"); got == fallback {
		t.Errorf(`HighlightCode(code, "snippet.go") matched plain-text fallback output`)
	}
}

// TestHighlightCode_CoalesceMergesAdjacentTokens pins the exact terminal bytes
// for adjacent same-type tokens: without chroma.Coalesce each punctuation
// character gets its own color span, so the golden only holds with coalescing.
func TestHighlightCode_CoalesceMergesAdjacentTokens(t *testing.T) {
	t.Parallel()
	got := HighlightCode("()", "go")
	const want = "\x1b[38;5;231m()\x1b[0m"
	if got != want {
		t.Errorf("HighlightCode(\"()\", \"go\") = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// stripLeadingANSI — the ANSI/whitespace prefix scanner
// ---------------------------------------------------------------------------

func TestStripLeadingANSI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// Trailing bare ESC: both prefix scanners must bound-check j == len
		// (comparison/logical/remove mutants index past the line here).
		{name: "bare ESC at end", in: "\x1b", want: "\x1b"},
		// Unterminated CSI: scan must stop at len without reading line[j]
		// or slicing past the end when collecting codes.
		{name: "CSI without final m", in: "\x1b[31", want: "\x1b[31"},
		// ESC not followed by '[' must not be treated as CSI start.
		{name: "ESC before plain char stays put", in: "\x1bX m rest", want: "\x1bX m rest"},
		// Complete CSI followed by whitespace is reordered verbatim.
		{name: "CSI then space then text", in: "\x1b[31m x", want: " \x1b[31mx"},
		{name: "two SGR codes then tab", in: "\x1b[31m\x1b[1m\tx", want: "\t\x1b[31m\x1b[1mx"},
		{name: "spaces only", in: "   ", want: "   "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stripLeadingANSI(tc.in)
			if got != tc.want {
				t.Errorf("stripLeadingANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
