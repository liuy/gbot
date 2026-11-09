package fileread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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

// A corrupt .rar sends rardecode/v2 into a permanent channel receive, so the
// archive-open path must abort on the caller's context instead of wedging the
// Read tool forever.
func TestExecute_CorruptRarHonorsContextDeadline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.rar")

	// RAR5 magic + garbage: enough for the format sniff to accept the file and
	// for the decoder to start walking a bogus block chain.
	fixture := append([]byte("Rar!\x1a\x07\x01"), bytes.Repeat([]byte{0xFF, 0x00, 'A', 0x7F}, 512)...)
	if err := os.WriteFile(path, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	input, err := json.Marshal(Input{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Execute(ctx, input, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Execute returned nil error for a corrupt .rar")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q does not mention the archive path %q", err.Error(), path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return within 5s — hang")
	}
}

// rardecode/v2 slices out of range on a zero-filled RAR5 block chain. The
// archive read runs off-stack, so the recover in openAndReadArchive is the only
// thing between that panic and a dead process.
func TestExecute_DecoderPanicBecomesToolError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panic.rar")

	fixture := append([]byte("Rar!\x1a\x07\x01"), make([]byte, 96)...)
	if err := os.WriteFile(path, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	input, err := json.Marshal(Input{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Execute(ctx, input, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Execute returned nil error for a panic-inducing .rar")
		}
		if !strings.Contains(err.Error(), "panic:") {
			t.Errorf("error = %q, want it to report the decoder panic", err.Error())
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q does not mention the archive path %q", err.Error(), path)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute did not return within 10s")
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

// Pin tests for the archive.go mutation sweep: each test kills specific
// escaped mutants (line:mutator pairs in the ledger); assertions are exact so
// a single changed byte flips the test.

// ---------------------------------------------------------------------------
// parseArchivePathCandidates — index-0 match, dedup, back-to-back ext, order
// ---------------------------------------------------------------------------

func TestParseArchivePathCandidates_ExtensionAtStart(t *testing.T) {
	t.Parallel()
	// A path that IS the extension: ".zip" matches at offset 0, and the scan
	// loop body must run for idx==0.
	cands := parseArchivePathCandidates(".zip")
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, want exactly 1", cands)
	}
	if cands[0] != (archivePathCandidate{archivePath: ".zip", subPath: ""}) {
		t.Errorf("candidate = %+v, want {.zip }", cands[0])
	}
}

func TestParseArchivePathCandidates_DedupSameKey(t *testing.T) {
	t.Parallel()
	// "x.tar.gz": the ".tar.gz" and ".gz" matches end at the same offset and
	// produce the SAME (archivePath, subPath) key — the seen map must collapse
	// them; only the distinct ".tar" split survives as a second candidate.
	cands := parseArchivePathCandidates("x.tar.gz")
	want := []archivePathCandidate{
		{archivePath: "x.tar.gz", subPath: ""},
		{archivePath: "x.tar", subPath: ".gz"},
	}
	if len(cands) != len(want) {
		t.Fatalf("candidates = %+v, want exactly %d", cands, len(want))
	}
	for i, w := range want {
		if cands[i] != w {
			t.Errorf("candidate[%d] = %+v, want %+v", i, cands[i], w)
		}
	}
}

func TestParseArchivePathCandidates_BackToBackExtension(t *testing.T) {
	t.Parallel()
	// After matching ".gz" at offset 1, the rescan of the remainder finds the
	// second ".gz" at relative offset 0 — the loop must continue, not break.
	cands := parseArchivePathCandidates("x.gz.gz")
	want := []archivePathCandidate{
		{archivePath: "x.gz.gz", subPath: ""},
		{archivePath: "x.gz", subPath: ".gz"},
	}
	if len(cands) != len(want) {
		t.Fatalf("candidates = %+v, want exactly %d", cands, len(want))
	}
	for i, w := range want {
		if cands[i] != w {
			t.Errorf("candidate[%d] = %+v, want %+v", i, cands[i], w)
		}
	}
}

func TestParseArchivePathCandidates_LongestFirstDespiteInsertionOrder(t *testing.T) {
	t.Parallel()
	// archiveExtensions scans ".tar" before ".gz", so the SHORTER archive path
	// is inserted first; the sort must still put the longer one first.
	cands := parseArchivePathCandidates("x.tar.y.gz")
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v, want exactly 2", cands)
	}
	if cands[0].archivePath != "x.tar.y.gz" || cands[0].subPath != "" {
		t.Errorf("first candidate = %+v, want {x.tar.y.gz }", cands[0])
	}
	if cands[1] != (archivePathCandidate{archivePath: "x.tar", subPath: ".y.gz"}) {
		t.Errorf("second candidate = %+v, want {x.tar .y.gz}", cands[1])
	}
}

// ---------------------------------------------------------------------------
// tryArchivePath — a stat failure on one candidate falls through to the next
// ---------------------------------------------------------------------------

func TestTryArchivePath_StatFailureFallsThroughToNextCandidate(t *testing.T) {
	t.Parallel()
	// Only testdata/test.tar exists on disk; the longest candidate "x.tar.gz"
	// stats ENOENT, the shorter "x.tar" stats fine. Breaking out of the
	// candidate loop on the first stat failure would misreport the input as a
	// non-archive path.
	dir := t.TempDir()
	tarCopy := filepath.Join(dir, "x.tar")
	src, err := os.ReadFile("testdata/test.tar")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tarCopy, src, 0o644); err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: filepath.Join(dir, "x.tar.gz")}
	result, handled, err := tryArchivePath(context.Background(), in)
	if !handled {
		t.Fatalf("handled = false, want true (x.tar exists; err=%v result=%v)", err, result)
	}
	// x.tar opens fine but has no ".gz" member — the not-found error proves
	// the loop reached the second candidate.
	if err == nil || !strings.Contains(err.Error(), "path '.gz' not found inside archive") {
		t.Fatalf("err = %v, want '.gz' not found inside archive", err)
	}
}

// ---------------------------------------------------------------------------
// readArchiveFS — wire fields, caps, empty member, slicing bookkeeping
// ---------------------------------------------------------------------------

func readFSData(t *testing.T, fsys fs.FS, subPath string, in Input) TextOutput {
	t.Helper()
	res, err := readArchiveFS(fsys, subPath, in)
	if err != nil {
		t.Fatalf("readArchiveFS(%q) unexpected error: %v", subPath, err)
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", res.Data)
	}
	return to
}

func TestReadArchiveFS_DirListingWireFields(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"sub/b.txt": &fstest.MapFile{Data: []byte("hi")}}
	to := readFSData(t, fsys, "sub", Input{FilePath: "arch.zip:sub"})
	if to.Type != "text" {
		t.Errorf("Type = %q, want %q", to.Type, "text")
	}
	if to.FilePath != "arch.zip:sub" {
		t.Errorf("FilePath = %q, want %q", to.FilePath, "arch.zip:sub")
	}
	if to.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", to.StartLine)
	}
	if to.Content != "b.txt (2B)" {
		t.Errorf("Content = %q, want %q", to.Content, "b.txt (2B)")
	}
}

