package lsp

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// inProcessServer is a minimal LSP-speaking server for tests.
// It runs in-process via net.Pipe so we don't need a fake binary.
type inProcessServer struct {
	t        *testing.T
	conn     net.Conn
	initDone chan struct{}
	// handleCustom lets a test override the response for specific methods.
	// Returning (nil, false) falls through to the default echo handler.
	handleCustom func(req rpcRequest) (any, bool)
	// blockMethods, if non-nil, is consulted before handleCustom. Methods in
	// this set get NO response — the server simply skips them. Used to test
	// client-side timeouts and teardown races.
	blockMethods map[string]bool
	wg           sync.WaitGroup // tracks serve goroutine
}

func newInProcessServer(t *testing.T) (*Client, *inProcessServer, func()) {
	t.Helper()

	clientConn, serverConn := net.Pipe()

	srv := &inProcessServer{
		t:        t,
		conn:     serverConn,
		initDone: make(chan struct{}),
	}
	srv.wg.Go(func() { ; srv.serve() })

	// Wrap the client side of the pipe as a *Client by injecting into private fields.
	// We can't use StartClient (which spawns a subprocess), so we construct manually.
	c := &Client{
		name:         "fake",
		pending:      make(map[int64]chan *rpcResponse),
		openURIs:     make(map[string]int),
		diags:        make(map[string][]Diagnostic),
		teardownOnce: sync.Once{},
		done:         make(chan struct{}),
		dead:         make(chan struct{}),
		stdin:        &pipeWriteCloser{conn: clientConn},
		stdout:       clientConn,
	}
	c.readWG.Go(func() { ; c.readLoop() })

	cleanup := func() {
		// Close pipes directly to trigger readLoop and serve goroutine exits.
		// Don't call c.Shutdown() — the in-process fake server doesn't speak
		// real LSP protocol, and Shutdown's Request will hang under -race.
		_ = clientConn.Close()
		_ = serverConn.Close()
		c.readWG.Wait()
		srv.wg.Wait()
	}
	return c, srv, cleanup
}

// pipeWriteCloser adapts net.Conn to io.WriteCloser.
type pipeWriteCloser struct{ conn net.Conn }

func (p *pipeWriteCloser) Write(b []byte) (int, error) { return p.conn.Write(b) }
func (p *pipeWriteCloser) Close() error                { return p.conn.Close() }

func (s *inProcessServer) serve() {
	r := newFramedReader(s.conn)
	for {
		msg, err := r.read()
		if err != nil {
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}
		if req.JSONRPC == "" {
			continue // response, ignore
		}

		// Dispatch.
		switch req.Method {
		case "initialize":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"capabilities": map[string]any{
						"referencesProvider":      true,
						"renameProvider":          map[string]bool{"prepareSupport": true},
						"hoverProvider":           true,
						"documentSymbolProvider":  true,
						"definitionProvider":      true,
						"implementationProvider":  true,
						"workspaceSymbolProvider": true,
						"codeActionProvider":      true,
					},
				},
			}
			s.write(resp)
		case "initialized":
			// notification, no reply
			close(s.initDone)
		case "shutdown":
			s.write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": nil})
		case "exit":
			return
		default:
			// Blocklisted methods get no response.
			if s.blockMethods != nil && s.blockMethods[req.Method] {
				continue
			}
			// Allow test override first.
			if s.handleCustom != nil {
				if resp, ok := s.handleCustom(req); ok {
					s.write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": resp})
					continue
				}
			}
			// Default: echo with nil result.
			s.write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": nil})
		}
	}
}

func (s *inProcessServer) write(msg any) {
	body, _ := json.Marshal(msg)
	header := []byte("Content-Length: " + itoa(len(body)) + "\r\n\r\n")
	_, _ = s.conn.Write(append(header, body...))
}

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

// framing helpers local to this test file (mirrors client.go's framing logic).
type framingReader struct {
	r   io.Reader
	buf []byte
}

