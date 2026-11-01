package mcp

// Mutation-escape kill tests for ws_transport.go (mutate batch m2).
// Each test pins a behavior an escaped mutant breaks; verdicts and evidence are
// in /home/yliu/mutate-mcp2/ledger-mcp2.txt.

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// wsHandshakeCapture records the headers of the first upgrade request.
type wsHandshakeCapture struct {
	mu     sync.Mutex
	header http.Header
}

func (c *wsHandshakeCapture) record(h http.Header) {
	c.mu.Lock()
	if c.header == nil {
		c.header = h
	}
	c.mu.Unlock()
}

func (c *wsHandshakeCapture) cloned() http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.header
}

// mutationWSServer serves the mcp subprotocol over a loopback httptest server
// and hands every upgraded connection to onConn. The handler then keeps the
// connection open until the peer hangs up, so tests control the socket lifetime.
func mutationWSServer(t *testing.T, cap *wsHandshakeCapture, onConn func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cap != nil {
			cap.record(r.Header.Clone())
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		onConn(conn)
		waitForPeerClose(conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// waitForPeerClose parks a served connection until the client closes it.
func waitForPeerClose(c *websocket.Conn) {
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

func mutationWSURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func dialRawWS(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := mutationWSURL(srv)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// closedLoopbackAddr returns a loopback address that refuses connections, so a
// dial failure is deterministic instead of dependent on a guessed port.
func closedLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// deadlineCall pairs a deadline with the instant the socket saw the call, which
// is the anchor for the exact-window assertions below.
type deadlineCall struct {
	at time.Time
	d  time.Time
}

var (
	errSpyWrite = errors.New("spy socket write failure")
	errSpyClose = errors.New("spy socket close failure")
)

// wsSpyConn wraps the socket behind a gorilla client connection so a test can
// read back the deadlines wsConn configures and inject socket failures.
// websocket.Conn.SetWriteDeadline only stores its argument; gorilla applies it
// to the socket inside write(), so the recorded call is the deadline the socket
// would really time out on.
type wsSpyConn struct {
	net.Conn
	mu       sync.Mutex
	writeErr error
	closeErr error
	closes   int
	readDL   chan deadlineCall
	writeDL  chan deadlineCall
}

func (s *wsSpyConn) record(ch chan deadlineCall, d time.Time) {
	select {
	case ch <- deadlineCall{at: time.Now(), d: d}: // REAL-TIME: monotonic anchor for the deadline window
	default:
	}
}

func (s *wsSpyConn) SetReadDeadline(t time.Time) error {
	s.record(s.readDL, t)
	return s.Conn.SetReadDeadline(t)
}

func (s *wsSpyConn) SetWriteDeadline(t time.Time) error {
	s.record(s.writeDL, t)
	return s.Conn.SetWriteDeadline(t)
}

func (s *wsSpyConn) Write(p []byte) (int, error) {
	s.mu.Lock()
	err := s.writeErr
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return s.Conn.Write(p)
}

func (s *wsSpyConn) Close() error {
	s.mu.Lock()
	s.closes++
	closeErr := s.closeErr
	s.mu.Unlock()
	// The socket is always really closed: the fixture's peer must observe the
	// hang-up even when the test injects a close failure.
	connErr := s.Conn.Close()
	if closeErr != nil {
		return closeErr
	}
	return connErr
}

func (s *wsSpyConn) setWriteErr(err error) {
	s.mu.Lock()
	s.writeErr = err
	s.mu.Unlock()
}

func (s *wsSpyConn) setCloseErr(err error) {
	s.mu.Lock()
	s.closeErr = err
	s.mu.Unlock()
}

func (s *wsSpyConn) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

func wsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// newSpyWSConn runs a real RFC6455 client handshake over an in-memory pipe with
// the client socket wrapped in a spy.
func newSpyWSConn(t *testing.T) (*websocket.Conn, *wsSpyConn) {
	t.Helper()
	clientEnd, serverEnd := net.Pipe()
	spy := &wsSpyConn{
		Conn:    clientEnd,
		readDL:  make(chan deadlineCall, 16),
		writeDL: make(chan deadlineCall, 16),
	}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverEnd.Close()
		br := bufio.NewReader(serverEnd)
		key := ""
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Sec-WebSocket-Key") {
				key = strings.TrimSpace(v)
			}
		}
		response := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsAcceptKey(key) + "\r\n\r\n"
		if _, err := io.WriteString(serverEnd, response); err != nil {
			return
		}
		if _, err := io.Copy(io.Discard, serverEnd); err != nil {
			return
		}
	}()
	u, err := url.Parse("ws://127.0.0.1:1/ws")
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	d := websocket.Dialer{
		NetDial:        func(network, addr string) (net.Conn, error) { return spy, nil },
		ReadBufferSize: 4096, WriteBufferSize: 4096,
	}
	conn, resp, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("NewClient: %v (resp=%v)", err, resp)
	}
	t.Cleanup(func() {
		_ = clientEnd.Close()
		<-serverDone
	})
	return conn, spy
}

// nextDeadline waits for the socket to be configured with a deadline and checks
// it against the configured timeout. The code stamps the deadline before the
// socket sees it, so the span can only be shorter than want, never longer.
func nextDeadline(t *testing.T, ch chan deadlineCall, want time.Duration) {
	t.Helper()
	select {
	case call := <-ch:
		delta := call.d.Sub(call.at) // REAL-TIME: monotonic span between the two stamps
		if delta > want || delta < want-100*time.Millisecond {
			t.Fatalf("socket deadline set to %v after the call, want %v", delta, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no socket deadline recorded within 2s (want %v)", want)
	}
}

// drainIncoming waits for readLoop to close the incoming channel, so goleak
// never sees a parked reader after the socket is shut.
func drainIncoming(c *wsConn) {
	for range c.incoming {
	}
}

// ---------------------------------------------------------------------------
// newWSTransport / newWSIDETransport — handshake headers, URL, error wrapping
// ---------------------------------------------------------------------------

// ws_transport.go:46 statement/remove (headers.Set("User-Agent", …) dropped):
// the handshake must carry the client User-Agent alongside config headers.
func TestNewWSTransport_SendsUserAgentAndConfigHeaders(t *testing.T) {
	handshake := &wsHandshakeCapture{}
	srv := mutationWSServer(t, handshake, func(*websocket.Conn) {})

	tr, err := newWSTransport("named", &WSConfig{
		URL:     mutationWSURL(srv),
		Headers: map[string]string{"X-Api-Key": "k1"},
	})
	if err != nil {
		t.Fatalf("newWSTransport: %v", err)
	}
	conn, err := tr.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	h := handshake.cloned()
	if h == nil {
		t.Fatal("server recorded no upgrade request")
	}
	if v := h.Get("User-Agent"); v != "gbot-mcp/1.0" {
		t.Errorf("User-Agent = %q, want %q", v, "gbot-mcp/1.0")
	}
	if v := h.Get("X-Api-Key"); v != "k1" {
		t.Errorf("X-Api-Key = %q, want %q", v, "k1")
	}
}

// ws_transport.go:65 statement/remove (User-Agent Set dropped) and :71
// composite/field-clear (url field cleared in the returned wsTransport): an
// IDE transport must dial the configured URL and present both the User-Agent
// and the authorization header.
func TestNewWSIDETransport_DialsConfiguredURLWithAuth(t *testing.T) {
	handshake := &wsHandshakeCapture{}
	srv := mutationWSServer(t, handshake, func(*websocket.Conn) {})

	tr, err := newWSIDETransport("ide", &WSIDEConfig{URL: mutationWSURL(srv), AuthToken: "tok123"})
	if err != nil {
		t.Fatalf("newWSIDETransport: %v", err)
	}
	conn, err := tr.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v (transport url = %q)", err, tr.(*wsTransport).url)
	}
	defer conn.Close()

	h := handshake.cloned()
	if h == nil {
		t.Fatal("server recorded no upgrade request")
	}
	if v := h.Get("User-Agent"); v != "gbot-mcp/1.0" {
		t.Errorf("User-Agent = %q, want %q", v, "gbot-mcp/1.0")
	}
	if v := h.Get("X-Claude-Code-Ide-Authorization"); v != "tok123" {
		t.Errorf("X-Claude-Code-Ide-Authorization = %q, want %q", v, "tok123")
	}
}

// ws_transport.go:42 expression/errorf-wrap (%w → %v) and :61
// expression/errorf-wrap: the URL validation failure must stay unwrappable —
// callers classify bad configs with errors.As(*url.Error).
func TestNewWSTransport_InvalidURLKeepsParseError(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{"ws", func() error { _, err := newWSTransport("named", &WSConfig{URL: "ws://[::1"}); return err }},
		{"ws-ide", func() error { _, err := newWSIDETransport("ide", &WSIDEConfig{URL: "ws://[::1"}); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("want error for unparseable URL")
			}
			var urlErr *url.Error
			if !errors.As(err, &urlErr) {
				t.Fatalf("error %v does not wrap the url.Parse error", err)
			}
			if urlErr.URL != "ws://[::1" {
				t.Errorf("url.Error.URL = %q, want %q", urlErr.URL, "ws://[::1")
			}
		})
	}
}

