// Package transport is gbot's plugin stream transport: one named byte pipe
// per plugin, between the plugin's protocol module (provider, running in the
// daemon's embedded JS host) and the single active wui client. TCP semantics
// — routing and bytes only. The hub never inspects payload contents;
// everything above the pipe (protocol, rendering, control) belongs to the
// plugin.
package transport

import (
	"fmt"
	"log/slog"
	"sync"
)

const (
	maxFrameSize = 8 << 20 // 8 MiB: a screencast JPEG is ~10-100 KiB; the guard exists for memory bombs
	maxChannelID = 255     // wire is 1 byte; 0x00 is reserved for legacy attachment frames
)

// ClientSink receives outbound frames destined for one wui WS connection.
// Both methods must be safe to call from any goroutine; the wui connector
// implements them by enqueueing into its era queue (writes to a superseded
// era are dropped, which is exactly the takeover semantics we want).
type ClientSink interface {
	// SendBinary delivers a full wire frame [channelID][payload].
	SendBinary(frame []byte)
	// SendJSON delivers a text frame (channel lifecycle notifications).
	SendJSON(payload []byte)
}

// Provider is one attached plugin protocol module (the far end of the
// plugin's pipe). The in-process implementation lives here; the interface
// exists so the hub's registry is not welded to one transport shape.
type Provider interface {
	// writeFrame forwards a client payload to the plugin side. An error
	// means the provider is gone; the hub unregisters it.
	writeFrame(payload []byte) error
	close()
}

// inprocProvider is the daemon-internal provider: frames raised by the JS
// protocol module arrive via Deliver, frames from the client land in onFrame
// (which must not block — the host bridges into its event loop).
type inprocProvider struct {
	onFrame func([]byte)

	mu     sync.Mutex
	dead   bool
	onGone func()
}

func (p *inprocProvider) writeFrame(payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return fmt.Errorf("provider closed")
	}
	if len(payload) > maxFrameSize {
		return fmt.Errorf("frame size %d out of bounds", len(payload))
	}
	p.onFrame(payload)
	return nil
}

func (p *inprocProvider) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dead {
		p.dead = true
		if p.onGone != nil {
			p.onGone()
		}
	}
}

// InprocHandle is the plugin host's grip on one registered provider.
type InprocHandle struct {
	hub    *Hub
	plugin string
	prov   *inprocProvider
}

// Deliver forwards a plugin-side payload to the bound client, if any. No
// binding = no client pipe open: bytes are dropped (idle periods are
// expected while no card is mounted).
func (h *InprocHandle) Deliver(payload []byte) {
	if len(payload) == 0 || len(payload) > maxFrameSize {
		slog.Warn("transport: frame size out of bounds, dropped", "plugin", h.plugin, "size", len(payload))
		return
	}
	h.hub.deliver(h.plugin, payload)
}

// Close unregisters the provider and notifies the bound client — the
// module died or was reloaded.
func (h *InprocHandle) Close() {
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	if cur, ok := h.hub.providers[h.plugin]; ok && cur == h.prov {
		delete(h.hub.providers, h.plugin)
		h.hub.dropBindingLocked(h.plugin, "provider gone")
	}
	h.prov.close()
}

// binding is one open pipe: a client sink attached to a provider.
type binding struct {
	id     uint8
	plugin string
	sink   ClientSink
}

// Hub owns the provider registry and the bindings of the single active
// client. Zero-value is not usable; call New.
type Hub struct {
	mu        sync.Mutex
	providers map[string]Provider
	bindings  map[uint8]*binding
	byName    map[string]uint8
	nextID    uint8 // starts at 1; 0x00 is reserved on the client wire
}

// New builds an empty Hub. Providers register via RegisterInproc when the
// plugin host loads each protocol module.
func New() *Hub {
	return &Hub{
		providers: make(map[string]Provider),
		bindings:  make(map[uint8]*binding),
		byName:    make(map[string]uint8),
	}
}

// RegisterInproc attaches a protocol-module provider under the plugin's
// name. Last registration wins — a reloaded module replaces its predecessor
// and any binding it held (the client sees channel_close, then may reopen).
// onFrame receives client→plugin payloads and must not block; onGone (may
// be nil) fires once when this provider is closed or replaced, so the host
// can stop its pumps.
func (h *Hub) RegisterInproc(plugin string, onFrame func([]byte), onGone func()) *InprocHandle {
	if onFrame == nil {
		return nil
	}
	prov := &inprocProvider{onFrame: onFrame, onGone: onGone}
	handle := &InprocHandle{hub: h, plugin: plugin, prov: prov}
	h.mu.Lock()
	if old := h.providers[plugin]; old != nil {
		old.close()
		h.dropBindingLocked(plugin, "provider replaced")
	}
	h.providers[plugin] = prov
	h.mu.Unlock()
	slog.Info("transport: provider registered", "plugin", plugin)
	return handle
}

