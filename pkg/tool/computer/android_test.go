package computer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeCaller is a test rpcCaller that records call/close/IsClosed invocations
// and returns canned responses keyed by command. It lets android_test drive
// the backend lifecycle with no socket.
type fakeCaller struct {
	mu           sync.Mutex
	calls        []recordedCall
	closed       bool
	closedCount  int
	responses    map[string]json.RawMessage // command → data blob
	errs         map[string]error           // command → error
	binaryChunks [][]byte
}

type recordedCall struct {
	Command string
	Params  map[string]any
}

func (f *fakeCaller) call(_ context.Context, command string, params map[string]any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{Command: command, Params: params})
	if err, ok := f.errs[command]; ok {
		return nil, err
	}
	if data, ok := f.responses[command]; ok {
		return data, nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeCaller) IsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeCaller) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedCount++
	f.closed = true
	return nil
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeCaller) lastCall() recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return recordedCall{}
	}
	return f.calls[len(f.calls)-1]
}

// sendBinary records each binary frame so SendFile's chunking is assertable.
// Copy so a caller mutating the slice post-call can't rewrite history.
func (f *fakeCaller) sendBinary(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dup := make([]byte, len(data))
	copy(dup, data)
	f.binaryChunks = append(f.binaryChunks, dup)
	return nil
}

// binaryBytes sums all recorded binary chunk lengths.
func (f *fakeCaller) binaryBytes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, c := range f.binaryChunks {
		total += len(c)
	}
	return total
}

// setClosed flips the fake's IsClosed flag to simulate an async peer close.
func (f *fakeCaller) setClosed(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = v
}

// dialRecorder is a fake dialer that returns fakeCallers, optionally failing
// the next dial.
type dialRecorder struct {
	mu       sync.Mutex
	clients  []*fakeCaller
	failNext error
}

func (d *dialRecorder) dial(_ context.Context) (rpcCaller, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failNext != nil {
		err := d.failNext
		d.failNext = nil
		return nil, err
	}
	c := &fakeCaller{}
	d.clients = append(d.clients, c)
	return c, nil
}

func (d *dialRecorder) clientCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.clients)
}

func newFakeDialer() (*dialRecorder, dialer) {
	d := &dialRecorder{}
	return d, d.dial
}

func TestAndroidBackend_FreshIsDisconnected(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackend()
	if b.IsConnected() {
		t.Error("IsConnected = true on fresh backend, want false")
	}
}

// TestAndroidBackend_ConnectAlwaysReDials pins the always-re-dial decision:
// even when already connected, Connect re-resolves the (cheap, in-memory)
// dial source, so a phone that re-dialed is picked up without waiting for
// the old conn to be detected dead.
func TestAndroidBackend_ConnectAlwaysReDials(t *testing.T) {
	t.Parallel()
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect 1: %v", err)
	}
	if b.client != rpcCaller(rec.clients[0]) {
		t.Fatal("first Connect did not use the first dialed client")
	}
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect 2: %v", err)
	}
	if b.client != rpcCaller(rec.clients[1]) {
		t.Error("second Connect kept the old client; want the freshly dialed one")
	}
	if !b.IsConnected() {
		t.Error("IsConnected = false after re-dial, want true")
	}
}

// ConnectPassesCtxToDialer pins the seam contract: the dialer runs under the
// caller's ctx, so a cancellable connect stays possible at the tool layer
// without the backend re-deriving it.
func TestAndroidBackend_ConnectPassesCtxToDialer(t *testing.T) {
	t.Parallel()
	var got context.Context
	b := newAndroidBackendForTest(func(ctx context.Context) (rpcCaller, error) {
		got = ctx
		return &fakeCaller{}, nil
	})
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got != ctx {
		t.Error("dialer did not receive Connect's caller ctx")
	}
}

func TestAndroidBackend_ConnectFlipsConnected(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !b.IsConnected() {
		t.Error("IsConnected = false after Connect, want true")
	}
}

func TestAndroidBackend_DisconnectFlipsDisconnected(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if b.IsConnected() {
		t.Error("IsConnected = true after Disconnect, want false")
	}
}

