// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: custom WebSocket transport via gorilla/websocket.
// Source: utils/mcpWebSocketTransport.ts (WebSocketTransport class)
package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// ---------------------------------------------------------------------------
// wsTransport — Source: utils/mcpWebSocketTransport.ts
//
// Implements mcp.Transport via gorilla/websocket. The TS version wraps an
// existing WebSocket instance; the Go version creates the connection in
// Connect() (matching the go-sdk Transport interface contract).
// ---------------------------------------------------------------------------

// wsTransport is a Transport that communicates over WebSocket using
// JSON-RPC text frames.
//
// Source: mcpWebSocketTransport.ts:22 — class WebSocketTransport
type wsTransport struct {
	url     string
	headers http.Header
}

// newWSTransport creates a wsTransport for the given URL and config headers.
// Source: client.ts:735-783 — WS transport creation
func newWSTransport(name string, cfg *WSConfig) (mcp.Transport, error) {
	if err := validateRemoteURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp: server %q: %w", name, err)
	}

	headers := http.Header{}
	headers.Set("User-Agent", "gbot-mcp/1.0")
	for k, v := range cfg.Headers {
		headers.Set(k, v)
	}

	return &wsTransport{
		url:     cfg.URL,
		headers: headers,
	}, nil
}

// newWSIDETransport creates a wsTransport for WS-IDE servers with auth token.
// Source: client.ts:708-734 — WS-IDE transport creation
func newWSIDETransport(name string, cfg *WSIDEConfig) (mcp.Transport, error) {
	if err := validateRemoteURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp: server %q: %w", name, err)
	}

	headers := http.Header{}
	headers.Set("User-Agent", "gbot-mcp/1.0")
	if cfg.AuthToken != "" {
		headers.Set("X-Claude-Code-Ide-Authorization", cfg.AuthToken)
	}

	return &wsTransport{
		url:     cfg.URL,
		headers: headers,
	}, nil
}

// Connect dials the WebSocket server and returns a Connection.
// Source: mcpWebSocketTransport.ts:142-154 — start()
//
// Uses the 'mcp' subprotocol as required by the MCP spec.
// TLS and proxy support come from gorilla/websocket's default dialer.
func (t *wsTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	dialer := websocket.Dialer{
		Subprotocols:    []string{"mcp"},
		Proxy:           http.ProxyFromEnvironment,
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
	}

	conn, resp, err := dialer.DialContext(ctx, t.url, t.headers)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("mcp: websocket dial %q: %w", t.url, err)
	}

	// Verify subprotocol negotiation
	if conn.Subprotocol() != "mcp" {
		_ = conn.Close()
		return nil, fmt.Errorf("mcp: server did not negotiate 'mcp' subprotocol, got %q", conn.Subprotocol())
	}

	return newWSConn(conn), nil
}

// ---------------------------------------------------------------------------
// wsConn — implements mcp.Connection
// Source: mcpWebSocketTransport.ts:22-200 — WebSocketTransport
//
// Read/Write use gorilla/websocket text frames with JSON-RPC encoding.
// Read is blocking, so we run it in a goroutine and deliver via channel
// to support context cancellation.
// ---------------------------------------------------------------------------

// readDeadline controls the read timeout for WebSocket reads.
// 60s balances between allowing slow servers and detecting dead connections.
// Source: mcpWebSocketTransport.ts — no explicit timeout in TS (relies on TCP keepalive).
const readDeadline = 60 * time.Second

type wsConn struct {
	conn *websocket.Conn

	closeOnce sync.Once
	closed    chan struct{}

	// incoming delivers messages read by the background reader goroutine.
	incoming chan incomingMsg
}

// incomingMsg is a message or error from the background read goroutine.
type incomingMsg struct {
	msg jsonrpc.Message
	err error
}

func newWSConn(conn *websocket.Conn) *wsConn {
	c := &wsConn{
		conn:     conn,
		closed:   make(chan struct{}),
		incoming: make(chan incomingMsg, 64),
	}
	go c.readLoop()
	return c
}

// readLoop runs in a background goroutine, reading messages and sending
// them on the incoming channel. Exits when the connection is closed or
// an error occurs.
func (c *wsConn) readLoop() {
	defer close(c.incoming)
	for {
		// Set read deadline for health check
		_ = c.conn.SetReadDeadline(time.Now().Add(readDeadline))

		msgType, data, err := c.conn.ReadMessage()
		if err != nil {
			select {
			case c.incoming <- incomingMsg{err: c.wrapReadErr(err)}:
			case <-c.closed:
			}
			return
		}

		// Ignore non-text frames (binary, ping, etc.)
		if msgType != websocket.TextMessage {
			continue
		}

		msg, err := jsonrpc.DecodeMessage(data)
		if err != nil {
			select {
			case c.incoming <- incomingMsg{err: fmt.Errorf("mcp: websocket decode: %w", err)}:
			case <-c.closed:
			}
			return
		}

		select {
		case c.incoming <- incomingMsg{msg: msg}:
		case <-c.closed:
			return
		}
	}
}

// wrapReadErr converts WebSocket close errors to io.EOF.
func (c *wsConn) wrapReadErr(err error) error {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return io.EOF
	}
	if websocket.IsUnexpectedCloseError(err) {
		return io.EOF
	}
	return err
}

// SessionID returns empty string — WebSocket transport has no session ID.
func (c *wsConn) SessionID() string { return "" }

// Read reads the next JSON-RPC message from the WebSocket.
// Source: mcpWebSocketTransport.ts:98-106 — onNodeMessage
func (c *wsConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, io.EOF
	case m, ok := <-c.incoming:
		if !ok {
			return nil, io.EOF
		}
		return m.msg, m.err
	}
}

// Write writes a JSON-RPC message to the WebSocket as a text frame.
// Source: mcpWebSocketTransport.ts:173-199 — send()
func (c *wsConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	select {
	case <-c.closed:
		return io.EOF
	default:
	}

	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return fmt.Errorf("mcp: websocket encode: %w", err)
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("mcp: websocket write: %w", err)
	}
	return nil
}

// Close sends a normal close frame and cleans up.
// Source: mcpWebSocketTransport.ts:159-168 — close()
func (c *wsConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Send close frame with normal closure
		_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		err = c.conn.Close()
		close(c.closed)
	})
	return err
}
