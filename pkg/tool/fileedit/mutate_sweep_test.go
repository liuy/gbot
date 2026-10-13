package fileedit

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// Tool surface — the ToolDef wiring is only observable through the interface,
// so a cleared field must be caught here rather than by Execute internals.
// ---------------------------------------------------------------------------

func TestEditToolSurface(t *testing.T) {
	t.Parallel()
	tt := New()

	if got := tt.Aliases(); !slices.Equal(got, []string{"fileedit", "edit"}) {
		t.Errorf("Aliases() = %v, want [fileedit edit]", got)
	}
	if got := tt.MaxResultSize(); got != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", got)
	}
	if got := tt.InterruptBehavior(); got != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", got)
	}
	input := json.RawMessage(`{"file_path":"/tmp/x.go","old_string":"a","new_string":"b"}`)
	if tt.IsReadOnly(input) {
		t.Error("IsReadOnly() = true, Edit writes to disk")
	}
	if tt.IsDestructive(input) {
		t.Error("IsDestructive() = true, Edit is not destructive")
	}

	// Call through the interface: a cleared Call_ field must not silently
	// leave the tool usable via the package-level Execute.
	dir := t.TempDir()
	fp := filepath.Join(dir, "surface.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := tt.Call(context.Background(),
		json.RawMessage(`{"file_path":`+jsonQuote(fp)+`,"old_string":"hello","new_string":"bye"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := res.Data.(*Output)
	if !ok {
		t.Fatalf("Data = %T, want *Output", res.Data)
	}
	if out.FilePath != fp || out.OldString != "hello" || out.NewString != "bye" || out.ReplaceAll {
		t.Errorf("Output = %+v, want FilePath=%s Old=hello New=bye ReplaceAll=false", out, fp)
	}
	if out.OriginalFile == nil || *out.OriginalFile != "hello world\n" {
		t.Errorf("OriginalFile = %v, want the pre-edit content", out.OriginalFile)
	}
}

// init() registers the Edit content checker; without it the engine falls back
// to ActionAllow and dangerous paths edit without an ask.
func TestInit_EditContentChecker(t *testing.T) {
	t.Parallel()
	rules := []permission.Rule{{
		Value:  permission.RuleValue{ToolName: "Edit"},
		Action: permission.ActionAsk,
	}}

	if got := permission.CheckContent("Edit", json.RawMessage(`{"file_path":"/repo/.git/config"}`), rules); got != permission.ActionAsk {
		t.Errorf("CheckContent(dangerous path) = %q, want ask", got)
	}
	if got := permission.CheckContent("Edit", json.RawMessage(`{"file_path":"/repo/main.go"}`), rules); got != permission.ActionAllow {
		t.Errorf("CheckContent(ordinary path) = %q, want allow", got)
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// With no hunks both the summary and the diff are empty, so the empty-diff
// branch must return "" — not a stray newline from concatenation.
func TestRenderEditResult_NoHunksIsEmpty(t *testing.T) {
	t.Parallel()
	if got := renderEditResult(&Output{FilePath: "f", OldString: "same", NewString: "same"}); got != "" {
		t.Errorf("renderEditResult(identical) = %q, want empty", got)
	}
}

func TestRenderEditError_Summaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"string to replace not found in file.\nString: foo", "Error editing file"},
		{"write file: permission denied", "write file: permission denied"},
	}
	for _, tc := range cases {
		if got := renderEditError(tc.in); got != tc.want {
			t.Errorf("renderEditError(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The read-first summary must fire on the sentence Execute actually returns; a
// hand-written fixture can drift from the production wording and stay green
// while the TUI keeps showing the raw error.
func TestRenderEditError_ReadFirstMatchesProductionError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "unread.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(),
		json.RawMessage(`{"file_path":`+jsonQuote(fp)+`,"old_string":"hello","new_string":"x"}`),
		&tool.ToolUseContext{ReadFileState: map[string]tool.FileState{}})
	if err == nil {
		t.Fatal("Execute(unread file) = nil error, want read-first rejection")
	}
	if got := renderEditError(err.Error()); got != "File must be read first" {
		t.Errorf("renderEditError(%q) = %q, want %q", err.Error(), got, "File must be read first")
	}
}

// The wire decoder must surface UnmarshalSingleBlock's own diagnostic;
// swallowing the guard would replace it with a generic JSON error.
func TestDecodeResult_NonArrayWire(t *testing.T) {
	t.Parallel()
	_, err := New().(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{"filePath":"/x"}`))
	if err == nil {
		t.Fatal("DecodeResult(non-array wire) = nil error, want array-form error")
	}
	// "array-form" is owned by pkg/tool (UnmarshalSingleBlock's message,
	// tool.go:314) — reword it there and this assertion has to follow.
	if !strings.Contains(err.Error(), "array-form") {
		t.Errorf("err = %v, want it to mention array-form", err)
	}
}