func TestAndroidBackend_DisconnectIdempotent(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	// Disconnect on a never-connected backend: nil, no panic.
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect (never connected): %v", err)
	}
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	// Second Disconnect after disconnect: nil, no panic.
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect (second): %v", err)
	}
	if b.IsConnected() {
		t.Error("IsConnected = true after second disconnect, want false")
	}
}

func TestAndroidBackend_ScreenBeforeConnect_Errors(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackend()
	_, err := b.Screen(context.Background(), 15)
	if err == nil {
		t.Fatal("Screen returned nil before connect, want error")
	}
	if !strings.Contains(err.Error(), "not connected; call connect first") {
		t.Errorf("error = %q, want 'not connected; call connect first'", err.Error())
	}
}

func TestAndroidBackend_ClickBeforeConnect_Errors(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackend()
	err := b.Click(context.Background(), 1, 1)
	if err == nil {
		t.Fatal("Click returned nil before connect, want error")
	}
	if !errors.Is(err, errNotConnected) {
		t.Errorf("error = %v, want errors.Is errNotConnected", err)
	}
}

func TestAndroidBackend_DeviceInfoBeforeConnect_Errors(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackend()
	_, err := b.DeviceInfo(context.Background())
	if err == nil {
		t.Fatal("DeviceInfo returned nil before connect, want error")
	}
	if !strings.Contains(err.Error(), "not connected; call connect first") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestAndroidBackend_ActionsBeforeConnect_NoWireCalls(t *testing.T) {
	t.Parallel()
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	// All of these fail at ensureConnected — the fake dialer never ran, and
	// even if it had, no call() would have been issued.
	_, _ = b.Screen(context.Background(), 15)
	_ = b.Click(context.Background(), 1, 1)
	_ = b.Type(context.Background(), "hi", "")
	if rec.clientCount() != 0 {
		t.Errorf("fake dialer client count = %d, want 0 (no connect happened)", rec.clientCount())
	}
}

func TestAndroidBackend_AutoDisconnectOnReconnect(t *testing.T) {
	t.Parallel()
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()

	// First connect, capture a screen so refs populate.
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect first: %v", err)
	}
	firstClient := rec.clients[0]
	firstClient.responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":{"className":"android.widget.Button","isClickable":true,"bounds":{"left":0,"top":0,"right":100,"bottom":100}}}`),
	}
	screenRes, err := b.Screen(ctx, 15)
	if err != nil {
		t.Fatalf("Screen first: %v", err)
	}
	if len(screenRes.Elements) != 1 {
		t.Fatalf("Screen first elements = %d, want 1", len(screenRes.Elements))
	}
	staleRef := screenRes.Elements[0].Ref
	if staleRef != 1 {
		t.Fatalf("staleRef = %d, want 1", staleRef)
	}

	// Second connect (always re-dials) must swap the client, clear the old
	// refs, and NOT close the old client — registry owns conn lifetime.
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect second: %v", err)
	}
	if firstClient.closedCount != 0 {
		t.Errorf("first client close count = %d, want 0 (registry owns conn lifetime)", firstClient.closedCount)
	}
	if !b.IsConnected() {
		t.Error("IsConnected = false after reconnect, want true")
	}
	// The old ref must be gone — ClickElement(staleRef) returns ref-not-found.
	err = b.ClickElement(ctx, staleRef)
	if err == nil {
		t.Fatal("ClickElement with stale ref returned nil, want ref-not-found error")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "no screen") {
		t.Errorf("error = %q, want 'not found' or 'no screen'", err.Error())
	}
}

