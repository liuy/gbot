package cron

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func lockPath(dir string) string { return filepath.Join(dir, lockFileName) }

func readLockBody(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(lockPath(dir))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("lock body not JSON: %v (%s)", err, raw)
	}
	return body
}

// reapedChildPID spawns a child that exits immediately and returns its pid —
// a guaranteed-dead pid with no pid-guessing.
func reapedChildPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	return cmd.Process.Pid
}

func TestLockAcquireCreatesFile(t *testing.T) {
	dir := t.TempDir()
	l := NewLock(dir, "identity-a")
	if !l.TryAcquire() {
		t.Fatal("first TryAcquire = false, want true")
	}
	body := readLockBody(t, dir)
	if body["sessionId"] != "identity-a" {
		t.Errorf("lock sessionId = %v, want identity-a", body["sessionId"])
	}
	if pid, ok := body["pid"].(float64); !ok || int(pid) != os.Getpid() {
		t.Errorf("lock pid = %v, want %d", body["pid"], os.Getpid())
	}
	if _, ok := body["acquiredAt"].(float64); !ok {
		t.Errorf("lock acquiredAt = %v, want a number", body["acquiredAt"])
	}
}

func TestLockSecondLiveOwnerBlocked(t *testing.T) {
	dir := t.TempDir()
	a := NewLock(dir, "identity-a")
	if !a.TryAcquire() {
		t.Fatal("a TryAcquire failed")
	}
	// Different identity, live pid (ours — the first lock holds it).
	b := NewLock(dir, "identity-b")
	if b.TryAcquire() {
		t.Fatal("b TryAcquire = true with a live owner holding the lock, want false")
	}
	// The file is untouched.
	if body := readLockBody(t, dir); body["sessionId"] != "identity-a" {
		t.Errorf("blocked acquire overwrote lock: %v", body["sessionId"])
	}
	// Same identity re-acquire is idempotent true.
	if !a.TryAcquire() {
		t.Error("same-identity re-acquire = false, want true")
	}
}

func TestLockStalePIDRecovered(t *testing.T) {
	dir := t.TempDir()
	deadPID := reapedChildPID(t)
	body := `{"sessionId":"ghost","pid":` + strconv.Itoa(deadPID) + `,"acquiredAt":1000}`
	if err := os.WriteFile(lockPath(dir), []byte(body), 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}
	l := NewLock(dir, "identity-new")
	if !l.TryAcquire() {
		t.Fatal("TryAcquire with a dead owner pid = false, want true (stale recovery)")
	}
	got := readLockBody(t, dir)
	if got["sessionId"] != "identity-new" {
		t.Errorf("after recovery sessionId = %v, want identity-new", got["sessionId"])
	}
}

func TestLockCorruptBodyRecovered(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(lockPath(dir), []byte("not json at all"), 0o644); err != nil {
		t.Fatalf("seed corrupt lock: %v", err)
	}
	l := NewLock(dir, "identity-new")
	if !l.TryAcquire() {
		t.Fatal("TryAcquire with corrupt lock body = false, want true (treated as stale)")
	}
	if got := readLockBody(t, dir)["sessionId"]; got != "identity-new" {
		t.Errorf("after corrupt recovery sessionId = %v, want identity-new", got)
	}
}

func TestLockMissingDirCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "projects")
	l := NewLock(dir, "identity-a")
	if !l.TryAcquire() {
		t.Fatal("TryAcquire into a missing dir = false, want true (mkdir + retry)")
	}
	if _, err := os.Stat(lockPath(dir)); err != nil {
		t.Fatalf("lock file missing after acquire: %v", err)
	}
}

func TestLockRelease(t *testing.T) {
	dir := t.TempDir()
	a := NewLock(dir, "identity-a")
	if !a.TryAcquire() {
		t.Fatal("a TryAcquire failed")
	}
	// Non-owner Release leaves the file alone.
	b := NewLock(dir, "identity-b")
	b.Release()
	if _, err := os.Stat(lockPath(dir)); err != nil {
		t.Fatalf("non-owner Release removed the lock: %v", err)
	}
	// Owner Release removes it; then another lock can acquire.
	a.Release()
	if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("owner Release left the lock behind (stat err = %v)", err)
	}
	if !b.TryAcquire() {
		t.Fatal("TryAcquire after owner Release = false, want true")
	}
	// Release when no file exists is a no-op.
	a.Release()
}