// ---------------------------------------------------------------------------
// Error wrapping — callers branch on the wrapped cause, so %w is load-bearing.
// ---------------------------------------------------------------------------

func TestExecute_ParseErrorWrapsJSONError(t *testing.T) {
	t.Parallel()
	_, err := Execute(context.Background(), json.RawMessage(`{not json`), nil)
	if err == nil {
		t.Fatal("Execute(malformed input) = nil error, want parse error")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("err = %v, want it to wrap *json.SyntaxError", err)
	}
}

func TestExecute_WriteErrorsWrapPermission(t *testing.T) {
	// Not parallel: each case chmods, and the assertions depend on the
	// write actually being rejected by the kernel.
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	cases := []struct {
		name   string
		write  func(string) error
		target string
		fields string
	}{
		{
			name: "new file in read-only dir",
			write: func(dir string) error {
				return os.Mkdir(filepath.Join(dir, "ro"), 0o555)
			},
			target: "ro/target",
			fields: `"old_string":"","new_string":"hello"`,
		},
		{
			name: "read-only empty file",
			write: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "target"), []byte(""), 0o444)
			},
			target: "target",
			fields: `"old_string":"","new_string":"hello"`,
		},
		{
			name: "read-only BOM file",
			write: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "target"), append([]byte{0xFF, 0xFE}, 'h', 0), 0o444)
			},
			target: "target",
			fields: `"old_string":"h","new_string":"x"`,
		},
		{
			name: "read-only plain file",
			write: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "target"), []byte("hello"), 0o444)
			},
			target: "target",
			fields: `"old_string":"hello","new_string":"x"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tc.write(dir); err != nil {
				t.Fatalf("setup: %v", err)
			}
			defer func() { _ = os.Chmod(filepath.Join(dir, "ro"), 0o755) }()
			fp := filepath.Join(dir, filepath.FromSlash(tc.target))
			_, err := Execute(context.Background(),
				json.RawMessage(`{"file_path":`+jsonQuote(fp)+`,`+tc.fields+`}`), nil)
			if err == nil {
				t.Fatalf("Execute(%s) = nil error, want write failure", tc.name)
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("err = %v, want it to wrap fs.ErrPermission", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Path handling
// ---------------------------------------------------------------------------

func TestExpandPath(t *testing.T) {
	t.Parallel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := expandPath("/abs/path.txt"); got != "/abs/path.txt" {
		t.Errorf("expandPath(absolute) = %q, want it unchanged", got)
	}
	// The verbatim path doubles as the ReadFileState key (fileread expands the
	// same way), so cleaning it here would desync read/edit state.
	if got := expandPath("/abs//nested/../path.txt"); got != "/abs//nested/../path.txt" {
		t.Errorf("expandPath(uncleaned absolute) = %q, want it unchanged", got)
	}
	if got := expandPath("rel/path.txt"); got != filepath.Join(cwd, "rel/path.txt") {
		t.Errorf("expandPath(relative) = %q, want %q", got, filepath.Join(cwd, "rel/path.txt"))
	}
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	if got := expandPath("~/file.txt"); got != filepath.Join(home, "file.txt") {
		t.Errorf("expandPath(tilde) = %q, want %q", got, filepath.Join(home, "file.txt"))
	}
}

// A ~/ path must expand against HOME even when a working directory is set —
// otherwise the edit silently targets a literal "~" directory under the CWD.
// Not parallel: t.Setenv is process-global, and writing into the real $HOME
// made the sibling tilde tests collide when the package suite ran concurrently.
func TestExecute_TildePathIgnoresWorkingDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fp := filepath.Join(home, ".gbot_test_tilde_wd.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(fp) }()

	wd := t.TempDir()
	_, err := Execute(context.Background(),
		json.RawMessage(`{"file_path":"~/.gbot_test_tilde_wd.txt","old_string":"hello","new_string":"expanded"}`),
		&tool.ToolUseContext{WorkingDir: wd})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("HOME file was not edited: %v", err)
	}
	if string(data) != "expanded world\n" {
		t.Errorf("HOME file = %q, want %q", data, "expanded world\n")
	}
	if _, err := os.Stat(filepath.Join(wd, "~")); !os.IsNotExist(err) {
		t.Errorf("working dir gained a literal ~ entry: %v", err)
	}
}

// The size limit is inclusive: a file of exactly the limit must still be editable.
func TestExecute_SizeLimitIsInclusive(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "exact.txt")
	content := "0123456789"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// MaxEditFileSize is package-global, so this swap is only safe while the
	// test stays sequential: a parallel Execute would see the shrunken cap.
	orig := MaxEditFileSize
	MaxEditFileSize = int64(len(content))
	defer func() { MaxEditFileSize = orig }()

	if _, err := Execute(context.Background(),
		json.RawMessage(`{"file_path":`+jsonQuote(fp)+`,"old_string":"012","new_string":"987"}`), nil); err != nil {
		t.Errorf("Execute(size == limit) error = %v, want the edit to succeed", err)
	}
}

// New-file creation must use 0644 and report the written text back.
func TestExecute_NewFileModeAndOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Reference file written with the same mode, so the expectation absorbs umask.
	ref := filepath.Join(dir, "reference.txt")
	if err := os.WriteFile(ref, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	refInfo, err := os.Stat(ref)
	if err != nil {
		t.Fatal(err)
	}

	fp := filepath.Join(dir, "created.txt")
	res, err := Execute(context.Background(),
		json.RawMessage(`{"file_path":`+jsonQuote(fp)+`,"old_string":"","new_string":"fresh"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	info, err := os.Stat(fp)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), refInfo.Mode().Perm(); got != want {
		t.Errorf("created file mode = %o, want %o", got, want)
	}
	out := res.Data.(*Output)
	if out.NewString != "fresh" || out.OldString != "" || out.FilePath != fp {
		t.Errorf("Output = %+v, want NewString=fresh OldString=empty FilePath=%s", out, fp)
	}
}

