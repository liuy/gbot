package app

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// exitUpgrader drives watchUpgradeExit's <-upg.Exit() from the test; the
// existing fakeUpgrader's Exit() is a nil channel (blocks forever).
type exitUpgrader struct {
	exitCh chan struct{}
}

func (f *exitUpgrader) Listen(network, addr string) (net.Listener, error) {
	return nil, net.ErrClosed
}
func (f *exitUpgrader) Ready() error          { return nil }
func (f *exitUpgrader) Upgrade() error        { return nil }
func (f *exitUpgrader) Exit() <-chan struct{} { return f.exitCh }
func (f *exitUpgrader) HasParent() bool       { return false }
func (f *exitUpgrader) Stop()                 {}

func newExitTestStore(t *testing.T) (*short.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dbPath
}

func assertStoreClosed(t *testing.T, store *short.Store, context string) {
	t.Helper()
	if err := store.DB().Ping(); err == nil {
		t.Fatalf("%s: store still open — committed frames would live only in the -wal file at process death (10-13 e3 lost 37 messages this way)", context)
	}
}

// assertStoreOpen pins the handover-exit contract: the upgraded child is
// attached to the same -wal/-shm, so the parent must not close under it
// (a close also checkpoints over the child's view; deletion itself is now
// barred by persistWAL).
func assertStoreOpen(t *testing.T, store *short.Store, context string) {
	t.Helper()
	if err := store.DB().Ping(); err != nil {
		t.Fatalf("%s: store closed — the upgrade handover must leave it open (closing under the attached child checkpoints over its view; its commits must stay private to its own close): %v", context, err)
	}
}

// interceptExit swaps the osExit seam for a recording no-op and returns a
// channel closed when watchUpgradeExit reaches its exit call. The exit code
// is pinned: the upgrade path must exit 0.
func interceptExit(t *testing.T) chan struct{} {
	t.Helper()
	orig := osExit
	t.Cleanup(func() { osExit = orig })
	exited := make(chan struct{})
	osExit = func(code int) {
		if code != 0 {
			t.Errorf("upgrade exit code = %d, want 0", code)
		}
		close(exited)
	}
	return exited
}

// The upgrade handover exit must LEAVE THE STORE OPEN: the upgraded child
// opened the same database during its boot (its NewStore runs before the
// parent's upg.Exit() fires), and a store.Close on this path runs the
// final checkpoint and then DELETES the -wal/-shm sidecars out from under
// the child's live connections — its later commits return nil but vanish
// into the unlinked ghost wal (2026-10-17: 15 messages). Normal exits
// close the store via Instance.Cleanup; the handover exit must not.
func TestWatchUpgradeExit_LeavesStoreOpenForAttachedChild(t *testing.T) {
	exited := interceptExit(t)
	store, dbPath := newExitTestStore(t)
	upg := &exitUpgrader{exitCh: make(chan struct{})}

	go watchUpgradeExit(upg, &http.Server{}, nil, nil, nil, store)
	close(upg.exitCh)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("watchUpgradeExit did not exit after upg.Exit() closed")
	}
	assertStoreOpen(t, store, "upgrade handover exit")
	// The sidecars the attached child needs must still be on disk — deleting
	// them is exactly what the parent-side Close did.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err != nil {
			t.Errorf("memory.db%s missing after the handover exit — the attached child needs it: %v", suffix, err)
		}
	}
}

// The exit must follow the HTTP drain (in-flight requests persist at turn
// boundaries), and at the exit the store must still be open — with the
// parent-side skip-close, durability comes from WAL crash semantics: the
// committed row sits in the on-disk -wal, and a FRESH opener must read it.
// That fresh-open visibility is the store-level contract the attached
// upgraded child relies on.
func TestWatchUpgradeExit_ExitsAfterDrain_LeavesStoreOpen(t *testing.T) {
	store, dbPath := newExitTestStore(t)
	sess, err := store.CreateSessionWithEngine(t.TempDir(), "glm-5", "e9")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	if err := store.AppendMessage(sess.SessionID, &short.TranscriptMessage{
		UUID:    "drain-probe-1",
		Type:    "user",
		Content: `[{"type":"text","text":"pre-exit"}]`,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	var pingErr error
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		// Causal ordering proof: at the moment the in-flight request
		// finishes, the store must still be open — anything that closed it
		// before the drain completed would make this ping fail.
		pingErr = store.DB().Ping()
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered

	exited := interceptExit(t)
	upg := &exitUpgrader{exitCh: make(chan struct{})}
	go watchUpgradeExit(upg, srv, nil, nil, nil, store)
	close(upg.exitCh)

	select {
	case <-exited:
		t.Fatal("exited before the drain completed")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("watchUpgradeExit did not exit after release")
	}
	if pingErr != nil {
		t.Fatalf("store closed while the HTTP drain was still in flight — the exit must follow the drain, and nothing on this path may close the store: %v", pingErr)
	}
	assertStoreOpen(t, store, "after watchUpgradeExit")
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Errorf("-wal missing after the handover exit — the committed frames live there for the attached child: %v", err)
	}
	fresh, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("fresh open alongside the still-open parent store: %v", err)
	}
	defer fresh.Close()
	msgs, err := fresh.LoadMessages(sess.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages from fresh opener: %v", err)
	}
	if len(msgs) != 1 || msgs[0].UUID != "drain-probe-1" {
		t.Errorf("pre-exit message not visible to a fresh opener (crash-semantics durability): %+v", msgs)
	}
}

