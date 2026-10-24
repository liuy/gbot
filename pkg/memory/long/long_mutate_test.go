package long

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
		{"bracket at byte zero", "[T](f.md)", IndexEntry{FileName: "f.md", Title: "T"}},
		{"bracket missing but text and parens present", "x] y (f.md)", IndexEntry{}},
		{"paren at byte zero", "(f.md) [T]", IndexEntry{FileName: "f.md", Title: "T"}},
		{"paren missing but text and bracket present", "[T] x)", IndexEntry{}},
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
		{"\nbc", '\n', 3, 0},
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
	if empty.LineCount != 0 || empty.ByteCount != 0 {
		t.Errorf("whitespace input counts = (%d, %d), want (0, 0)", empty.LineCount, empty.ByteCount)
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

	// Exactly at the line cap: no truncation at all, counts reported verbatim.
	atCap := strings.Repeat("l\n", 199) + "l"
	if len(strings.Split(atCap, "\n")) != 200 {
		t.Fatalf("setup: want 200 lines, got %d", len(strings.Split(atCap, "\n")))
	}
	got = TruncateEntrypoint(atCap)
	if got.WasLineTruncated || got.WasByteTruncated {
		t.Errorf("200 lines exactly = %+v, want no truncation at the cap", got)
	}
	if got.LineCount != 200 || got.ByteCount != len(atCap) {
		t.Errorf("counts = (%d, %d), want (200, %d)", got.LineCount, got.ByteCount, len(atCap))
	}
	if got.Content != atCap {
		t.Errorf("at-cap content = %q, want unchanged input", got.Content)
	}

	// Exactly at the byte cap on a single line: still no truncation.
	atBytes := strings.Repeat("x", MaxEntrypointBytes)
	got = TruncateEntrypoint(atBytes)
	if got.WasLineTruncated || got.WasByteTruncated {
		t.Errorf("%d bytes exactly = %+v, want no truncation at the cap", MaxEntrypointBytes, got)
	}
	if got.Content != atBytes {
		t.Errorf("at-cap byte content changed: %d bytes, want %d", len(got.Content), MaxEntrypointBytes)
	}

	// First 200 lines land exactly on the byte cap: the byte cut must not
	// fire on content that is at (not over) the limit after the line cut.
	capLines := make([]string, 201)
	for i := range capLines {
		if i < 199 {
			capLines[i] = strings.Repeat("a", 124)
		} else if i == 199 {
			capLines[i] = strings.Repeat("a", 125)
		} else {
			capLines[i] = "tail"
		}
	}
	first200 := strings.Join(capLines[:200], "\n")
	if len(first200) != MaxEntrypointBytes {
		t.Fatalf("setup: first 200 lines = %d bytes, want %d", len(first200), MaxEntrypointBytes)
	}
	got = TruncateEntrypoint(strings.Join(capLines, "\n"))
	if !got.WasLineTruncated || !got.WasByteTruncated {
		t.Fatalf("cap-boundary setup = %+v, want both flags set", got)
	}
	if payload(got.Content) != first200 {
		t.Errorf("payload = %d bytes, want the full %d-byte first-200-lines cut", len(payload(got.Content)), MaxEntrypointBytes)
	}

	// Newline at index 1 is the last newline under the cap: cut there, not
	// at the hard byte limit.
	got = TruncateEntrypoint("a\n" + strings.Repeat("y", MaxEntrypointBytes-1))
	if payload(got.Content) != "a" {
		t.Errorf("early-newline payload = %q, want %q", payload(got.Content), "a")
	}
}

func TestSanitizeFileName(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"Hello World", "hello_world"},
		{"a/b:c", "abc"},
		{"keep-99_x", "keep-99_x"},
		{"z", "z"},
		{"0", "0"},
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

