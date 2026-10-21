package wui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
)

// RegisterRemoteDesktopRoutes mounts the remote-desktop settings surface:
//
//   - GET  /api/settings/remotedesktop      — devices array (nil coerced to [])
//   - PUT  /api/settings/remotedesktop      — replace the devices array (bare-array body)
//   - POST /api/settings/remotedesktop/test — WS reachability probe, always 200
//
// Like the providers API: read/write ~/.gbot/settings.json directly, validate
// before any write, probe outcomes are data (200) not transport failures.
func RegisterRemoteDesktopRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/remotedesktop", handleGetRemoteDevices)
	mux.HandleFunc("PUT /api/settings/remotedesktop", handlePutRemoteDevices)
	mux.HandleFunc("POST /api/settings/remotedesktop/test", handleTestRemoteDevice)
}

// remoteDevicesPayload is the GET response shape. Devices is coerced to [] so
// the wire shape is always an array, never null (the frontend iterates it).
type remoteDevicesPayload struct {
	Devices []config.RemoteDevice `json:"devices"`
}

func handleGetRemoteDevices(w http.ResponseWriter, r *http.Request) {
	// Cold start (missing/unreadable settings.json) is a normal state, not
	// an error: the settings page just opens empty.
	cfg, err := config.Load()
	if err != nil {
		writeJSON(w, http.StatusOK, remoteDevicesPayload{Devices: []config.RemoteDevice{}})
		return
	}
	if cfg.Desktops == nil {
		cfg.Desktops = []config.RemoteDevice{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, remoteDevicesPayload{Devices: cfg.Desktops})
}

func handlePutRemoteDevices(w http.ResponseWriter, r *http.Request) {
	var devices []config.RemoteDevice
	if err := json.NewDecoder(r.Body).Decode(&devices); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := validateRemoteDevices(devices); msg != "" {
		errorJSON(w, http.StatusBadRequest, msg)
		return
	}
	if err := config.SaveRemoteDevices(devices); err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// validateRemoteDevices returns the first rule violation as a user-facing
// message, or "" when the device set is acceptable. Checked before any write
// happens, so a rejected PUT never touches settings.json. Unlike providers,
// an empty list is VALID — deleting the last device is a normal state.
// A name containing '/' would break the single-segment {device} proxy route.
func validateRemoteDevices(devices []config.RemoteDevice) string {
	seen := make(map[string]bool)
	for _, d := range devices {
		if strings.TrimSpace(d.Name) == "" {
			return "device name is required"
		}
		if seen[d.Name] {
			return fmt.Sprintf("duplicate device name %q", d.Name)
		}
		seen[d.Name] = true
		if strings.TrimSpace(d.Addr) == "" {
			return fmt.Sprintf("device %s: address is required", d.Name)
		}
		if strings.ContainsRune(d.Name, '/') {
			return fmt.Sprintf("device %s: name must not contain '/'", d.Name)
		}
	}
	return ""
}

// handleTestRemoteDevice probes one device address (decoded from the request
// body). Outcomes are data: the response is always 200 with an ok/error
// envelope — a failing probe is not a transport failure.
func handleTestRemoteDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	latencyMs, err := probeVNC(r.Context(), req.Addr)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "latencyMs": latencyMs})
}

// normalizeVNCAddr maps user-entered addresses onto a ws:// dial URL:
//
//	ws:// / wss://            → as-is
//	http:// / https://        → scheme swapped; /websockify appended when the
//	                             path is empty or "/" (the websockify default);
//	                             an explicit non-root path means the operator
//	                             runs websockify there and is kept verbatim
//	bare host[:port][/path]   → ws:// prefixed
//
// Anything else (empty, whitespace, other schemes like ftp://, unparsable or
// empty host after normalization) is an error. Scheme prefixes are
// case-sensitive. The client never normalizes — both the test button and the
// sidebar probe POST the raw addr.
func normalizeVNCAddr(addr string) (string, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return "", errors.New("address is required")
	}
	switch {
	case strings.HasPrefix(trimmed, "ws://"), strings.HasPrefix(trimmed, "wss://"):
		if u, err := url.Parse(trimmed); err != nil || u.Host == "" {
			return "", errors.New("invalid address")
		}
		return trimmed, nil
	case strings.HasPrefix(trimmed, "http://"), strings.HasPrefix(trimmed, "https://"):
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return "", errors.New("invalid address")
		}
		wsScheme := "ws"
		if u.Scheme == "https" {
			wsScheme = "wss"
		}
		if u.Path == "" || u.Path == "/" {
			return wsScheme + "://" + u.Host + "/websockify", nil
		}
		return wsScheme + "://" + u.Host + u.Path, nil
	case strings.Contains(trimmed, "://"):
		return "", errors.New("invalid address")
	default:
		normalized := "ws://" + trimmed
		if u, err := url.Parse(normalized); err != nil || u.Host == "" {
			return "", errors.New("invalid address")
		}
		return normalized, nil
	}
}