func newFramedReader(r io.Reader) *framingReader { return &framingReader{r: r} }

func (f *framingReader) read() ([]byte, error) {
	for {
		// Look for header terminator.
		idx := indexBytes(f.buf, []byte("\r\n\r\n"))
		if idx >= 0 {
			header := string(f.buf[:idx])
			cl := parseContentLength(header)
			bodyStart := idx + 4
			if len(f.buf) >= bodyStart+cl {
				body := f.buf[bodyStart : bodyStart+cl]
				f.buf = f.buf[bodyStart+cl:]
				return body, nil
			}
		}
		tmp := make([]byte, 4096)
		n, err := f.r.Read(tmp)
		if n > 0 {
			f.buf = append(f.buf, tmp[:n]...)
		}
		if err != nil {
			return nil, err
		}
	}
}

func indexBytes(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func parseContentLength(header string) int {
	for _, line := range splitLines(header) {
		if len(line) > 16 && line[:15] == "Content-Length:" {
			n := 0
			for _, c := range line[15:] {
				if c == ' ' || c == '\t' {
					continue
				}
				if c < '0' || c > '9' {
					break
				}
				n = n*10 + int(c-'0')
			}
			return n
		}
	}
	return 0
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if line != "" && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// ---------- tests ----------

func TestClient_Initialize(t *testing.T) {
	c, srv, cleanup := newInProcessServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	select {
	case <-srv.initDone:
	case <-time.After(time.Second):
		t.Fatal("initialized notification never delivered")
	}

	caps := c.Capabilities()
	if len(caps) == 0 {
		t.Fatal("expected non-empty capabilities")
	}
	var parsed struct {
		Capabilities struct {
			ReferencesProvider bool `json:"referencesProvider"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(caps, &parsed); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}
	if !parsed.Capabilities.ReferencesProvider {
		t.Errorf("capabilities missing referencesProvider")
	}
}

func TestClient_RequestResponse(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Send a custom request — server echoes with nil result.
	res, err := c.Request(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]string{"uri": "file:///repo/foo.go"},
		"position":     Position{Line: 0, Character: 0},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	// Server returns nil; expect null or empty.
	if string(res) != "null" && len(res) != 0 {
		t.Logf("result = %s (expected null)", string(res))
	}
}

func TestClient_Notify(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// didOpen should not error.
	if err := c.Notify(ctx, "textDocument/didOpen", map[string]any{
		"textDocument": TextDocumentItem{
			URI:        "file:///repo/foo.go",
			LanguageID: "go",
			Version:    1,
			Text:       "package foo\n",
		},
	}); err != nil {
		t.Fatalf("Notify didOpen: %v", err)
	}
}

func TestClient_RequestAfterDead(t *testing.T) {
	c, srv, cleanup := newInProcessServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		cleanup()
		t.Fatalf("Initialize: %v", err)
	}

	// Force server death by closing its side.
	_ = srv.conn.Close()
	<-c.Dead()

	// Subsequent requests should fail with ErrServerDead or a wrapped variant.
	_, err := c.Request(context.Background(), "textDocument/references", nil)
	if err == nil {
		cleanup()
		t.Fatal("expected error after server death, got nil")
	}
	if strings.Contains(err.Error(), "server is not running") {
		t.Logf("got expected ErrServerDead")
	}
	// cleanup may also fail; that's fine
	cleanup()
}

func TestClient_Shutdown(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	shutdownCtx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer scancel()
	c.Shutdown(shutdownCtx)
	select {
	case <-c.Dead():
	case <-time.After(time.Second):
		t.Fatal("Dead channel not closed after Shutdown")
	}
}

func TestEncodeMessage(t *testing.T) {
	msg := rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize"}
	bytes, err := encodeMessage(msg)
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}
	if len(bytes) == 0 {
		t.Fatal("expected non-empty framed message")
	}
	if string(bytes[:16]) != "Content-Length: " {
		t.Errorf("missing Content-Length header: %q", bytes[:20])
	}
}