// The 0o755 mode is part of EnsureMemoryDir's contract (readable by group
// tools, writable by the owner); umask is pinned so the assertion sees the
// literal mode the code passes to MkdirAll.
func TestEnsureMemoryDir_Permissions(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	workingDir := filepath.Join(t.TempDir(), "proj")
	if err := EnsureMemoryDir(workingDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(GetMemoryPath(workingDir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("memory dir mode = %o, want 755", perm)
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

// Callers up the stack match on the wrapped error (os.IsNotExist /
// errors.As), which silently breaks when a %w verb degrades to %v.
func TestErrorWrapChains(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based error injection does not fail as root")
	}
	base := t.TempDir()

	readOnly := filepath.Join(base, "ro")
	if err := os.MkdirAll(readOnly, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnly, 0o755) }()

	fileAsPath := filepath.Join(base, "plainfile")
	if err := os.WriteFile(fileAsPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirAsIndex := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirAsIndex, EntrypointName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirAsIndex, "test.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTargetIsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(writeTargetIsDir, "test.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	roIndexUpdate := t.TempDir()
	if err := os.WriteFile(filepath.Join(roIndexUpdate, EntrypointName), []byte("- [x](x.md) — d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(roIndexUpdate, EntrypointName), 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Join(roIndexUpdate, EntrypointName), 0o644) }()

	roIndexRemove := t.TempDir()
	if err := os.WriteFile(filepath.Join(roIndexRemove, "test.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roIndexRemove, EntrypointName), []byte("- [test](test.md) — d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(roIndexRemove, EntrypointName), 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Join(roIndexRemove, EntrypointName), 0o644) }()

	notEmptyDir := t.TempDir()
	target := filepath.Join(notEmptyDir, "test.md")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "nested"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		fn   func() error
		msg  string
	}{
		{"LoadMemoryIndex read failure", func() error { _, err := LoadMemoryIndex(dirAsIndex); return err }, "read MEMORY.md"},
		{"LoadMemoryFile read failure", func() error { _, err := LoadMemoryFile(filepath.Join(base, "missing.md")); return err }, "read memory file"},
		{"LoadAllMemoryFiles readdir failure", func() error { _, err := LoadAllMemoryFiles(fileAsPath); return err }, "read memory dir"},
		{"WriteMemoryFile mkdir failure", func() error {
			return WriteMemoryFile(filepath.Join(readOnly, "nested"), "test", MemoryTypeUser, "d", "c")
		}, "create memory dir"},
		{"WriteMemoryFile write failure", func() error { return WriteMemoryFile(writeTargetIsDir, "test", MemoryTypeUser, "d", "c") }, "write memory file"},
		{"UpdateMemoryIndex read failure", func() error { return UpdateMemoryIndex(dirAsIndex, "new.md", "d") }, "read MEMORY.md"},
		{"UpdateMemoryIndex write failure", func() error { return UpdateMemoryIndex(roIndexUpdate, "new.md", "d") }, "write MEMORY.md"},
		{"RemoveMemoryFile remove failure", func() error { return RemoveMemoryFile(notEmptyDir, "test.md") }, "remove memory file"},
		{"RemoveMemoryFile read failure", func() error { return RemoveMemoryFile(dirAsIndex, "test.md") }, "read MEMORY.md"},
		{"RemoveMemoryFile write failure", func() error { return RemoveMemoryFile(roIndexRemove, "test.md") }, "write MEMORY.md"},
	}
	for _, c := range cases {
		err := c.fn()
		if err == nil {
			t.Errorf("%s: expected error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: error %q missing %q", c.name, err, c.msg)
		}
		var pe *os.PathError
		if !errors.As(err, &pe) {
			t.Errorf("%s: error does not wrap *os.PathError: %v", c.name, err)
		}
	}
}

func TestWriteMemoryFile_Permissions(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := filepath.Join(t.TempDir(), "nested", "mem")
	if err := WriteMemoryFile(dir, "test", MemoryTypeUser, "d", "c"); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o755 {
		t.Errorf("memory dir mode = %o, want 755", perm)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "test.md"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o644 {
		t.Errorf("memory file mode = %o, want 644", perm)
	}
}

func TestIndexWritePermissions(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	updateDir := t.TempDir()
	if err := UpdateMemoryIndex(updateDir, "a.md", "d"); err != nil {
		t.Fatal(err)
	}
	removeDir := t.TempDir()
	if err := UpdateMemoryIndex(removeDir, "a.md", "d"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMemoryFile(removeDir, "a.md"); err != nil {
		t.Fatal(err)
	}
	perm := func(dir string) os.FileMode {
		t.Helper()
		info, err := os.Stat(filepath.Join(dir, EntrypointName))
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode().Perm()
	}
	if got := perm(updateDir); got != 0o644 {
		t.Errorf("index after update: mode = %o, want 644", got)
	}
	if got := perm(removeDir); got != 0o644 {
		t.Errorf("index after remove: mode = %o, want 644", got)
	}
}

// An already-consistent file is a no-op, so removing a file that never
// existed must not surface an error either.
func TestRemoveMemoryFile_MissingFileIsNoError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte("- [a](a.md) — d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMemoryFile(dir, "ghost.md"); err != nil {
		t.Errorf("RemoveMemoryFile for missing file = %v, want nil", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, EntrypointName))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "- [a](a.md) — d\n" {
		t.Errorf("index rewritten by no-op removal: %q", data)
	}
}

// UpdateMemoryIndex must persist the truncated form when a cap fires —
// line-only, byte-only, and both, since each flag guards the decision.
func TestUpdateMemoryIndex_PersistsTruncation(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		name  string
		lines int
		width int
	}{
		{"line cap only", 200, 10},
		{"byte cap only", 150, 200},
		{"both caps", 250, 200},
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			dir := t.TempDir()
			var lines []string
			for i := range s.lines {
				lines = append(lines, fmt.Sprintf("- [e%d](f%d.md) — %s", i, i, strings.Repeat("x", s.width)))
			}
			if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := UpdateMemoryIndex(dir, "fresh.md", "fresh desc"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, EntrypointName))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "WARNING") {
				t.Errorf("%s: index not truncated on disk (%d bytes)", s.name, len(data))
			}
		})
	}
}

