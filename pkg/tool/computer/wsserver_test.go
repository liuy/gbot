package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/liuy/gbot/pkg/tool"
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

// dialAcceptConnPinned dials url via dialAcceptConnAsync and pins the
// client-side conn for the test's whole lifetime. The dialer goroutine's
// only reference to that conn is the value buffered in resCh, and the
// compiler may treat a never-read-again channel as unreachable — Go's
// netFD finalizer then closes the conn on the next GC, the server-side
// readLoop hits EOF, and Sole's IsClosed filter reports the just-registered
// conn as dead. Draining resCh at cleanup keeps the conn referenced during
// the test and closes it deterministically after.
func dialAcceptConnPinned(t *testing.T, url string, resCh chan dialResult) {
	t.Helper()
	dialAcceptConnAsync(url, resCh)
	t.Cleanup(func() {
		select {
		case r := <-resCh:
			if r.conn != nil {
				_ = r.conn.Close()
			}
		default:
		}
	})
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
	dialAcceptConnPinned(t, url, resCh)
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
		dialAcceptConnPinned(t, url, resCh)
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
		dialAcceptConnPinned(t, url, resCh)
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

func TestConnectionRegistry_Sole_Empty(t *testing.T) {
	t.Parallel()
	reg := NewConnectionRegistry()
	c, ok := reg.Sole()
	if ok || c != nil {
		t.Errorf("Sole on empty registry = (%p, %v), want (nil, false)", c, ok)
	}
}

func TestConnectionRegistry_Sole_SingleLiveEntry(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 2)
	dialAcceptConnPinned(t, url, resCh)
	select {
	case ws := <-accepted:
		reg := NewConnectionRegistry()
		c := reg.Register("1.2.3.4", ws)
		got, ok := reg.Sole()
		if !ok {
			t.Fatal("Sole ok = false, want true")
		}
		if got != c {
			t.Error("Sole returned a different *deviceClient than Register")
		}
		// Sole must release its read lock: a leaked RLock would block every
		// later Close/Register forever. Probe with a short-timeout write-lock
		// acquire instead of deadlocking on Close, so a regression fails in
		// 2s rather than hanging the suite.
		acquired := make(chan struct{})
		go func() {
			reg.mu.Lock()
			close(acquired)
			reg.mu.Unlock()
		}()
		select {
		case <-acquired:
		case <-time.After(2 * time.Second):
			t.Fatal("write lock not acquired within 2s; Sole leaked its read lock")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted conn")
	}
}

func TestConnectionRegistry_Sole_AllDead(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 2)
	dialAcceptConnPinned(t, url, resCh)
	select {
	case ws := <-accepted:
		reg := NewConnectionRegistry()
		c := reg.Register("1.2.3.4", ws)
		if err := c.close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		got, ok := reg.Sole()
		if ok || got != nil {
			t.Errorf("Sole with only dead entries = (%p, %v), want (nil, false)", got, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted conn")
	}
}

// SkipsDead pins the phone-changed-IP recovery: the corpse under the
// sort-earlier key must not shadow the live conn behind it.
func TestConnectionRegistry_Sole_SkipsDead(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 2)
	reg := NewConnectionRegistry()
	dialAcceptConnPinned(t, url, resCh)
	var deadWS *websocket.Conn
	select {
	case deadWS = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first accepted conn")
	}
	dead := reg.Register("a.a.a.a", deadWS)
	if err := dead.close(); err != nil {
		t.Fatalf("close dead: %v", err)
	}
	dialAcceptConnPinned(t, url, resCh)
	select {
	case ws := <-accepted:
		live := reg.Register("b.b.b.b", ws)
		got, ok := reg.Sole()
		if !ok {
			t.Fatal("Sole ok = false with a live entry present")
		}
		if got != live {
			t.Error("Sole returned the dead conn, want the live one under b.b.b.b")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second accepted conn")
	}
}

// MultipleDeterministicFirst catches unsorted map iteration: Sole must hand
// out the same client every call, not a random entry per connect.
func TestConnectionRegistry_Sole_MultipleDeterministicFirst(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 4)
	reg := NewConnectionRegistry()
	clients := map[string]*deviceClient{}
	for _, host := range []string{"b.b.b.b", "a.a.a.a"} {
		dialAcceptConnPinned(t, url, resCh)
		select {
		case ws := <-accepted:
			clients[host] = reg.Register(host, ws)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for accepted conn for %s", host)
		}
	}
	want := clients["a.a.a.a"]
	for i := range 20 {
		got, ok := reg.Sole()
		if !ok {
			t.Fatalf("Sole call %d ok = false, want true", i)
		}
		if got != want {
			t.Fatalf("Sole call %d did not return the a.a.a.a client; map iteration leaked into selection", i)
		}
	}
}

// HoldsReadLockDuringScan kills an early-released read lock (e.g. the deferred
// RUnlock executed immediately): Sole would iterate r.clients unguarded while
// the daemon's accept loop Registers. go-mutesting runs without -race, so the
// detector is Go's own runtime — a write overlapping an unlocked map iteration
// throws "fatal error: concurrent map iteration and map write" and fails the
// binary. Under correct locking the fatal is impossible, so this test cannot
// false-fail; under the mutant, a spinning writer overlapping any of the many
// full dead-map scans throws within milliseconds. The writer mirrors what
// Register does to the map (store under the write lock) without needing a real
// websocket.Conn.
func TestConnectionRegistry_Sole_HoldsReadLockDuringScan(t *testing.T) {
	t.Parallel()
	reg := NewConnectionRegistry()
	// Dead entries only: Sole must sweep all of them per call, giving the
	// writer a long overlap window. Direct struct literals are safe — Sole
	// only reads the closed flag, no conn or read loop is involved.
	for i := range 10_000 {
		c := &deviceClient{}
		c.closed.Store(true)
		reg.clients[fmt.Sprintf("h%05d", i)] = c
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
	})
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			c := &deviceClient{}
			c.closed.Store(true)
			reg.mu.Lock()
			reg.clients[fmt.Sprintf("w%04d", i%2_000)] = c
			reg.mu.Unlock()
		}
	})
	for range 100 {
		if c, ok := reg.Sole(); ok || c != nil {
			t.Fatalf("Sole with only dead entries = (%p, %v), want (nil, false)", c, ok)
		}
	}
}

