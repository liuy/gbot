package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/wui"
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

// interceptExit installs an exit hook for the duration of t and returns a
// channel closed when the code under test reaches its exit call. The exit code
// is pinned: the upgrade path must exit 0. The close is idempotent because a
// signal fan-out can drive the shared seam from several goroutines.
func interceptExit(t *testing.T) chan struct{} {
	t.Helper()
	exited := make(chan struct{})
	onExit(t, func(code int) {
		if code != 0 {
			t.Errorf("upgrade exit code = %d, want 0", code)
		}
		select {
		case <-exited:
		default:
			close(exited)
		}
	})
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
	// The gate's own flag is reset here so the assertion below proves THIS
	// call set it, not an earlier test.
	handedOver.Store(false)
	t.Cleanup(func() { handedOver.Store(false) })

	go watchUpgradeExit(upg, &http.Server{}, nil, nil, nil, store, nil)
	close(upg.exitCh)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("watchUpgradeExit did not exit after upg.Exit() closed")
	}
	// From the exit onward this process is not the PID-file owner; a SIGINT
	// landing here must skip pidCleanup, which is what handedOver gates.
	if !handedOver.Load() {
		t.Error("handedOver must be set on the handover exit — a late SIGINT would delete the child's PID file")
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
	go watchUpgradeExit(upg, srv, nil, nil, nil, store, nil)
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
	logs := captureAppLogs(t)
	orig := dreamJoinTimeout
	dreamJoinTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dreamJoinTimeout = orig })

	exited := interceptExit(t)
	store, _ := newExitTestStore(t)
	upg := &exitUpgrader{exitCh: make(chan struct{})}
	dreamDone := make(chan struct{}) // never closed: dream tick "in flight"

	go watchUpgradeExit(upg, &http.Server{}, nil, nil, dreamDone, store, nil)
	close(upg.exitCh)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("watchUpgradeExit hung on an unfinished dream tick")
	}
	assertStoreOpen(t, store, "after bounded dream join")
	// Exiting under the dream loses its in-flight persist; that must be said
	// out loud, not silently.
	if recs := logs.find("upgrade exit: dream timer still running; exiting under it (in-flight persist lost whole)"); len(recs) != 1 {
		t.Errorf("dream-timeout warnings = %d, want 1", len(recs))
	} else if recs[0].level != slog.LevelWarn {
		t.Errorf("dream-timeout log level = %v, want WARN", recs[0].level)
	}
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
	dreamCtx, dreamCancel := context.WithCancel(context.Background())
	t.Cleanup(dreamCancel)

	go watchUpgradeExit(upg, &http.Server{}, nil, dreamCancel, dreamDone, store, nil)
	close(upg.exitCh)

	select {
	case <-exited:
		t.Fatal("exited without waiting for the in-flight dream tick — join removed or misplaced")
	case <-time.After(150 * time.Millisecond):
	}
	// The cancel must precede the join: RunDreamTimer only checks ctx between
	// ticks, so a missing cancel leaves the tick running with no way to stop it.
	if dreamCtx.Err() == nil {
		t.Error("dream context not cancelled — the join would wait on a tick nothing stops")
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

// TestWatchUpgradeExit_ClosesBrowserWithUpgradeFrame pins the 1012 close frame
// reaching the browser before teardown: the WebView tells "the daemon is
// upgrading, reconnect" from a dropped link only by that code.
func TestWatchUpgradeExit_ClosesBrowserWithUpgradeFrame(t *testing.T) {
	exited := interceptExit(t)
	store, _ := newExitTestStore(t)
	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})
	mux := http.NewServeMux()
	wui.RegisterChatWS(mux, wc)
	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()

	ws, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial /ws/chat: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	if err := ws.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil { // REAL-TIME — bounded read
		t.Fatalf("ws deadline: %v", err)
	}

	upg := &exitUpgrader{exitCh: make(chan struct{})}
	go watchUpgradeExit(upg, srv, wc, nil, nil, store, nil)
	close(upg.exitCh)

	for {
		_, _, rerr := ws.ReadMessage()
		if rerr == nil {
			continue // metadata/history frames precede the close
		}
		var ce *websocket.CloseError
		if !errors.As(rerr, &ce) {
			t.Fatalf("browser saw %v, want a 1012 close frame from the upgrade", rerr)
		}
		if ce.Code != 1012 {
			t.Errorf("close code = %d, want 1012 (service restart)", ce.Code)
		}
		break
	}
	<-exited
}

// TestWatchUpgradeExit_ClosesUndrainedConnectionsOnShutdownTimeout pins the
// Close fallback behind the Shutdown error: a request that outlives the drain
// makes Shutdown return its deadline error, and only srv.Close reaps the
// connection. Without that fallback the socket stays open past the exit.
func TestWatchUpgradeExit_ClosesUndrainedConnectionsOnShutdownTimeout(t *testing.T) {
	exited := interceptExit(t)
	store, _ := newExitTestStore(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	requestIn := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestIn)
		<-release // outlives the drain window, forcing Shutdown's deadline error
		_, _ = w.Write([]byte("late"))
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	<-requestIn

	upg := &exitUpgrader{exitCh: make(chan struct{})}
	go watchUpgradeExit(upg, srv, nil, nil, nil, store, nil)
	close(upg.exitCh)

	// The exit lands only after the drain deadline (drainTimeout).
	<-exited
	// The server must have hard-closed the socket the drain could not finish:
	// a read that only stops on OUR deadline means the connection outlived the
	// exit, which is what srv.Close prevents.
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil { // REAL-TIME — bounded read
		t.Fatalf("conn deadline: %v", err)
	}
	n, rerr := conn.Read(make([]byte, 1))
	if rerr == nil {
		t.Fatalf("undrained connection still serving %d bytes after the exit — the srv.Close fallback is missing", n)
	}
	var ne net.Error
	if errors.As(rerr, &ne) && ne.Timeout() {
		t.Fatalf("undrained connection stayed open past the exit (read hit the local deadline): %v", rerr)
	}
}