// ---------------------------------------------------------------------------
// Desanitization
// ---------------------------------------------------------------------------

// Every entry of the DESANITIZATIONS table must round-trip; a dropped entry
// silently breaks edits against API-sanitized text.
func TestDesanitizeMatchString_EveryEntry(t *testing.T) {
	t.Parallel()
	want := [][2]string{
		{"<fnr>", "<function_results>"},
		{"</fnr>", "</function_results>"},
		{"<n>", "<name>"},
		{"</n>", "</name>"},
		{"<o>", "<output>"},
		{"</o>", "</output>"},
		{"<e>", "<error>"},
		{"</e>", "</error>"},
		{"<s>", "<system>"},
		{"</s>", "</system>"},
		{"<r>", "<result>"},
		{"</r>", "</result>"},
		{"< META_START >", "<META_START>"},
		{"< META_END >", "<META_END>"},
		{"< EOT >", "<EOT>"},
		{"< META >", "<META>"},
		{"< SOS >", "<SOS>"},
		{"\n\nH:", "\n\nHuman:"},
		{"\n\nA:", "\n\nAssistant:"},
	}
	if len(desanitizations) != len(want) {
		t.Errorf("len(desanitizations) = %d, want %d", len(desanitizations), len(want))
	}
	for _, pair := range want {
		got, applied := desanitizeMatchString(pair[0])
		if got != pair[1] {
			t.Errorf("desanitizeMatchString(%q) = %q, want %q", pair[0], got, pair[1])
		}
		if len(applied) != 1 || applied[0].From != pair[0] || applied[0].To != pair[1] {
			t.Errorf("desanitizeMatchString(%q) applied = %+v, want exactly one %q→%q", pair[0], applied, pair[0], pair[1])
		}
	}

	got, applied := desanitizeMatchString("plain text stays")
	if got != "plain text stays" || len(applied) != 0 {
		t.Errorf("desanitizeMatchString(plain) = %q/%+v, want unchanged with no replacements", got, applied)
	}
}

