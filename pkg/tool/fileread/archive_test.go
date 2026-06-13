package fileread

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

func TestParseArchivePathCandidates_Zip(t *testing.T) {
	tt := []struct {
		input    string
		wantArch string
		wantSub  string
	}{
		{"foo.zip", "foo.zip", ""},
		{"foo.zip:dir/file.ts", "foo.zip", "dir/file.ts"},
		{"a/foo.zip:src/main.go", "a/foo.zip", "src/main.go"},
		{"foo.tar.gz:src", "foo.tar.gz", "src"},
		{"foo.tgz", "foo.tgz", ""},
		{"foo.7z:secret.txt", "foo.7z", "secret.txt"},
		{"foo.rar:doc.pdf", "foo.rar", "doc.pdf"},
	}
	for _, c := range tt {
		cands := parseArchivePathCandidates(c.input)
		if len(cands) == 0 {
			t.Errorf("input %q: expected at least 1 candidate, got 0", c.input)
			continue
		}
		got := cands[0]
		if got.archivePath != c.wantArch {
			t.Errorf("input %q: archivePath = %q, want %q", c.input, got.archivePath, c.wantArch)
		}
		if got.subPath != c.wantSub {
			t.Errorf("input %q: subPath = %q, want %q", c.input, got.subPath, c.wantSub)
		}
	}
}

func TestParseArchivePathCandidates_LongestWins(t *testing.T) {
	// "foo.tar.gz" should match as tar.gz, not two splits (".tar" + ".gz").
	cands := parseArchivePathCandidates("foo.tar.gz")
	if len(cands) == 0 {
		t.Fatal("expected candidates")
	}
	if cands[0].archivePath != "foo.tar.gz" {
		t.Errorf("longest match should win; got %q", cands[0].archivePath)
	}
}

func TestParseArchivePathCandidates_NoArchive(t *testing.T) {
	cands := parseArchivePathCandidates("plain.txt")
	if len(cands) != 0 {
		t.Errorf("plain file should produce 0 candidates, got %d", len(cands))
	}
}

func TestTryArchivePath_ListRootZip(t *testing.T) {
	abs, err := filepath.Abs("testdata/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for zip path")
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	text := resultText(t, result)
	// The test.zip contains src/, config.yaml, hello.txt
	for _, want := range []string{"src/", "config.yaml", "hello.txt"} {
		if !strings.Contains(text, want) {
			t.Errorf("listing missing %q; got: %s", want, text)
		}
	}
}

func TestTryArchivePath_ListRootTarGz(t *testing.T) {
	abs, err := filepath.Abs("testdata/test.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for tar.gz path")
	}
	text := resultText(t, result)
	if !strings.Contains(text, "src/") {
		t.Errorf("listing missing 'src/'; got: %s", text)
	}
}

func TestTryArchivePath_ListRootTar(t *testing.T) {
	abs, err := filepath.Abs("testdata/test.tar")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for tar path")
	}
	text := resultText(t, result)
	if !strings.Contains(text, "src/") {
		t.Errorf("listing missing 'src/'; got: %s", text)
	}
}

