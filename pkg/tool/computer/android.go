package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// errNotConnected is returned by every perception/action method until the
// model calls connect. It is also the message surfaced to the model via the
// tool layer.
var errNotConnected = errors.New("computer: not connected; call connect first")

// rpcCaller is the subset of dpClient that AndroidBackend uses. *dpClient
// satisfies it; tests pass a fake. Defined here (not droidpilot.go) because
// the backend is its only consumer.
type rpcCaller interface {
	call(ctx context.Context, command string, params map[string]any) (json.RawMessage, error)
	IsClosed() bool
	close() error
}

// dialer builds a connected rpcCaller for the given target. Production uses
// wsDial; tests pass a fake that returns a fake rpcCaller without touching
// the network.
type dialer func(ctx context.Context, host string, port int, password string) (rpcCaller, error)

// Compile-time check that the production client satisfies the seam.
var _ rpcCaller = (*dpClient)(nil)

// wsDial is the production dialer — construct a *dpClient (which dials inline
// and starts the read loop) and return it.
func wsDial(ctx context.Context, host string, port int, password string) (rpcCaller, error) {
	c, err := newDPClient(host, port, password)
	if err != nil {
		return nil, err
	}
	if err := c.connect(ctx); err != nil {
		_ = c.close()
		return nil, err
	}
	return c, nil
}

// AndroidBackend drives a DroidPilot Android app over a long-lived WebSocket.
// It starts disconnected; the model must call Connect before any action.
// refs from the last Screen() are held in refs and resolved by
// ClickElement/OpenMenuElement to the element's bounds center.
//
// Lock ordering invariant: AndroidBackend.mu is ALWAYS acquired before any
// dpClient.mu. The dpClient read loop never acquires AndroidBackend.mu. The
// only code touching both locks is Connect/Disconnect/ensureConnected, all of
// which acquire b.mu first and then call client.close()/IsClosed() (which may
// touch dpClient.mu). No dpClient method calls back into the backend, so the
// total order has no cycle and the two-lock design cannot deadlock.
type AndroidBackend struct {
	mu       sync.Mutex
	client   rpcCaller // nil == not connected; *dpClient in prod, fake in tests
	dial     dialer
	host     string // last connected host (diagnostics only)
	port     int    // last connected port
	password string // retained so a manual reconnect needs no re-prompt
	refs     *refRegistry
}

// NewAndroidBackend returns a disconnected backend. The tool is always
// registered, so construction must never touch the network.
func NewAndroidBackend() *AndroidBackend {
	return &AndroidBackend{
		dial: wsDial,
		refs: newRefRegistry(),
	}
}

// newAndroidBackendForTest constructs a backend with an injectable dialer so
// tests can drive the lifecycle without a socket.
func newAndroidBackendForTest(d dialer) *AndroidBackend {
	b := NewAndroidBackend()
	b.dial = d
	return b
}

// IsConnected reports whether a live rpcCaller exists. Safe for concurrent
// use; it only checks the pointer under mu, never the wire.
func (b *AndroidBackend) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client != nil && !b.client.IsClosed()
}

// Connect dials ws://host:port (via b.dial) with an optional Bearer token.
// The dial runs BEFORE b.mu is taken so a slow dial does not block in-flight
// actions on the current connection. On dial success the old client (if any)
// is swapped out under b.mu and then closed outside b.mu; on dial failure the
// old connection is left untouched. So Connect leaves the backend either on
// the new device or still on the old one — never half-open and never
// accidentally disconnected.
func (b *AndroidBackend) Connect(ctx context.Context, host string, port int, password string) error {
	newClient, err := b.dial(ctx, host, port, password)
	if err != nil {
		// Old connection stays live — the model is still driving the prior device.
		return err
	}

	b.mu.Lock()
	old := b.client
	b.client = newClient
	b.host = host
	b.port = port
	b.password = password
	b.refs.clear()
	b.mu.Unlock()

	// Close the old client outside b.mu (its close() acquires only dpClient.mu).
	if old != nil {
		_ = old.close()
	}
	return nil
}

// Disconnect closes the current connection if any and clears the ref
// registry. Idempotent: returns nil when already disconnected.
func (b *AndroidBackend) Disconnect() error {
	b.mu.Lock()
	old := b.client
	b.client = nil
	b.refs.clear()
	b.mu.Unlock()

	if old != nil {
		_ = old.close()
	}
	return nil
}

// ensureConnected is the gate every perception/action method calls. It
// returns errNotConnected when no client is set OR when the current client's
// read loop has terminated (async peer-close/read-error detected via
// IsClosed). It never dials — Connect is the only place a connection opens.
// On detecting a dead client it nils b.client + clears refs so the next
// action reports a clean "not connected" state.
func (b *AndroidBackend) ensureConnected(_ context.Context) error {
	b.mu.Lock()
	if b.client == nil {
		b.mu.Unlock()
		return errNotConnected
	}
	if b.client.IsClosed() {
		old := b.client
		b.client = nil
		b.refs.clear()
		b.mu.Unlock()
		_ = old.close()
		return errNotConnected
	}
	b.mu.Unlock()
	return nil
}

// Close is an alias for Disconnect; called by engine teardown if registered.
func (b *AndroidBackend) Close() error { return b.Disconnect() }