// TestRegDial_AttachesSoleInboundConn covers the production hot path with a
// real accepted conn: registry → Connect → active client pointer.
func TestRegDial_AttachesSoleInboundConn(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 2)
	dialAcceptConnPinned(t, url, resCh)
	select {
	case ws := <-accepted:
		reg := NewConnectionRegistry()
		c := reg.Register("1.2.3.4", ws)
		b := NewAndroidBackendWithRegistry(reg)
		if err := b.Connect(context.Background()); err != nil {
			t.Fatalf("Connect via registry: %v", err)
		}
		if !b.IsConnected() {
			t.Error("IsConnected = false after Connect, want true")
		}
		if b.client != rpcCaller(c) {
			t.Error("b.client is not the registered *deviceClient")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted conn")
	}
}

func TestRegDial_EmptyRegistry_Hints8765(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackendWithRegistry(NewConnectionRegistry())
	err := b.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect on empty registry returned nil, want error")
	}
	if !strings.Contains(err.Error(), "no GBot app connected") {
		t.Errorf("error = %q, want 'no GBot app connected'", err.Error())
	}
	if !strings.Contains(err.Error(), "8765") {
		t.Errorf("error = %q, want it to contain '8765'", err.Error())
	}
}

func TestRegDial_DeadConnInRegistry(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 2)
	dialAcceptConnPinned(t, url, resCh)
	select {
	case ws := <-accepted:
		reg := NewConnectionRegistry()
		c := reg.Register("1.2.3.4", ws)
		if err := c.close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		b := NewAndroidBackendWithRegistry(reg)
		err := b.Connect(context.Background())
		if err == nil {
			t.Fatal("Connect with only dead conns returned nil, want error")
		}
		if !strings.Contains(err.Error(), "no GBot app connected") {
			t.Errorf("error = %q, want 'no GBot app connected'", err.Error())
		}
		if !strings.Contains(err.Error(), "8765") {
			t.Errorf("error = %q, want it to contain '8765'", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted conn")
	}
}

// TestComputerTool_ConnectViaRegistry is the integration test: tool.Call
// entry → parseInput/dispatch → AndroidBackend.Connect → registry.Sole →
// ToolResult, all against a real accepted WS conn.
func TestComputerTool_ConnectViaRegistry(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	resCh := make(chan dialResult, 2)
	dialAcceptConnPinned(t, url, resCh)
	select {
	case ws := <-accepted:
		reg := NewConnectionRegistry()
		c := reg.Register("1.2.3.4", ws)
		b := NewAndroidBackendWithRegistry(reg)
		tt := New(b)
		res, err := tt.Call(context.Background(), json.RawMessage(`{"action":"connect"}`), &tool.ToolUseContext{})
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		ar, ok := res.Data.(*ActionResult)
		if !ok {
			t.Fatalf("Data type = %T, want *ActionResult", res.Data)
		}
		if ar.Action != "connect" || !ar.OK {
			t.Errorf("ActionResult = {%q, %v}, want {connect, true}", ar.Action, ar.OK)
		}
		if !b.IsConnected() {
			t.Error("backend not connected after tool-level connect")
		}
		if b.client != rpcCaller(c) {
			t.Error("backend client is not the registered *deviceClient")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted conn")
	}
}

func TestStartWSServer_RouteByRemoteIP(t *testing.T) {
	t.Parallel()
	reg := NewConnectionRegistry()
	port := freePort(t)
	addr := "127.0.0.1:" + itoa(port)
	srv, err := StartWSServer(reg, addr, http.NewServeMux())
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
	_, err := StartWSServer(reg, "127.0.0.1:1", http.NewServeMux())
	if err == nil {
		t.Fatal("StartWSServer on port 1 returned nil error, want bind failure")
	}
}

// The tableflip path injects an already-open listener (inherited fd), so
// StartWSServerOn must serve the mux on exactly that listener — including the
// device routes RegisterDeviceWS mounts — and srv.Close must stop it.
func TestStartWSServerOn_ServesInjectedListener(t *testing.T) {
	t.Parallel()
	reg := NewConnectionRegistry()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srv, err := StartWSServerOn(reg, ln, http.NewServeMux())
	if err != nil {
		t.Fatalf("StartWSServerOn: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// Unrouted path → 404 proves the injected listener serves the mux.
	resp, err := http.Get("http://" + addr + "/no-such-path")
	if err != nil {
		t.Fatalf("GET on injected listener: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /no-such-path status = %d, want 404", resp.StatusCode)
	}

	// Plain GET on /ws fails the gorilla handshake with 400 — proves the
	// device route is registered even on the injected-listener path.
	respWS, err := http.Get("http://" + addr + "/ws")
	if err != nil {
		t.Fatalf("GET /ws: %v", err)
	}
	defer respWS.Body.Close()
	if respWS.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET /ws status = %d, want 400 (failed WS handshake)", respWS.StatusCode)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("srv.Close: %v", err)
	}
	dialErr := make(chan error, 1)
	go func() {
		c, _, derr := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
		if c != nil {
			_ = c.Close()
		}
		dialErr <- derr
	}()
	select {
	case derr := <-dialErr:
		if derr == nil {
			t.Fatal("dial succeeded after srv.Close, want refusal")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for post-Close dial attempt")
	}
}

func TestRegDial_NoRegistry(t *testing.T) {
	t.Parallel()
	// NewAndroidBackend() sets dial = b.regDial with registry nil → Connect
	// must return the "daemon not running" error (do NOT use the test-for
	// constructor, which overrides dial with a fake).
	b := NewAndroidBackend()
	ctx := context.Background()
	err := b.Connect(ctx)
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