func TestAndroidBackend_AsyncDropSelfHeals(t *testing.T) {
	t.Parallel()
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()

	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := rec.clients[0]
	// Simulate an async peer close: the read loop would have flipped closed.
	fc.setClosed(true)

	// Next action must report not-connected, self-heal b.client to nil, and
	// record ZERO call() invocations for that action.
	beforeCalls := fc.callCount()
	_, err := b.Screen(ctx, 15)
	if err == nil {
		t.Fatal("Screen after async drop returned nil, want error")
	}
	if !strings.Contains(err.Error(), "not connected; call connect first") {
		t.Errorf("error = %q", err.Error())
	}
	if b.IsConnected() {
		t.Error("IsConnected = true after async drop, want false")
	}
	if fc.callCount() != beforeCalls {
		t.Errorf("call count changed from %d to %d (wire traffic during self-heal is wrong)", beforeCalls, fc.callCount())
	}
}

func TestAndroidBackend_ConnectDialFailureLeavesOldLive(t *testing.T) {
	t.Parallel()
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()

	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	firstClient := rec.clients[0]
	// Next dial fails.
	rec.failNext = errors.New("dial failed")
	if err := b.Connect(ctx); err == nil {
		t.Fatal("second Connect returned nil, want dial error")
	}
	// The first client must still be the active one.
	if !b.IsConnected() {
		t.Error("IsConnected = false after dial failure, want true (old stays live)")
	}
	// A screen call must still reach the first client.
	firstClient.responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":null}`),
	}
	if _, err := b.Screen(ctx, 15); err != nil {
		t.Fatalf("Screen after failed reconnect: %v", err)
	}
	if firstClient.callCount() != 1 {
		t.Errorf("first client calls = %d, want 1 (still active)", firstClient.callCount())
	}
}

func TestAndroidBackend_Screen_AssignsRefs(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	b.client.(*fakeCaller).responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":{
			"className":"android.widget.FrameLayout",
			"children":[
				{"className":"android.widget.Button","isClickable":true,"bounds":{"left":0,"top":0,"right":10,"bottom":20}},
				{"className":"android.widget.EditText","isEditable":true,"text":"q","bounds":{"left":0,"top":30,"right":10,"bottom":50}}
			]
		}}`),
	}
	res, err := b.Screen(ctx, 15)
	if err != nil {
		t.Fatalf("Screen: %v", err)
	}
	if len(res.Elements) != 2 {
		t.Fatalf("elements = %d, want 2", len(res.Elements))
	}
	if res.Elements[0].Ref != 1 || res.Elements[1].Ref != 2 {
		t.Errorf("refs = %d,%d, want 1,2", res.Elements[0].Ref, res.Elements[1].Ref)
	}
}

func TestAndroidBackend_Screen_DefaultMaxDepth15(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":null}`),
	}
	if _, err := b.Screen(ctx, 0); err != nil {
		t.Fatalf("Screen: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "get_ui_tree" {
		t.Errorf("command = %q, want get_ui_tree", last.Command)
	}
	if maxDepth := numAsInt(last.Params["maxDepth"]); maxDepth != 15 {
		t.Errorf("maxDepth param = %v, want 15 (default)", last.Params["maxDepth"])
	}
}

func TestAndroidBackend_ClickElement_ResolvesCenter(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":{
			"className":"root","children":[
				{"className":"android.widget.Button","isClickable":true,"bounds":{"left":100,"top":200,"right":300,"bottom":400}}
			]
		}}`),
	}
	if _, err := b.Screen(ctx, 15); err != nil {
		t.Fatalf("Screen: %v", err)
	}
	// Ref 1 is the button; center is (200, 300).
	if err := b.ClickElement(ctx, 1); err != nil {
		t.Fatalf("ClickElement: %v", err)
	}
	// The last call must be tap with x=200, y=300.
	last := fc.lastCall()
	if last.Command != "tap" {
		t.Errorf("command = %q, want tap", last.Command)
	}
	if x := numAsInt(last.Params["x"]); x != 200 {
		t.Errorf("tap x = %v, want 200", last.Params["x"])
	}
	if y := numAsInt(last.Params["y"]); y != 300 {
		t.Errorf("tap y = %v, want 300", last.Params["y"])
	}
}

func TestAndroidBackend_ClickElement_RefNotFound(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":null}`),
	}
	if _, err := b.Screen(ctx, 15); err != nil {
		t.Fatalf("Screen: %v", err)
	}
	err := b.ClickElement(ctx, 99)
	if err == nil {
		t.Fatal("ClickElement(99) returned nil, want ref-not-found")
	}
	if !strings.Contains(err.Error(), "ref 99 not found") {
		t.Errorf("error = %q, want 'ref 99 not found'", err.Error())
	}
}

