package wui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/transport"
)

// startTestTransport spins a real transport hub on the connector so channel
// wiring is exercised end-to-end (registration, provider, frames).
func startTestTransport(t *testing.T, c *WUIConnector) *transport.Hub {
	t.Helper()
	hub := transport.New()
	t.Cleanup(hub.Close)
	c.SetTransport(hub)
	return hub
}

// chanTestProvider is the in-process provider double: send pushes frames
// downstream (provider→client), awaitClient asserts an upstream frame.
type chanTestProvider struct {
	t      *testing.T
	handle *transport.InprocHandle
	recv   chan []byte
}

func (p *chanTestProvider) send(payload string) {
	p.t.Helper()
	p.handle.Deliver([]byte(payload))
}

func (p *chanTestProvider) awaitClient(want string) {
	p.t.Helper()
	select {
	case got := <-p.recv:
		if string(got) != want {
			p.t.Fatalf("provider got %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		p.t.Fatalf("provider never received %q", want)
	}
}

func registerChanProvider(t *testing.T, hub *transport.Hub, name string) *chanTestProvider {
	t.Helper()
	p := &chanTestProvider{t: t, recv: make(chan []byte, 16)}
	p.handle = hub.RegisterInproc(name, func(payload []byte) { p.recv <- payload }, nil)
	if p.handle == nil {
		t.Fatal("RegisterInproc nil handle")
	}
	t.Cleanup(func() { p.handle.Close() })
	return p
}

func readChannelOpened(t *testing.T, ws *websocket.Conn) (channel string, id byte) {
	t.Helper()
	frame := readUntilType(t, ws, "channel_opened")
	var msg struct {
		Channel   string `json:"channel"`
		ChannelID uint8  `json:"channelId"`
	}
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("channel_opened parse: %v", err)
	}
	return msg.Channel, msg.ChannelID
}

func TestChannelOpenRoutesBothDirections(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	prov := registerChanProvider(t, hub, "browser")

	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	channel, id := readChannelOpened(t, ws)
	if channel != "browser" || id != 1 {
		t.Fatalf("opened = (%q,%d), want (browser,1)", channel, id)
	}

	// Client → provider: binary frame [id][payload] relays to the provider.
	frame := append([]byte{id}, `{"method":"Target.getTargets","id":1}`...)
	if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	prov.awaitClient(`{"method":"Target.getTargets","id":1}`)

	// Provider → client: hub pushes [id][payload] as one binary frame.
	prov.send(`{"id":1,"result":{"targetInfos":[]}}`)
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
	mt, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if mt != websocket.BinaryMessage || data[0] != id || !strings.Contains(string(data[1:]), "targetInfos") {
		t.Fatalf("client frame wrong: mt=%d data=%q", mt, data)
	}
}

func TestChannelOpenErrors(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	// No transport wired: polite error, connection stays alive.
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	frame := readUntilType(t, ws, "channel_error")
	var msg struct {
		Channel string `json:"channel"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Channel != "browser" || msg.Error == "" {
		t.Fatalf("channel_error malformed: %s", frame)
	}
	mutCBarrier(t, ws)

	// Transport wired but no provider for the channel.
	hub := startTestTransport(t, c)
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "no.such"})
	frame = readUntilType(t, ws, "channel_error")
	if !strings.Contains(string(frame), "no provider") {
		t.Fatalf("want no-provider error, got %s", frame)
	}
	// This connection predates the transport: no sink was stored (nil era).
	// With a provider registered the open must SUCCEED — neither the gate nor
	// the post-bind supersede check may treat a nil sink era as superseded.
	registerChanProvider(t, hub, "browser")
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	readChannelOpened(t, ws)
}

func TestChannelCloseStopsRelay(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	registerChanProvider(t, hub, "browser")

	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id := readChannelOpened(t, ws)

	// Explicit close, then provider frames must not reach the client.
	// Negative window: the relay teardown is silent, so a settled wait is
	// the observable (waitFor is the file's REAL-TIME sanctioned helper).
	writeWSText(t, ws, map[string]any{"type": "channel_close", "channelId": id})
	waitFor(50*time.Millisecond, func() bool { return false })

	// Reopen gets a fresh never-reused id.
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id2 := readChannelOpened(t, ws)
	if id2 == id || id2 != 2 {
		t.Fatalf("reopen id = %d, want 2", id2)
	}
}

// TestChannelCloseByNameWithoutId: a card closed while still connecting has
// no server-assigned id to close with — the close must go by channel NAME,
// or the server-side binding leaks and every reopen on this connection is
// rejected "already open" until a takeover or provider bounce.
func TestChannelCloseByNameWithoutId(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	registerChanProvider(t, hub, "browser")

	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	if _, id := readChannelOpened(t, ws); id != 1 {
		t.Fatalf("first open id = %d, want 1", id)
	}

	// Close by name alone (no channelId): the mid-connecting close.
	writeWSText(t, ws, map[string]any{"type": "channel_close", "channel": "browser"})
	waitFor(50*time.Millisecond, func() bool { return false })

	// Reopen must succeed — the name-keyed close freed the binding.
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	frame := readUntilType(t, ws, "channel_opened")
	if !strings.Contains(string(frame), `"browser"`) {
		t.Fatalf("reopen after name-keyed close wedged: %s", frame)
	}
}

// TestChannelCloseById: the normal in-session teardown — a client that holds
// a server-assigned id closes with that id. The binding frees, later frames
// for the stale id are dropped without wedging the connection, and reopen
// gets a fresh id. Pins the by-id arm of channel_close (the by-name arm
// covers the mid-connecting close above).
//
// All reads go through a single persistent collector goroutine: gorilla
// forbids reading a connection after one of its read deadlines has fired,
// so the stale-frame drop check observes recorded frames — it must never
// probe-read a connection it intends to keep using.
func TestChannelCloseById(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	prov := registerChanProvider(t, hub, "browser")
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)
	rec := make(chan string, 64)
	done := make(chan struct{})
	t.Cleanup(func() { _ = ws.Close(); <-done })
	go func() {
		defer close(done)
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				rec <- string(data)
			} else {
				rec <- "bin:" + fmt.Sprintf("% x", data)
			}
		}
	}()
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	var opened string
	for opened == "" {
		select {
		case f := <-rec:
			if strings.Contains(f, "channel_error") {
				t.Fatalf("open rejected: %s", f)
			}
			if strings.Contains(f, "channel_opened") {
				opened = f
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no channel_opened")
		}
	}
	if !strings.Contains(opened, `"channelId":1`) {
		t.Fatalf("first open id wrong: %s", opened)
	}
	writeWSText(t, ws, map[string]any{"type": "channel_close", "channelId": 1})
	// The hub echoes one channel_close notification back to the closer
	// (pinned by the round-trip test); it must carry the id we closed.
	waitFor(50*time.Millisecond, func() bool { return false })
	// Provider frames for the stale id are dropped: settle, then assert the
	// record holds only the close echo and nothing binary after it.
	prov.send(`{"after":"close"}`)
	waitFor(120*time.Millisecond, func() bool { return false })
	var binAfterClose []string
drain:
	for {
		select {
		case f := <-rec:
			if strings.HasPrefix(f, "bin:") {
				binAfterClose = append(binAfterClose, f)
			}
		default:
			break drain
		}
	}
	if len(binAfterClose) != 0 {
		t.Fatalf("stale-id frames must be dropped, got %v", binAfterClose)
	}
	// Reopen must succeed — the by-id close freed the binding. Hub ids
	// never reuse, so the fresh open must hand back id 2.
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	for {
		select {
		case f := <-rec:
			if strings.Contains(f, "channel_error") {
				t.Fatalf("reopen rejected: %s", f)
			}
			if strings.Contains(f, "channel_opened") {
				if !strings.Contains(f, `"channelId":2`) {
					t.Fatalf("reopen id wrong (hub ids never reuse): %s", f)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reopen after by-id close wedged")
		}
	}
}

func TestTakeoverUnbindsOldClientChannels(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	prov := registerChanProvider(t, hub, "browser")

	ws1 := mutCDialChat(t, c)
	drainInitialFrames(t, ws1)
	writeWSText(t, ws1, map[string]any{"type": "channel_open", "channel": "browser"})
	if _, id := readChannelOpened(t, ws1); id != 1 {
		t.Fatalf("first client id = %d, want 1", id)
	}

	// Second client takes over; the old connection is closed by the server.
	ws2 := mutCDialChat(t, c)
	drainInitialFrames(t, ws2)
	writeWSText(t, ws2, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id2 := readChannelOpened(t, ws2)
	if id2 != 2 {
		t.Fatalf("new client id = %d, want a fresh id (2: hub ids never reuse)", id2)
	}

	// Old client got the takeover close; frames now flow only to ws2.
	prov.send(`{"after":"takeover"}`)
	_ = ws2.SetReadDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
	mt, data, err := ws2.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage || data[0] != id2 {
		t.Fatalf("new client frame wrong: mt=%d err=%v", mt, err)
	}
	_ = ws1.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
	if _, _, err := ws1.ReadMessage(); err == nil {
		t.Fatal("old client must be closed after takeover")
	}
}

// TestTakeoverThenOldHandlerExitKeepsNewBindings pins the disconnect-side
// CAS: once a newer client owns the sink, the old connection's teardown must
// NOT unbind the new client's channels.
func TestTakeoverThenOldHandlerExitKeepsNewBindings(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	prov := registerChanProvider(t, hub, "browser")

	ws1 := mutCDialChat(t, c)
	drainInitialFrames(t, ws1)
	writeWSText(t, ws1, map[string]any{"type": "channel_open", "channel": "browser"})
	readChannelOpened(t, ws1)

	ws2 := mutCDialChat(t, c)
	drainInitialFrames(t, ws2)
	writeWSText(t, ws2, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id2 := readChannelOpened(t, ws2)

	// Wait out ws1's handler goroutine entirely (its disconnect CAS must
	// fail against the swapped sink), then prove ws2's binding survived.
	waitFor(300*time.Millisecond, func() bool { return false })
	prov.send(`{"after":"old-handler-exit"}`)
	_ = ws2.SetReadDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
	mt, data, err := ws2.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage || data[0] != id2 {
		t.Fatalf("new client binding lost after old handler exit: mt=%d err=%v", mt, err)
	}
}

// TestLateChannelOpenAfterTakeoverDoesNotBrickChannel pins the straggler
// window: a channel_open dispatched by the OLD connection's readLoop after
// the new connection already swapped the sink binds the dead sink again.
// The old handler's teardown must remove that binding, or the channel is
// "already open" for the new client until the provider reconnects.
func TestLateChannelOpenAfterTakeoverDoesNotBrickChannel(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	registerChanProvider(t, hub, "browser")

	ws1 := mutCDialChat(t, c)
	drainInitialFrames(t, ws1)
	writeWSText(t, ws1, map[string]any{"type": "channel_open", "channel": "browser"})
	if _, id := readChannelOpened(t, ws1); id != 1 {
		t.Fatalf("first client id = %d, want 1", id)
	}
	sink1 := c.activeSink.Load()
	if sink1 == nil {
		t.Fatal("no sink installed for first client")
	}

	// Takeover: ws2 swaps in its own sink and unbinds sink1's pre-swap
	// bindings. ws1's readLoop is still winding down at this point.
	ws2 := mutCDialChat(t, c)
	drainInitialFrames(t, ws2)

	// Straggler dispatch — the exact call a late channel_open from ws1's
	// readLoop makes, bound to ws1's (now superseded) sink.
	c.handleChannelOpen(sink1, "browser")

	// Wait out ws1's handler goroutine so its cleanup has run.
	waitFor(300*time.Millisecond, func() bool { return false })

	// The new client must still be able to open the channel. A leaked
	// binding surfaces here as channel_error "already open" instead.
	writeWSText(t, ws2, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id := readChannelOpened(t, ws2)
	if id < 2 {
		t.Fatalf("reopen id = %d, want a fresh id >= 2 (ids never reuse)", id)
	}
}

// TestSupersededAfterBind closes the residual gate/bind window: a binding
// that lands on a sink a takeover already replaced is torn down at once and
// the channel stays openable — the active client never sees "already open".
func TestSupersededAfterBind(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	registerChanProvider(t, hub, "browser")

	ws1 := mutCDialChat(t, c)
	drainInitialFrames(t, ws1)
	sink1 := c.activeSink.Load()
	if sink1 == nil {
		t.Fatal("no sink installed for first client")
	}
	ws2 := mutCDialChat(t, c)
	drainInitialFrames(t, ws2)
	sink2 := c.activeSink.Load()
	if sink2 == nil || sink2 == sink1 {
		t.Fatal("second client must install a distinct sink")
	}

	// The active client opens normally — pins the false path: no error, no
	// premature close, the binding survives.
	writeWSText(t, ws2, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id2 := readChannelOpened(t, ws2)
	if id2 < 1 {
		t.Fatalf("active client open id = %d", id2)
	}

	// A bind that landed on the superseded sink after the swap: the helper
	// must close it and tell the (now active) client why.
	c.finishChannelOpen(sink1, "browser", id2)
	frame := readUntilType(t, ws2, "channel_error")
	if !strings.Contains(string(frame), "connection superseded") {
		t.Fatalf("expected superseded error, got %s", frame)
	}

	// The straggler binding is gone: a fresh open succeeds with a new id.
	writeWSText(t, ws2, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id3 := readChannelOpened(t, ws2)
	if id3 == id2 {
		t.Fatalf("reopen reused id %d (ids never reuse)", id3)
	}

	// The still-active sink is NOT superseded — the same helper must deliver
	// the channel_opened reply (this duplicate confirm also pins the false
	// leg: a false positive would have closed id3 and errored instead).
	c.finishChannelOpen(sink2, "browser", id3)
	frame = readUntilType(t, ws2, "channel_opened")
	var confirm struct {
		ChannelID uint8 `json:"channelId"`
	}
	if json.Unmarshal(frame, &confirm) != nil || confirm.ChannelID != id3 {
		t.Fatalf("expected channel_opened confirm for id %d, got %s", id3, frame)
	}
}

// TestChannelGuardsIgnoreMalformedClientTraffic pins the readLoop guards:
// empty binary frames, channel_open with an empty channel and channel_close
// with a bad payload are silently ignored and the connection stays usable.
func TestChannelGuardsIgnoreMalformedClientTraffic(t *testing.T) {
	c := newTestConnector(t)
	hub := startTestTransport(t, c)
	registerChanProvider(t, hub, "browser")

	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{}); err != nil {
		t.Fatal(err)
	}
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": ""})
	writeWSText(t, ws, map[string]any{"type": "channel_open"})
	writeWSText(t, ws, map[string]any{"type": "channel_close", "channelId": 99, "bogus": true})

	// Negative window: none of the above may produce any frame.
	waitFor(150*time.Millisecond, func() bool { return false })

	// The connection is still fully alive: a valid open succeeds and the
	// text-lane close notification comes back as a text message.
	writeWSText(t, ws, map[string]any{"type": "channel_open", "channel": "browser"})
	_, id := readChannelOpened(t, ws)
	writeWSText(t, ws, map[string]any{"type": "channel_close", "channelId": id})
	frame := readUntilType(t, ws, "channel_close")
	var msg struct {
		ChannelID uint8 `json:"channelId"`
	}
	if err := json.Unmarshal(frame, &msg); err != nil || msg.ChannelID != id {
		t.Fatalf("channel_close text notification wrong: %s", frame)
	}
}
