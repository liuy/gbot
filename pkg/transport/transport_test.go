package transport

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// mockSink records everything the hub pushes to a wui client.
type mockSink struct {
	mu     sync.Mutex
	bin    [][]byte
	jsons  []string
	onJSON func(string)
}

func (m *mockSink) SendBinary(frame []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(frame))
	copy(cp, frame)
	m.bin = append(m.bin, cp)
}

func (m *mockSink) SendJSON(payload []byte) {
	m.mu.Lock()
	s := string(payload)
	cbs := m.onJSON
	m.mu.Unlock()
	m.mu.Lock()
	m.jsons = append(m.jsons, s)
	m.mu.Unlock()
	if cbs != nil {
		cbs(s)
	}
}

func (m *mockSink) frames() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.bin))
	copy(out, m.bin)
	return out
}

func (m *mockSink) jsonFrames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.jsons))
	copy(out, m.jsons)
	return out
}

// testProvider is the in-process provider double: frames from the client
// land in recv, provider death fires gone exactly once.
type testProvider struct {
	hub    *Hub
	handle *InprocHandle
	recv   chan []byte
	gone   chan struct{}
	once   sync.Once
}

func startProvider(t *testing.T, hub *Hub, plugin string) *testProvider {
	t.Helper()
	tp := &testProvider{hub: hub, recv: make(chan []byte, 64), gone: make(chan struct{})}
	tp.handle = hub.RegisterInproc(plugin, func(p []byte) { tp.recv <- p }, func() {
		tp.once.Do(func() { close(tp.gone) })
	})
	if tp.handle == nil {
		t.Fatal("RegisterInproc returned nil handle")
	}
	return tp
}

// deliver pushes a provider→client payload (JS module side).
func (tp *testProvider) deliver(payload string) {
	tp.handle.Deliver([]byte(payload))
}

// clientSend simulates the client writing a frame on channel id.
func (tp *testProvider) awaitClientFrame(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-tp.recv:
		if string(got) != want {
			t.Fatalf("provider frame: want %q got %q", want, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("provider never received %q", want)
	}
}

func waitReal(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)     // REAL-TIME
	for time.Now().Before(deadline) { // REAL-TIME
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestRegisterReplaceClosesOldProvider(t *testing.T) {
	hub := New()
	a := startProvider(t, hub, "plugA")
	startProvider(t, hub, "plugA")
	select {
	case <-a.gone:
	case <-time.After(time.Second):
		t.Fatal("replaced provider must be closed")
	}
	if !hub.Connected("plugA") {
		t.Fatal("successor provider must be registered")
	}
}

func TestRegisterReplaceWhileBoundNotifiesClient(t *testing.T) {
	hub := New()
	startProvider(t, hub, "plugA")
	sink := &mockSink{}
	id, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatal(err)
	}
	startProvider(t, hub, "plugA") // replacement
	waitReal(t, time.Second, func() bool { return len(sink.jsonFrames()) == 1 }, "close notification")
	if jsons := sink.jsonFrames(); len(jsons) != 1 || !strings.Contains(jsons[0], "channel_close") {
		t.Fatalf("replacement must notify the client: %v", jsons)
	}
	id2, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id {
		t.Fatal("reopen must allocate a fresh id")
	}
}

func TestRegisterInprocNilCallbacks(t *testing.T) {
	hub := New()
	if hub.RegisterInproc("plugA", nil, nil) != nil {
		t.Fatal("nil onFrame must yield a nil handle, not a panic later")
	}
}

func TestConnectedTracksProviderPresence(t *testing.T) {
	hub := New()
	if hub.Connected("plugA") {
		t.Fatal("no provider registered yet")
	}
	tp := startProvider(t, hub, "plugA")
	if !hub.Connected("plugA") {
		t.Fatal("provider registered")
	}
	tp.handle.Close()
	if hub.Connected("plugA") {
		t.Fatal("provider closed")
	}
}

func TestChannelRoundTripAndDropSemantics(t *testing.T) {
	hub := New()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	id, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatal(err)
	}

	// client → provider
	hub.ChannelSend(id, []byte(`{"m":"hello"}`))
	tp.awaitClientFrame(t, `{"m":"hello"}`)

	// provider → client, stamped with the binding id at delivery time
	tp.deliver(`{"r":"ok"}`)
	waitReal(t, time.Second, func() bool { return len(sink.frames()) == 1 }, "frame to client")
	f := sink.frames()[0]
	if f[0] != id || string(f[1:]) != `{"r":"ok"}` {
		t.Fatalf("frame must carry id %d and payload: %v", id, f)
	}

	// close → provider frames no longer routed, client frames dropped
	hub.CloseChannel(id)
	tp.deliver(`{"dropped":1}`)
	hub.ChannelSend(id, []byte(`{"also":"dropped"}`))
	time.Sleep(300 * time.Millisecond) // REAL-TIME: settle window for "no frame arrives"
	if n := len(sink.frames()); n != 1 {
		t.Fatalf("post-close frames must not reach the client: %d", n)
	}
	select {
	case got := <-tp.recv:
		t.Fatalf("post-close client frame reached provider: %q", got)
	default:
	}
}

