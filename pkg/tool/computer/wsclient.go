package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// rpcRequest is the GBot app's command envelope. Mirrors
// mcp-server/src/android-client.ts CommandRequest.
type rpcRequest struct {
	ID      string         `json:"id"`
	Command string         `json:"command"`
	Params  map[string]any `json:"params,omitempty"`
}

// rpcResponse is the GBot app's reply envelope. Mirrors
// mcp-server/src/android-client.ts CommandResponse. Data is raw so each
// command decodes its own shape.
type rpcResponse struct {
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// errConnectionClosed is used to fail all pending requests when the read loop
// terminates (peer close / read error / explicit close).
var errConnectionClosed = errors.New("computer: connection closed")

// dpClient owns one WebSocket connection to the GBot app and matches
// requests to responses by id. Mirrors mcp-server/src/android-client.ts
// AndroidClient minus the EventEmitter surface — we use ctx + pending chans.
type dpClient struct {
	mu      sync.Mutex
	ws      *websocket.Conn
	counter atomic.Int64
	pending map[string]chan rpcResponse
	closed  atomic.Bool
}

// newDPClient constructs an unconnected client for the given target.
// authToken is sent as Authorization: Bearer <token> when non-empty.
func newDPClient(host string, port int, authToken string) (*dpClient, error) {
	header := http.Header{}
	if authToken != "" {
		header.Set("Authorization", "Bearer "+authToken)
	}
	url := fmt.Sprintf("ws://%s:%d", host, port)
	ws, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, fmt.Errorf("computer: dial %s: %w", url, err)
	}
	c := &dpClient{
		ws:      ws,
		pending: make(map[string]chan rpcResponse),
	}
	go c.readLoop()
	return c, nil
}

// connect is a hook mirroring the AndroidClient.connect() lifecycle. The
// production newDPClient dials inline (so the call site gets a synchronous
// failure), so connect is a no-op kept for API symmetry with the plan's
// dialer seam. It exists so a future lazy-connect implementation can swap in
// without changing callers.
func (c *dpClient) connect(_ context.Context) error { return nil }

// readLoop runs in a background goroutine, reading one text message per loop,
// decoding rpcResponse, and fulfilling the matching pending request. On read
// error or close frame it marks the client closed and fails all pending with
// errConnectionClosed. The reader never reaches back into AndroidBackend —
// it only flips its own closed flag (AndroidBackend.ensureConnected polls it).
func (c *dpClient) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		var resp rpcResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			// Unparseable frame: drop it. The pending request will time out
			// via ctx on the caller side; we never violate the id contract.
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			// Non-blocking: the caller may have already given up via ctx.
			select {
			case ch <- resp:
			default:
			}
		}
	}
}

// shutdown marks the client closed and fails every pending request with
// errConnectionClosed. Called exactly once by the read loop on terminal error.
func (c *dpClient) shutdown(_ error) {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan rpcResponse)
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcResponse{Success: false, Error: errConnectionClosed.Error()}:
		default:
		}
	}
}

// call sends one command, blocks until the matching response arrives or ctx
// is canceled, and translates the GBot app's success=false into a Go error.
func (c *dpClient) call(ctx context.Context, command string, params map[string]any) (json.RawMessage, error) {
	// ID format mirrors the GBot app's `req_<n>_<ts>` convention (UnixMilli).
	id := fmt.Sprintf("req_%d_%d", c.counter.Add(1), time.Now().UnixMilli())
	ch := make(chan rpcResponse, 1)

	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return nil, errConnectionClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()

	// Cleanup on any exit path so a cancelled call doesn't leak a map slot.
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := rpcRequest{ID: id, Command: command, Params: params}
	if err := c.ws.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("computer: write %s: %w", command, err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if !resp.Success {
			if resp.Error == "" {
				return nil, fmt.Errorf("computer: %s failed", command)
			}
			return nil, fmt.Errorf("computer: %s: %s", command, resp.Error)
		}
		return resp.Data, nil
	}
}

// IsClosed reports whether the read loop has terminated (peer close, read
// error, or explicit close()). AndroidBackend.ensureConnected polls it so an
// async drop is detected without the read loop reaching back into the backend.
func (c *dpClient) IsClosed() bool { return c.closed.Load() }

// close idempotently closes the underlying WebSocket. Marking closed first
// prevents the read loop from racing to fail pending requests we are about to
// drain.
func (c *dpClient) close() error {
	if c.closed.CompareAndSwap(false, true) {
		_ = c.ws.Close()
	}
	return nil
}

// decodeScreenResult decodes the GBot app's get_ui_tree data blob. The data is
// `{"tree": <UINode>}` and carries NO top-level screen size — get_ui_tree
// does not report one — so the returned ScreenResult has Width=0/Height=0.
// renderScreenResult renders "screen size unknown" when both are 0.
func decodeScreenResult(data json.RawMessage) (*ScreenResult, error) {
	if len(data) == 0 {
		return &ScreenResult{}, nil
	}
	var raw struct {
		Tree *UINode `json:"tree"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("computer: decode get_ui_tree: %w", err)
	}
	return &ScreenResult{Tree: raw.Tree}, nil
}

// decodeScreenshot decodes the GBot app's screenshot data blob. Maps
// image→DataB64, "image/"+format→MIMEType (empty format defaults to jpeg),
// width/height→Width/Height.
func decodeScreenshot(data json.RawMessage) (*Screenshot, error) {
	if len(data) == 0 {
		return nil, errors.New("computer: empty screenshot data")
	}
	var raw struct {
		Image  string `json:"image"`
		Format string `json:"format"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("computer: decode screenshot: %w", err)
	}
	mime := "image/" + raw.Format
	if raw.Format == "" {
		mime = "image/jpeg"
	}
	return &Screenshot{DataB64: raw.Image, MIMEType: mime, Width: raw.Width, Height: raw.Height}, nil
}

// decodeDeviceInfo decodes the GBot app's get_device_info data blob. Each of
// the 8 fields maps explicitly; none are dropped.
func decodeDeviceInfo(data json.RawMessage) (*DeviceInfo, error) {
	if len(data) == 0 {
		return nil, errors.New("computer: empty device_info data")
	}
	var raw struct {
		Manufacturer string  `json:"manufacturer"`
		Model        string  `json:"model"`
		SDK          int     `json:"sdk"`
		Release      string  `json:"release"`
		ScreenWidth  int     `json:"screenWidth"`
		ScreenHeight int     `json:"screenHeight"`
		Density      float64 `json:"density"`
		DensityDpi   int     `json:"densityDpi"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("computer: decode get_device_info: %w", err)
	}
	return &DeviceInfo{
		Manufacturer: raw.Manufacturer,
		Model:        raw.Model,
		SDK:          raw.SDK,
		Release:      raw.Release,
		ScreenWidth:  raw.ScreenWidth,
		ScreenHeight: raw.ScreenHeight,
		Density:      raw.Density,
		DensityDPI:   raw.DensityDpi,
	}, nil
}