// Screen fetches the UI tree, assigns refs, and returns the numbered list.
// maxDepth ≤0 defaults to 15 (DroidPilot's own default); see coerceMaxDepth.
func (b *AndroidBackend) Screen(ctx context.Context, maxDepth int) (*ScreenResult, error) {
	if err := b.ensureConnected(ctx); err != nil {
		return nil, err
	}
	depth := coerceMaxDepth(maxDepth)
	data, err := b.client.call(ctx, "get_ui_tree", map[string]any{"maxDepth": depth})
	if err != nil {
		return nil, err
	}
	res, err := decodeScreenResult(data)
	if err != nil {
		return nil, err
	}
	res.Elements = b.refs.assign(res.Tree)
	return res, nil
}

// Screenshot captures a JPEG screenshot.
func (b *AndroidBackend) Screenshot(ctx context.Context) (*Screenshot, error) {
	if err := b.ensureConnected(ctx); err != nil {
		return nil, err
	}
	data, err := b.client.call(ctx, "screenshot", map[string]any{"quality": 80})
	if err != nil {
		return nil, err
	}
	return decodeScreenshot(data)
}

// Click taps the given screen coordinate.
func (b *AndroidBackend) Click(ctx context.Context, x, y int) error {
	if err := b.ensureConnected(ctx); err != nil {
		return err
	}
	_, err := b.client.call(ctx, "tap", map[string]any{"x": x, "y": y, "duration": 100})
	return err
}

// ClickElement resolves a ref to its bounds center and taps it.
func (b *AndroidBackend) ClickElement(ctx context.Context, ref int) error {
	x, y, err := b.resolveRef(ref)
	if err != nil {
		return err
	}
	return b.Click(ctx, x, y)
}

// OpenMenu long-presses the given screen coordinate.
func (b *AndroidBackend) OpenMenu(ctx context.Context, x, y int) error {
	if err := b.ensureConnected(ctx); err != nil {
		return err
	}
	_, err := b.client.call(ctx, "long_press", map[string]any{"x": x, "y": y, "duration": 1000})
	return err
}

// OpenMenuElement resolves a ref to its bounds center and long-presses it.
func (b *AndroidBackend) OpenMenuElement(ctx context.Context, ref int) error {
	x, y, err := b.resolveRef(ref)
	if err != nil {
		return err
	}
	return b.OpenMenu(ctx, x, y)
}

// Type replaces the focused field's text. Uses DroidPilot's set_text (REPLACE
// semantics) to match Anthropic computer-use conventions and avoid a
// double-type hazard on retry — type_text would append.
func (b *AndroidBackend) Type(ctx context.Context, text string) error {
	if err := b.ensureConnected(ctx); err != nil {
		return err
	}
	_, err := b.client.call(ctx, "set_text", map[string]any{"text": text})
	return err
}

// SendKey issues a single system key. The key allowlist is enforced in
// dispatch (single-sited, pre-ensureConnected); the backend does not re-check.
func (b *AndroidBackend) SendKey(ctx context.Context, key string) error {
	if err := b.ensureConnected(ctx); err != nil {
		return err
	}
	_, err := b.client.call(ctx, "press_key", map[string]any{"key": key})
	return err
}

// Scroll scrolls by the given direction ("up"|"down"|"left"|"right").
func (b *AndroidBackend) Scroll(ctx context.Context, direction string) error {
	if err := b.ensureConnected(ctx); err != nil {
		return err
	}
	_, err := b.client.call(ctx, "scroll", map[string]any{"direction": direction, "amount": 500})
	return err
}

// Zoom pinches at the given center with the given scale factor.
func (b *AndroidBackend) Zoom(ctx context.Context, x, y int, scale float64) error {
	if err := b.ensureConnected(ctx); err != nil {
		return err
	}
	_, err := b.client.call(ctx, "pinch", map[string]any{"x": x, "y": y, "scale": scale, "duration": 400})
	return err
}

// DeviceInfo fetches manufacturer/model/screen metadata.
func (b *AndroidBackend) DeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	if err := b.ensureConnected(ctx); err != nil {
		return nil, err
	}
	data, err := b.client.call(ctx, "get_device_info", nil)
	if err != nil {
		return nil, err
	}
	return decodeDeviceInfo(data)
}

// OpenApp launches an installed app by package name via DroidPilot's
// open_app command. Package-name presence is validated in dispatch BEFORE
// ensureConnected (fail-fast, no wire traffic), so the backend does not
// re-check.
func (b *AndroidBackend) OpenApp(ctx context.Context, packageName string) error {
	if err := b.ensureConnected(ctx); err != nil {
		return err
	}
	_, err := b.client.call(ctx, "open_app", map[string]any{"package": packageName})
	return err
}

// resolveRef maps a ref number to tap coordinates, returning the actionable
// error (no-screen / ref-not-found) instead of a silent tap at 0,0.
func (b *AndroidBackend) resolveRef(ref int) (int, int, error) {
	if b.refs == nil || !b.refs.hasScreen() {
		return 0, 0, errors.New("computer: no screen captured; call screen before click_element")
	}
	el, ok := b.refs.resolve(ref)
	if !ok {
		return 0, 0, fmt.Errorf("computer: ref %d not found; call screen first", ref)
	}
	x, y := el.Bounds.Center()
	return x, y, nil
}
