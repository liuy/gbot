package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// ---------------------------------------------------------------------------
// Log rotation — the limit is exclusive: exactly maxLogSize stays put
// ---------------------------------------------------------------------------

func TestSetupLogFile_ExactlyAtLimitDoesNotRotate(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	if err := os.WriteFile(logPath, make([]byte, maxLogSize), 0o600); err != nil {
		t.Fatalf("write %d-byte log: %v", maxLogSize, err)
	}

	f := setupLogFile(logPath)
	if f == nil {
		t.Fatal("setupLogFile returned nil for a writable path")
	}
	defer f.Close()

	if _, err := os.Stat(logPath + ".old"); !os.IsNotExist(err) {
		t.Errorf("a log of exactly maxLogSize (%d) bytes was rotated: stat(.old) err = %v", maxLogSize, err)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() != int64(maxLogSize) {
		t.Errorf("log size after setupLogFile = %d, want %d (append mode, no rotation)", info.Size(), maxLogSize)
	}
}

func TestSetupLogFile_OneByteOverLimitRotates(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	if err := os.WriteFile(logPath, make([]byte, maxLogSize+1), 0o600); err != nil {
		t.Fatalf("write oversized log: %v", err)
	}

	f := setupLogFile(logPath)
	if f == nil {
		t.Fatal("setupLogFile returned nil for a writable path")
	}
	defer f.Close()

	old, err := os.Stat(logPath + ".old")
	if err != nil {
		t.Fatalf("maxLogSize+1 bytes must rotate, stat(.old): %v", err)
	}
	if old.Size() != int64(maxLogSize+1) {
		t.Errorf(".old size = %d, want %d", old.Size(), maxLogSize+1)
	}
	if fresh, err := os.Stat(logPath); err != nil || fresh.Size() != 0 {
		t.Errorf("new log after rotation = %+v (err %v), want an empty file", fresh, err)
	}
}

// ---------------------------------------------------------------------------
// pprof — both "off" and "-" disable the server
// ---------------------------------------------------------------------------

// captureHandler records every slog record so the pprof decisions are
// observable without probing :6060 — a live gbot daemon already owns that
// port on a developer machine, so absence-of-listener cannot be asserted.
type captureHandler struct {
	mu      sync.Mutex
	records []string
}

func (c *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := []string{r.Level.String(), r.Message}
	r.Attrs(func(a slog.Attr) bool {
		parts = append(parts, a.Key+"="+a.Value.String())
		return true
	})
	c.records = append(c.records, strings.Join(parts, " "))
	return nil
}

func (c *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(_ string) slog.Handler      { return c }

func (c *captureHandler) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.records...)
}

func (c *captureHandler) waitFor(want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout) // REAL-TIME: bounded poll for the pprof listen record
	for {
		for _, rec := range c.all() {
			if strings.HasPrefix(rec, want) {
				return true
			}
		}
		if time.Now().After(deadline) { // REAL-TIME: timeout makes the poll a hard assertion
			return false
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: re-check loop for the async listen record
	}
}

func TestStartPprofServer_DisableTokensSkipTheBind(t *testing.T) {
	t.Setenv("GBOT_PPROF_ADDR", "")
	rec := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(prev)

	startPprofServer("off")
	startPprofServer("-")

	if got := rec.all(); !reflect.DeepEqual(got, []string{"INFO pprof:disabled", "INFO pprof:disabled"}) {
		t.Errorf("pprof records for \"off\" and \"-\" = %v, want two pprof:disabled records", got)
	}
}

func TestStartPprofServer_EnvAddrWinsAndIsLogged(t *testing.T) {
	envAddr := freePort(t)
	t.Setenv("GBOT_PPROF_ADDR", envAddr)
	rec := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(prev)

	startPprofServer("off") // the env address must win over the cfg argument

	if !rec.waitFor("INFO pprof:listen addr="+envAddr, 3*time.Second) {
		t.Errorf("no pprof:listen record for the env address %q, got %v", envAddr, rec.all())
	}
}

// ---------------------------------------------------------------------------
// needWS — a non-default port or GBOT_WS_ADDR mounts the server without -p
// ---------------------------------------------------------------------------

func waitForHTTP(t *testing.T, base string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout) // REAL-TIME: bounded wait for the WS listener
	for {
		resp, err := http.Get(base + "/")
		if err == nil {
			if cerr := resp.Body.Close(); cerr != nil {
				t.Logf("close probe body: %v", cerr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("first response after port open: status %d, want 200", resp.StatusCode)
			}
			return
		}
		if time.Now().After(deadline) { // REAL-TIME: timeout makes the poll a hard assertion
			t.Fatalf("port at %s never accepted connections: %v", base, err)
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: re-check loop for the listener
	}
}

func shutdownInstance(t *testing.T, inst *Instance) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if inst.LSPReg != nil {
			inst.LSPReg.Shutdown(ctx)
		}
	})
}

// -p <port> (WSPort != "8765") must mount the server even without NoTUI.
func TestStart_NonDefaultPortMountsServerWithoutNoTUI(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "")
	port := freeTCPPort(t)
	startInTempProject(t)

	inst, err := Start(Options{WSPort: port})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)

	waitForHTTP(t, "http://127.0.0.1:"+port, 5*time.Second)
}

// GBOT_WS_ADDR alone must mount the server on the env address, even with the
// default WSPort and no NoTUI.
func TestStart_EnvWSAddrMountsServerWithoutNoTUI(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)

	inst, err := Start(Options{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)

	waitForHTTP(t, "http://127.0.0.1:"+port, 5*time.Second)
}

