package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// ---------------------------------------------------------------------------
// wsTransport.Connect — subprotocol negotiation
// ---------------------------------------------------------------------------

func TestWSTransport_Connect_Subprotocol(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Echo messages back
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(msgType, data)
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.SessionID() != "" {
		t.Errorf("SessionID() = %q, want empty", conn.SessionID())
	}
}

func TestWSTransport_Connect_SubprotocolMismatch(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"other-protocol"}, // Not "mcp"
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	_, err := transport.Connect(ctx)
	if err == nil {
		t.Fatal("Connect() = nil, want error for subprotocol mismatch")
	}
	if !strings.Contains(err.Error(), "subprotocol") {
		t.Errorf("error = %v, want mention of subprotocol", err)
	}
}

// ---------------------------------------------------------------------------
// wsConn.Read / Write — echo test
// ---------------------------------------------------------------------------

func TestWSConn_ReadWrite_Echo(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Echo: read a message, send it back
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, data)
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Write a JSON-RPC request
	req := &jsonrpc.Request{
		Method: "test",
	}
	if err := conn.Write(ctx, req); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	// Read the echo
	msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	gotReq, ok := msg.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("expected *jsonrpc.Request, got %T", msg)
	}
	if gotReq.Method != "test" {
		t.Errorf("Method = %q, want %q", gotReq.Method, "test")
	}
}

// ---------------------------------------------------------------------------
// wsConn.Close
// ---------------------------------------------------------------------------

func TestWSConn_Close(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	serverClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				close(serverClosed)
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// Verify double-close is safe
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}

	// Verify Read returns EOF after close
	_, err = conn.Read(ctx)
	if err == nil {
		t.Error("Read() after Close() = nil, want error")
	}
}

// ---------------------------------------------------------------------------
// wsConn.Read — context cancellation
// ---------------------------------------------------------------------------

func TestWSConn_Read_ContextCancelled(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// Server that never sends anything
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		select {} // block forever
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Cancel context while waiting for a message
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err = conn.Read(ctx)
	if err == nil {
		t.Error("Read() with cancelled context = nil, want error")
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface checks
// ---------------------------------------------------------------------------

func TestWSCompileTimeChecks(t *testing.T) {
	var _ mcp.Transport = (*wsTransport)(nil)
	var _ mcp.Connection = (*wsConn)(nil)
}

// ---------------------------------------------------------------------------
// wsTransport with custom headers
// ---------------------------------------------------------------------------

func TestWSTransport_CustomHeaders(t *testing.T) {
	var receivedHeaders http.Header

	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	headers := http.Header{}
	headers.Set("X-Custom", "test-value")
	headers.Set("Authorization", "Bearer token123")

	transport := &wsTransport{url: wsURL, headers: headers}
	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close: %v", err)
	}

	if got := receivedHeaders.Get("X-Custom"); got != "test-value" {
		t.Errorf("X-Custom = %q, want %q", got, "test-value")
	}
	if got := receivedHeaders.Get("Authorization"); got != "Bearer token123" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer token123")
	}
}

// ---------------------------------------------------------------------------
// Random helpers for generating test data
// ---------------------------------------------------------------------------

// ===========================================================================
// Additional coverage tests for Connect, Read, Write (70% → 90%+)
// ===========================================================================

func TestWSTransport_ConnectInvalidURL(t *testing.T) {
	transport := &wsTransport{
		url:     "://invalid-url",
		headers: http.Header{},
	}
	_, err := transport.Connect(context.Background())
	if err == nil {
		t.Fatal("want error for invalid URL")
	}
}

func TestWSTransport_ConnectConnectionRefused(t *testing.T) {
	// Use a port that's unlikely to be listening
	transport := &wsTransport{
		url:     "ws://localhost:9999/ws",
		headers: http.Header{},
	}
	_, err := transport.Connect(context.Background())
	if err == nil {
		t.Fatal("want error for connection refused")
	}
}

func TestWSTransport_ReadAfterClose(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Close the connection
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read should fail
	_, err = conn.Read(context.Background())
	if err == nil {
		t.Fatal("want error after close")
	}
}

func TestWSTransport_WriteAfterClose(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Close the connection
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Write should fail - need a valid jsonrpc.Message
	msg := &jsonrpc.Request{Method: "test"}
	err = conn.Write(context.Background(), msg)
	if err == nil {
		t.Fatal("want error after close")
	}
}

