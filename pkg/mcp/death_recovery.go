package mcp

import (
	"errors"
	"io"
	"syscall"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// IsConnectionDeadError reports whether err means the MCP connection (and
// with it the server channel) is dead. Three shapes:
//   - ErrConnectionClosed: jsonrpc2 marked the session closing (transport.go:227)
//   - io.EOF / EPIPE: a stdio pipe broke before the session was marked
//   - ECONNREFUSED: a remote (streamable HTTP) server process is gone —
//     the SDK's request-driven client only discovers this on the next call
//     (Wait does not fire for remote death)
//   - ErrSessionMissing: the remote server restarted on the same address
//     and no longer recognizes the session — the connection state is dead
func IsConnectionDeadError(err error) bool {
	return errors.Is(err, mcpsdk.ErrConnectionClosed) ||
		errors.Is(err, mcpsdk.ErrSessionMissing) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

// EvictIfSame removes the stored connection for name only if it is still cs.
// Identity matching guarantees a stale watcher/retry never evicts a connection
// that has already been replaced by a live one.
func (r *Registry) EvictIfSame(name string, cs *ConnectedServer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connections[name] != ServerConnection(cs) {
		return false
	}
	delete(r.connections, name)
	r.rebuildAggregatesLocked()
	go func() { _ = cs.Close() }()
	return true
}

// adoptConnection stores a successful connection. No death watcher: the call
// path classifies dead-connection errors (IsConnectionDeadError) and performs
// evict → reconnect → retry itself, and the config-driven tool table keeps
// the tool callable so that path is always reachable. A watcher added no
// recovery the call path doesn't already provide, and its proactive eviction
// is what exposed the tool-table coupling.
//
// cfg may be nil (stores connection only); when non-nil it is also recorded
// in r.configs, preserving the two store sites that wrote configs inline.
// SetConnectionForTest intentionally bypasses this.
func (r *Registry) adoptConnection(name string, conn ServerConnection, cfg *ScopedMcpServerConfig) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.connections[name] = conn
	if cfg != nil {
		r.configs[name] = *cfg
	}
	r.mu.Unlock()
}