// Close shuts every provider. Bindings die with their providers; the client
// notices via its channel_close notifications.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, p := range h.providers {
		p.close()
		delete(h.providers, name)
	}
	// Bindings are not torn down here individually: their sinks belong to
	// the wui connector, whose own shutdown closes the WS. Clearing the
	// maps just releases references.
	h.bindings = make(map[uint8]*binding)
	h.byName = make(map[string]uint8)
}

// deliver forwards one provider payload to the bound client, if any.
func (h *Hub) deliver(plugin string, payload []byte) {
	h.mu.Lock()
	id, ok := h.byName[plugin]
	h.mu.Unlock()
	if !ok {
		return
	}
	frame := make([]byte, 1+len(payload))
	frame[0] = id
	copy(frame[1:], payload)
	if b := h.binding(id); b != nil {
		b.sink.SendBinary(frame)
	}
}

func (h *Hub) binding(id uint8) *binding {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bindings[id]
}

// dropBindingLocked removes a plugin's binding and notifies its sink. The
// provider side needs no lifecycle message in v1: it holds one persistent
// connection and simply sees no traffic while unbound.
func (h *Hub) dropBindingLocked(plugin, reason string) {
	id, ok := h.byName[plugin]
	if !ok {
		return
	}
	delete(h.byName, plugin)
	if b := h.bindings[id]; b != nil {
		delete(h.bindings, id)
		notify := fmt.Sprintf(`{"type":"channel_close","channelId":%d}`, id)
		b.sink.SendJSON([]byte(notify))
	}
	slog.Debug("transport: pipe dropped", "plugin", plugin, "reason", reason)
}

// Connected reports whether a provider for the plugin is currently attached —
// the "online" bit the plugin cards show (the module being up AND wired).
func (h *Hub) Connected(plugin string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.providers[plugin] != nil
}

// OpenChannel binds the named plugin's pipe to sink and returns the wire
// channel id. Ids are allocated monotonically from 1 and never reused
// within a hub's lifetime; 0x00 stays reserved for attachment frames.
func (h *Hub) OpenChannel(sink ClientSink, plugin string) (uint8, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.providers[plugin]; !ok {
		return 0, fmt.Errorf("transport: no provider for plugin %q", plugin)
	}
	if id, ok := h.byName[plugin]; ok {
		b := h.bindings[id]
		if b == nil || b.sink != sink {
			return 0, fmt.Errorf("transport: plugin %q pipe already open", plugin)
		}
		// Same client re-opening its own pipe: the card was remounted without
		// a close frame (closed mid-connecting — no id existed to close with).
		// Replace silently; notifying the asking client back at itself would
		// only trip the reconnect state it is climbing out of.
		delete(h.bindings, id)
		delete(h.byName, plugin)
	}
	if h.nextID >= maxChannelID {
		return 0, fmt.Errorf("transport: channel id space exhausted")
	}
	id := h.nextID + 1
	h.nextID = id
	h.bindings[id] = &binding{id: id, plugin: plugin, sink: sink}
	h.byName[plugin] = id
	return id, nil
}

// ChannelSend relays a client payload to the plugin's provider. A dead
// provider tears the binding down (the client gets channel_close).
func (h *Hub) ChannelSend(id uint8, payload []byte) {
	h.mu.Lock()
	b := h.bindings[id]
	if b == nil {
		h.mu.Unlock()
		return
	}
	p := h.providers[b.plugin]
	if p == nil {
		h.dropBindingLocked(b.plugin, "provider missing")
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	if err := p.writeFrame(payload); err != nil {
		h.mu.Lock()
		if h.providers[b.plugin] == p {
			delete(h.providers, b.plugin)
			p.close()
			h.dropBindingLocked(b.plugin, "provider write failed")
		}
		h.mu.Unlock()
	}
}

// CloseChannel unbinds a client-initiated close. The provider connection
// stays; a future OpenChannel gets a fresh id.
func (h *Hub) CloseChannel(id uint8) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if b := h.bindings[id]; b != nil {
		h.dropBindingLocked(b.plugin, "client closed")
	}
}

// CloseChannelByName unbinds by plugin name — the client closed before the
// server assigned an id (card closed mid-connecting) and has no id to
// reference. Without this the binding leaks until a takeover or provider
// bounce, wedging every reopen on that connection.
func (h *Hub) CloseChannelByName(plugin string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.byName[plugin]; ok {
		h.dropBindingLocked(plugin, "client closed")
	}
}

// UnbindClient tears down every pipe bound to sink — the WS connection
// behind it died or was taken over. Providers survive.
func (h *Hub) UnbindClient(sink ClientSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, b := range h.bindings {
		if b.sink == sink {
			delete(h.bindings, id)
			delete(h.byName, b.plugin)
		}
	}
}