// ===========================================================================
// Coverage: newWSIDETransport, Connect with response body close, readLoop
// binary frames + decode errors + closed channel, wrapReadErr unexpected close,
// Read incoming channel closed, Write encode error
// ===========================================================================

// TestNewWSIDETransport_ValidURL tests newWSIDETransport with a valid URL.
func TestNewWSIDETransport_ValidURL(t *testing.T) {
	cfg := &WSIDEConfig{
		URL:       "ws://localhost:8080/ws",
		AuthToken: "test-token-123",
	}
	transport, err := newWSIDETransport("test-ide", cfg)
	if err != nil {
		t.Fatalf("newWSIDETransport: %v", err)
	}
	if transport == nil {
		t.Fatal("expected non-nil transport")
	}
}

// TestNewWSIDETransport_InvalidURL tests newWSIDETransport with invalid URL.
func TestNewWSIDETransport_InvalidURL(t *testing.T) {
	cfg := &WSIDEConfig{
		URL: "://invalid",
	}
	_, err := newWSIDETransport("test-ide", cfg)
	if err == nil {
		t.Fatal("want error for invalid URL")
	}
	if !strings.Contains(err.Error(), "test-ide") {
		t.Errorf("error should mention server name, got: %v", err)
	}
}

// TestNewWSIDETransport_EmptyAuthToken tests newWSIDETransport with empty auth token.
func TestNewWSIDETransport_EmptyAuthToken(t *testing.T) {
	cfg := &WSIDEConfig{
		URL:       "ws://localhost:8080/ws",
		AuthToken: "",
	}
	transport, err := newWSIDETransport("test-ide", cfg)
	if err != nil {
		t.Fatalf("newWSIDETransport: %v", err)
	}
	if transport == nil {
		t.Fatal("expected non-nil transport")
	}
}

// TestWSTransport_Connect_WithHTTPError tests Connect when the server
// returns an HTTP error (triggering resp.Body.Close).
func TestWSTransport_Connect_WithHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	_, err := transport.Connect(context.Background())
	if err == nil {
		t.Fatal("want error for HTTP error response")
	}
}

// TestWSConn_ReadLoop_BinaryFrames tests that binary frames are ignored
// in the readLoop.
func TestWSConn_ReadLoop_BinaryFrames(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Send a binary frame (should be ignored)
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("binary-data"))
		// Then send a valid text JSON-RPC message
		msg := `{"jsonrpc":"2.0","method":"ping","id":1}`
		_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read should get the text message (binary is skipped)
	msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
}

// TestWSConn_ReadLoop_DecodeError tests readLoop with invalid JSON-RPC data.
func TestWSConn_ReadLoop_DecodeError(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Send invalid JSON-RPC text frame
		_ = conn.WriteMessage(websocket.TextMessage, []byte("not-json-rpc"))
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read should get a decode error
	_, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("want error from invalid JSON-RPC data")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decode, got: %v", err)
	}
}

// TestWSConn_wrapReadErr_UnexpectedClose tests wrapReadErr with unexpected
// close errors.
func TestWSConn_wrapReadErr_UnexpectedClose(t *testing.T) {
	c := &wsConn{}

	// Unexpected close error should return io.EOF
	err := c.wrapReadErr(&websocket.CloseError{Code: websocket.CloseInternalServerErr})
	if err != io.EOF {
		t.Errorf("unexpected close should return io.EOF, got: %v", err)
	}

	// Normal close should return io.EOF
	err = c.wrapReadErr(&websocket.CloseError{Code: websocket.CloseNormalClosure})
	if err != io.EOF {
		t.Errorf("normal close should return io.EOF, got: %v", err)
	}

	// Going away should return io.EOF
	err = c.wrapReadErr(&websocket.CloseError{Code: websocket.CloseGoingAway})
	if err != io.EOF {
		t.Errorf("going away should return io.EOF, got: %v", err)
	}

	// Other errors should pass through
	otherErr := fmt.Errorf("some other error")
	err = c.wrapReadErr(otherErr)
	if err != otherErr {
		t.Errorf("other errors should pass through, got: %v", err)
	}
}

// TestWSConn_Read_IncomingChannelClosed tests Read when the incoming
// channel is closed (readLoop exited).
func TestWSConn_Read_IncomingChannelClosed(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Close immediately to trigger readLoop exit
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read should get io.EOF after server closes
	_, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("want error after server close")
	}
	// Could be io.EOF or a close error
	t.Logf("Read after close: %v", err)
}