// readDirCountDir records the n passed to ReadDir; the contract for "read all"
// is the -1 sentinel (n==0 may legally return an empty list).
type readDirCountDir struct{ got *int }

func (d readDirCountDir) Stat() (fs.FileInfo, error) { return fakeInfo{name: ".", isDir: true}, nil }
func (d readDirCountDir) Close() error               { return nil }
func (d readDirCountDir) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }
func (d readDirCountDir) ReadDir(n int) ([]fs.DirEntry, error) {
	*d.got = n
	return []fs.DirEntry{fakeDirEntry{name: "member.txt", size: 5, infoOK: true}}, nil
}

func TestReadArchiveFS_DirReadAllSentinel(t *testing.T) {
	t.Parallel()
	got := 0
	fsys := readDirCountFS{dir: readDirCountDir{got: &got}}
	to := readFSData(t, fsys, "", Input{FilePath: "arch.zip"})
	if got != -1 {
		t.Errorf("ReadDir n = %d, want -1", got)
	}
	if to.Content != "member.txt (5B)" {
		t.Errorf("Content = %q, want %q", to.Content, "member.txt (5B)")
	}
}

type readDirCountFS struct{ dir readDirCountDir }

func (f readDirCountFS) Open(string) (fs.File, error) { return f.dir, nil }

