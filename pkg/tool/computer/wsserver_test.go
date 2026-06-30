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

// dialWSClient connects a gorilla client to the given ws:// URL, reads a
// single rpcRequest, builds a reply via respFn (echoing the id), and returns
// the captured request + the client conn. The caller closes the conn.
func dialWSClient(t *testing.T, url string, respFn func(req rpcRequest) rpcResponse) (*websocket.Conn, rpcRequest) {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	var req rpcRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("client unmarshal: %v", err)
	}
	resp := respFn(req)
	resp.ID = req.ID
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := c.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("client write: %v", err)
	}
	return c, req
}

// dialWSEmpty connects a gorilla client that sends one request and reads
// nothing (used for route-by-IP tests that only assert registry population).
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

// dialAcceptConn returns a *websocket.Conn connected to the test server, used
// to feed an accepted conn into the registry directly.
func dialAcceptConn(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// makeAcceptConnServer starts an httptest WS server that upgrades any conn
// and returns the accepted *websocket.Conn on a channel so the test can wrap
// it in a *deviceClient and Register it. The server keeps the conn open for
// the test's lifetime.
func makeAcceptConnServer(t *testing.T) (string, chan *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
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
		// Hold the conn open until the test closes it; read-loop-ish block.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
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

func TestConnectionRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	url, accepted := makeAcceptConnServer(t)
	// Dial from a gorilla client to obtain a server-accepted *websocket.Conn.
	go dialAcceptConn(t, url)
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
		go dialAcceptConn(t, url)
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
	reg := NewConnectionRegistry()
	for i := 0; i < 2; i++ {
		go dialAcceptConn(t, url)
		select {
		case ws := <-accepted:
			reg.Register(hostKey(i), ws)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for accepted conn %d", i)
		}
	}
	reg.Close()
	for i := 0; i < 2; i++ {
		c, ok := reg.Get(hostKey(i))
		if !ok {
			t.Fatalf("Get(%q) ok = false after Close", hostKey(i))
		}
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("127.0.0.1"); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
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
	go dialWSClient(t, "ws://"+addr+"/ws", func(req rpcRequest) rpcResponse {
		return rpcResponse{Success: true, Data: json.RawMessage(`{}`)}
	})
	// Allow the second conn to register (also under 127.0.0.1, replacing the
	// first). Then call() through it.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cur, ok := reg.Get("127.0.0.1")
		if ok && cur != c {
			c = cur
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
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