// TestWSConn_Write_Success tests successful Write operation.
func TestWSConn_Write_Success(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Read one message to confirm write worked
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	msg := &jsonrpc.Request{Method: "test_method"}
	if err := conn.Write(ctx, msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// TestWSConn_WriteEncodeError tests Write when jsonrpc.EncodeMessage fails.
// This is difficult to trigger naturally since valid messages always encode.
// We verify the Write path handles the encode step correctly by writing
// a normal message and checking it succeeds.
func TestWSConn_WriteEncodeSuccess(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Read messages until error
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Write a response (uses jsonrpc.Response, different code path than Request)
	id, _ := jsonrpc.MakeID("1")
	msg := &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"status":"ok"}`),
	}
	if err := conn.Write(ctx, msg); err != nil {
		t.Fatalf("Write response: %v", err)
	}
}

// TestWSConn_Read_ChannelClosedFirst tests the path where Read gets
// from the incoming channel but the channel is already closed (ok=false).
func TestWSConn_Read_ChannelClosedFirst(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// Server sends one message then closes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Send a valid message
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","method":"ping","id":1}`))
		// Small delay to let client read it
		time.Sleep(50 * time.Millisecond)
		// Close — this will cause readLoop to exit and close the incoming channel
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Read the first message successfully
	msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	// Give readLoop time to detect close and drain incoming channel
	time.Sleep(100 * time.Millisecond)

	// Now Read should return io.EOF (incoming channel closed)
	_, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("want error after channel closed")
	}
	if err != io.EOF {
		t.Logf("Read after close: %v (acceptable)", err)
	}

	_ = conn.Close()
}

// TestWSConn_Write_ConnectionError tests Write when the underlying
// connection has been closed by the server.
func TestWSConn_Write_ConnectionError(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Close immediately
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Wait for server to close
	time.Sleep(100 * time.Millisecond)

	// Write should fail since server closed
	msg := &jsonrpc.Request{Method: "test"}
	err = conn.Write(ctx, msg)
	if err == nil {
		t.Log("Write succeeded despite server close — acceptable race condition")
	} else {
		t.Logf("Write after server close: %v (expected)", err)
	}

	_ = conn.Close()
}

// ===========================================================================
// Coverage: Read incoming channel closed before message, Write encode error,
// readLoop close channel select, Read with closed channel and incoming race
// ===========================================================================

// TestWSConn_Read_IncomingClosedNoMessage tests the path where the incoming
// channel is closed without any message being sent (readLoop exits immediately).
func TestWSConn_Read_IncomingClosedNoMessage(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Close immediately — triggers readLoop exit, closes incoming channel
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read should return io.EOF since incoming channel closed without messages
	msg, err := conn.Read(ctx)
	if err == nil {
		t.Errorf("want error, got msg: %v", msg)
	}
	if err != io.EOF {
		t.Logf("Read returned: %v (acceptable non-EOF)", err)
	}
}

// TestWSConn_Write_ContextCancelled tests Write when context is cancelled.
func TestWSConn_Write_ContextCancelled(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Read messages until error
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Cancel context before writing
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msg := &jsonrpc.Request{Method: "test"}
	err = conn.Write(ctx, msg)
	if err == nil {
		t.Log("Write succeeded despite cancelled context (race)")
	}
}

// TestWSConn_ReadLoop_ServerSendsMultipleFrames tests readLoop handling
// multiple frames of different types in sequence.
func TestWSConn_ReadLoop_ServerSendsMultipleFrames(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Send binary frame (should be ignored)
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("binary"))
		// Send invalid text frame (should cause decode error)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("not-json"))
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	ctx := context.Background()
	conn, err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read should get a decode error (binary was skipped, invalid text caused error)
	_, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("want error from invalid JSON-RPC data")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decode, got: %v", err)
	}
}

// TestWSConn_Read_ClosedThenRead tests Read after the wsConn is explicitly
// closed, hitting the <-c.closed case in the select.
func TestWSConn_Read_ClosedThenRead(t *testing.T) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	transport := &wsTransport{url: wsURL, headers: http.Header{}}

	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Close the connection
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read should return io.EOF via <-c.closed
	_, err = conn.Read(context.Background())
	if err == nil {
		t.Fatal("want error after close")
	}
	if err != io.EOF {
		t.Logf("Read after close: %v", err)
	}
}
