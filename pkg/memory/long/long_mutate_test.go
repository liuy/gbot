package long

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseIndexLine_Shapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, line string
		want       IndexEntry
	}{
		{"em dash", "- [My Title](my_title.md) — some description",
			IndexEntry{FileName: "my_title.md", Title: "My Title", Description: "some description"}},
		{"double dash", "- [My Title](my_title.md) -- some description",
			IndexEntry{FileName: "my_title.md", Title: "My Title", Description: "some description"}},
		{"no description", "- [My Title](my_title.md)",
			IndexEntry{FileName: "my_title.md", Title: "My Title", Description: ""}},
		{"empty title", "- [](my_title.md)",
			IndexEntry{FileName: "my_title.md", Title: "", Description: ""}},
		{"empty parens", "- [My Title]()", IndexEntry{Title: "My Title"}},
		{"no parens", "- [My Title]", IndexEntry{}},
		{"closing before opening", "- ]My Title[", IndexEntry{}},
		{"paren closed before title", ") [Title](f.md)", IndexEntry{}},
		{"bracket closed before title", "] [Title](f.md)", IndexEntry{}},
	}
	for _, c := range cases {
		got := parseIndexLine(c.line)
		if got != c.want {
			t.Errorf("%s: parseIndexLine(%q) = %+v, want %+v", c.name, c.line, got, c.want)
		}
	}
}

func TestIsMarkdownFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
	}{
		{"a.md", true}, {"A.MD", true}, {"a.markdown", true}, {"a.MarkDown", true},
		{"a.mdx", true}, {"a.txt", false}, {"a", false}, {".md", true}, {"a.md.bak", false},
	}
	for _, c := range cases {
		if got := isMarkdownFile(c.name); got != c.want {
			t.Errorf("isMarkdownFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsAutoMemoryEnabled_Truthiness(t *testing.T) {
	for _, c := range []struct {
		val  string
		want bool
	}{
		{"", true}, {"1", true}, {"true", true}, {"TRUE", true}, {"True", true},
		{"0", false}, {"false", false}, {"FALSE", false}, {"False", false},
		{"yes", true}, {"no", true},
	} {
		t.Setenv("GBOT_AUTO_MEMORY_ENABLED", c.val)
		if got := IsAutoMemoryEnabled(); got != c.want {
			t.Errorf("GBOT_AUTO_MEMORY_ENABLED=%q → %v, want %v", c.val, got, c.want)
		}
	}
}

func TestValidateMemoryPath(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"has\x00null", ""},
		{"relative/path", ""},
		{"/", ""},
		{"//", ""},
		{`\\server\share`, ""},
		{"/a/b", "/a/b" + string(filepath.Separator)},
		{"/a/b/", "/a/b" + string(filepath.Separator)},
		{"/a/b/../c", "/a/c" + string(filepath.Separator)},
		{"/a", ""}, // near-root paths are rejected
		{"/ab", "/ab" + string(filepath.Separator)},
	}
	for _, c := range cases {
		if got := ValidateMemoryPath(c.in); got != c.want {
			t.Errorf("ValidateMemoryPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateMemoryPath_Tilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	for _, c := range []struct{ in, want string }{
		{"~/memdir", filepath.Join(home, "memdir") + string(filepath.Separator)},
		{"~\\memdir", filepath.Join(home, "memdir") + string(filepath.Separator)},
		{"~/", ""},
		{"~", ""},
		{"~/../x", filepath.Join(home, "../x") + string(filepath.Separator)},
		{"~/..", ""},
	} {
		if got := ValidateMemoryPath(c.in); got != c.want {
			t.Errorf("ValidateMemoryPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLastIndexByte(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    string
		c    byte
		n    int
		want int
	}{
		{"abc\ndef\nghi", '\n', 11, 7},
		{"abc\ndef\nghi", '\n', 8, 7},
		{"abc\ndef\nghi", '\n', 4, 3},
		{"abc\ndef\nghi", '\n', 3, -1},
		{"abc\ndef\nghi", '\n', 0, -1},
		{"abc\ndef\nghi", '\n', 100, 7},
		{"abcdef", '\n', 6, -1},
		{"", '\n', 0, -1},
	}
	for _, c := range cases {
		if got := lastIndexByte(c.s, c.c, c.n); got != c.want {
			t.Errorf("lastIndexByte(%q, '\\n', %d) = %d, want %d", c.s, c.n, got, c.want)
		}
	}
}

func TestFormatTruncationReason(t *testing.T) {
	t.Parallel()
	if got := formatTruncationReason(false, true, 5, 30000); got != "30000 bytes (limit: 25000 bytes) — index entries are too long" {
		t.Errorf("byte-only = %q", got)
	}
	if got := formatTruncationReason(true, false, 250, 100); got != "250 lines (limit: 200)" {
		t.Errorf("line-only = %q", got)
	}
	if got := formatTruncationReason(true, true, 250, 30000); got != "250 lines and 30000 bytes" {
		t.Errorf("both = %q", got)
	}
	if got := formatTruncationReason(false, false, 3, 10); got != "3 lines and 10 bytes" {
		t.Errorf("neither = %q", got)
	}
}

// TruncateEntrypoint cuts at the last newline under the byte cap, and hard
// cuts when a single line exceeds it. A warning is appended to the content.
func TestTruncateEntrypoint(t *testing.T) {
	t.Parallel()
	empty := TruncateEntrypoint("   \n  ")
	if empty.Content != "" || empty.WasLineTruncated || empty.WasByteTruncated {
		t.Errorf("whitespace input = %+v, want an empty result", empty)
	}

	// Split the truncated payload off the appended warning.
	payload := func(c string) string {
		before, _, ok := strings.Cut(c, "\n\n> WARNING:")
		if !ok {
			return c
		}
		return before
	}

	// 201 lines → line-truncated to 200.
	lines := make([]string, 201)
	for i := range lines {
		lines[i] = "l"
	}
	got := TruncateEntrypoint(strings.Join(lines, "\n"))
	if !got.WasLineTruncated || got.WasByteTruncated {
		t.Errorf("201 lines = %+v, want line-truncated only", got)
	}
	if got.LineCount != 201 || got.ByteCount != 401 {
		t.Errorf("counts = (%d, %d), want (201, 401)", got.LineCount, got.ByteCount)
	}
	if payload(got.Content) != strings.Repeat("l\n", 199)+"l" {
		t.Errorf("line-truncated payload = %q, want the first 200 lines", payload(got.Content))
	}
	if !strings.Contains(got.Content, "WARNING:") {
		t.Error("truncation must append a warning")
	}

	// One oversized line: hard cut at the byte cap.
	got = TruncateEntrypoint(strings.Repeat("x", MaxEntrypointBytes+10))
	if !got.WasByteTruncated || got.WasLineTruncated {
		t.Errorf("oversized single line = %+v, want byte-truncated only", got)
	}
	if payload(got.Content) != strings.Repeat("x", MaxEntrypointBytes) {
		t.Errorf("hard cut payload length = %d, want %d", len(payload(got.Content)), MaxEntrypointBytes)
	}

	// Many long lines under the line cap: cut at the last newline before the cap.
	block := strings.Repeat("y", 200)
	parts := make([]string, 150)
	for i := range parts {
		parts[i] = block
	}
	body := strings.Join(parts, "\n") // 150 lines, 30149 bytes
	got = TruncateEntrypoint(body)
	if got.WasLineTruncated || !got.WasByteTruncated {
		t.Errorf("150 long lines = %+v, want byte-truncated only", got)
	}
	// 124 whole lines fit before byte 25000 (124*201-1 = 24923).
	want := strings.Join(parts[:124], "\n")
	if payload(got.Content) != want {
		t.Errorf("newline-aligned cut = %d bytes, want the first 124 lines", len(payload(got.Content)))
	}
}

func TestSanitizeFileName(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"Hello World", "hello_world"},
		{"a/b:c", "abc"},
		{"keep-99_x", "keep-99_x"},
		{"!!!", "memory"},
		{"", "memory"},
		{"café note", "caf_note"},
		{"中文", "memory"},
		{"A  B", "a__b"},
	}
	for _, c := range cases {
		if got := sanitizeFileName(c.in); got != c.want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// UpdateMemoryIndex appends a new entry and keeps existing content intact.
func TestUpdateMemoryIndex_Append(t *testing.T) {
	dir := t.TempDir()
	if err := UpdateMemoryIndex(dir, "first.md", "first desc"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "- [first](first.md) — first desc\n" {
		t.Errorf("index after first = %q", data)
	}
	if err := UpdateMemoryIndex(dir, "second.md", "second desc"); err != nil {
		t.Fatalf("second update: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "- [first](first.md) — first desc\n- [second](second.md) — second desc\n" {
		t.Errorf("index after second = %q", data)
	}
	// Re-updating the same file replaces its line rather than appending.
	if err := UpdateMemoryIndex(dir, "first.md", "updated desc"); err != nil {
		t.Fatalf("third update: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "first.md") != 1 {
		t.Errorf("entry duplicated: %q", data)
	}
	if !strings.Contains(string(data), "updated desc") {
		t.Errorf("entry not updated: %q", data)
	}
}
