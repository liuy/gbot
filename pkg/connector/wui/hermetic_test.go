package wui

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/hub"
)

// Hermetic transport for synctest bubbles.
//
// The wui WS tests historically ran each connection over a real loopback TCP
// httptest.Server. Under -race the netpoll coordination (accept, handshake,
// per-frame wakeups across OS threads) plus the deliberate real-clock waits
// (300ms read deadlines proving "no frame follows", flood windows) put the
// suite far past the 180s Makefile timeout. Per the project's test-selection
// framework, time behavior here IS the subject under test (takeover
// observation spans, flood windows, negative "no frame arrives" assertions),
// so these tests run inside testing/synctest bubbles — and a bubble only
// advances its virtual clock when every goroutine is durably blocked on
// bubble-tracked resources. Real loopback TCP never cooperates; net.Pipe
// (channel-based in-process transport) does, and its read/write deadlines are
// backed by bubble-virtualized timers. Verified empirically: a
// SetReadDeadline(300ms)+expect-timeout assertion completes in zero real time
// inside the bubble.

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "wui-hermetic" }

// pipeListener adapts net.Pipe pairs to net.Listener: Accept hands out the
// server end of each pair, the test dials with the client end. The channel
// hand-off is bubble-tracked, so a Serve loop parked on Accept keeps the
// bubble's clock semantics intact.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// chatServer is an http.Server on a pipeListener plus the bookkeeping needed
// to tear the whole subtree down BEFORE the synctest bubble exits: a synctest
// bubble only ends when every goroutine inside it has exited, so cleanup must
// be deterministic and ordered (close listener → close client conns → wait
// for request handlers → stop http server).
type chatServer struct {
	ln        *pipeListener
	srv       *http.Server
	handlerWG *sync.WaitGroup
	conn      *WUIConnector

	mu      sync.Mutex
	clients []net.Conn
}

type wgHandler struct {
	h  http.Handler
	wg sync.WaitGroup
}

func (w *wgHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	w.wg.Add(1)
	defer w.wg.Done()
	w.h.ServeHTTP(rw, req)
}

// startHermeticServer serves h over the in-memory transport. Call inside a
// synctest bubble; the caller must defer s.close() in the same bubble —
// deferred close also runs when the test goroutine Goexits via t.Fatalf,
// draining every server goroutine so the bubble can end.
func startHermeticServer(h http.Handler) *chatServer {
	wrapped := &wgHandler{h: h}
	s := &chatServer{ln: newPipeListener(), srv: &http.Server{Handler: wrapped}, handlerWG: &wrapped.wg}
	go func() {
		// Serve returns http.ErrServerClosed from close(); nothing to do —
		// the bubble's teardown is driven by close(), not by this error.
		_ = s.srv.Serve(s.ln)
	}()
	return s
}

// hermeticChatServer mounts the production /ws/chat endpoint for c.
func hermeticChatServer(c *WUIConnector) *chatServer {
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	s := startHermeticServer(mux)
	s.conn = c
	return s
}

// newClientConn creates one pipe pair, queues the server end for Accept, and
// returns the client end. All client ends are tracked so close() can unblock
// every server goroutine parked on them.
func (s *chatServer) newClientConn() net.Conn {
	c1, c2 := net.Pipe()
	s.mu.Lock()
	s.clients = append(s.clients, c1)
	s.mu.Unlock()
	s.ln.conns <- c2
	return c1
}

// dialWS opens a chat WS connection over the in-memory transport.
func (s *chatServer) dialWS(t *testing.T) *websocket.Conn {
	t.Helper()
	return s.dialWSAt(t, "/ws/chat")
}

// dialWSAt is dialWS for endpoints mounted at a non-default path (custom
// test handlers).
func (s *chatServer) dialWSAt(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	cc := s.newClientConn()
	d := websocket.Dialer{NetDial: func(_, _ string) (net.Conn, error) { return cc, nil }}
	ws, _, err := d.Dial("ws://wui-hermetic"+path, nil)
	if err != nil {
		// Errorf, not Fatalf: dialWS also runs on wg.Go goroutines (takeover
		// stress loop), where Fatalf's runtime.Goexit is illegal.
		t.Errorf("hermetic ws dial %s: %v", path, err)
		return nil
	}
	return ws
}

// awaitTakeover waits until the connector's active slot is activated, i.e.
// sendMetadata's critical section has ended. sendMetadata pushes the metadata
// frame and only THEN ends with slot.active.Store(true); wsWriter can deliver
// the frame before that store lands, so a caller that Handles events
// immediately after reading metadata would race the activation (state-only
// capture, frame never pushed). The historical real-TCP tests masked this
// behind multi-second real-time waits; the virtual clock skips those
// instantly, so this wait is what makes post-dial Handles deterministic.
func (s *chatServer) awaitTakeover(t *testing.T) {
	t.Helper()
	if s.conn == nil {
		return
	}
	waitFor(2*time.Second, func() bool {
		slot := s.conn.activeSlot()
		return slot != nil && slot.active.Load()
	})
}

// dialDrained opens a chat WS connection, consumes the initial takeover
// frames up to and including the metadata frame, and waits for the takeover
// to fully settle — the hermetic counterpart of the real-TCP dialAndStore
// helper.
func (s *chatServer) dialDrained(t *testing.T) *websocket.Conn {
	t.Helper()
	ws := s.dialWS(t)
	drainInitialFrames(t, ws)
	s.awaitTakeover(t)
	return ws
}

// client returns an http.Client whose connections flow through the same
// in-memory transport, for HTTP-only route tests.
func (s *chatServer) client() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return s.newClientConn(), nil
		},
	}}
}

// close tears the server subtree down deterministically. Order matters:
// closing client conns first is what unblocks handlers parked in ReadMessage
// or mid-write on the synchronous pipe; only then can handlerWG.Wait make
// progress.
func (s *chatServer) close() {
	_ = s.ln.Close()
	s.mu.Lock()
	clients := s.clients
	s.clients = nil
	s.mu.Unlock()
	for _, cc := range clients {
		_ = cc.Close()
	}
	s.handlerWG.Wait()
	_ = s.srv.Close()
}

// hermeticWUI runs fn inside a synctest bubble wired with a fresh mock-engine
// connector and the in-memory /ws/chat server. Both are stopped by defers in
// the bubble's main goroutine, so a t.Fatalf inside fn cannot strand bubble
// goroutines — the bubble would otherwise never drain.
func hermeticWUI(t *testing.T, fn func(t *testing.T, c *WUIConnector, hs *chatServer)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		c := newTestConnector(t)
		defer c.Stop()
		hs := hermeticChatServer(c)
		defer hs.close()
		fn(t, c, hs)
	})
}

// hermeticWUIHub is hermeticWUI with an explicit hub, for hub-routed dispatch
// tests.
func hermeticWUIHub(t *testing.T, h *hub.Hub, fn func(t *testing.T, c *WUIConnector, hs *chatServer)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		c := newTestConnectorWithHub(t, h)
		defer c.Stop()
		hs := hermeticChatServer(c)
		defer hs.close()
		fn(t, c, hs)
	})
}

// inBubble wraps fn in a bare synctest bubble for tests that build their own
// connector/server topology. Everything fn starts must exit before fn
// returns — bubble teardown is the test's own responsibility here.
func inBubble(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	synctest.Test(t, fn)
}