// ws_transport.go:94 expression/errorf-wrap (%w → %v): a dial failure must stay
// inspectable — the wrapped *net.OpError is what tells a caller the server was
// unreachable rather than misconfigured.
func TestWSTransport_ConnectDialErrorKeepsNetOpError(t *testing.T) {
	tr := &wsTransport{url: "ws://" + closedLoopbackAddr(t) + "/ws", headers: http.Header{}}
	_, err := tr.Connect(context.Background())
	if err == nil {
		t.Fatal("want dial error for a closed port")
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("error %v does not wrap *net.OpError", err)
	}
	if opErr.Op != "dial" {
		t.Errorf("OpError.Op = %q, want %q", opErr.Op, "dial")
	}
}

// ws_transport.go:84 composite/field-clear (Proxy: http.ProxyFromEnvironment
// dropped from the dialer): a ws:// dial must go through HTTP_PROXY.
//
// net/http reads the proxy environment through a process-wide sync.Once, so the
// dial has to run in a freshly started child of this test binary where
// HTTP_PROXY is set before anything reads it. The proxy answers 502, so the
// dial fails either way — what separates the mutant is whether the proxy saw
// the request at all.
const proxyChildEnv = "GBOT_MCP2_PROXY_CHILD"

func TestWSTransport_DialHonorsProxyFromEnvironment(t *testing.T) {
	if os.Getenv(proxyChildEnv) == "1" {
		tr, err := newWSTransport("named", &WSConfig{URL: os.Getenv("GBOT_MCP2_PROXY_TARGET")})
		if err != nil {
			t.Fatalf("newWSTransport: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := tr.Connect(ctx); err == nil {
			t.Fatal("want dial error through the proxy")
		}
		return
	}

	var seen atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)

	// RFC 5737 TEST-NET-1: never proxied for loopback, so the target has to be
	// off-box. Nothing routes here, which only matters for the mutant — the
	// real dial stops at the proxy.
	target := "ws://192.0.2.1:9/ws"
	child := exec.Command(os.Args[0],
		"-test.run=TestWSTransport_DialHonorsProxyFromEnvironment",
		"-test.timeout=60s",
	)
	child.Env = append(os.Environ(),
		proxyChildEnv+"=1",
		"GBOT_MCP2_PROXY_TARGET="+target,
		"HTTP_PROXY="+proxy.URL,
		"http_proxy="+proxy.URL,
		"NO_PROXY=",
		"no_proxy=",
	)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("proxy child run failed: %v\n%s", err, out)
	}
	if n := seen.Load(); n != 1 {
		t.Errorf("proxy requests = %d, want 1 (the dial bypassed HTTP_PROXY)", n)
	}
}