func TestProviderDeathNotifiesClient(t *testing.T) {
	hub := New()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	tp.handle.Close()
	waitReal(t, time.Second, func() bool { return len(sink.jsonFrames()) == 1 }, "close notification")
	if jsons := sink.jsonFrames(); len(jsons) != 1 || !strings.Contains(jsons[0], "channel_close") {
		t.Fatalf("provider death must notify: %v", jsons)
	}
	if _, err := hub.OpenChannel(sink, "plugA"); err == nil {
		t.Fatal("open after provider death must fail")
	}
}

func TestChannelSendDeadProviderUnbinds(t *testing.T) {
	hub := New()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	id, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatal(err)
	}
	// Kill the provider WITHOUT unregistering: the next client write takes
	// the writeFrame-failure cleanup path.
	tp.handle.prov.close()
	hub.ChannelSend(id, []byte(`{"m":1}`))
	waitReal(t, time.Second, func() bool { return len(sink.jsonFrames()) == 1 }, "unbind notification")
	hub.mu.Lock()
	_, hasProv := hub.providers["plugA"]
	_, bound := hub.byName["plugA"]
	hub.mu.Unlock()
	if hasProv {
		t.Fatal("dead provider must be unregistered")
	}
	if bound {
		t.Fatal("binding must be dropped")
	}
}

func TestChannelSendMissingProviderUnbinds(t *testing.T) {
	hub := New()
	startProvider(t, hub, "plugA")
	sink := &mockSink{}
	id, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatal(err)
	}
	// Remove the provider behind the binding's back.
	hub.mu.Lock()
	delete(hub.providers, "plugA")
	hub.mu.Unlock()
	hub.ChannelSend(id, []byte(`x`))
	hub.mu.Lock()
	_, bound := hub.byName["plugA"]
	hub.mu.Unlock()
	if bound {
		t.Fatal("send against missing provider must drop the binding")
	}
}

func TestUnbindClientOnlyOwnSink(t *testing.T) {
	hub := New()
	startProvider(t, hub, "plugA")
	startProvider(t, hub, "plugB")
	sinkA := &mockSink{}
	sinkB := &mockSink{}
	if _, err := hub.OpenChannel(sinkA, "plugA"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.OpenChannel(sinkB, "plugB"); err != nil {
		t.Fatal(err)
	}
	hub.UnbindClient(sinkA)
	hub.mu.Lock()
	_, aBound := hub.byName["plugA"]
	_, bBound := hub.byName["plugB"]
	hub.mu.Unlock()
	if aBound {
		t.Fatal("sinkA pipes must be unbound")
	}
	if !bBound {
		t.Fatal("sinkB pipes must survive")
	}
}

func TestSameSinkReopenReplacesLeakedBinding(t *testing.T) {
	hub := New()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	// The leaked binding: same sink, same plugin, client "forgot" it.
	id, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatalf("same-sink reopen must replace the leaked binding, got %v", err)
	}
	if id == 0 {
		t.Fatal("rebind must allocate a fresh id")
	}
	// Exactly one live binding for the plugin afterwards.
	hub.mu.Lock()
	_, nameOpen := hub.byName["plugA"]
	hub.mu.Unlock()
	if !nameOpen {
		t.Fatal("rebind must leave the plugin pipe open")
	}
	// A different sink still may not steal it.
	if _, err := hub.OpenChannel(&mockSink{}, "plugA"); err == nil {
		t.Fatal("cross-sink open must stay rejected")
	}
	// Silent replace: the hub sends no channel_close for the abandoned id —
	// a self-directed notification would trip the reconnect state the client
	// is climbing out of (pinned: the replace path sends nothing).
	if n := len(sink.jsonFrames()); n != 0 {
		t.Fatalf("same-sink replace must not notify the asking sink, got %v", sink.jsonFrames())
	}
	// Deliver routes by the CURRENT binding: everything lands under the
	// fresh id; the abandoned id itself must never appear as a prefix.
	tp.deliver(`{"stale":1}`)
	tp.deliver(`{"fresh":1}`)
	waitReal(t, 2*time.Second, func() bool { return len(sink.frames()) == 2 }, "both frames")
	frames := sink.frames()
	for _, f := range frames {
		if f[0] != id {
			t.Fatalf("frames must deliver under the fresh id only: %v", frames)
		}
	}
	if string(frames[len(frames)-1][1:]) != `{"fresh":1}` {
		t.Fatalf("fresh payload must be last: %v", frames)
	}
}

