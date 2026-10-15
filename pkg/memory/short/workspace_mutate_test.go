package short

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// workspace.go:37-41 — a read error that is not "missing file" must surface
// wrapped, not degrade into the (nil, nil) "no meta yet" answer.
func TestReadWorkspaceMeta_ReadPermissionError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod meta: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	meta, err := ReadWorkspaceMeta(dir)
	wantErrWrappingCause(t, err, "read workspace meta: open "+path+": permission denied")
	if meta != nil {
		t.Errorf("meta = %+v, want nil on read error", meta)
	}
}

// workspace.go:44-45 — an empty meta.json is "no meta yet", not a parse error.
func TestReadWorkspaceMeta_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), nil, 0o644); err != nil {
		t.Fatalf("write empty meta: %v", err)
	}

	meta, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta(empty file) = %v, want nil error", err)
	}
	if meta != nil {
		t.Errorf("meta = %+v, want nil for empty file", meta)
	}
}

// workspace.go:50 — the JSON parse failure keeps its cause.
func TestReadWorkspaceMeta_InvalidJSONWrapsCause(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	_, err := ReadWorkspaceMeta(dir)
	wantErrWrappingCause(t, err,
		"parse workspace meta: invalid character 'b' looking for beginning of object key string")
}

// workspace.go:59-60 — MkdirAll failure is reported with its cause, and the
// write must not continue past it.
func TestWriteWorkspaceMeta_MkdirError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	err := WriteWorkspaceMeta(filepath.Join(blocker, "sub"), &WorkspaceMeta{CurrentSessionID: "s"})
	wantErrWrappingCause(t, err, "create project directory: mkdir "+blocker+": not a directory")
}

// workspace.go:59 — the project directory is created with mode 0755.
func TestWriteWorkspaceMeta_DirMode(t *testing.T) {
	dir := t.TempDir()
	created := filepath.Join(dir, "nested", "project")

	if err := WriteWorkspaceMeta(created, &WorkspaceMeta{CurrentSessionID: "s"}); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}
	info, err := os.Stat(created)
	if err != nil {
		t.Fatalf("stat created dir: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("project dir perm = %v, want -rwxr-xr-x", info.Mode().Perm())
	}
}

// workspace.go:64-65 — an unmarshalable meta fails before anything is written.
func TestWriteWorkspaceMeta_MarshalError(t *testing.T) {
	dir := t.TempDir()

	err := WriteWorkspaceMeta(dir, &WorkspaceMeta{LastActiveAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)})
	wantErrWrappingCause(t, err,
		"marshal workspace meta: json: error calling MarshalJSON for type *time.Time: year outside of range [0,9999]")

	if _, err := os.Stat(filepath.Join(dir, "meta.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("meta.json after marshal error: stat err = %v, want os.ErrNotExist", err)
	}
}

// workspace.go:69-70 — the atomic write failure is reported with its cause.
func TestWriteWorkspaceMeta_WriteError(t *testing.T) {
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	err := WriteWorkspaceMeta(ro, &WorkspaceMeta{CurrentSessionID: "s"})
	wantErrWrappingCause(t, err,
		"write workspace meta: write temp file: open "+filepath.Join(ro, ".meta.json.tmp")+": permission denied")
}

// workspace.go:69 — meta.json is written with mode 0644.
func TestWriteWorkspaceMeta_FileMode(t *testing.T) {
	dir := t.TempDir()

	if err := WriteWorkspaceMeta(dir, &WorkspaceMeta{CurrentSessionID: "s"}); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("stat meta.json: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("meta.json perm = %v, want -rw-r--r--", info.Mode().Perm())
	}
}