func TestReadArchiveFS_TextMemberWireFields(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"f.txt": &fstest.MapFile{Data: []byte("hello")}}
	to := readFSData(t, fsys, "f.txt", Input{FilePath: "arch.zip:f.txt"})
	if to.Type != "text" {
		t.Errorf("Type = %q, want %q", to.Type, "text")
	}
	if to.FilePath != "arch.zip:f.txt" {
		t.Errorf("FilePath = %q, want %q", to.FilePath, "arch.zip:f.txt")
	}
	if to.Content != "hello" || to.TotalLines != 1 || to.NumLines != 1 || to.StartLine != 1 {
		t.Errorf("member fields = %+v, want content hello / 1 line", to)
	}
}

func TestReadArchiveFS_BinaryMemberWireFields(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"b.bin": &fstest.MapFile{Data: []byte{0, 1}}}
	to := readFSData(t, fsys, "b.bin", Input{FilePath: "arch.zip:b.bin"})
	if to.Type != "text" {
		t.Errorf("Type = %q, want %q", to.Type, "text")
	}
	if to.FilePath != "arch.zip:b.bin" {
		t.Errorf("FilePath = %q, want %q", to.FilePath, "arch.zip:b.bin")
	}
	if to.Content != "[Cannot read binary archive entry 'b.bin' (2 bytes)]" {
		t.Errorf("Content = %q, want binary placeholder", to.Content)
	}
	if to.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", to.StartLine)
	}
}

func TestReadArchiveFS_EmptyMemberTotals(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"e.txt": &fstest.MapFile{Data: nil}}
	to := readFSData(t, fsys, "e.txt", Input{FilePath: "arch.zip:e.txt"})
	if to.Content != "" || to.TotalLines != 0 || to.NumLines != 0 {
		t.Errorf("empty member fields = %+v, want empty content with zero totals", to)
	}
}

func TestReadArchiveFS_LimitOneSlicesFirstLine(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"f.txt": &fstest.MapFile{Data: []byte("a\nb\nc")}}
	to := readFSData(t, fsys, "f.txt", Input{FilePath: "z:f.txt", Offset: 1, Limit: 1})
	if to.Content != "a" {
		t.Errorf("Content = %q, want %q", to.Content, "a")
	}
	if to.NumLines != 1 || to.TotalLines != 3 || to.StartLine != 1 {
		t.Errorf("line bookkeeping = %+v, want 1 of 3 lines", to)
	}
}

