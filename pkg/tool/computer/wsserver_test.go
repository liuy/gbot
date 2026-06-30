package computer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWSClient dials url, reads one rpcRequest, replies via respFn (echoing
// the id), and keeps the conn open for the test's lifetime. The dial+reply
// runs in a background goroutine; it reports a dial failure on errCh instead
// of calling t.Fatalf (govet forbids t.Fatalf from a non-test goroutine). The
// main goroutine asserts errCh stays empty. Returns immediately.
func dialWSClient(t *testing.T, url string, respFn func(req rpcRequest) rpcResponse, errCh chan<- error) {
	t.Helper()
	go func() {
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
			return
		}
		defer c.Close()
		// REAL-TIME: read deadline guards against a hung peer over a real socket.
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var req rpcRequest
		if json.Unmarshal(data, &req) != nil {
			return
		}
		resp := respFn(req)
		resp.ID = req.ID
		out, err := json.Marshal(resp)
		if err != nil {
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, out)
		// Hold the conn open until the test tears down (caller closes via cleanup
		// of the underlying server); block on a read that returns when the server
		// closes.
		_, _, _ = c.ReadMessage()
	}()
}

// dialWSEmpty connects a gorilla client that sends one request and reads
// nothing (used for route-by-IP tests that only assert registry population).
// It runs in the main test goroutine and may t.Fatalf on dial failure.
func dialWSEmpty(t *testing.T, url string, req rpcRequest) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := c.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("client write: %v", err)
	}
	return c
}

// dialAcceptConnAsync dials url in a background goroutine and reports the
// result on resCh (nil conn + non-nil error on failure). Avoids t.Fatalf from
// a goroutine (govet testinggoroutine). The caller asserts the error after.
func dialAcceptConnAsync(url string, resCh chan<- dialResult) {
	go func() {
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		select {
		case resCh <- dialResult{conn: c, err: err}:
		default:
		}
		// conn lifetime is managed via the httptest server handler blocking;
		// do NOT close here — the test hands the accepted conn to the registry.
	}()
}

// dialResult carries the outcome of an async dial.
type dialResult struct {
	conn *websocket.Conn
	err  error
}

// makeAcceptConnServer starts an httptest WS server that upgrades any conn
// and returns the accepted *websocket.Conn on a channel so the test can wrap
// it in a *deviceClient and Register it. The server holds the conn open
// (without reading — the deviceClient readLoop owns reads after Register) by
// blocking on a per-conn channel until test cleanup.
func makeAcceptConnServer(t *testing.T) (string, chan *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		select {
		case accepted <- conn:
		default:
		}
		// Block until cleanup so the httptest handler does not return and
		// tear the conn down. Do NOT read — the deviceClient readLoop owns reads.
		<-release
	}))
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	return url, accepted
}

// freePort grabs an ephemeral port by binding then closing a listener, so the
// caller can rebind StartWSServer onto a known-free address. Slightly racy
// (TOCTOU between Close and the server's Listen) but acceptable for tests
// running sequentially within a package.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return addr
}

// waitFor polls cond until it returns true or the timeout elapses, returning
// whether cond ever held. The poll loop uses a real wall-clock deadline and a
// short sleep — the registry is populated by the server's network goroutine,
// so there is no channel to select on and synctest cannot model the real TCP
// round-trip.
//
// REAL-TIME
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout) // REAL-TIME
	for time.Now().Before(deadline) {   // REAL-TIME
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	return cond()
}

func TestConnectionRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 4)
	// Dial from a gorilla client to obtain a server-accepted *websocket.Conn.
	dialAcceptConnAsync(url, resCh)
	select {
	case ws := <-accepted:
		reg := NewConnectionRegistry()
		c := reg.Register("1.2.3.4", ws)
		got, ok := reg.Get("1.2.3.4")
		if !ok {
			t.Fatal("Get(1.2.3.4) ok = false, want true")
		}
		if got != c {
			t.Error("Get returned a different *deviceClient than Register")
		}
		if _, ok := reg.Get("9.9.9.9"); ok {
			t.Error("Get(9.9.9.9) ok = true, want false")
		}
		// Host collision: register a second conn for the same host, the old
		// client must be closed and replaced.
		dialAcceptConnAsync(url, resCh)
		var ws2 *websocket.Conn
		select {
		case ws2 = <-accepted:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for second accepted conn")
		}
		old := c
		newC := reg.Register("1.2.3.4", ws2)
		if !old.IsClosed() {
			t.Error("old client IsClosed = false after collision, want true")
		}
		got2, ok := reg.Get("1.2.3.4")
		if !ok || got2 != newC {
			t.Error("Get after collision did not return the new client")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted conn")
	}
}

