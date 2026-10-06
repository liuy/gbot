package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

// projectRootFor walks from a file's directory up to "/", so every test whose
// premise is "this directory has no project" depends on /tmp and / carrying no
// go.mod, package.json or Cargo.toml. If TestProjectRootFor_NoMarker goes red
// without a code change, look for a stray marker in /tmp before looking here.
func TestProjectRootFor_NearestMarker(t *testing.T) {
	dir := cleanTestDir(t)
	sub := filepath.Join(dir, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.27\n")
	writeFile(t, filepath.Join(sub, "x.go"), "package sub\n")

	if got := projectRootFor(filepath.Join(sub, "x.go"), ".go"); got != dir {
		t.Errorf("projectRootFor = %q, want %q", got, dir)
	}
}

func TestProjectRootFor_NestedMarkerWins(t *testing.T) {
	dir := cleanTestDir(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/outer\n\ngo 1.27\n")
	writeFile(t, filepath.Join(sub, "go.mod"), "module example.com/inner\n\ngo 1.27\n")
	writeFile(t, filepath.Join(sub, "x.go"), "package inner\n")

	got := projectRootFor(filepath.Join(sub, "x.go"), ".go")
	if got != sub {
		t.Errorf("projectRootFor = %q, want %q (the nearest marker, not %q)", got, sub, dir)
	}
}

func TestProjectRootFor_NoMarker(t *testing.T) {
	dir := cleanTestDir(t)
	writeFile(t, filepath.Join(dir, "x.go"), "package x\n")

	if got := projectRootFor(filepath.Join(dir, "x.go"), ".go"); got != "" {
		t.Errorf("projectRootFor = %q, want empty", got)
	}
}

func TestProjectRootFor_ExtWithoutMarkers(t *testing.T) {
	dir := cleanTestDir(t)
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.27\n")
	writeFile(t, filepath.Join(dir, "x.txt"), "hello\n")

	if got := projectRootFor(filepath.Join(dir, "x.txt"), ".txt"); got != "" {
		t.Errorf("projectRootFor(.txt) = %q, want empty even though go.mod is present", got)
	}
}

func TestProjectRootFor_RustCargo(t *testing.T) {
	dir := cleanTestDir(t)
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"x\"\n")
	writeFile(t, filepath.Join(src, "x.rs"), "fn main() {}\n")

	if got := projectRootFor(filepath.Join(src, "x.rs"), ".rs"); got != dir {
		t.Errorf("projectRootFor = %q, want %q", got, dir)
	}
}

func TestProjectRootFor_MarkerIsDirectory(t *testing.T) {
	dir := cleanTestDir(t)
	if err := os.Mkdir(filepath.Join(dir, "go.mod"), 0o755); err != nil {
		t.Fatalf("mkdir go.mod-as-directory: %v", err)
	}
	writeFile(t, filepath.Join(dir, "x.go"), "package x\n")

	if got := projectRootFor(filepath.Join(dir, "x.go"), ".go"); got != "" {
		t.Errorf("projectRootFor = %q, want empty: a directory named go.mod is not a marker", got)
	}
}

func TestPathWithin(t *testing.T) {
	cases := []struct {
		p, root string
		want    bool
	}{
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/x", "/", true},
		{"/a/b/c", "/a/b/", true},
		{"/x", "", false},
	}
	for _, c := range cases {
		if got := pathWithin(c.p, c.root); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", c.p, c.root, got, c.want)
		}
	}
}

func TestCleanAbs_ResolvesSymlink(t *testing.T) {
	base := cleanTestDir(t)
	realDir := filepath.Join(base, "real")
	linkDir := filepath.Join(base, "link")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// EvalSymlinks Lstats every component and returns on the first error, so
	// the final component has to exist; without this file the resolution fails
	// and cleanAbs legitimately falls back to the unresolved path.
	writeFile(t, filepath.Join(realDir, "x.go"), "package x\n")

	got := cleanAbs(linkDir + "/x.go")
	want := cleanAbs(realDir) + "/x.go"
	if got != want {
		t.Errorf("cleanAbs = %q, want %q: one project must not produce two roots", got, want)
	}
}

func TestCleanAbs_BrokenSymlinkFallsBack(t *testing.T) {
	base := cleanTestDir(t)
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "does-not-exist"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got := cleanAbs(link)
	if got != link {
		t.Errorf("cleanAbs = %q, want %q (the cleaned absolute path)", got, link)
	}
}

// cleanTestDir returns a temp dir that is already absolute, clean and free of
// symlinks, so tests can compare results against the raw t.TempDir() string.
func cleanTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if got := cleanAbs(dir); got != dir {
		t.Fatalf("t.TempDir() = %q but cleanAbs resolves it to %q; these tests compare against the raw path", dir, got)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
