package dream

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mutableFakeEngine allows changing sessionID after construction, simulating
// the real engine where SessionID() returns empty at factory time but a real
// value after NewSession/SwitchSession.
type mutableFakeEngine struct {
	mu  sync.Mutex
	sid string
}

func (f *mutableFakeEngine) SessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sid
}

func (f *mutableFakeEngine) setSessionID(sid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sid = sid
}

// capturingLister records the excludeSID passed to SessionsTouchedSince.
type capturingLister struct {
	excludeSID string
	called     bool
}

func (c *capturingLister) SessionsTouchedSince(projectDir string, since time.Time, excludeSID string) ([]string, error) {
	c.excludeSID = excludeSID
	c.called = true
	return []string{"s1", "s2", "s3", "s4", "s5"}, nil
}

// TestDream_ReadsSessionIDLive is the red-light test for the bug where
// factory passed newEng.SessionID() (empty at factory time) as a string
// argument, so dream never excluded the current session from consolidation.
//
// After the fix, Manager holds an engine interface and reads sessionID live
// at ShouldDream time. Scenario: construct with empty sessionID → later set
// sessionID → ShouldDream must pass the live value to SessionsTouchedSince.
func TestDream_ReadsSessionIDLive(t *testing.T) {
	t.Setenv("GBOT_AUTO_DREAM", "true")

	tmpDir := t.TempDir()
	lister := &capturingLister{}

	// Engine starts with empty sessionID (like factory time, before session
	// is created post-factory).
	eng := &mutableFakeEngine{sid: ""}

	// Set lock file mtime = 25h ago to pass time gate.
	lockPath := filepath.Join(tmpDir, lockFileName)
	if err := os.WriteFile(lockPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour) // REAL-TIME: testing time-based gate
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(Config{MinHours: 24, MinSessions: 5},
		tmpDir, "/project", eng,
		lister, nil, &mockDispatcher{}, slog.Default())

	// Later, session is created — sessionID becomes non-empty.
	eng.setSessionID("real-session-id")

	if _, _, _, err := mgr.ShouldDream(context.Background()); err != nil {
		t.Fatalf("ShouldDream: %v", err)
	}

	if !lister.called {
		t.Fatal("SessionsTouchedSince was never called")
	}
	if lister.excludeSID != "real-session-id" {
		t.Errorf("excludeSID = %q, want %q (live sessionID from engine, "+
			"not empty string from construction)", lister.excludeSID, "real-session-id")
	}
}
