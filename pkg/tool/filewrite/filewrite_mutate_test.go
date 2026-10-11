package filewrite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

func writeCall(t *testing.T, input string, tctx *tool.ToolUseContext) (*Output, error) {
	t.Helper()
	res, err := Execute(context.Background(), json.RawMessage(input), tctx)
	if err != nil {
		return nil, err
	}
	return res.Data.(*Output), nil
}

// A relative path is resolved against the tool context's working directory,
// while absolute and ~ paths bypass that join.
func TestExecute_WorkingDirResolution(t *testing.T) {
	// Not parallel: overrides HOME.
	wd := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	tctx := &tool.ToolUseContext{WorkingDir: wd}

	out, err := writeCall(t, `{"file_path":"sub/rel.txt","content":"rel"}`, tctx)
	if err != nil {
		t.Fatalf("relative write: %v", err)
	}
	if out.FilePath != filepath.Join(wd, "sub", "rel.txt") {
		t.Errorf("FilePath = %q, want %q", out.FilePath, filepath.Join(wd, "sub", "rel.txt"))
	}
	data, err := os.ReadFile(filepath.Join(wd, "sub", "rel.txt"))
	if err != nil {
		t.Fatalf("relative file not under WorkingDir: %v", err)
	}
	if string(data) != "rel" {
		t.Errorf("content = %q, want rel", data)
	}

	// Absolute paths must not be re-rooted under WorkingDir.
	abs := filepath.Join(t.TempDir(), "abs.txt")
	if _, err := writeCall(t, `{"file_path":`+jsonQuote(abs)+`,"content":"abs"}`, tctx); err != nil {
		t.Fatalf("absolute write: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("absolute target missing: %v", err)
	}

	// "~/…" is home-relative, not WorkingDir-relative.
	if _, err := writeCall(t, `{"file_path":"~/tilde.txt","content":"tilde"}`, tctx); err != nil {
		t.Fatalf("tilde write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "tilde.txt")); err != nil {
		t.Errorf("~/tilde.txt did not land under HOME: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, "tilde.txt")); err == nil {
		t.Error("~/tilde.txt was written under WorkingDir; ~ must win")
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Created files and directories carry the tool's fixed permission bits.
func TestExecute_PermissionBits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deeper", "f.txt")
	if _, err := writeCall(t, `{"file_path":`+jsonQuote(target)+`,"content":"x"}`, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %o, want 644", st.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Join(dir, "nested", "deeper"))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o755 {
		t.Errorf("created dir mode = %o, want 755", dst.Mode().Perm())
	}
}

// contentChanged reports a read state whose timestamp disagrees with the file.
func TestExecute_ContentChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	mtime := st.ModTime().UnixMilli()

	// Timestamp ahead of the file: not stale, but the mtimes disagree.
	tctx := &tool.ToolUseContext{ReadFileState: map[string]tool.FileState{
		target: {Content: "old", Timestamp: mtime + 5000},
	}}
	out, err := writeCall(t, `{"file_path":`+jsonQuote(target)+`,"content":"new"}`, tctx)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !out.ContentChanged {
		t.Error("ContentChanged = false, want true when the read timestamp differs from the file mtime")
	}

	// Matching timestamp: nothing changed since the read.
	target2 := filepath.Join(dir, "steady.txt")
	if err := os.WriteFile(target2, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(target2)
	if err != nil {
		t.Fatal(err)
	}
	tctx2 := &tool.ToolUseContext{ReadFileState: map[string]tool.FileState{
		target2: {Content: "old", Timestamp: st2.ModTime().UnixMilli()},
	}}
	out2, err := writeCall(t, `{"file_path":`+jsonQuote(target2)+`,"content":"new"}`, tctx2)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if out2.ContentChanged {
		t.Error("ContentChanged = true, want false when the read timestamp matches")
	}
}

// The decode guard is a conjunction: either field alone identifies the result.
func TestDecodeResult_IdentifyingFields(t *testing.T) {
	t.Parallel()
	tl := New()
	dec := tl.(tool.ToolWithDecodeResult)

	v, err := dec.DecodeResult(tool.WrapSingleBlock(`{"type":"create","filePath":""}`))
	if err != nil {
		t.Fatalf("type-only wire rejected: %v", err)
	}
	if o := v.(*Output); o.Type != WriteTypeCreate {
		t.Errorf("decoded = %+v, want type create", o)
	}
	v, err = dec.DecodeResult(tool.WrapSingleBlock(`{"type":"","filePath":"/tmp/x"}`))
	if err != nil {
		t.Fatalf("path-only wire rejected: %v", err)
	}
	if o := v.(*Output); o.FilePath != "/tmp/x" {
		t.Errorf("decoded = %+v, want filePath /tmp/x", o)
	}
	_, err = dec.DecodeResult(tool.WrapSingleBlock(`{"name":"gbot"}`))
	if err == nil {
		t.Fatal("all-zero wire must be rejected")
	}
	want := "write: decoded output lacks identifying fields (not a legacy JSON result)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestNew_ToolDefContract(t *testing.T) {
	t.Parallel()
	tl := New()
	if got := tl.MaxResultSize(); got != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", got)
	}
	if got := tl.InterruptBehavior(); got != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", got)
	}
	if got := tl.IsDestructive(nil); got != true {
		t.Errorf("IsDestructive() = %v, want true", got)
	}
	if got := tl.IsConcurrencySafe(nil); got != true {
		t.Errorf("IsConcurrencySafe() = %v, want true", got)
	}
	if got := tl.Prompt(); got != fileWritePrompt() {
		t.Errorf("Prompt() not wired to fileWritePrompt()")
	}
}

func TestGetMtimeMsErrorReturnsZero(t *testing.T) {
	t.Parallel()
	mtime, err := getMtimeMs("/nonexistent/file/path")
	if err == nil {
		t.Fatal("getMtimeMs must fail for a missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v must be a not-exist error", err)
	}
	if mtime != 0 {
		t.Errorf("mtime on error = %d, want 0", mtime)
	}
}

// A tracked file with no diff against the ref yields no GitDiff at all.
func TestFetchGitDiffForFile_TrackedWithoutDiff(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")
	file := filepath.Join(repo, "clean.txt")
	if err := os.WriteFile(file, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "clean.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")

	got, err := fetchGitDiffForFile(file)
	if err != nil {
		t.Fatalf("fetchGitDiffForFile: %v", err)
	}
	if got != nil {
		t.Errorf("GitDiff = %+v, want nil for an unmodified tracked file", got)
	}
}

// CLAUDE_CODE_BASE_REF overrides the branch the merge-base is taken against.
func TestGetDiffRef_BaseRefEnv(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")

	t.Setenv("CLAUDE_CODE_BASE_REF", "refs/heads/does-not-exist")
	if got := getDiffRef(repo); got != "HEAD" {
		t.Errorf("getDiffRef with an unresolvable base ref = %q, want the HEAD fallback", got)
	}

	t.Setenv("CLAUDE_CODE_BASE_REF", "main")
	got := getDiffRef(repo)
	if got == "HEAD" || len(got) != 40 {
		t.Errorf("getDiffRef(main) = %q, want the 40-char merge-base SHA", got)
	}

	// Unset env: the default-branch lookup fills in the base.
	t.Setenv("CLAUDE_CODE_BASE_REF", "")
	got = getDiffRef(repo)
	if got == "HEAD" || len(got) != 40 {
		t.Errorf("getDiffRef(default branch) = %q, want the 40-char merge-base SHA", got)
	}
}

// A cloned repo whose origin default is master must report master, not the
// "main" fallback.
func TestGetDefaultBranch_OriginMaster(t *testing.T) {
	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "master")
	runGit(t, src, "config", "user.email", "t@t")
	runGit(t, src, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "a.txt")
	runGit(t, src, "commit", "-q", "-m", "init")

	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(clone), "clone", "-q", src, clone)

	if got := getDefaultBranch(clone); got != "master" {
		t.Errorf("getDefaultBranch = %q, want master (the main fallback would be wrong)", got)
	}
}