// Supervised mode binds alongside the still-draining predecessor, so an
// occupied port must NOT fail the cold-boot preflight.
func TestStart_SupervisedModeSkipsPortPreflight(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_SUPERVISED", "1")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)

	ln, err := listenTCPReuseport("127.0.0.1:" + port)
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer ln.Close()

	inst, err := Start(Options{DaemonMode: true, NoTUI: true})
	if err != nil {
		t.Fatalf("Start in supervised mode must bind alongside the predecessor, got: %v", err)
	}
	shutdownInstance(t, inst)
}

// ---------------------------------------------------------------------------
// Session cleanup — the retention window is 30 days, not "any age"
// ---------------------------------------------------------------------------

func TestStartSessionCleanup_KeepsSessionsInsideThirtyDays(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "memory.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessions := map[string]string{"29 days": "-29 days", "31 days": "-31 days"}
	ids := make(map[string]string, len(sessions))
	for label, offset := range sessions {
		ses, err := store.CreateSessionWithEngine(projectDir, "test-model", "main")
		if err != nil {
			t.Fatalf("CreateSessionWithEngine(%s): %v", label, err)
		}
		msg := &short.TranscriptMessage{
			UUID:      "uuid-" + label,
			Type:      "user",
			Content:   `{"type":"text","text":"` + label + `"}`,
			CreatedAt: time.Now(), // REAL-TIME: message timestamps share the wall clock the cleanup cuts against
		}
		if err := store.AppendMessage(ses.SessionID, msg); err != nil {
			t.Fatalf("AppendMessage(%s): %v", label, err)
		}
		if _, err := store.DB().Exec(
			"UPDATE sessions SET updated_at = datetime('now', ?) WHERE session_id = ?",
			offset, ses.SessionID,
		); err != nil {
			t.Fatalf("backdate %s session: %v", label, err)
		}
		ids[label] = ses.SessionID
	}

	startSessionCleanup(store, 10*time.Millisecond, time.Hour)

	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded wait for the cleanup goroutine's real firstDelay
	for {
		if _, err := store.GetSession(ids["31 days"]); err != nil {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME: timeout makes the poll a hard assertion
			t.Fatalf("the 31-day session was not deleted within 2s of the cleanup pass")
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: re-check loop for the wall-clock cleanup pass
	}

	if _, err := store.GetSession(ids["29 days"]); err != nil {
		t.Errorf("29-day session %s was deleted, want kept (retention window is 30 days): %v", ids["29 days"], err)
	}
	if _, err := store.MessageExists(ids["29 days"], "uuid-29 days"); err != nil {
		t.Errorf("29-day session's message lookup failed: %v", err)
	}
}

func TestVersionInfo_JoinsBuildAndCommit(t *testing.T) {
	got := VersionInfo()
	want := version + " · " + commit
	if got != want {
		t.Errorf("VersionInfo() = %q, want %q", got, want)
	}
	if strings.Count(got, " · ") != 1 {
		t.Errorf("VersionInfo() = %q, want exactly one separator", got)
	}
}

// The rotation fixtures above derive their size from maxLogSize itself, so a
// mutated constant moves the threshold and the fixture together and nothing
// changes. These pin the limit to its literal byte count instead.
func TestSetupLogFile_LiteralLimitBoundary(t *testing.T) {
	const limit = 20 * 1024 * 1024

	for _, tc := range []struct {
		size    int
		wantOld bool
	}{
		{limit - 1, false},
		{limit, false},
		{limit + 1, true},
	} {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "gbot.log")
		if err := os.WriteFile(logPath, make([]byte, tc.size), 0o600); err != nil {
			t.Fatalf("write %d-byte log: %v", tc.size, err)
		}

		f := setupLogFile(logPath)
		if f == nil {
			t.Fatalf("setupLogFile returned nil for a %d-byte log", tc.size)
		}

		_, oldErr := os.Stat(logPath + ".old")
		rotated := oldErr == nil
		if rotated != tc.wantOld {
			t.Errorf("log of %d bytes: rotated = %v, want %v", tc.size, rotated, tc.wantOld)
		}
		if fresh, err := os.Stat(logPath); err == nil && tc.wantOld && fresh.Size() != 0 {
			t.Errorf("log of %d bytes: new file is %d bytes, want 0 after rotation", tc.size, fresh.Size())
		}
		_ = f.Close()
	}
}

// A dream session recorded under engine_id "dream" must be resumed by the
// next boot instead of a fresh one being minted.
func TestStart_DreamResumesItsPreviousSession(t *testing.T) {
	inst := startForGates(t, "")
	projectDir := inst.ProjectDir

	prior, err := inst.Store.CreateSessionWithEngine(projectDir, "m", "dream")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine(dream): %v", err)
	}
	if inst.PIDCleanup != nil {
		inst.PIDCleanup() // release the lock so the second boot can take it
	}

	// Second boot in the same project: resumeDreamSession must switch the
	// freshly built dream engine back onto the recorded session.
	second, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	shutdownInstance(t, second)

	vs := second.EngineMgr.Get("dream")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("dream engine missing on the second boot, holds %v", engineIDs(second.EngineMgr))
	}
	if vs.Engine.SessionID() != prior.SessionID {
		t.Errorf("dream session = %q, want the recorded %q", vs.Engine.SessionID(), prior.SessionID)
	}
	if vs.ActiveSessionID != prior.SessionID {
		t.Errorf("dream ActiveSessionID = %q, want %q", vs.ActiveSessionID, prior.SessionID)
	}
}