// The desanitize fallback may only claim a match when the desanitized text is
// really in the file; otherwise the edit "succeeds" while changing nothing.
func TestExecute_DesanitizeRequiresRealTextInFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "desan.txt")
	content := "before\nplain data here\nafter\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(),
		json.RawMessage(`{"file_path":`+jsonQuote(fp)+`,"old_string":"<fnr>data here</fnr>","new_string":"<fnr>replaced</fnr>"}`), nil)
	if err == nil {
		t.Fatal("Execute(desanitized text absent) = nil error, want not-found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want not-found", err)
	}
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("file = %q, want it unchanged", data)
	}
}

// ---------------------------------------------------------------------------
// Not-found error shape
// ---------------------------------------------------------------------------

func TestExecute_NotFoundHintShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Overlapping tokens → the error carries the nearby-lines hint.
	near := filepath.Join(dir, "near.txt")
	if err := os.WriteFile(near, []byte("alpha line one\nbeta line two\ngamma line three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(),
		json.RawMessage(`{"file_path":`+jsonQuote(near)+`,"old_string":"beta line twp","new_string":"x"}`), nil)
	if err == nil {
		t.Fatal("Execute(missing string) = nil error, want not-found")
	}
	if !strings.Contains(err.Error(), "Nearby lines in file:") || !strings.Contains(err.Error(), "2: beta line two") {
		t.Errorf("err = %v, want the nearby-lines hint with the matching line", err)
	}

	// No token overlap → no hint section at all.
	bare := filepath.Join(dir, "bare.txt")
	if err := os.WriteFile(bare, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(),
		json.RawMessage(`{"file_path":`+jsonQuote(bare)+`,"old_string":"SELECT * FROM users","new_string":"x"}`), nil)
	if err == nil {
		t.Fatal("Execute(missing string) = nil error, want not-found")
	}
	if strings.Contains(err.Error(), "Nearby lines") {
		t.Errorf("err = %v, want no hint section for a zero-overlap search", err)
	}
}

// ---------------------------------------------------------------------------
// readFileForEdit — BOM sniffing must require the exact two-byte signature
// ---------------------------------------------------------------------------