// Only the first matching line is replaced; a duplicate entry line for the
// same file survives untouched.
func TestUpdateMemoryIndex_ReplacesOnlyFirstDuplicate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original := "- [a](a.md) — first\n- [a](a.md) — second\n"
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMemoryIndex(dir, "a.md", "updated"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, EntrypointName))
	if err != nil {
		t.Fatal(err)
	}
	want := "- [a](a.md) — updated\n- [a](a.md) — second\n"
	if string(data) != want {
		t.Errorf("index = %q, want %q", data, want)
	}
}

// Entries after the removed one must survive index rewriting.
func TestRemoveMemoryFile_KeepsEntriesAfterRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original := "- [a](a.md) — da\n- [b](b.md) — db\n- [c](c.md) — dc\n"
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMemoryFile(dir, "b.md"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, EntrypointName))
	if err != nil {
		t.Fatal(err)
	}
	want := "- [a](a.md) — da\n- [c](c.md) — dc\n"
	if string(data) != want {
		t.Errorf("index = %q, want %q", data, want)
	}
}

// LoadMemoryIndex scans the whole file: entries after blank or plain-text
// lines are still collected, and indented entries count, while stray
// bracket text outside "- [" lines does not.
func TestLoadMemoryIndex_ScansPastNonEntryLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "# Notes\n\n- [A](a.md) — x\n  - [B](b.md) — y\nx [T](t.md)\n"
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := LoadMemoryIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (A + indented B): %+v", len(idx.Entries), idx.Entries)
	}
	if idx.Entries[0].FileName != "a.md" || idx.Entries[1].FileName != "b.md" {
		t.Errorf("entries = %+v, want a.md then b.md", idx.Entries)
	}
	if idx.Raw != content {
		t.Errorf("Raw = %q, want the file verbatim", idx.Raw)
	}
}

func TestLoadMemoryFile_RecordsFilePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.md")
	if err := os.WriteFile(legacy, []byte("plain body"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf, err := LoadMemoryFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if mf.FilePath != legacy {
		t.Errorf("legacy FilePath = %q, want %q", mf.FilePath, legacy)
	}

	if err := WriteMemoryFile(dir, "typed", MemoryTypeUser, "d", "c"); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(dir, "typed.md")
	mf, err = LoadMemoryFile(typed)
	if err != nil {
		t.Fatal(err)
	}
	if mf.FilePath != typed {
		t.Errorf("typed FilePath = %q, want %q", mf.FilePath, typed)
	}
}

func TestLoadAllMemoryFiles_SortedByFilename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"c.md", "a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3", len(files))
	}
	got := []string{files[0].FilePath, files[1].FilePath, files[2].FilePath}
	want := []string{filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md"), filepath.Join(dir, "c.md")}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// An unreadable file is skipped and scanning continues with the rest.
func TestLoadAllMemoryFiles_SkipsUnreadableAndContinues(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based error injection does not fail as root")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("body"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0].FilePath) != "b.md" {
		t.Errorf("files = %+v, want only b.md", files)
	}
}

// Frontmatter is only recognized when the opening "---" starts at byte 0;
// a document that merely contains a frontmatter-shaped block stays legacy.
func TestParseFrontmatter_PrefixMustStartAtByteZero(t *testing.T) {
	t.Parallel()
	content := "----name: x\n---\nbody"
	name, _, _, body, ok := ParseFrontmatter(content)
	if ok || name != "" {
		t.Errorf("ok=%v name=%q, want ok=false: content must not parse as frontmatter", ok, name)
	}
	if body != content {
		t.Errorf("body = %q, want the original content", body)
	}
}

// A colon-less "name" line must not reset the parsed name to empty.
func TestParseFrontmatter_ColonlessLineKeepsName(t *testing.T) {
	t.Parallel()
	content := "---\nname: x\nname\n---\nbody"
	name, _, _, body, ok := ParseFrontmatter(content)
	if !ok || name != "x" {
		t.Errorf("ok=%v name=%q, want ok=true name=x", ok, name)
	}
	if body != "body" {
		t.Errorf("body = %q, want %q", body, "body")
	}
}

// The prompt's section skeleton (headers, blank-line separation, index
// workflow) is the behavioral contract injected into every conversation.
func TestBuildMemoryPrompt_SectionSkeleton(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	prompt := BuildMemoryPrompt(t.TempDir(), "")
	for _, want := range []string{
		"# auto memory",
		"## Types of memory",
		"## What NOT to save",
		"\n\n## How to save memories",
		"**Step 2**",
		"\n\n## When to access memories",
		"\n\n## Before recommending from memory",
		"\n\n## Archive",
		"\n\n## Memory and other forms of persistence",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// The override directory must win over the path computed from workingDir.
func TestBuildMemoryPrompt_OverrideDirWins(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	override := t.TempDir()
	if err := os.WriteFile(filepath.Join(override, EntrypointName), []byte("- [Ov](ov.md) — override content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := BuildMemoryPrompt(t.TempDir(), override)
	if !strings.Contains(prompt, "override content") {
		t.Error("prompt missing override MEMORY.md content")
	}
	if strings.Contains(prompt, "currently empty") {
		t.Error("prompt fell back to the computed (empty) memory dir")
	}
}