// ---------------------------------------------------------------------------
// socket timeouts — readDeadline, write deadline, close deadline
// ---------------------------------------------------------------------------

// ws_transport.go:118 numbers/incrementer (60 → 61) and numbers/decrementer
// (60 → 59): the readLoop arms the socket with exactly the documented 60s
// health-check deadline.
func TestWSConn_ReadDeadlineIs60s(t *testing.T) {
	ws, spy := newSpyWSConn(t)
	c := newWSConn(ws)
	defer func() {
		_ = ws.Close()
		drainIncoming(c)
	}()
	nextDeadline(t, spy.readDL, 60*time.Second)
}

// ws_transport.go:230 numbers/incrementer (30 → 31) and numbers/decrementer
// (30 → 29): every frame write is armed with the 30s write deadline.
func TestWSConn_WriteDeadlineIs30s(t *testing.T) {
	ws, spy := newSpyWSConn(t)
	c := newWSConn(ws)
	defer func() {
		_ = ws.Close()
		drainIncoming(c)
	}()
	if err := c.Write(context.Background(), &jsonrpc.Request{Method: "ping"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	nextDeadline(t, spy.writeDL, 30*time.Second)
}

// ws_transport.go:243 numbers/incrementer (5 → 6) and numbers/decrementer
// (5 → 4): the close frame is armed with the 5s write deadline.
func TestWSConn_CloseDeadlineIs5s(t *testing.T) {
	ws, spy := newSpyWSConn(t)
	c := newWSConn(ws)
	defer drainIncoming(c)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	nextDeadline(t, spy.writeDL, 5*time.Second)
}

// ---------------------------------------------------------------------------
// Write failure paths — encode error, socket error
// ---------------------------------------------------------------------------

// ws_transport.go:226 expression/error-guard, :227 branch/if, :227
// statement/return and :227 expression/errorf-wrap: an unencodable message must
// fail the write with the marshal error attached — otherwise an empty frame is
// pushed onto the wire and the caller is told the message was sent.
func TestWSConn_WriteEncodeErrorIsWrapped(t *testing.T) {
	srv := mutationWSServer(t, nil, waitForPeerClose)
	c := newWSConn(dialRawWS(t, srv))
	defer func() {
		_ = c.Close()
		drainIncoming(c)
	}()

	msg := &jsonrpc.Request{Method: "x", Params: json.RawMessage("{")}
	err := c.Write(context.Background(), msg)
	if err == nil {
		t.Fatal("Write: want encode error for invalid params")
	}
	unwrapped := errors.Unwrap(err)
	if unwrapped == nil {
		t.Fatalf("error %v does not wrap the encode error", err)
	}
	_, wantErr := jsonrpc.EncodeMessage(msg)
	if wantErr == nil {
		t.Fatal("fixture: EncodeMessage accepted invalid params")
	}
	if unwrapped.Error() != wantErr.Error() {
		t.Errorf("unwrapped error = %q, want %q", unwrapped.Error(), wantErr.Error())
	}
}

// ws_transport.go:231 expression/error-guard, :232 branch/if, :232
// statement/return and :232 expression/errorf-wrap: a socket write failure must
// be reported, wrapped, and stay unwrappable.
func TestWSConn_WriteSocketErrorIsWrapped(t *testing.T) {
	ws, spy := newSpyWSConn(t)
	spy.setWriteErr(errSpyWrite)
	c := newWSConn(ws)
	defer func() {
		_ = ws.Close()
		drainIncoming(c)
	}()

	err := c.Write(context.Background(), &jsonrpc.Request{Method: "ping"})
	if err == nil {
		t.Fatal("Write: want socket error")
	}
	if !errors.Is(err, errSpyWrite) {
		t.Fatalf("error %v does not wrap the socket failure", err)
	}
	if unwrapped := errors.Unwrap(err); unwrapped != errSpyWrite {
		t.Errorf("unwrapped error = %v, want %v", unwrapped, errSpyWrite)
	}
}

// ws_transport.go:248 statement/remove (err = c.conn.Close() dropped): Close
// must actually shut the socket and surface its error.
func TestWSConn_CloseReturnsSocketCloseError(t *testing.T) {
	ws, spy := newSpyWSConn(t)
	spy.setCloseErr(errSpyClose)
	c := newWSConn(ws)
	defer drainIncoming(c)

	err := c.Close()
	if !errors.Is(err, errSpyClose) {
		t.Fatalf("Close() = %v, want %v", err, errSpyClose)
	}
	if n := spy.closeCount(); n != 1 {
		t.Errorf("socket Close calls = %d, want 1", n)
	}
}

// ws_transport.go:220 select/case-remove (the <-c.closed case in Write's guard)
// and :249 statement/remove (close(c.closed) dropped in Close): a write on a
// closed connection must be reported as io.EOF, not as a socket error on a
// connection that was never really usable.
func TestWSConn_WriteAfterCloseReturnsEOF(t *testing.T) {
	ws, _ := newSpyWSConn(t)
	c := newWSConn(ws)
	defer drainIncoming(c)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := c.Write(context.Background(), &jsonrpc.Request{Method: "ping"})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Write after Close = %v, want io.EOF", err)
	}
}

// ---------------------------------------------------------------------------
// readLoop — the <-c.closed escape hatches
// ---------------------------------------------------------------------------

// ws_transport.go:159, :173 and :180 select/case-remove: with the incoming
// buffer full, only the <-c.closed case can release a blocked readLoop send.
// Without it the reader parks forever and incoming is never closed.
func TestWSConn_ReadLoopExitsWhenClosedWhileSendBlocked(t *testing.T) {
	cases := []struct {
		name  string
		serve func(*websocket.Conn)
	}{
		// Server hangs up: the next client read fails.
		{"read-error", func(c *websocket.Conn) { _ = c.Close() }},
		// Undecodable frame: the decode branch reports the failure.
		{"decode-error", func(c *websocket.Conn) {
			if err := c.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
				return
			}
		}},
		// Valid frame: the delivery branch forwards it.
		{"message", func(c *websocket.Conn) {
			if err := c.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
				return
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			served := make(chan struct{})
			srv := mutationWSServer(t, nil, func(c *websocket.Conn) {
				tc.serve(c)
				close(served)
				waitForPeerClose(c)
			})
			raw := dialRawWS(t, srv)

			c := &wsConn{conn: raw, closed: make(chan struct{}), incoming: make(chan incomingMsg, 64)}
			for range 64 {
				c.incoming <- incomingMsg{}
			}
			readLoopDone := make(chan struct{})
			go func() {
				c.readLoop()
				close(readLoopDone)
			}()
			<-served
			close(c.closed)

			// The buffer is full and nobody is reading, so the only case that can
			// release the readLoop's send is <-c.closed. Without it the reader
			// parks and incoming is never closed.
			select {
			case <-readLoopDone:
			case <-time.After(2 * time.Second):
				t.Fatal("readLoop is still blocked on the incoming send after the connection was closed")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Read — the closed-connection and closed-channel exits
// ---------------------------------------------------------------------------

// ws_transport.go:206 select/case-remove (the <-c.closed case in Read): a Read
// on a closed connection must return io.EOF instead of parking forever.
func TestWSConn_ReadReturnsEOFWhenConnClosed(t *testing.T) {
	srv := mutationWSServer(t, nil, waitForPeerClose)
	c := &wsConn{conn: dialRawWS(t, srv), closed: make(chan struct{}), incoming: make(chan incomingMsg, 64)}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	type outcome struct {
		msg jsonrpc.Message
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		m, err := c.Read(context.Background())
		done <- outcome{m, err}
	}()
	select {
	case o := <-done:
		if o.err != io.EOF {
			t.Errorf("Read error = %v, want io.EOF", o.err)
		}
		if o.msg != nil {
			t.Errorf("Read message = %v, want nil", o.msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read blocked after Close: Read's select lost the <-c.closed case")
	}
}

// ws_transport.go:210 branch/if and :210 statement/return: when readLoop has
// finished and closed the incoming channel, Read must report io.EOF rather than
// a nil message with a nil error.
func TestWSConn_ReadReturnsEOFWhenIncomingClosed(t *testing.T) {
	srv := mutationWSServer(t, nil, waitForPeerClose)
	c := &wsConn{conn: dialRawWS(t, srv), closed: make(chan struct{}), incoming: make(chan incomingMsg, 64)}
	close(c.incoming)

	msg, err := c.Read(context.Background())
	if err != io.EOF {
		t.Errorf("Read error = %v, want io.EOF", err)
	}
	if msg != nil {
		t.Errorf("Read message = %v, want nil", msg)
	}
}

// ws_transport.go:172 expression/errorf-wrap (%w → %v): the decode failure must
// stay unwrappable so callers can tell a protocol violation from a transport
// failure.
func TestWSConn_DecodeErrorKeepsJSONRPCError(t *testing.T) {
	srv := mutationWSServer(t, nil, func(c *websocket.Conn) {
		if err := c.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
			return
		}
		waitForPeerClose(c)
	})
	c := newWSConn(dialRawWS(t, srv))
	defer func() {
		_ = c.Close()
		drainIncoming(c)
	}()

	_, err := c.Read(context.Background())
	if err == nil {
		t.Fatal("Read: want decode error for a non-JSON frame")
	}
	unwrapped := errors.Unwrap(err)
	if unwrapped == nil {
		t.Fatalf("Read error %v does not wrap the decode error", err)
	}
	_, wantErr := jsonrpc.DecodeMessage([]byte("not json"))
	if wantErr == nil {
		t.Fatal("fixture: DecodeMessage accepted a non-JSON frame")
	}
	if unwrapped.Error() != wantErr.Error() {
		t.Errorf("unwrapped error = %q, want %q", unwrapped.Error(), wantErr.Error())
	}
}