func TestReadArchiveFS_OffsetTwoNoLimitSlices(t *testing.T) {
	t.Parallel()
	// offset=2 alone must slice (Offset > 1); without the slice the full
	// member and its unsliced NumLines would leak through.
	fsys := fstest.MapFS{"f.txt": &fstest.MapFile{Data: []byte("a\nb\nc")}}
	res, err := readArchiveFS(fsys, "f.txt", Input{FilePath: "z:f.txt", Offset: 2, Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", res.Data)
	}
	if to.Content != "b\nc" {
		t.Errorf("Content = %q, want %q", to.Content, "b\nc")
	}
	if to.NumLines != 2 || to.TotalLines != 3 || to.StartLine != 2 {
		t.Errorf("line bookkeeping = %+v, want 2 lines from offset 2 of 3", to)
	}
}

func TestReadArchiveFS_SlicedTrailingNewlineKeepsNumLines(t *testing.T) {
	t.Parallel()
	// Sliced content keeps its trailing newline; the newline is part of the
	// last returned line, so NumLines must stay 2 (no decrement).
	fsys := fstest.MapFS{"f.txt": &fstest.MapFile{Data: []byte("l1\nl2\n")}}
	to := readFSData(t, fsys, "f.txt", Input{FilePath: "z:f.txt", Offset: 2})
	if to.Content != "l2\n" {
		t.Errorf("Content = %q, want %q", to.Content, "l2\n")
	}
	if to.NumLines != 2 || to.TotalLines != 2 || to.StartLine != 2 {
		t.Errorf("line bookkeeping = %+v, want 2 lines from offset 2", to)
	}
}

func TestReadArchiveFS_UnslicedNoTrailingNewlineNumLines(t *testing.T) {
	t.Parallel()
	// Unsliced "a\nb" has no trailing newline to discount: NumLines is 2.
	fsys := fstest.MapFS{"f.txt": &fstest.MapFile{Data: []byte("a\nb")}}
	to := readFSData(t, fsys, "f.txt", Input{FilePath: "z:f.txt"})
	if to.Content != "a\nb" {
		t.Errorf("Content = %q, want %q", to.Content, "a\nb")
	}
	if to.NumLines != 2 || to.TotalLines != 2 {
		t.Errorf("line bookkeeping = %+v, want 2 lines", to)
	}
}

func TestReadArchiveFS_DotDotSubPathResolvesRoot(t *testing.T) {
	t.Parallel()
	// Both ".." and "../x" subPaths must be rejected by normalizeArchivePath
	// and resolve to the archive root listing, never reach the underlying
	// fs.Open (which would either error or escape the archive root).
	for _, sub := range []string{"..", "../x"} {
		fsys := fstest.MapFS{"b.txt": &fstest.MapFile{Data: []byte("x")}}
		to := readFSData(t, fsys, sub, Input{FilePath: "z:.."})
		if to.Content != "b.txt (1B)" {
			t.Errorf("subPath %q: Content = %q, want root listing %q", sub, to.Content, "b.txt (1B)")
		}
	}
}

func TestReadArchiveFS_MemberExceedsCap(t *testing.T) {
	t.Parallel()
	big := bytes.Repeat([]byte("A"), int(archiveMaxMemberBytes)+1)
	fsys := fstest.MapFS{"big.bin": &fstest.MapFile{Data: big}}
	_, err := readArchiveFS(fsys, "big.bin", Input{FilePath: "z:big.bin"})
	want := fmt.Sprintf("archive member 'big.bin' exceeds %d bytes", archiveMaxMemberBytes)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestReadArchiveFS_MemberExactlyAtCap(t *testing.T) {
	t.Parallel()
	exact := bytes.Repeat([]byte("A"), int(archiveMaxMemberBytes))
	fsys := fstest.MapFS{"cap.bin": &fstest.MapFile{Data: exact}}
	to := readFSData(t, fsys, "cap.bin", Input{FilePath: "z:cap.bin"})
	if len(to.Content) != int(archiveMaxMemberBytes) {
		t.Errorf("len(Content) = %d, want %d", len(to.Content), archiveMaxMemberBytes)
	}
	if to.TotalLines != 1 || to.NumLines != 1 {
		t.Errorf("line bookkeeping = %+v, want single line", to)
	}
}

// ---------------------------------------------------------------------------
// readArchiveFS — error paths via fake filesystems
// ---------------------------------------------------------------------------

var errPinStat = errors.New("pin stat boom")

type statErrFile struct{}

func (statErrFile) Stat() (fs.FileInfo, error) { return nil, errPinStat }
func (statErrFile) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }
func (statErrFile) Close() error               { return nil }

type statErrFS struct{}

func (statErrFS) Open(string) (fs.File, error) { return statErrFile{}, nil }

func TestReadArchiveFS_StatErrorPropagates(t *testing.T) {
	t.Parallel()
	_, err := readArchiveFS(statErrFS{}, "f.txt", Input{FilePath: "z:f.txt"})
	if !errors.Is(err, errPinStat) {
		t.Fatalf("err = %v, want errors.Is(errPinStat)", err)
	}
}

var errPinRead = errors.New("pin read boom")

type readErrFile struct{}

func (readErrFile) Stat() (fs.FileInfo, error) { return fakeInfo{name: "f.txt", size: 10}, nil }
func (readErrFile) Read([]byte) (int, error)   { return 0, errPinRead }
func (readErrFile) Close() error               { return nil }

type readErrFS struct{}

func (readErrFS) Open(string) (fs.File, error) { return readErrFile{}, nil }

func TestReadArchiveFS_MemberReadErrorWrapped(t *testing.T) {
	t.Parallel()
	_, err := readArchiveFS(readErrFS{}, "f.txt", Input{FilePath: "z:f.txt"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, errPinRead) {
		t.Errorf("err = %v, want errors.Is(errPinRead) (%%w wrap)", err)
	}
	if !strings.HasPrefix(err.Error(), "read archive member 'f.txt':") {
		t.Errorf("err = %v, want 'read archive member' prefix", err)
	}
}

var errPinReadDir = errors.New("pin readdir boom")

type readDirErrDir struct{}

func (readDirErrDir) Stat() (fs.FileInfo, error) { return fakeInfo{name: ".", isDir: true}, nil }
func (readDirErrDir) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }
func (readDirErrDir) ReadDir(int) ([]fs.DirEntry, error) {
	return nil, errPinReadDir
}
func (readDirErrDir) Close() error { return nil }

type readDirErrFS struct{}

func (readDirErrFS) Open(string) (fs.File, error) { return readDirErrDir{}, nil }

func TestReadArchiveFS_DirReadErrorWrapped(t *testing.T) {
	t.Parallel()
	_, err := readArchiveFS(readDirErrFS{}, "", Input{FilePath: "z"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, errPinReadDir) {
		t.Errorf("err = %v, want errors.Is(errPinReadDir) (%%w wrap)", err)
	}
	if !strings.HasPrefix(err.Error(), "read archive directory '':") {
		t.Errorf("err = %v, want 'read archive directory' prefix", err)
	}
}

// ---------------------------------------------------------------------------
// formatArchiveEntries — multi-entry shapes and the unit-size branch
// ---------------------------------------------------------------------------

func TestFormatArchiveEntries_InfoErrorThenFile(t *testing.T) {
	t.Parallel()
	entries := []fs.DirEntry{
		fakeDirEntry{name: "broken", infoOK: false},
		fakeDirEntry{name: "data.txt", size: 2048, infoOK: true},
	}
	got := formatArchiveEntries(entries)
	if got != "broken\ndata.txt (2.0KB)" {
		t.Errorf("entries = %q, want %q", got, "broken\ndata.txt (2.0KB)")
	}
}

func TestFormatArchiveEntries_DirThenFile(t *testing.T) {
	t.Parallel()
	// The dir name sorts first so the loop reaches the dir before the file;
	// every entry after it must still be rendered.
	entries := []fs.DirEntry{
		fakeDirEntry{name: "adir", isDir: true, infoOK: true},
		fakeDirEntry{name: "b.txt", size: 2048, infoOK: true},
	}
	got := formatArchiveEntries(entries)
	if got != "adir/\nb.txt (2.0KB)" {
		t.Errorf("entries = %q, want %q", got, "adir/\nb.txt (2.0KB)")
	}
}

func TestFormatArchiveEntries_UnitFileSize(t *testing.T) {
	t.Parallel()
	entries := []fs.DirEntry{fakeDirEntry{name: "one.txt", size: 1, infoOK: true}}
	got := formatArchiveEntries(entries)
	if got != "one.txt (1B)" {
		t.Errorf("entries = %q, want %q", got, "one.txt (1B)")
	}
}

func TestFormatArchiveEntries_CaseCollisionStable(t *testing.T) {
	t.Parallel()
	// "A.txt" and "a.txt" compare equal case-insensitively; insertion order
	// must survive (the comparator stays strict for equal keys).
	entries := []fs.DirEntry{
		fakeDirEntry{name: "A.txt", size: 0, infoOK: true},
		fakeDirEntry{name: "a.txt", size: 0, infoOK: true},
	}
	got := formatArchiveEntries(entries)
	if got != "A.txt\na.txt" {
		t.Errorf("entries = %q, want %q", got, "A.txt\na.txt")
	}
}

// ---------------------------------------------------------------------------
// isValidUTF8 — lead-byte classification edges
// ---------------------------------------------------------------------------

func TestIsValidUTF8_LeadByteEdges(t *testing.T) {
	t.Parallel()
	tt := []struct {
		name string
		in   string
		want bool
	}{
		{"leading NUL", "\x00", false},
		{"0x01 is text", "\x01", true},
		{"lone continuation lead", "\x80", false},
		{"DEL is text", "\x7f", true},
		{"4-byte F1 lead", "\xF1\x80\x80\x80", true},
		{"4-byte F0 lead", "\xF0\x90\x80\x80", true},
	}
	for _, c := range tt {
		if got := isValidUTF8([]byte(c.in)); got != c.want {
			t.Errorf("%s: isValidUTF8(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