func TestReadFileForEdit_BOMByteVectors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name     string
		data     []byte
		wantBOM  bool
		wantText string
	}{
		{"lone 0xFF", []byte{0xFF}, false, "\xff"},
		{"FE in second byte", []byte{0x00, 0xFE}, false, "\x00\xfe"},
		{"FF without FE", []byte{0xFF, 0x41}, false, "\xffA"},
		{"BOM plus one rune", []byte{0xFF, 0xFE, 'h', 0x00}, true, "h"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := filepath.Join(dir, "bom"+string(rune('a'+i))+".bin")
			if err := os.WriteFile(fp, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			res := readFileForEdit(fp)
			if !res.fileExists {
				t.Fatal("fileExists = false, want true")
			}
			if res.hasBOM != tc.wantBOM {
				t.Errorf("hasBOM = %v, want %v for %v", res.hasBOM, tc.wantBOM, tc.data)
			}
			if res.content != tc.wantText {
				t.Errorf("content = %q, want %q", res.content, tc.wantText)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Whitespace-insensitive matching
// ---------------------------------------------------------------------------

// Only leading whitespace collapses; a line with no indent keeps none, so an
// unindented search line must not match an indented file line.
func TestNormalizeLeadingWhitespace(t *testing.T) {
	t.Parallel()
	if got := normalizeLeadingWhitespace("abc\n\tdef"); got != "abc\n def" {
		t.Errorf("normalizeLeadingWhitespace = %q, want %q", got, "abc\n def")
	}
}

func TestFindActualString_WhitespaceBoundaries(t *testing.T) {
	t.Parallel()
	// Unindented search line vs indented file line → no match.
	if got, ok := FindActualString("foo\n    bar\n", "foo\nbar"); ok {
		t.Errorf("FindActualString = %q, want no match for an unindented first line", got)
	}
	// Substring match that is not line-aligned must not be reported as a match.
	if got, ok := FindActualString("\tabc\n\tdef", "abc\n d"); ok {
		t.Errorf("FindActualString = %q, want no match for a mid-line alignment", got)
	}
	// A match on the very first line must be found.
	if got, ok := FindActualString("\tbody\n", "  body\n"); !ok || got != "\tbody\n" {
		t.Errorf("FindActualString = %q/%v, want %q", got, ok, "\tbody\n")
	}
	// Partial alignment (first line off, last line on) is not a match.
	if got, ok := FindActualString("\ta\n\tb\n\t\tb\nc", "b\n\tb"); ok {
		t.Errorf("FindActualString = %q, want no match for a partial alignment", got)
	}
}

// ---------------------------------------------------------------------------
// Curly-quote preservation
// ---------------------------------------------------------------------------

// The exact-match fast path runs before quote normalization: a literal match
// later in the file must win over an earlier curly-quoted look-alike.
func TestFindActualString_ExactMatchWinsOverNormalized(t *testing.T) {
	t.Parallel()
	got, ok := FindActualString("\u201CB\u201D and \"B\"", `"B"`)
	if !ok || got != `"B"` {
		t.Errorf("FindActualString = %q/%v, want the straight-quoted match", got, ok)
	}
}

// old_string == actualOldString means nothing was normalized: new_string must
// stay verbatim even when the file text carries curly quotes.
func TestPreserveQuoteStyle_IdenticalStringsAreVerbatim(t *testing.T) {
	t.Parallel()
	got := PreserveQuoteStyle("\u2018it\u2019s", "\u2018it\u2019s", "don't go")
	if got != "don't go" {
		t.Errorf("PreserveQuoteStyle = %q, want the verbatim new_string", got)
	}
}

// Either curly variant in the matched text is enough to trigger conversion.
func TestPreserveQuoteStyle_SingleCurlyVariant(t *testing.T) {
	t.Parallel()
	cases := []struct {
		actual string
		newStr string
		want   string
	}{
		{"\u201Cfoo", `say "hi"`, "say \u201Chi\u201D"},
		{"foo\u201D", `say "hi"`, "say \u201Chi\u201D"},
		{"\u2018foo", "say 'hi'", "say \u2018hi\u2019"},
		{"foo\u2019", "say 'hi'", "say \u2018hi\u2019"},
	}
	for _, tc := range cases {
		got := PreserveQuoteStyle("different", tc.actual, tc.newStr)
		if got != tc.want {
			t.Errorf("PreserveQuoteStyle(actual=%q, new=%q) = %q, want %q", tc.actual, tc.newStr, got, tc.want)
		}
	}
}

// A quote preceded by punctuation/space opens even when the next char is a letter.
func TestApplyCurlySingleQuotes_OpeningAfterSpace(t *testing.T) {
	t.Parallel()
	for _, in := range []string{" 'a", "a 'b"} {
		want := strings.Replace(in, "'", "\u2018", 1)
		if got := applyCurlySingleQuotes(in); got != want {
			t.Errorf("applyCurlySingleQuotes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyCurlySingleQuotes_ExactForms(t *testing.T) {
	t.Parallel()
	if got := applyCurlySingleQuotes("don't"); got != "don\u2019t" {
		t.Errorf("applyCurlySingleQuotes(don't) = %q, want don\\u2019t", got)
	}
	if got := applyCurlySingleQuotes("'hello'"); got != "\u2018hello\u2019" {
		t.Errorf("applyCurlySingleQuotes('hello') = %q, want \\u2018hello\\u2019", got)
	}
}

// ---------------------------------------------------------------------------
// ApplyEditToFile
// ---------------------------------------------------------------------------

// replace_all=false must touch exactly one occurrence.
func TestApplyEditToFile_SingleOccurrenceOnly(t *testing.T) {
	t.Parallel()
	if got := ApplyEditToFile("a foo b foo c", "foo", "X", false); got != "a X b foo c" {
		t.Errorf("ApplyEditToFile = %q, want only the first occurrence replaced", got)
	}
}

// Trailing-newline stripping only applies when old_string itself has none.
func TestApplyEditToFile_DeleteKeepsOldStringNewline(t *testing.T) {
	t.Parallel()
	if got := ApplyEditToFile("a\nb\n\n", "b\n", "", false); got != "a\n\n" {
		t.Errorf("ApplyEditToFile = %q, want %q", got, "a\n\n")
	}
}

// ---------------------------------------------------------------------------
// findNearbyHint — meaningful-line thresholds
// ---------------------------------------------------------------------------

func TestFindNearbyHint_MeaningfulLineThreshold(t *testing.T) {
	t.Parallel()
	// 4 and 5 chars are below the threshold even though they are not generic.
	if got := findNearbyHint("x beta y", "beta"); got != "" {
		t.Errorf("4-char old line = %q, want no hint", got)
	}
	if got := findNearbyHint("x beta2 y", "beta2"); got != "" {
		t.Errorf("5-char old line = %q, want no hint", got)
	}
	// 6 chars clears it.
	if got := findNearbyHint("x beta2x y", "beta2x"); got != "1: x beta2x y\n" {
		t.Errorf("6-char old line = %q, want the matching line", got)
	}
	// Long but generic lines never count.
	if got := findNearbyHint("func f() {\n\treturn 42\n}", "return"); got != "" {
		t.Errorf("generic old line = %q, want no hint", got)
	}
}

// A one-character line can still be the best match, and a single-token line
// must be scored like any other.
func TestFindNearbyHint_ShortFileLines(t *testing.T) {
	t.Parallel()
	if got := findNearbyHint("x\nfiller filler filler", "x y z w v"); got != "1: x\n2: filler filler filler\n" {
		t.Errorf("hint = %q, want the 1-char line with its line number", got)
	}
}

// Equal scores keep the first match, which anchors the context window.
func TestFindNearbyHint_TiesKeepFirstMatch(t *testing.T) {
	t.Parallel()
	var lines []string
	for i := range 12 {
		if i == 0 || i == 9 {
			lines = append(lines, "target needle token here")
		} else {
			lines = append(lines, "filler"+strconv.Itoa(i)+" junk junk")
		}
	}
	content := strings.Join(lines, "\n")
	want := "1: target needle token here\n2: filler1 junk junk\n3: filler2 junk junk\n4: filler3 junk junk\n5: filler4 junk junk\n6: filler5 junk junk\n"
	if got := findNearbyHint(content, "target needle token here"); got != want {
		t.Errorf("hint =\n%q\nwant\n%q", got, want)
	}
}

// The window is exactly ±5 lines when it fits inside the file.
func TestFindNearbyHint_WindowSize(t *testing.T) {
	t.Parallel()
	var lines []string
	for i := range 15 {
		if i == 7 {
			lines = append(lines, "target needle token here")
		} else {
			lines = append(lines, "filler"+strconv.Itoa(i)+" junk junk")
		}
	}
	content := strings.Join(lines, "\n")
	want := "3: filler2 junk junk\n4: filler3 junk junk\n5: filler4 junk junk\n6: filler5 junk junk\n7: filler6 junk junk\n8: target needle token here\n9: filler8 junk junk\n10: filler9 junk junk\n11: filler10 junk junk\n12: filler11 junk junk\n13: filler12 junk junk\n"
	if got := findNearbyHint(content, "target needle token here"); got != want {
		t.Errorf("hint =\n%q\nwant\n%q", got, want)
	}
}