// TestCloseChannelByName: the mid-connecting close has no id — the name is
// the only handle. In-package coverage for the hub-side name-keyed teardown
// (the behavioral test lives in pkg/wui).
func TestCloseChannelByName(t *testing.T) {
	hub := New()
	startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	hub.CloseChannelByName("plugA")
	hub.mu.Lock()
	_, open := hub.byName["plugA"]
	hub.mu.Unlock()
	if open {
		t.Fatal("name-keyed close must free the binding")
	}
	// Unknown names are a silent no-op.
	hub.CloseChannelByName("plugA")
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatalf("reopen after name-keyed close: %v", err)
	}
}

func TestOpenChannelIDExhaustion(t *testing.T) {
	hub := New()
	sink := &mockSink{}
	startProvider(t, hub, "plugX")
	if _, err := hub.OpenChannel(sink, "plugX"); err != nil {
		t.Fatal(err)
	}
	hub.CloseChannel(1)

	hub.mu.Lock()
	hub.nextID = maxChannelID
	hub.mu.Unlock()
	if _, err := hub.OpenChannel(sink, "plugX"); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("want exhaustion error, got %v", err)
	}
}

func TestOpenChannelIDBoundary(t *testing.T) {
	hub := New()
	sink := &mockSink{}
	startProvider(t, hub, "plugX")
	hub.mu.Lock()
	hub.nextID = maxChannelID - 1
	hub.mu.Unlock()
	id, err := hub.OpenChannel(sink, "plugX")
	if err != nil {
		t.Fatal(err)
	}
	if id != maxChannelID {
		t.Fatalf("last usable id must be %d, got %d", maxChannelID, id)
	}
	if _, err := hub.OpenChannel(&mockSink{}, "plugX"); err == nil {
		t.Fatal("space must be exhausted after the last id")
	}
}

func TestFrameSizeGuardDropsOversizedDeliver(t *testing.T) {
	hub := New()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	tp.handle.Deliver(make([]byte, maxFrameSize+1))
	time.Sleep(300 * time.Millisecond) // REAL-TIME: settle window for "no frame arrives"
	if n := len(sink.frames()); n != 0 {
		t.Fatalf("oversized deliver must be dropped, got %d frames", n)
	}
	// The provider stays alive and sane frames still flow.
	tp.deliver(`{"ok":1}`)
	waitReal(t, time.Second, func() bool { return len(sink.frames()) == 1 }, "sane frame")
}

func TestFrameSizeGuardClientOversizeUnbinds(t *testing.T) {
	hub := New()
	startProvider(t, hub, "plugA")
	sink := &mockSink{}
	id, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatal(err)
	}
	hub.ChannelSend(id, make([]byte, maxFrameSize+1))
	waitReal(t, time.Second, func() bool { return len(sink.jsonFrames()) == 1 }, "unbind notification")
	hub.mu.Lock()
	_, hasProv := hub.providers["plugA"]
	hub.mu.Unlock()
	if hasProv {
		t.Fatal("oversized client frame must unregister the provider")
	}
}

func TestFrameSizeExactBoundaryDelivered(t *testing.T) {
	hub := New()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	tp.handle.Deliver(make([]byte, maxFrameSize))
	waitReal(t, 2*time.Second, func() bool { return len(sink.frames()) == 1 }, "boundary frame")
	if f := sink.frames()[0]; len(f) != 1+maxFrameSize {
		t.Fatalf("exact-boundary frame must deliver, got %d bytes", len(f))
	}
}