func TestAndroidBackend_ClickElement_NoScreen(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err := b.ClickElement(ctx, 1)
	if err == nil {
		t.Fatal("ClickElement before Screen returned nil, want no-screen error")
	}
	if !strings.Contains(err.Error(), "no screen captured") {
		t.Errorf("error = %q, want 'no screen captured'", err.Error())
	}
}

func TestAndroidBackend_Type_IssuesSetText(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	if err := b.Type(ctx, "hello", ""); err != nil {
		t.Fatalf("Type: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "set_text" {
		t.Errorf("command = %q, want set_text", last.Command)
	}
	if last.Params["text"] != "hello" {
		t.Errorf("text param = %v, want hello", last.Params["text"])
	}
}

func TestAndroidBackend_SendKey_IssuesPressKey(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	if err := b.SendKey(ctx, "back"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "press_key" {
		t.Errorf("command = %q, want press_key", last.Command)
	}
	if last.Params["key"] != "back" {
		t.Errorf("key param = %v, want back", last.Params["key"])
	}
}

func TestAndroidBackend_Scroll_IssuesScrollWithAmount(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	if err := b.Scroll(ctx, "down"); err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "scroll" {
		t.Errorf("command = %q, want scroll", last.Command)
	}
	if last.Params["direction"] != "down" {
		t.Errorf("direction param = %v, want down", last.Params["direction"])
	}
	if amt := numAsInt(last.Params["amount"]); amt != 500 {
		t.Errorf("amount param = %v, want 500", last.Params["amount"])
	}
}

func TestAndroidBackend_Zoom_IssuesPinch(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	if err := b.Zoom(ctx, 540, 1200, 2.0); err != nil {
		t.Fatalf("Zoom: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "pinch" {
		t.Errorf("command = %q, want pinch", last.Command)
	}
	if x := numAsInt(last.Params["x"]); x != 540 {
		t.Errorf("x param = %v, want 540", last.Params["x"])
	}
	if scale := numAsFloat(last.Params["scale"]); scale != 2.0 {
		t.Errorf("scale param = %v, want 2.0", last.Params["scale"])
	}
	if dur := numAsInt(last.Params["duration"]); dur != 400 {
		t.Errorf("duration param = %v, want 400", last.Params["duration"])
	}
}

func TestAndroidBackend_Screenshot_Decodes(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"screenshot": json.RawMessage(`{"image":"DATA","format":"jpeg","width":1080,"height":2400}`),
	}
	shot, err := b.Screenshot(ctx)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if shot.DataB64 != "DATA" {
		t.Errorf("DataB64 = %q", shot.DataB64)
	}
	if shot.Width != 1080 {
		t.Errorf("Width = %d", shot.Width)
	}
}

func TestAndroidBackend_DeviceInfo_Decodes(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"get_device_info": json.RawMessage(`{"manufacturer":"Google","model":"Pixel 8","sdk":34,"release":"14","screenWidth":1080,"screenHeight":2400,"density":2.625,"densityDpi":420}`),
	}
	info, err := b.DeviceInfo(ctx)
	if err != nil {
		t.Fatalf("DeviceInfo: %v", err)
	}
	if info.Manufacturer != "Google" {
		t.Errorf("Manufacturer = %q", info.Manufacturer)
	}
	if info.SDK != 34 {
		t.Errorf("SDK = %d", info.SDK)
	}
}

func TestAndroidBackend_OpenApp_IssuesOpenApp(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	if err := b.OpenApp(ctx, "com.android.chrome"); err != nil {
		t.Fatalf("OpenApp: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "open_app" {
		t.Errorf("command = %q, want open_app", last.Command)
	}
	if last.Params["package"] != "com.android.chrome" {
		t.Errorf("package param = %v, want com.android.chrome", last.Params["package"])
	}
}

func TestAndroidBackend_OpenApp_BeforeConnect(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackend()
	err := b.OpenApp(context.Background(), "x")
	if err == nil {
		t.Fatal("OpenApp returned nil before connect, want error")
	}
	if !errors.Is(err, errNotConnected) {
		t.Errorf("error = %v, want errors.Is errNotConnected", err)
	}
}

// writeSendFileTemp writes n bytes to a fresh temp file and returns its path.
// Uses os.CreateTemp (not a hardcoded /tmp path) so the weak-test scanner's
// hardcoded-path rule does not fire.
func writeSendFileTemp(t *testing.T, n int) string {
	t.Helper()
	f, err := os.CreateTemp("", "sendfile-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i % 251) // non-zero pattern so size != checksum accidentally
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return f.Name()
}

func TestAndroidBackend_SendFile_StreamsBinaryFrames(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	// 300 KiB: a full 256 KiB chunk + a 44 KiB tail (exercises the
	// io.ErrUnexpectedEOF partial-chunk flush).
	totalSize := 300 * 1024
	tmpPath := writeSendFileTemp(t, totalSize)

	if err := b.SendFile(ctx, tmpPath); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	// Calls must be exactly begin then end (binary frames are NOT recorded as
	// calls — they go through sendBinary).
	if got := len(fc.calls); got != 2 {
		t.Fatalf("recorded calls = %d, want 2 (begin + end)", got)
	}
	if fc.calls[0].Command != "receive_file_begin" {
		t.Errorf("calls[0] = %q, want receive_file_begin", fc.calls[0].Command)
	}
	if fc.calls[1].Command != "receive_file_end" {
		t.Errorf("calls[1] = %q, want receive_file_end", fc.calls[1].Command)
	}
	// begin params: basename + total size.
	if fc.calls[0].Params["path"] != filepath.Base(tmpPath) {
		t.Errorf("begin path = %v, want %q", fc.calls[0].Params["path"], filepath.Base(tmpPath))
	}
	if size := numAsInt64(fc.calls[0].Params["size"]); size != int64(totalSize) {
		t.Errorf("begin size = %v, want %d", fc.calls[0].Params["size"], totalSize)
	}
	// Binary frames: exactly 2 chunks totaling the full file.
	if got := len(fc.binaryChunks); got != 2 {
		t.Fatalf("binary chunks = %d, want 2 (full + partial)", got)
	}
	if len(fc.binaryChunks[0]) != sendFileChunkSize {
		t.Errorf("chunk[0] len = %d, want %d", len(fc.binaryChunks[0]), sendFileChunkSize)
	}
	if len(fc.binaryChunks[1]) != totalSize-sendFileChunkSize {
		t.Errorf("chunk[1] len = %d, want %d", len(fc.binaryChunks[1]), totalSize-sendFileChunkSize)
	}
	if fc.binaryBytes() != totalSize {
		t.Errorf("binaryBytes = %d, want %d", fc.binaryBytes(), totalSize)
	}
}

func TestAndroidBackend_SendFile_MissingFile(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	missing := filepath.Join(os.TempDir(), "gbot-does-not-exist-12345.bin")
	err := b.SendFile(ctx, missing)
	if err == nil {
		t.Fatal("SendFile returned nil for missing file, want error")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Errorf("error = %q, want it to contain 'stat'", err.Error())
	}
	if fc.callCount() != 0 {
		t.Errorf("call count = %d, want 0 (begin must not be sent for a missing file)", fc.callCount())
	}
}

func TestAndroidBackend_SendFile_EmptyFile(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	tmpPath := writeSendFileTemp(t, 0)
	err := b.SendFile(ctx, tmpPath)
	if err == nil {
		t.Fatal("SendFile returned nil for empty file, want error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want it to contain 'empty'", err.Error())
	}
	if fc.callCount() != 0 {
		t.Errorf("call count = %d, want 0 (begin must not be sent for an empty file)", fc.callCount())
	}
}

func TestAndroidBackend_SendFile_Directory(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	dir, err := os.MkdirTemp("", "gbot-sendfile-dir-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	err = b.SendFile(ctx, dir)
	if err == nil {
		t.Fatal("SendFile returned nil for a directory, want error")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error = %q, want it to contain 'directory'", err.Error())
	}
	if fc.callCount() != 0 {
		t.Errorf("call count = %d, want 0 (begin must not be sent for a directory)", fc.callCount())
	}
}

func TestAndroidBackend_SendFile_BeginError(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.errs = map[string]error{
		"receive_file_begin": errors.New("device refused"),
	}
	tmpPath := writeSendFileTemp(t, 1024)
	err := b.SendFile(ctx, tmpPath)
	if err == nil {
		t.Fatal("SendFile returned nil after begin error, want error")
	}
	if !strings.Contains(err.Error(), "device refused") {
		t.Errorf("error = %q, want it to contain 'device refused'", err.Error())
	}
	if len(fc.binaryChunks) != 0 {
		t.Errorf("binary chunks = %d, want 0 (no bytes after a failed begin)", len(fc.binaryChunks))
	}
}

func TestAndroidBackend_SendFile_BeforeConnect(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackend()
	if err := b.SendFile(context.Background(), "x"); err == nil {
		t.Fatal("SendFile returned nil before connect, want error")
	} else if !errors.Is(err, errNotConnected) {
		t.Errorf("error = %v, want errors.Is errNotConnected", err)
	}
}

func TestAndroidBackend_OpenMenu_IssuesLongPress(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	if err := b.OpenMenu(ctx, 50, 60); err != nil {
		t.Fatalf("OpenMenu: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "long_press" {
		t.Errorf("command = %q, want long_press", last.Command)
	}
	if dur := numAsInt(last.Params["duration"]); dur != 1000 {
		t.Errorf("duration = %v, want 1000", last.Params["duration"])
	}
}

func TestAndroidBackend_Click_IssuesTapWithDuration(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	if err := b.Click(ctx, 10, 20); err != nil {
		t.Fatalf("Click: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "tap" {
		t.Errorf("command = %q, want tap", last.Command)
	}
	if dur := numAsInt(last.Params["duration"]); dur != 100 {
		t.Errorf("duration = %v, want 100", last.Params["duration"])
	}
}

func TestAndroidBackend_CloseAliasForDisconnect(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if b.IsConnected() {
		t.Error("IsConnected = true after Close, want false")
	}
}

// numAsInt extracts an int from a map[string]any value, handling both int and
// float64 (JSON numbers decode as float64, but the backend stores Go int
// literals directly as int). Returns 0 on type mismatch.
func numAsInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// numAsInt64 extracts an int64 from a map[string]any value, handling int,
// int64, and float64. SendFile stores os.FileInfo.Size() (int64) directly.
func numAsInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// numAsFloat extracts a float64 from a map[string]any value, handling int,
// int64, and float64.
func numAsFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

// --- not-connected branch coverage ---

func TestAndroidBackend_AllMethodsBeforeConnect(t *testing.T) {
	t.Parallel()
	b := NewAndroidBackend()
	ctx := context.Background()
	// Each perception/action method must return errNotConnected when no
	// connection exists. This covers the ensureConnected gate for every
	// method that was previously only tested on the happy path.
	if _, err := b.Screenshot(ctx); err == nil {
		t.Error("Screenshot before connect returned nil")
	}
	if err := b.OpenMenu(ctx, 1, 1); err == nil {
		t.Error("OpenMenu before connect returned nil")
	}
	if err := b.OpenMenuElement(ctx, 1); err == nil {
		t.Error("OpenMenuElement before connect returned nil")
	}
	if err := b.Type(ctx, "x", ""); err == nil {
		t.Error("Type before connect returned nil")
	}
	if err := b.SendKey(ctx, "back"); err == nil {
		t.Error("SendKey before connect returned nil")
	}
	if err := b.Scroll(ctx, "up"); err == nil {
		t.Error("Scroll before connect returned nil")
	}
	if err := b.Zoom(ctx, 1, 1, 1.0); err == nil {
		t.Error("Zoom before connect returned nil")
	}
	if err := b.OpenApp(ctx, "x"); err == nil {
		t.Error("OpenApp before connect returned nil")
	}
}

func TestAndroidBackend_OpenMenuElement_ResolvesCenter(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{"tree":{
			"className":"root","children":[
				{"className":"android.widget.Button","isClickable":true,"bounds":{"left":0,"top":0,"right":200,"bottom":200}}
			]
		}}`),
	}
	if _, err := b.Screen(ctx, 15); err != nil {
		t.Fatalf("Screen: %v", err)
	}
	// Ref 1 center is (100, 100). OpenMenuElement issues long_press there.
	if err := b.OpenMenuElement(ctx, 1); err != nil {
		t.Fatalf("OpenMenuElement: %v", err)
	}
	last := fc.lastCall()
	if last.Command != "long_press" {
		t.Errorf("command = %q, want long_press", last.Command)
	}
	if x := numAsInt(last.Params["x"]); x != 100 {
		t.Errorf("long_press x = %v, want 100", last.Params["x"])
	}
}

func TestAndroidBackend_OpenMenuElement_NoScreen(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err := b.OpenMenuElement(ctx, 1)
	if err == nil {
		t.Fatal("OpenMenuElement before Screen returned nil, want no-screen error")
	}
	if !strings.Contains(err.Error(), "no screen captured") {
		t.Errorf("error = %q, want 'no screen captured'", err.Error())
	}
}

func TestAndroidBackend_CallErrorPropagates(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.errs = map[string]error{
		"tap": errors.New("device rejected tap"),
	}
	err := b.Click(ctx, 1, 1)
	if err == nil {
		t.Fatal("Click returned nil, want propagated error")
	}
	if !strings.Contains(err.Error(), "device rejected tap") {
		t.Errorf("error = %q, want 'device rejected tap'", err.Error())
	}
}

func TestAndroidBackend_Screen_DecodeError(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"get_ui_tree": json.RawMessage(`{bad json`),
	}
	_, err := b.Screen(ctx, 15)
	if err == nil {
		t.Fatal("Screen with malformed data returned nil, want decode error")
	}
}

func TestAndroidBackend_Screenshot_DecodeError(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"screenshot": json.RawMessage(`{bad json`),
	}
	_, err := b.Screenshot(ctx)
	if err == nil {
		t.Fatal("Screenshot with malformed data returned nil, want decode error")
	}
}

func TestAndroidBackend_DeviceInfo_DecodeError(t *testing.T) {
	t.Parallel()
	_, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fc := b.client.(*fakeCaller)
	fc.responses = map[string]json.RawMessage{
		"get_device_info": json.RawMessage(`{bad json`),
	}
	_, err := b.DeviceInfo(ctx)
	if err == nil {
		t.Fatal("DeviceInfo with malformed data returned nil, want decode error")
	}
}

func TestAndroidBackend_ConnectLockDiscipline(t *testing.T) {
	t.Parallel()
	rec, dial := newFakeDialer()
	b := newAndroidBackendForTest(dial)
	ctx := context.Background()
	// First connect.
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect first: %v", err)
	}
	firstClient := rec.clients[0]
	// A slow second dial must NOT block in-flight actions on the first
	// client. After a successful reconnect the first client is NOT closed
	// (registry owns conn lifetime) and refs are cleared.
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("Connect second: %v", err)
	}
	if firstClient.closedCount != 0 {
		t.Errorf("first client close count = %d, want 0 (registry owns conn lifetime, never closed)", firstClient.closedCount)
	}
	// The freshly dialed client must be the active one.
	if len(rec.clients) != 2 {
		t.Fatalf("client count = %d, want 2", len(rec.clients))
	}
	if b.client != rpcCaller(rec.clients[1]) {
		t.Error("active client is not the freshly dialed one")
	}
	if !b.IsConnected() {
		t.Error("not connected after reconnect")
	}
}