// With CLAUDE_CODE_REMOTE truthy and the target inside a repo, the output
// carries the synthetic git diff for the new file.
func TestExecute_GitDiffForUntrackedInRepo(t *testing.T) {
	t.Setenv("CLAUDE_CODE_REMOTE", "1")
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "seed.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")

	target := filepath.Join(repo, "brand-new.txt")
	out, err := writeCall(t, `{"file_path":`+jsonQuote(target)+`,"content":"one\ntwo\n"}`, nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if out.GitDiff == nil {
		t.Fatal("GitDiff = nil, want the synthetic diff for the new file")
	}
	if out.GitDiff.Filename != "brand-new.txt" {
		t.Errorf("GitDiff.Filename = %q, want brand-new.txt", out.GitDiff.Filename)
	}
	if out.GitDiff.Status != "added" {
		t.Errorf("GitDiff.Status = %q, want added", out.GitDiff.Status)
	}
	if out.GitDiff.Additions != 2 || out.GitDiff.Deletions != 0 || out.GitDiff.Changes != 2 {
		t.Errorf("GitDiff counts = +%d/-%d (%d), want +2/-0 (2)",
			out.GitDiff.Additions, out.GitDiff.Deletions, out.GitDiff.Changes)
	}
}

// Create and update outputs must carry every field the UI consumes.
func TestExecute_OutputFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "shaped.txt")

	created, err := writeCall(t, `{"file_path":`+jsonQuote(target)+`,"content":"a\nb\n"}`, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Type != WriteTypeCreate {
		t.Errorf("Type = %q, want create", created.Type)
	}
	if created.FilePath != target {
		t.Errorf("FilePath = %q, want %q", created.FilePath, target)
	}
	if created.Content != "a\nb\n" {
		t.Errorf("Content = %q, want a\\nb\\n", created.Content)
	}
	if created.OriginalFile != nil {
		t.Errorf("OriginalFile = %q, want nil for a create", *created.OriginalFile)
	}
	if len(created.StructuredPatch) != 0 {
		t.Errorf("StructuredPatch = %d hunks, want 0 for a create", len(created.StructuredPatch))
	}

	tctx := &tool.ToolUseContext{ReadFileState: map[string]tool.FileState{
		target: {Content: "a\nb\n", Timestamp: 1 << 60},
	}}
	updated, err := writeCall(t, `{"file_path":`+jsonQuote(target)+`,"content":"a\nB\n"}`, tctx)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Type != WriteTypeUpdate {
		t.Errorf("Type = %q, want update", updated.Type)
	}
	if updated.FilePath != target {
		t.Errorf("FilePath = %q, want %q", updated.FilePath, target)
	}
	if updated.Content != "a\nB\n" {
		t.Errorf("Content = %q, want a\\nB\\n", updated.Content)
	}
	if updated.OriginalFile == nil || *updated.OriginalFile != "a\nb\n" {
		t.Errorf("OriginalFile = %v, want the previous contents", updated.OriginalFile)
	}
	if len(updated.StructuredPatch) != 1 {
		t.Fatalf("StructuredPatch = %d hunks, want 1", len(updated.StructuredPatch))
	}
	h := updated.StructuredPatch[0]
	if h.OldStart != 1 || h.OldLines != 2 || h.NewStart != 1 || h.NewLines != 2 {
		t.Errorf("hunk range = (%d,%d,%d,%d), want (1,2,1,2)", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	}
	wantLines := []string{" a", "-b", "+B"}
	if len(h.Lines) != len(wantLines) {
		t.Fatalf("hunk lines = %q, want %q", h.Lines, wantLines)
	}
	for i := range wantLines {
		if h.Lines[i] != wantLines[i] {
			t.Errorf("hunk Lines[%d] = %q, want %q", i, h.Lines[i], wantLines[i])
		}
	}
}

// A relative path with no tool context must not dereference the nil context.
func TestExecute_RelativePathWithoutContext(t *testing.T) {
	rel := "filewrite_mutate_tmp_rel"
	t.Cleanup(func() { _ = os.RemoveAll(rel) })
	out, err := writeCall(t, `{"file_path":"`+rel+`/x.txt","content":"x"}`, nil)
	if err != nil {
		t.Fatalf("relative write without tctx: %v", err)
	}
	want, _ := filepath.Abs(filepath.Join(rel, "x.txt"))
	if out.FilePath != want {
		t.Errorf("FilePath = %q, want %q", out.FilePath, want)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