func TestCloseCutsProviders(t *testing.T) {
	hub := New()
	a := startProvider(t, hub, "plugA")
	b := startProvider(t, hub, "plugB")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	select {
	case <-a.gone:
	case <-time.After(time.Second):
		t.Fatal("Close must cut provider A")
	}
	select {
	case <-b.gone:
	case <-time.After(time.Second):
		t.Fatal("Close must cut provider B")
	}
	hub.mu.Lock()
	n := len(hub.providers)
	hub.mu.Unlock()
	if n != 0 {
		t.Fatalf("provider map must be empty after Close, got %d", n)
	}
}

func TestInprocDeliverEmptyDropped(t *testing.T) {
	hub := New()
	defer hub.Close()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	tp.deliver("")
	waitReal(t, 300*time.Millisecond, func() bool { return len(sink.frames()) == 0 }, "empty payload stays dropped")
	if n := len(sink.frames()); n != 0 {
		t.Fatalf("empty deliver must be dropped, got %d frames", n)
	}
}

func TestInprocDeliverAfterClose(t *testing.T) {
	hub := New()
	defer hub.Close()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	tp.handle.Close()
	tp.deliver("late")
	if n := len(sink.frames()); n != 0 {
		t.Fatalf("deliver after provider close must not reach client, got %d", n)
	}
	if hub.Connected("plugA") {
		t.Fatal("closed provider must be unregistered")
	}
}

func TestInprocDoubleClose(t *testing.T) {
	hub := New()
	defer hub.Close()
	tp := startProvider(t, hub, "plugA")
	tp.handle.Close()
	tp.handle.Close() // must not panic or deadlock
	if hub.Connected("plugA") {
		t.Fatal("provider must be gone after close")
	}
}

func TestInprocMultipleSendsBothWays(t *testing.T) {
	hub := New()
	defer hub.Close()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	id, err := hub.OpenChannel(sink, "plugA")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		hub.ChannelSend(id, []byte{byte('a' + i)})
	}
	for i := range 3 {
		tp.deliver(string(rune('A' + i)))
	}
	waitReal(t, 2*time.Second, func() bool {
		return len(sink.frames()) == 3 && len(tp.recv) == 3
	}, "3 frames each direction")
	if n := len(sink.frames()); n != 3 {
		t.Fatalf("client frames = %d, want 3", n)
	}
	if n := len(tp.recv); n != 3 {
		t.Fatalf("provider frames = %d, want 3", n)
	}
}

func TestInprocStaleHandleCloseKeepsNew(t *testing.T) {
	hub := New()
	defer hub.Close()
	old := startProvider(t, hub, "plugA")
	// Re-registration replaces (silent same-sink semantics live elsewhere;
	// here the OLD handle must not tear down the NEW provider).
	newTP := startProvider(t, hub, "plugA")
	old.handle.Close()
	if !hub.Connected("plugA") {
		t.Fatal("stale handle close must not unregister the new provider")
	}
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	newTP.deliver("alive")
	waitReal(t, 1*time.Second, func() bool { return len(sink.frames()) == 1 }, "new provider still delivers")
	if n := len(sink.frames()); n != 1 || string(sink.frames()[0][1:]) != "alive" {
		t.Fatalf("new provider frames = %q, want [alive]", sink.frames())
	}
}

func TestInprocDeliverJustOverConstantLimitDropped(t *testing.T) {
	// Literal-boundary payloads pin maxFrameSize itself (±1 mutants).
	hub := New()
	defer hub.Close()
	tp := startProvider(t, hub, "plugA")
	sink := &mockSink{}
	if _, err := hub.OpenChannel(sink, "plugA"); err != nil {
		t.Fatal(err)
	}
	tp.handle.Deliver(make([]byte, (8<<20)+1))
	tp.handle.Deliver(make([]byte, (8<<20)+2))
	tp.deliver("ok")
	waitReal(t, 1*time.Second, func() bool { return len(sink.frames()) == 1 }, "only the ok frame survives")
	if n := len(sink.frames()); n != 1 {
		t.Fatalf("frames = %d, want exactly 1 (both oversized dropped)", n)
	}
}

func TestProviderGoneFiresOnHubClose(t *testing.T) {
	hub := New()
	tp := startProvider(t, hub, "plugA")
	hub.Close()
	select {
	case <-tp.gone:
	case <-time.After(2 * time.Second):
		t.Fatal("hub close must fire the provider gone callback")
	}
}