func TestTryArchivePath_ReadMember(t *testing.T) {
	abs, err := filepath.Abs("testdata/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs + ":config.yaml"}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	text := resultText(t, result)
	if !strings.Contains(text, "name: test") {
		t.Errorf("config.yaml content missing 'name: test'; got: %s", text)
	}
	if !strings.Contains(text, "value: 42") {
		t.Errorf("config.yaml content missing 'value: 42'; got: %s", text)
	}
}

func TestTryArchivePath_ReadNestedMember(t *testing.T) {
	abs, err := filepath.Abs("testdata/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs + ":src/main.go"}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	text := resultText(t, result)
	if !strings.Contains(text, "package main") {
		t.Errorf("src/main.go content missing 'package main'; got: %s", text)
	}
}

func TestTryArchivePath_ListSubdir(t *testing.T) {
	abs, err := filepath.Abs("testdata/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs + ":src"}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	text := resultText(t, result)
	if !strings.Contains(text, "main.go") {
		t.Errorf("src/ listing missing 'main.go'; got: %s", text)
	}
	if !strings.Contains(text, "lib.go") {
		t.Errorf("src/ listing missing 'lib.go'; got: %s", text)
	}
}

func TestTryArchivePath_NotArchive(t *testing.T) {
	abs, err := filepath.Abs("testdata/test.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs}
	_, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("sqlite file should not be handled as archive")
	}
}

func TestNormalizeArchivePath(t *testing.T) {
	tt := []struct {
		in   string
		want string
	}{
		{"", ""},
		{".", ""},
		{"./foo", "foo"},
		{"foo/bar", "foo/bar"},
		{"foo//bar", "foo/bar"},
		{"../etc/passwd", ""},
		{"foo/../../etc", ""},
		{"/abs/path", "abs/path"},
	}
	for _, c := range tt {
		if got := normalizeArchivePath(c.in); got != c.want {
			t.Errorf("normalizeArchivePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsValidUTF8(t *testing.T) {
	tt := []struct {
		name string
		in   []byte
		want bool
	}{
		{"empty", []byte{}, true},
		{"ascii", []byte("hello"), true},
		{"utf8", []byte("héllo"), true},
		{"cjk", []byte("你好"), true},
		{"emoji", []byte("hi 🙂"), true},
		{"nul byte", []byte("a\x00b"), false},
		{"invalid utf8", []byte{0xFF, 0xFE}, false},
		{"truncated 2-byte", []byte{0xC3, 0x28}, false},
	}
	for _, c := range tt {
		t.Run(c.name, func(t *testing.T) {
			if got := isValidUTF8(c.in); got != c.want {
				t.Errorf("isValidUTF8(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// resultText extracts the text content from a ToolResult.
func resultText(t *testing.T, r *tool.ToolResult) string {
	t.Helper()
	to, ok := r.Data.(TextOutput)
	if !ok {
		t.Fatalf("expected TextOutput, got %T", r.Data)
	}
	return to.Content
}

// TestTryArchivePath_AllFormats verifies that every supported archive format
// can be opened and listed. Fixtures live in testdata/ alongside the package.
func TestTryArchivePath_AllFormats(t *testing.T) {
	// Multi-entry archives: each should list src/, config.yaml, hello.txt
	multiEntry := []string{
		"test.zip",
		"test.tar",
		"test.tar.gz",
		"test.tar.xz",
		"test.tar.bz2",
		"test.tar.zst",
		"test.tar.lz4",
		"test.7z",
		"test.rar",
	}
	for _, name := range multiEntry {
		t.Run(name, func(t *testing.T) {
			abs, err := filepath.Abs("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			in := Input{FilePath: abs}
			result, handled, err := tryArchivePath(context.Background(), in)
			if err != nil {
				t.Fatalf("open %s: %v", name, err)
			}
			if !handled {
				t.Fatalf("%s not handled as archive", name)
			}
			text := resultText(t, result)
			// Each fixture has src/, config.yaml, hello.txt at root.
			for _, want := range []string{"src/", "config.yaml", "hello.txt"} {
				if !strings.Contains(text, want) {
					t.Errorf("%s listing missing %q; got:\n%s", name, want, text)
				}
			}
		})
	}

	// Single-file compressed: gzip/bzip2/xz/zst/lz4
	singleFile := []struct {
		fixture string
		want    string
	}{
		{"single.txt.gz", "compressed single file"},
		{"single.txt.bz2", "compressed single file"},
		{"single.txt.xz", "compressed single file"},
		{"single.txt.zst", "compressed single file"},
		{"single.txt.lz4", "compressed single file"},
	}
	for _, c := range singleFile {
		t.Run(c.fixture, func(t *testing.T) {
			abs, err := filepath.Abs("testdata/" + c.fixture)
			if err != nil {
				t.Fatal(err)
			}
			in := Input{FilePath: abs}
			result, handled, err := tryArchivePath(context.Background(), in)
			if err != nil {
				t.Fatalf("open %s: %v", c.fixture, err)
			}
			if !handled {
				t.Fatalf("%s not handled as archive", c.fixture)
			}
			text := resultText(t, result)
			if !strings.Contains(text, c.want) {
				t.Errorf("%s content missing %q; got:\n%s", c.fixture, c.want, text)
			}
		})
	}
}

// TestTryArchivePath_ReadMember_AllFormats verifies that a member file can
// be read from every multi-entry format.
func TestTryArchivePath_ReadMember_AllFormats(t *testing.T) {
	formats := []string{
		"test.zip", "test.tar", "test.tar.gz",
		"test.tar.xz", "test.tar.bz2", "test.tar.zst", "test.tar.lz4",
		"test.7z", "test.rar",
	}
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			abs, err := filepath.Abs("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			in := Input{FilePath: abs + ":hello.txt"}
			result, handled, err := tryArchivePath(context.Background(), in)
			if err != nil {
				t.Fatalf("read hello.txt from %s: %v", name, err)
			}
			if !handled {
				t.Fatalf("%s not handled", name)
			}
			text := resultText(t, result)
			if !strings.Contains(text, "hello world") {
				t.Errorf("%s:hello.txt content wrong; got:\n%s", name, text)
			}
		})
	}
}
