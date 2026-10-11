package grep

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// fakeRg installs a stand-in ripgrep that records its argv to a file, so the
// constructed command line becomes observable for every output mode.
func fakeRg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv.txt")
	script := filepath.Join(dir, "fake-rg.sh")
	body := "#!/bin/sh\n: > " + argsFile + "\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argsFile + "; done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake rg: %v", err)
	}
	t.Setenv("GBOT_RG_PATH", script)
	return argsFile
}

func rgArgs(t *testing.T, argsFile, input string) []string {
	t.Helper()
	if _, err := Execute(context.Background(), json.RawMessage(input), &tool.ToolUseContext{WorkingDir: "/wd"}); err != nil {
		t.Fatalf("Execute(%s): %v", input, err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	var args []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if line != "" {
			args = append(args, line)
		}
	}
	return args
}

// baseArgs are the flags Grep always passes before the mode flags.
var baseArgs = []string{
	"--hidden", "--color=never", "--max-columns", "500",
	"--glob", "!.git", "--glob", "!.svn", "--glob", "!.hg",
	"--glob", "!.bzr", "--glob", "!.jj", "--glob", "!.sl",
}

func wantArgs(rest ...string) []string {
	out := append([]string{}, baseArgs...)
	return append(out, rest...)
}

// The ripgrep argv is the tool's contract with rg: mode flags, line numbers,
// context flags and pattern escaping are all pinned here.
func TestExecute_RgArgv(t *testing.T) {
	argsFile := fakeRg(t)
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"content default", `{"pattern":"foo","output_mode":"content","path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "/wd")},
		{"content no line numbers", `{"pattern":"foo","output_mode":"content","-n":false,"path":"/wd"}`,
			wantArgs("--no-heading", "--with-filename", "foo", "/wd")},
		{"content line numbers explicit", `{"pattern":"foo","output_mode":"content","-n":true,"path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "/wd")},
		{"files_with_matches", `{"pattern":"foo","output_mode":"files_with_matches","path":"/wd"}`,
			wantArgs("-l", "foo", "/wd")},
		{"count", `{"pattern":"foo","output_mode":"count","path":"/wd"}`,
			wantArgs("-c", "foo", "/wd")},
		{"case insensitive", `{"pattern":"foo","output_mode":"content","-i":true,"path":"/wd"}`,
			wantArgs("-i", "--line-number", "--no-heading", "--with-filename", "foo", "/wd")},
		{"dash pattern uses -e", `{"pattern":"-dash","output_mode":"content","path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "-e", "-dash", "/wd")},
		{"type filter", `{"pattern":"foo","output_mode":"content","type":"rust","path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "--type", "rust", "/wd")},
		{"glob split", `{"pattern":"foo","output_mode":"content","glob":"*.go,*.mod","path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "--glob", "*.go", "--glob", "*.mod", "/wd")},
		{"context C wins", `{"pattern":"foo","output_mode":"content","context":2,"path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "-C", "2", "/wd")},
		{"contextC alias when context unset", `{"pattern":"foo","output_mode":"content","-C":3,"path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "-C", "3", "/wd")},
		{"context beats contextC alias", `{"pattern":"foo","output_mode":"content","context":1,"-C":3,"path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "-C", "1", "/wd")},
		{"before and after", `{"pattern":"foo","output_mode":"content","-B":1,"-A":2,"path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "-B", "1", "-A", "2", "/wd")},
		{"after only", `{"pattern":"foo","output_mode":"content","-A":2,"path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "-A", "2", "/wd")},
		{"before only", `{"pattern":"foo","output_mode":"content","-B":1,"path":"/wd"}`,
			wantArgs("--line-number", "--no-heading", "--with-filename", "foo", "-B", "1", "/wd")},
		{"multiline", `{"pattern":"foo","output_mode":"content","multiline":true,"path":"/wd"}`,
			wantArgs("-U", "--multiline-dotall", "--line-number", "--no-heading", "--with-filename", "foo", "/wd")},
		{"context flags ignored outside content", `{"pattern":"foo","output_mode":"files_with_matches","-B":1,"-A":2,"path":"/wd"}`,
			wantArgs("-l", "foo", "/wd")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rgArgs(t, argsFile, c.input)
			if len(got) != len(c.want) {
				t.Fatalf("argv = %q, want %q", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("argv = %q, want %q", got, c.want)
				}
			}
		})
	}
}

// rg's stderr is surfaced verbatim; an empty stderr keeps the bare error.
func TestExecute_RgStderr(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fail-rg.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'regex parse error' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GBOT_RG_PATH", script)
	_, err := Execute(context.Background(), json.RawMessage(`{"pattern":"(","path":"/wd"}`), nil)
	if err == nil {
		t.Fatal("rg failure must be reported")
	}
	if err.Error() != "ripgrep error (exit 2): regex parse error" {
		t.Errorf("err = %q, want %q", err.Error(), "ripgrep error (exit 2): regex parse error")
	}

	quiet := filepath.Join(dir, "quiet-rg.sh")
	if err := os.WriteFile(quiet, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GBOT_RG_PATH", quiet)
	_, err = Execute(context.Background(), json.RawMessage(`{"pattern":"x","path":"/wd"}`), nil)
	if err == nil {
		t.Fatal("rg failure must be reported")
	}
	if err.Error() != "ripgrep error: exit status 2" {
		t.Errorf("err = %q, want the bare exit-status error", err.Error())
	}
}

// A count line whose colon is at index 0 is not a "file:count" pair.
func TestBuildResult_CountModeColonEdge(t *testing.T) {
	t.Parallel()
	res, err := buildResult("count", ":5\n", 250, 0)
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	out := res.Data.(*Output)
	if out.NumFiles != 1 {
		t.Errorf("NumFiles = %d, want 1", out.NumFiles)
	}
	if out.NumMatches != 0 {
		t.Errorf("NumMatches = %d, want 0 for a colon-led line (unparseable count)", out.NumMatches)
	}
	res, err = buildResult("count", "f.rs:7\n", 250, 0)
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if got := res.Data.(*Output).NumMatches; got != 7 {
		t.Errorf("NumMatches = %d, want 7", got)
	}
	// Single-character file names put the colon at index 1.
	res, err = buildResult("count", "a:9\n", 250, 0)
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if got := res.Data.(*Output).NumMatches; got != 9 {
		t.Errorf("NumMatches = %d, want 9 for a one-character file name", got)
	}
}

// AppliedOffset is reported only for a non-zero offset.
func TestBuildResult_CountModeAppliedOffset(t *testing.T) {
	t.Parallel()
	res, err := buildResult("count", "a:1\nb:2\n", 250, 0)
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if got := res.Data.(*Output).AppliedOffset; got != nil {
		t.Errorf("AppliedOffset = %v, want nil for offset 0", *got)
	}
	res, err = buildResult("count", "a:1\nb:2\n", 250, 1)
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	got := res.Data.(*Output).AppliedOffset
	if got == nil || *got != 1 {
		t.Errorf("AppliedOffset = %v, want 1", got)
	}
}

// applyHeadLimit boundaries: unlimited escape hatch, offset at/over the end,
// and the truncation report.
func TestApplyHeadLimit_Boundaries(t *testing.T) {
	t.Parallel()
	items := []string{"a", "b", "c", "d", "e"}

	got, lim := applyHeadLimit(items, 0, 0)
	if len(got) != 5 || lim != nil {
		t.Errorf("unlimited offset 0 = %q lim=%v, want all items and no limit", got, lim)
	}
	got, lim = applyHeadLimit(items, 0, 5)
	if len(got) != 0 || lim != nil {
		t.Errorf("unlimited offset == len = %q lim=%v, want empty", got, lim)
	}
	got, lim = applyHeadLimit(items, 0, 3)
	if len(got) != 2 || got[0] != "d" || lim != nil {
		t.Errorf("unlimited offset 3 = %q lim=%v, want [d e]", got, lim)
	}
	got, lim = applyHeadLimit(items, 2, 5)
	if len(got) != 0 || lim != nil {
		t.Errorf("offset past end = %q lim=%v, want empty", got, lim)
	}
	got, lim = applyHeadLimit(items, 2, 4)
	if len(got) != 1 || got[0] != "e" || lim != nil {
		t.Errorf("offset 4 limit 2 = %q lim=%v, want [e] with no truncation flag", got, lim)
	}
	got, lim = applyHeadLimit(items, 2, 1)
	if len(got) != 2 || got[0] != "b" || lim == nil || *lim != 2 {
		t.Errorf("offset 1 limit 2 = %q lim=%v, want [b c] with limit 2 reported", got, lim)
	}
	got, lim = applyHeadLimit(items, 5, 0)
	if len(got) != 5 || lim != nil {
		t.Errorf("limit == len = %q lim=%v, want all items and no truncation flag", got, lim)
	}
}

// files_with_matches renders one line per file, and nothing when empty.
func TestRenderResult_FilesWithMatches(t *testing.T) {
	t.Parallel()
	tl := New()
	if got := tl.RenderResult(&Output{Mode: "files_with_matches", Filenames: nil}); got != "" {
		t.Errorf("empty render = %q, want empty string", got)
	}
	if got := tl.RenderResult(&Output{Mode: "files_with_matches", Filenames: []string{"a.go"}}); got != "a.go" {
		t.Errorf("single-file render = %q, want a.go", got)
	}
	if got := tl.RenderResult(&Output{Mode: "files_with_matches", Filenames: []string{"a.go", "b.go"}}); got != "a.go\nb.go" {
		t.Errorf("two-file render = %q, want a.go\\nb.go", got)
	}
	if got := tl.RenderResult(&Output{Mode: "count", NumMatches: 3, NumFiles: 2}); got != "3 matches in 2 files" {
		t.Errorf("count render = %q, want %q", got, "3 matches in 2 files")
	}
	if got := tl.RenderResult(&Output{Mode: "count", NumMatches: 2, NumFiles: 1}); got != "2 matches in 1 file" {
		t.Errorf("count render = %q, want %q", got, "2 matches in 1 file")
	}
}

// The decode guard accepts a wire object that carries any identifying field,
// including the legacy count.
func TestDecodeResult_IdentifyingFields(t *testing.T) {
	t.Parallel()
	tl := New()
	dec := tl.(tool.ToolWithDecodeResult)
	for _, raw := range []string{
		`{"mode":"content","content":"x"}`,
		`{"count":3}`,
		`{"matches":[{"filePath":"a.go"}]}`,
	} {
		if _, err := dec.DecodeResult(tool.WrapSingleBlock(raw)); err != nil {
			t.Errorf("DecodeResult(%s) rejected: %v", raw, err)
		}
	}
	_, err := dec.DecodeResult(tool.WrapSingleBlock(`{"numFiles":2}`))
	if err == nil {
		t.Fatal("all-zero-identity wire must be rejected")
	}
	if err.Error() != "grep: decoded output lacks identifying fields (not a legacy JSON result)" {
		t.Errorf("err = %q, want the pinned message", err.Error())
	}
}

// Brace groups stay intact only when both braces are present.
func TestSplitGlobPatterns_BraceRequirement(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"*.{ts,tsx}", []string{"*.{ts,tsx}"}},
		{"a,b}", []string{"a", "b}"}},
		{"a{b,c", []string{"a{b", "c"}},
		{"a{b,c}", []string{"a{b,c}"}},
		{"*.go ,*.mod", []string{"*.go", "*.mod"}},
	}
	for _, c := range cases {
		got := splitGlobPatterns(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitGlobPatterns(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("splitGlobPatterns(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}

func TestFormatLimitInfo(t *testing.T) {
	t.Parallel()
	lim, off := 250, 0
	off5 := 5
	limNil := (*int)(nil)
	cases := []struct {
		in   *int
		off  *int
		want string
	}{
		{&lim, nil, "limit: 250"},
		{&lim, &off, "limit: 250"},
		{&lim, &off5, "limit: 250, offset: 5"},
		{limNil, &off5, "offset: 5"},
		{limNil, nil, ""},
	}
	for _, c := range cases {
		if got := formatLimitInfo(c.in, c.off); got != c.want {
			t.Errorf("formatLimitInfo(%v, %v) = %q, want %q", c.in, c.off, got, c.want)
		}
	}
}

// files_with_matches sorts newest first; unstatable paths sort last by name.
func TestSortByMtime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := filepath.Join(dir, "a_old.txt")
	fresh := filepath.Join(dir, "b_fresh.txt")
	for _, f := range []string{old, fresh} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "zz_missing.txt")
	got := sortByMtime([]string{missing, old, fresh})
	want := []string{fresh, old, missing}
	if len(got) != len(want) {
		t.Fatalf("sortByMtime = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortByMtime = %q, want %q", got, want)
		}
	}
}

func TestToolDefFields(t *testing.T) {
	t.Parallel()
	tl := New()
	if got := tl.Name(); got != "Grep" {
		t.Errorf("Name() = %q, want Grep", got)
	}
	aliases := tl.Aliases()
	if len(aliases) != 1 || aliases[0] != "grep" {
		t.Errorf("Aliases() = %q, want [grep]", aliases)
	}
	if got := tl.MaxResultSize(); got != 20000 {
		t.Errorf("MaxResultSize() = %d, want 20000", got)
	}
	if got := tl.IsReadOnly(nil); got != true {
		t.Errorf("IsReadOnly() = %v, want true", got)
	}
	if got := tl.IsConcurrencySafe(nil); got != true {
		t.Errorf("IsConcurrencySafe() = %v, want true", got)
	}
	if got := tl.InterruptBehavior(); got != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", got)
	}
	if got := tool.SearchHint(tl); got != "" {
		t.Errorf("SearchHint() = %q, want empty (Grep declares no hint)", got)
	}
	if got := tl.Prompt(); got != grepPrompt() {
		t.Error("Prompt() not wired to grepPrompt()")
	}
}
