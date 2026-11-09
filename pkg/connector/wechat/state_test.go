package wechat

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinCaptureSlog runs fn with the default logger redirected into a buffer so
// warning/error log lines become observable test output.
func pinCaptureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	fn()
	return buf.String()
}

// LoadState must surface a non-ENOENT read failure (path is a directory) as an
// error instead of swallowing it into the "missing file" nil,nil result.
func TestPinLoadState_DirectoryAtStatePath(t *testing.T) {
	dir := t.TempDir()
	path := StateFilePath(dir, "acct")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir at state path: %v", err)
	}
	st, err := LoadState("acct", dir)
	if err == nil {
		t.Fatalf("LoadState(dir-at-state-path) err = nil, state = %+v; want EISDIR error", st)
	}
	if os.IsNotExist(err) {
		t.Errorf("LoadState error = %v, want a non-ENOENT error", err)
	}
	if st != nil {
		t.Errorf("LoadState state = %+v, want nil alongside the error", st)
	}
}

// LoadAllStates must propagate a non-ENOENT ReadDir failure. With <projectDir>/
// wechat replaced by a regular file the error is ENOTDIR, which the IsNotExist
// guard must not swallow and the outer guard must not drop.
func TestPinLoadAllStates_WechatIsAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wechat"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write wechat file: %v", err)
	}
	states, err := LoadAllStates(dir)
	if err == nil {
		t.Fatalf("LoadAllStates(wechat is a file) err = nil, states = %v; want ENOTDIR error", states)
	}
	if os.IsNotExist(err) {
		t.Errorf("LoadAllStates error = %v, want a non-ENOENT error", err)
	}
	if states != nil {
		t.Errorf("LoadAllStates states = %v, want nil alongside the error", states)
	}
}

// The scan must skip directories, non-.json names and corrupt JSON while
// keeping every valid account, and must name each skipped file in a warning.
func TestPinLoadAllStates_SkipsAndWarnings(t *testing.T) {
	dir := t.TempDir()
	wd := filepath.Join(dir, "wechat")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatalf("mkdir wechat dir: %v", err)
	}
	files := []struct {
		name    string
		content string
		dir     bool
	}{
		{"a.txt", `{"account_id":"from-txt"}`, false},   // valid JSON, wrong suffix
		{"b.json", `{"account_id":"from-json"}`, false}, // the only valid account
		{"c.json", "", true},                            // directory wearing a .json name
		{"d.json", "{not json", false},                  // corrupt
	}
	for _, f := range files {
		p := filepath.Join(wd, f.name)
		if f.dir {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", f.name, err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
	var states []*State
	var err error
	logs := pinCaptureSlog(t, func() { states, err = LoadAllStates(dir) })
	if err != nil {
		t.Fatalf("LoadAllStates: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d (%v), want exactly 1 (b.json only)", len(states), states)
	}
	if states[0].AccountID != "from-json" {
		t.Errorf("states[0].AccountID = %q, want %q", states[0].AccountID, "from-json")
	}
	if !strings.Contains(logs, "skip corrupt state file") || !strings.Contains(logs, "d.json") {
		t.Errorf("logs = %q, want a corrupt-file warning naming d.json", logs)
	}
	// c.json is a directory: the IsDir guard must drop it before the read, so
	// no unreadable-file warning may appear.
	if strings.Contains(logs, "unreadable") {
		t.Errorf("logs = %q, want no unreadable-file warning (the directory must be skipped by name)", logs)
	}
}

// An unreadable .json file must be skipped (not abort the scan) and reported.
func TestPinLoadAllStates_UnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not deny reads for root")
	}
	dir := t.TempDir()
	wd := filepath.Join(dir, "wechat")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatalf("mkdir wechat dir: %v", err)
	}
	// a.json sorts first: a break-instead-of-continue mutant stops before b.json.
	if err := os.WriteFile(filepath.Join(wd, "a.json"), []byte(`{"account_id":"unreadable"}`), 0o000); err != nil {
		t.Fatalf("write a.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wd, "b.json"), []byte(`{"account_id":"readable"}`), 0o644); err != nil {
		t.Fatalf("write b.json: %v", err)
	}
	var states []*State
	var err error
	logs := pinCaptureSlog(t, func() { states, err = LoadAllStates(dir) })
	if err != nil {
		t.Fatalf("LoadAllStates: %v", err)
	}
	if len(states) != 1 || states[0].AccountID != "readable" {
		t.Fatalf("states = %v, want exactly the readable account", states)
	}
	if !strings.Contains(logs, "skip unreadable state file") || !strings.Contains(logs, "a.json") {
		t.Errorf("logs = %q, want an unreadable-file warning naming a.json", logs)
	}
}

// SaveState creates the wechat/ directory 0755 and the state file 0644; the
// modes are the on-disk contract for a file that holds a bot token.
func TestPinSaveState_FileModes(t *testing.T) {
	dir := t.TempDir()
	if err := SaveState(&State{AccountID: "acct", Token: "tok"}, dir); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	di, err := os.Stat(filepath.Join(dir, "wechat"))
	if err != nil {
		t.Fatalf("stat wechat dir: %v", err)
	}
	if di.Mode().Perm() != 0o755 {
		t.Errorf("wechat dir mode = %v, want -rwxr-xr-x", di.Mode().Perm())
	}
	fi, err := os.Stat(StateFilePath(dir, "acct"))
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("state file mode = %v, want -rw-r--r--", fi.Mode().Perm())
	}
}