func TestProcessAliveBoundaries(t *testing.T) {
	if processAlive(0) {
		t.Error("processAlive(0) = true, want false (malformed lock bodies must not signal the process group)")
	}
	if processAlive(-5) {
		t.Error("processAlive(-5) = true, want false")
	}
	// PID 1 always exists; as a non-root user signal 0 returns EPERM — the
	// alive-despite-EPERM branch.
	if os.Getuid() != 0 && !processAlive(1) {
		t.Error("processAlive(1) = false, want true (EPerm counts as alive)")
	}
}

func TestLockSameIdentityNewPIDRewritten(t *testing.T) {
	dir := t.TempDir()
	deadPID := reapedChildPID(t)
	// Lock held by OUR identity but a dead pid (engine reload): re-acquire
	// refreshes the body to the live pid.
	body := `{"sessionId":"identity-a","pid":` + strconv.Itoa(deadPID) + `,"acquiredAt":1000}`
	if err := os.WriteFile(lockPath(dir), []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	l := NewLock(dir, "identity-a")
	if !l.TryAcquire() {
		t.Fatal("same-identity re-acquire = false, want true")
	}
	got := readLockBody(t, dir)
	if pid, _ := got["pid"].(float64); int(pid) != os.Getpid() {
		t.Errorf("lock pid after re-acquire = %v, want live pid %d", got["pid"], os.Getpid())
	}
}

func TestLockMissingAcquiredAtTreatedStale(t *testing.T) {
	dir := t.TempDir()
	// Parseable JSON missing acquiredAt: CC's zod safeParse requires all three
	// fields, so this body fails the schema → undefined → stale treatment.
	// Our own (live) pid proves the classification: honored as a lock body it
	// would be a live owner (blocked), not a stale recovery.
	body := `{"sessionId":"other","pid":` + strconv.Itoa(os.Getpid()) + `}`
	if err := os.WriteFile(lockPath(dir), []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := NewLock(dir, "identity-a").readLock(); got != nil {
		t.Fatalf("readLock = %+v, want nil (safeParse-failure semantics)", got)
	}
	if !NewLock(dir, "identity-a").TryAcquire() {
		t.Fatal("TryAcquire with missing acquiredAt = false, want true (stale recovery)")
	}
	if got := readLockBody(t, dir)["sessionId"]; got != "identity-a" {
		t.Errorf("after recovery sessionId = %v, want identity-a", got)
	}
}

func TestLockMissingPIDTreatedStale(t *testing.T) {
	dir := t.TempDir()
	// The symmetric guard term: sessionId and acquiredAt present, pid absent.
	// readLock must classify it nil (stale) — dereferencing the nil PID here
	// would panic on the probe-ticker goroutine and kill the daemon.
	if err := os.WriteFile(lockPath(dir), []byte(`{"sessionId":"other","acquiredAt":1000}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := NewLock(dir, "identity-a").readLock(); got != nil {
		t.Fatalf("readLock = %+v, want nil (safeParse-failure semantics)", got)
	}
	if !NewLock(dir, "identity-a").TryAcquire() {
		t.Fatal("TryAcquire with missing pid = false, want true (stale recovery)")
	}
}

func TestLockMalformedTypedBodyRecovered(t *testing.T) {
	dir := t.TempDir()
	// pid as a string / missing sessionId: the typed parse fails → stale
	// treatment → recovery succeeds.
	if err := os.WriteFile(lockPath(dir), []byte(`{"sessionId":"x","pid":"123"}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !NewLock(dir, "identity-a").TryAcquire() {
		t.Fatal("string-pid lock body not recovered as stale")
	}
	NewLock(dir, "identity-a").Release()
	if err := os.WriteFile(lockPath(dir), []byte(`{"pid":123,"acquiredAt":1}`), 0o644); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	if !NewLock(dir, "identity-b").TryAcquire() {
		t.Fatal("sessionId-less lock body not recovered as stale")
	}
}

func TestLockReleaseCorruptBodyNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(lockPath(dir), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	l := NewLock(dir, "identity-a")
	l.Release()
	if _, err := os.Stat(lockPath(dir)); err != nil {
		t.Errorf("Release on a corrupt body removed or otherwise touched the file: %v", err)
	}
}