func TestConnectionRegistry_Close(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 4)
	reg := NewConnectionRegistry()
	registered := make([]*deviceClient, 0, 2)
	for i := range 2 {
		dialAcceptConnAsync(url, resCh)
		select {
		case ws := <-accepted:
			registered = append(registered, reg.Register(hostKey(i), ws))
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for accepted conn %d", i)
		}
	}
	reg.Close()
	for i, c := range registered {
		if !c.IsClosed() {
			t.Errorf("client %d IsClosed = false after Close, want true", i)
		}
	}
}

// hostKey returns a deterministic host string for index i.
func hostKey(i int) string {
	return string(rune('a'+i)) + ".b.c.d"
}

func TestStartWSServer_RouteByRemoteIP(t *testing.T) {
	t.Parallel()
	reg := NewConnectionRegistry()
	port := freePort(t)
	addr := "127.0.0.1:" + itoa(port)
	srv, err := StartWSServer(reg, addr)
	if err != nil {
		t.Fatalf("StartWSServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	// Dial as a gorilla client and send a request; the server-side deviceClient
	// must be registered under 127.0.0.1.
	dialWSEmpty(t, "ws://"+addr+"/ws", rpcRequest{ID: "x", Command: "tap", Params: map[string]any{"x": 1, "y": 2}})
	// Wait for the registry to populate (Upgrade + Register is async-ish).
	if !waitFor(2*time.Second, func() bool {
		_, ok := reg.Get("127.0.0.1")
		return ok
	}) {
		t.Fatal("timed out waiting for inbound client to register under 127.0.0.1")
	}
	c, ok := reg.Get("127.0.0.1")
	if !ok {
		t.Fatal("Get(127.0.0.1) ok = false, want true (inbound client routed by RemoteIP)")
	}
	if c.IsClosed() {
		t.Error("registered client IsClosed = true, want false")
	}
	// The registered *deviceClient must serve a call() round-trip: dial a
	// fresh client that replies, then call() through the registry client.
	errCh := make(chan error, 1)
	dialWSClient(t, "ws://"+addr+"/ws", func(req rpcRequest) rpcResponse {
		return rpcResponse{Success: true, Data: json.RawMessage(`{}`)}
	}, errCh)
	// Allow the second conn to register (also under 127.0.0.1, replacing the
	// first). Then call() through it.
	first := c
	if !waitFor(2*time.Second, func() bool {
		cur, ok := reg.Get("127.0.0.1")
		return ok && cur != first
	}) {
		t.Fatal("timed out waiting for second conn to replace the first")
	}
	c, _ = reg.Get("127.0.0.1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.call(ctx, "tap", map[string]any{"x": 1, "y": 2}); err != nil {
		t.Errorf("call through registry client: %v", err)
	}
}

func TestStartWSServer_BindFailure(t *testing.T) {
	t.Parallel()
	reg := NewConnectionRegistry()
	// Port 1 is unprivileged → bind denied.
	_, err := StartWSServer(reg, "127.0.0.1:1")
	if err == nil {
		t.Fatal("StartWSServer on port 1 returned nil error, want bind failure")
	}
}

func TestRegDial_NoRegistry(t *testing.T) {
	t.Parallel()
	// NewAndroidBackend() sets dial = b.regDial with registry nil → Connect
	// must return the "daemon not running" error (do NOT use the test-for
	// constructor, which overrides dial with a fake).
	b := NewAndroidBackend()
	ctx := context.Background()
	err := b.Connect(ctx, "1.2.3.4", DefaultWSPort, "")
	if err == nil {
		t.Fatal("Connect returned nil with nil registry, want error")
	}
	if !strings.Contains(err.Error(), "daemon not running") {
		t.Errorf("error = %q, want it to contain 'daemon not running'", err.Error())
	}
}

// itoa converts a non-negative int to its decimal string without strconv
// (keeps this file's imports focused on the websocket/registry surface).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