// RunDreamTimer only checks ctx between ticks, so a mid-tick dream Query
// outlives dreamCancel; watchUpgradeExit must join it (bounded) before the
// exit — the join lets the mid-tick dream persist commit before process
// death. The bound keeps a long dream Query from stalling the upgrade
// forever.
func TestWatchUpgradeExit_DreamJoinIsBounded(t *testing.T) {
	orig := dreamJoinTimeout
	dreamJoinTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dreamJoinTimeout = orig })

	exited := interceptExit(t)
	store, _ := newExitTestStore(t)
	upg := &exitUpgrader{exitCh: make(chan struct{})}
	dreamDone := make(chan struct{}) // never closed: dream tick "in flight"

	go watchUpgradeExit(upg, &http.Server{}, nil, nil, dreamDone, store)
	close(upg.exitCh)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("watchUpgradeExit hung on an unfinished dream tick")
	}
	assertStoreOpen(t, store, "after bounded dream join")
}

// The join must actually happen: with dreamDone open past the (generously
// raised) join timeout's reach, watchUpgradeExit must NOT exit — removing
// the join block turns this red immediately.
func TestWatchUpgradeExit_JoinsDreamBeforeExit(t *testing.T) {
	orig := dreamJoinTimeout
	dreamJoinTimeout = 2 * time.Second
	t.Cleanup(func() { dreamJoinTimeout = orig })

	exited := interceptExit(t)
	store, _ := newExitTestStore(t)
	upg := &exitUpgrader{exitCh: make(chan struct{})}
	dreamDone := make(chan struct{})

	go watchUpgradeExit(upg, &http.Server{}, nil, nil, dreamDone, store)
	close(upg.exitCh)

	select {
	case <-exited:
		t.Fatal("exited without waiting for the in-flight dream tick — join removed or misplaced")
	case <-time.After(150 * time.Millisecond):
	}
	close(dreamDone)

	// Exit must FOLLOW dreamDone promptly: a removed join case falls
	// through to the 2s timeout, which misses this window.
	select {
	case <-exited:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("exit did not follow dreamDone close — join case removed, only the timeout path exits")
	}
	assertStoreOpen(t, store, "after dream join")
}

// Instance.Cleanup is the shutdown path for non-upgrade exits; it must close
// the store too, not just engines/media/LSP.
func TestCleanup_ClosesStore(t *testing.T) {
	store, _ := newExitTestStore(t)
	inst := &Instance{Store: store}
	inst.Cleanup()
	assertStoreClosed(t, store, "after Instance.Cleanup")
}

// Cleanup must join DreamDone before closing the store (same race as the
// upgrade path: a mid-tick dream persist must not hit a closed db). The
// 500ms follow window kills a removed join case, which would otherwise
// fall through to the full dreamJoinTimeout.
func TestCleanup_JoinsDreamBeforeStoreClose(t *testing.T) {
	orig := dreamJoinTimeout
	dreamJoinTimeout = 2 * time.Second
	t.Cleanup(func() { dreamJoinTimeout = orig })

	store, _ := newExitTestStore(t)
	dreamDone := make(chan struct{})
	inst := &Instance{Store: store, DreamDone: dreamDone}

	done := make(chan struct{})
	go func() { inst.Cleanup(); close(done) }()

	select {
	case <-done:
		t.Fatal("Cleanup returned without joining DreamDone")
	case <-time.After(150 * time.Millisecond):
	}
	if err := store.DB().Ping(); err != nil {
		t.Fatalf("store closed before the dream join completed: %v", err)
	}

	close(dreamDone)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Cleanup did not resume after DreamDone closed — join case removed, only the timeout path proceeds")
	}
	assertStoreClosed(t, store, "after Cleanup dream join")
}

// The Cleanup join must also be bounded: a never-closing DreamDone must
// not hang shutdown (kills the timeout-case-removed mutant).
func TestCleanup_DreamJoinIsBounded(t *testing.T) {
	orig := dreamJoinTimeout
	dreamJoinTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dreamJoinTimeout = orig })

	store, _ := newExitTestStore(t)
	inst := &Instance{Store: store, DreamDone: make(chan struct{})}

	done := make(chan struct{})
	go func() { inst.Cleanup(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Cleanup hung on a never-closing DreamDone — timeout case removed")
	}
	assertStoreClosed(t, store, "after bounded Cleanup dream join")
}
