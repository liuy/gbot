package computer

import (
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// DefaultWSPort is the single source of truth for the daemon's WS listen
// port and the connect action's port default. The GBot app dials this port.
const DefaultWSPort = 8765

// ConnectionRegistry holds dpClients built from server-accepted WebSockets,
// keyed by the peer's source IP. The daemon's upgrade handler populates it;
// AndroidBackend.Connect (via regDial) reads from it. nil registry == TUI
// mode (no server) and Connect returns a clear "daemon not running" error.
type ConnectionRegistry struct {
	mu      sync.RWMutex
	clients map[string]*dpClient // host (RemoteIP host portion) → client
}

// NewConnectionRegistry returns an empty registry.
func NewConnectionRegistry() *ConnectionRegistry {
	return &ConnectionRegistry{clients: make(map[string]*dpClient)}
}

// Register wraps an accepted *websocket.Conn in a *dpClient (starting its
// readLoop), stores it under host (replacing any prior conn for that host —
// the old one is closed), and returns the new client. Closing the old client
// happens outside the registry lock: old.close() only touches its own
// dpClient.mu, so there is no lock-ordering conflict with the registry lock.
func (r *ConnectionRegistry) Register(host string, ws *websocket.Conn) *dpClient {
	c := newDPClientFromConn(ws)
	var old *dpClient
	r.mu.Lock()
	old = r.clients[host]
	r.clients[host] = c
	r.mu.Unlock()
	if old != nil {
		_ = old.close()
	}
	return c
}

// Get returns the client for host and whether one exists. Does NOT remove it
// — removal happens implicitly when Register overwrites the slot, and a dead
// client (readLoop terminated, IsClosed()==true) is left in place for
// regDial to detect and reject until the next inbound conn replaces it.
func (r *ConnectionRegistry) Get(host string) (*dpClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[host]
	return c, ok
}

// Close drops and closes every client (daemon shutdown). Idempotent.
func (r *ConnectionRegistry) Close() {
	r.mu.Lock()
	clients := r.clients
	r.clients = make(map[string]*dpClient)
	r.mu.Unlock()
	for _, c := range clients {
		_ = c.close()
	}
}

// hostOnly strips the :port from an addr like "1.2.3.4:5678" using
// net.SplitHostPort, falling back to the raw string on error so a connect
// attempt still produces a deterministic, if ugly, key.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// StartWSServer starts the daemon's inbound WebSocket listener. Each upgraded
// connection is wrapped in a *dpClient and stored in reg under its source IP
// host portion. Mirrors cmd/gbot/main.go startPprofServer's net.Listen-then-
// go pattern (synchronous bind failure, async serve). The returned
// *http.Server's Close stops the listener.
func StartWSServer(reg *ConnectionRegistry, addr string) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		reg.Register(hostOnly(r.RemoteAddr), ws)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return srv, nil
}