// probeVNC dials the device's WS endpoint and returns the wall-time latency.
// The VNC password never enters the probe: WS handshake reachability is what
// the sidebar dot and the ms number mean; auth happens in the RFB stream
// after the proxy relays it.
func probeVNC(ctx context.Context, addr string) (int, error) {
	normalized, err := normalizeVNCAddr(addr)
	if err != nil {
		return 0, err
	}
	dialer := websocket.Dialer{
		Subprotocols:     []string{"binary"},
		HandshakeTimeout: 5 * time.Second,
	}
	start := time.Now()
	conn, resp, err := dialer.DialContext(ctx, normalized, nil)
	if err != nil {
		return 0, err
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	_ = conn.Close()
	// A loopback dial can complete sub-millisecond and the REST envelope +
	// tests pin a positive integer.
	latency := int(time.Since(start).Milliseconds())
	if latency < 1 {
		latency = 1
	}
	return latency, nil
}

// vncUpgrader upgrades wui clients for the console proxy. "binary" is the
// subprotocol websockify and noVNC negotiate (RFB is a byte stream, not
// UTF-8 text). No read-limit change: gorilla's 64 MiB default is far above
// any RFB frame.
var vncUpgrader = websocket.Upgrader{
	CheckOrigin:  func(*http.Request) bool { return true },
	Subprotocols: []string{"binary"},
}

// RegisterVNCProxyRoutes mounts the WS reverse proxy at /wui/vnc/{device}.
// The browser connects here (same-origin, CSP-safe); the server relays
// frames to the device's ws endpoint. v1 does NOT bridge raw RFB TCP —
// device endpoints must be ws/wss (websockify/QEMU-ws/dockur).
func RegisterVNCProxyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /wui/vnc/{device}", handleVNCProxy)
}

func handleVNCProxy(w http.ResponseWriter, r *http.Request) {
	deviceName := r.PathValue("device")
	// A Load error behaves like cold start (no devices) — the device simply
	// cannot be found, same semantics as handleGetRemoteDevices.
	var devices []config.RemoteDevice
	if cfg, err := config.Load(); err == nil {
		devices = cfg.Desktops
	}
	var device *config.RemoteDevice
	for i := range devices {
		if devices[i].Name == deviceName {
			device = &devices[i]
			break
		}
	}
	if device == nil {
		http.Error(w, "unknown device", http.StatusNotFound)
		return
	}
	addr, err := normalizeVNCAddr(device.Addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	client, err := vncUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer client.Close()
	// Upgrade hijacked the connection: net/http does not cancel r.Context()
	// when the client peer closes, so the handshake timeout — not context
	// cancellation — is the actual bound on a stuck dial.
	dialer := websocket.Dialer{
		Subprotocols:     []string{"binary"},
		HandshakeTimeout: 10 * time.Second,
	}
	target, _, err := dialer.DialContext(r.Context(), addr, nil)
	if err != nil {
		msg := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "device dial failed: "+err.Error())
		_ = client.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
		return
	}
	defer target.Close()
	relayWS(client, target)
}

// relayWS pumps frames both ways until the first pump exits; it then closes
// both conns so the peer's blocked read unblocks.
func relayWS(a, b *websocket.Conn) {
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	go func() {
		defer stop()
		pumpWS(a, b)
	}()
	pumpWS(b, a)
	stop()
}

// pumpWS copies messages from src to dst preserving the opcode. Single
// reader + single writer per conn per direction satisfies gorilla's
// concurrency contract.
func pumpWS(src, dst *websocket.Conn) {
	for {
		msgType, data, err := src.ReadMessage()
		if err != nil {
			return
		}
		if err := dst.WriteMessage(msgType, data); err != nil {
			return
		}
	}
}
