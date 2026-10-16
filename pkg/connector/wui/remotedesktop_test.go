package wui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newRemoteDesktopServer mounts the remote-desktop routes on a fresh mux
// over an isolated HOME (t.Setenv keeps every test off the real ~/.gbot).
func newRemoteDesktopServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	mux := http.NewServeMux()
	RegisterRemoteDesktopRoutes(mux)
	RegisterVNCProxyRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// putRemoteDevices PUTs body to the devices endpoint and returns the status
// code plus the response body text.
func putRemoteDevices(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/remotedesktop", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func TestNormalizeVNCAddr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "ws://127.0.0.1:8006", want: "ws://127.0.0.1:8006"},
		{in: "wss://nas.local:8006/novnc", want: "wss://nas.local:8006/novnc"},
		{in: "http://127.0.0.1:8006", want: "ws://127.0.0.1:8006/websockify"},
		{in: "https://vnc.example.com", want: "wss://vnc.example.com/websockify"},
		{in: "https://vnc.example.com/", want: "wss://vnc.example.com/websockify"},
		{in: "http://127.0.0.1:8006/novnc", want: "ws://127.0.0.1:8006/novnc"},
		{in: "127.0.0.1:8006", want: "ws://127.0.0.1:8006"},
		{in: "nas.local", want: "ws://nas.local"},
		{in: "", wantErr: "address is required"},
		{in: "  ", wantErr: "address is required"},
		{in: "a b", wantErr: "invalid address"},
		{in: "ftp://x", wantErr: "invalid address"},
		{in: "http://", wantErr: "invalid address"},
		{in: "ws://", wantErr: "invalid address"},
	}
	for _, tc := range cases {
		got, err := normalizeVNCAddr(tc.in)
		if tc.wantErr != "" {
			if err == nil {
				t.Errorf("normalizeVNCAddr(%q) = %q, want error %q", tc.in, got, tc.wantErr)
				continue
			}
			if err.Error() != tc.wantErr {
				t.Errorf("normalizeVNCAddr(%q) error = %q, want %q", tc.in, err.Error(), tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeVNCAddr(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeVNCAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRemoteDesktopGetPutRoundTrip(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	seed := `{
  "providers": [{"name": "zhipu", "url": "https://a", "keys": ["k"], "models": {"glm-5.3": {}}}],
  "desktops": [
    {"name": "win11", "addr": "ws://127.0.0.1:8006"},
    {"name": "nas", "addr": "wss://nas.local:8006", "pass": "pw"}
  ]
}`
	path, old := seedSettings(t, seed)

	// GET returns the file's devices in order.
	var got struct {
		Devices []struct {
			Name string `json:"name"`
			Addr string `json:"addr"`
			Pass string `json:"pass"`
		} `json:"devices"`
	}
	resp := getJSON(t, srv.URL+"/api/settings/remotedesktop", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(got.Devices) != 2 {
		t.Fatalf("devices length = %d, want 2", len(got.Devices))
	}
	if got.Devices[0].Name != "win11" || got.Devices[0].Addr != "ws://127.0.0.1:8006" || got.Devices[0].Pass != "" {
		t.Errorf("devices[0] = %+v, want win11 with empty pass", got.Devices[0])
	}
	if got.Devices[1].Name != "nas" || got.Devices[1].Addr != "wss://nas.local:8006" || got.Devices[1].Pass != "pw" {
		t.Errorf("devices[1] = %+v, want nas with pass pw", got.Devices[1])
	}

	// PUT a new one-device list.
	status, body := putRemoteDevices(t, srv, `[{"name":"only","addr":"ws://127.0.0.1:6080"}]`)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", status, body)
	}
	if strings.TrimSpace(body) != `{"ok":true}` {
		t.Errorf("PUT body = %s, want {\"ok\":true}", body)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("file invalid: %v", err)
	}
	if !jsonEqual(t, file["desktops"], json.RawMessage(`[{"name":"only","addr":"ws://127.0.0.1:6080"}]`)) {
		t.Errorf("desktops = %s, want the PUT body", file["desktops"])
	}
	if !jsonEqual(t, file["providers"], json.RawMessage(`[{"name":"zhipu","url":"https://a","keys":["k"],"models":{"glm-5.3":{}}}]`)) {
		t.Errorf("providers = %s, want unchanged", file["providers"])
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf(".bak missing: %v", err)
	}
	if string(bak) != string(old) {
		t.Errorf(".bak must hold the pre-save bytes verbatim")
	}

	// GET again reflects the PUT.
	got.Devices = nil
	getJSON(t, srv.URL+"/api/settings/remotedesktop", &got)
	if len(got.Devices) != 1 || got.Devices[0].Name != "only" || got.Devices[0].Addr != "ws://127.0.0.1:6080" {
		t.Errorf("devices after PUT = %+v, want only", got.Devices)
	}
}

func TestRemoteDesktopPutValidation(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	path, old := seedSettings(t, `{"providers":[{"name":"zhipu","url":"https://a","keys":["k"],"models":{"m":{}}}],"desktops":[{"name":"keep","addr":"ws://127.0.0.1:8006"}]}`)

	cases := []struct {
		name string
		body string
	}{
		{"missing name", `[{"name":"","addr":"ws://127.0.0.1:8006"}]`},
		{"duplicate names", `[
		  {"name":"dupe","addr":"ws://127.0.0.1:1"},
		  {"name":"dupe","addr":"ws://127.0.0.1:2"}]`},
		{"missing addr", `[{"name":"p","addr":""}]`},
		{"name with slash", `[{"name":"a/b","addr":"ws://127.0.0.1:8006"}]`},
		{"malformed JSON", `[{"name":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := putRemoteDevices(t, srv, tc.body)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", status, body)
			}
			var e struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error == "" {
				t.Errorf("body %s must carry a non-empty error", body)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(old) {
				t.Errorf("rejected PUT must leave the file byte-identical")
			}
		})
	}

	// An empty list is VALID — deleting the last device is a normal state.
	status, body := putRemoteDevices(t, srv, `[]`)
	if status != http.StatusOK {
		t.Fatalf("PUT [] status = %d, body %s; an empty list is valid", status, body)
	}
	var file map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("file invalid: %v", err)
	}
	if string(file["desktops"]) != "[]" {
		t.Errorf("desktops = %s, want []", file["desktops"])
	}
	var got struct {
		Devices []json.RawMessage `json:"devices"`
	}
	getJSON(t, srv.URL+"/api/settings/remotedesktop", &got)
	if len(got.Devices) != 0 {
		t.Errorf("devices after PUT [] = %v, want empty", got.Devices)
	}
}

func TestRemoteDesktopPutUndecodableBody(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	seedSettings(t, `{"desktops":[{"name":"keep","addr":"ws://127.0.0.1:8006"}]}`)

	status, body := putRemoteDevices(t, srv, `{"addr":`)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body %s)", status, body)
	}
}

func TestRemoteDesktopPutSaveFailure(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	// A regular file where ~/.gbot should be makes the config dir
	// un-creatable, so the save fails after validation.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home+"/.gbot", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	status, body := putRemoteDevices(t, srv, `[{"name":"p","addr":"ws://127.0.0.1:8006"}]`)
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (body %s)", status, body)
	}
}

func TestRemoteDesktopTestUndecodableBody(t *testing.T) {
	srv := newRemoteDesktopServer(t)

	var e struct {
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/remotedesktop/test", `{"addr":`, &e)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d — only undecodable JSON is a 400 here", resp.StatusCode)
	}
	if e.Error == "" {
		t.Errorf("body must carry a non-empty error")
	}
}

func TestRemoteDesktopTestProbe(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if err := ws.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(echo.Close)
	host := strings.TrimPrefix(echo.URL, "http://")

	probe := func(body string) (bool, int, string, int) {
		t.Helper()
		var res struct {
			OK        bool   `json:"ok"`
			LatencyMs int    `json:"latencyMs"`
			Error     string `json:"error"`
		}
		resp := postSettingsJSON(t, srv.URL+"/api/settings/remotedesktop/test", body, &res)
		return res.OK, res.LatencyMs, res.Error, resp.StatusCode
	}

	for _, addr := range []string{"ws://" + host, host} {
		ok, latencyMs, errMsg, status := probe(`{"addr":"` + addr + `","pass":"pw"}`)
		if status != http.StatusOK {
			t.Fatalf("addr %q: status = %d — probe outcomes are data, not transport failures", addr, status)
		}
		if !ok {
			t.Fatalf("addr %q: ok=false, error %q", addr, errMsg)
		}
		if latencyMs < 1 {
			t.Errorf("addr %q: latencyMs = %d, want >= 1", addr, latencyMs)
		}
	}

	ok, _, errMsg, status := probe(`{"addr":"ws://127.0.0.1:1"}`)
	if status != http.StatusOK {
		t.Fatalf("unreachable status = %d, want 200", status)
	}
	if ok {
		t.Fatal("unreachable addr: ok=true, want false")
	}
	err := errors.New(errMsg)
	if !strings.Contains(err.Error(), "dial tcp") {
		t.Errorf("unreachable error = %q, want it to contain the OS dial error", errMsg)
	}

	ok, _, _, _ = probe(`{"addr":"nope"}`)
	if ok {
		t.Fatal("unresolvable host: ok=true, want false")
	}

	ok, _, errMsg, _ = probe(`{"addr":""}`)
	if ok {
		t.Fatal("empty addr: ok=true, want false")
	}
	if errMsg != "address is required" {
		t.Errorf("empty addr error = %q, want %q", errMsg, "address is required")
	}
}

// wsProxyURL rewrites the httptest http base into the ws scheme clients dial.
func wsProxyURL(srv *httptest.Server, path string) string {
	return "ws://" + strings.TrimPrefix(srv.URL, "http://") + path
}

func TestVNCProxyRelay(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	connCh := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- ws
		for {
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if err := ws.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(device.Close)
	// The http:// seed form exercises normalizeVNCAddr through the proxy path
	// (the echo handler upgrades any path, including /websockify).
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"`+device.URL+`"}]}`)

	dialer := websocket.Dialer{Subprotocols: []string{"binary"}}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/echo"), nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer client.Close()

	small := []byte("gbot-vnc-relay")
	if err := client.WriteMessage(websocket.BinaryMessage, small); err != nil {
		t.Fatalf("write small: %v", err)
	}
	msgType, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read small echo: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Errorf("small echo type = %d, want BinaryMessage", msgType)
	}
	if !bytes.Equal(data, small) {
		t.Errorf("small echo = %q, want %q", data, small)
	}

	big := make([]byte, 64*1024)
	for i := range big {
		big[i] = byte(i % 251)
	}
	if err := client.WriteMessage(websocket.BinaryMessage, big); err != nil {
		t.Fatalf("write big: %v", err)
	}
	msgType, data, err = client.ReadMessage()
	if err != nil {
		t.Fatalf("read big echo: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Errorf("big echo type = %d, want BinaryMessage", msgType)
	}
	if !bytes.Equal(data, big) {
		t.Errorf("big echo len = %d, want %d with identical bytes", len(data), len(big))
	}

	// Raw TCP close on the device side — no WS close frame.
	deviceConn := <-connCh
	if err := deviceConn.Close(); err != nil {
		t.Fatalf("raw device close: %v", err)
	}
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Fatal("client read after device close must fail")
	}
	if !websocket.IsUnexpectedCloseError(err) {
		t.Errorf("client read error = %v, want an unexpected-close error", err)
	}
}

func TestVNCProxyUnknownDevice(t *testing.T) {
	srv := newRemoteDesktopServer(t)

	_, resp, err := websocket.DefaultDialer.Dial(wsProxyURL(srv, "/wui/vnc/ghost"), nil)
	if !errors.Is(err, websocket.ErrBadHandshake) {
		t.Fatalf("dial err = %v, want ErrBadHandshake", err)
	}
	if resp == nil {
		t.Fatal("gorilla returns the handshake response on failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestVNCProxyMalformedSettingsFile(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	seedSettings(t, "{garbage")

	_, resp, err := websocket.DefaultDialer.Dial(wsProxyURL(srv, "/wui/vnc/anything"), nil)
	if !errors.Is(err, websocket.ErrBadHandshake) {
		t.Fatalf("dial err = %v, want ErrBadHandshake", err)
	}
	if resp == nil {
		t.Fatal("gorilla returns the handshake response on failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (a Load error behaves like cold start)", resp.StatusCode)
	}
}

func TestVNCProxyDialFailure(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	seedSettings(t, `{"desktops":[{"name":"broken","addr":"ws://127.0.0.1:1"}]}`)

	client, _, err := websocket.DefaultDialer.Dial(wsProxyURL(srv, "/wui/vnc/broken"), nil)
	if err != nil {
		t.Fatalf("client dial must upgrade before the device dial: %v", err)
	}
	defer client.Close()
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Fatal("read must fail — the proxy sends a close frame on dial failure")
	}
	if !websocket.IsCloseError(err, websocket.CloseInternalServerErr) {
		t.Fatalf("read error = %v, want close code 1011", err)
	}
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("error type = %T, want *websocket.CloseError", err)
	}
	if !strings.HasPrefix(ce.Text, "device dial failed:") {
		t.Errorf("close text = %q, want prefix \"device dial failed:\"", ce.Text)
	}
	if !strings.Contains(err.Error(), "dial tcp") {
		t.Errorf("error = %v, want it to embed the OS dial error", err)
	}
}

func TestVNCProxyInvalidAddr(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	// Seeded directly: PUT validation only checks non-empty addr, so a
	// hand-edited file reaches this path.
	seedSettings(t, `{"desktops":[{"name":"ftpdev","addr":"ftp://x"}]}`)

	_, resp, err := websocket.DefaultDialer.Dial(wsProxyURL(srv, "/wui/vnc/ftpdev"), nil)
	if !errors.Is(err, websocket.ErrBadHandshake) {
		t.Fatalf("dial err = %v, want ErrBadHandshake", err)
	}
	if resp == nil {
		t.Fatal("gorilla returns the handshake response on failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (written pre-upgrade, pre-hijack)", resp.StatusCode)
	}
}

func TestRemoteDesktopGetColdStart(t *testing.T) {
	srv := newRemoteDesktopServer(t)

	var got struct {
		Devices []json.RawMessage `json:"devices"`
	}
	resp := getJSON(t, srv.URL+"/api/settings/remotedesktop", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 on cold start", resp.StatusCode)
	}
	if got.Devices == nil || len(got.Devices) != 0 {
		t.Errorf("devices = %v, want [] (never null)", got.Devices)
	}

	// A malformed settings.json is also a cold start, not an error.
	seedSettings(t, "{garbage")
	got.Devices = nil
	resp = getJSON(t, srv.URL+"/api/settings/remotedesktop", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 on malformed file", resp.StatusCode)
	}
	if got.Devices == nil || len(got.Devices) != 0 {
		t.Errorf("devices = %v, want [] on malformed file", got.Devices)
	}
}

func TestVNCProxyNonWebSocketRequest(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"ws://127.0.0.1:8006"}]}`)

	// A plain GET (no upgrade headers) fails the upgrade; gorilla has
	// already written the 400 by the time the handler's error return runs.
	resp, err := http.Get(srv.URL + "/wui/vnc/echo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestVNCPumpWriteFailure(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	pair := func() (*websocket.Conn, *websocket.Conn) {
		ch := make(chan *websocket.Conn, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			ch <- ws
		}))
		t.Cleanup(srv.Close)
		client, _, err := websocket.DefaultDialer.Dial("ws://"+strings.TrimPrefix(srv.URL, "http://")+"/", nil)
		if err != nil {
			t.Fatalf("dial %s: %v", srv.URL, err)
		}
		return client, <-ch
	}
	aClient, aServer := pair()
	_, bServer := pair()

	go relayWS(aServer, bServer)

	// Poison only b's write path: a past deadline fails the pump's next
	// WriteMessage while every read stays healthy — an isolated, racy-free
	// hit of the write-failure branch (a closed peer breaks reads first).
	if err := bServer.SetWriteDeadline(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := aClient.WriteMessage(websocket.BinaryMessage, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := aClient.ReadMessage(); err == nil {
		t.Error("relay must close both conns after a pump write failure")
	}
}

// ---------------------------------------------------------------------------
// remotedesktop.go
// ---------------------------------------------------------------------------

// Kills remotedesktop.go:49 (statement/remove: Cache-Control header).
func TestMutD_RemoteDesktopGET_NoStoreHeader(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	resp, err := http.Get(srv.URL + "/api/settings/remotedesktop")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (devices may change via PUT at any time)", cc)
	}
}

// Kills remotedesktop.go:170 (numbers x2), :179 (numbers x2) and :181
// (conditional/negated, which panics on the nil resp of a refused dial):
// error paths must report latency 0.
func TestMutD_ProbeVNC_ErrorPathsReportZeroLatency(t *testing.T) {
	latency, err := probeVNC(context.Background(), "")
	if err == nil || err.Error() != "address is required" {
		t.Fatalf("empty addr err = %v, want 'address is required'", err)
	}
	if latency != 0 {
		t.Errorf("latency on normalize error = %d, want 0", latency)
	}

	latency2, err2 := probeVNC(context.Background(), "ws://127.0.0.1:1")
	if err2 == nil {
		t.Fatal("dial to a refused port must fail")
	}
	if latency2 != 0 {
		t.Errorf("latency on dial error = %d, want 0", latency2)
	}
}

// mutDDeviceServer upgrades one connection and records the negotiated
// subprotocol from the device side.
func mutDDeviceServer(t *testing.T, record func(string)) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{Subprotocols: []string{"binary"}, CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		record(ws.Subprotocol())
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Kills remotedesktop.go:173 (composite/field-clear: probe dialer
// subprotocols): the probe must negotiate the binary subprotocol websockify
// and noVNC speak.
func TestMutD_ProbeVNC_NegotiatesBinarySubprotocol(t *testing.T) {
	got := make(chan string, 4)
	dev := mutDDeviceServer(t, func(s string) { got <- s })
	if _, err := probeVNC(context.Background(), dev.URL); err != nil {
		t.Fatalf("probe: %v", err)
	}
	select {
	case sub := <-got:
		if sub != "binary" {
			t.Fatalf("device negotiated subprotocol %q, want binary", sub)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("device never saw the probe dial")
	}
}

// Kills remotedesktop.go:189 (numbers/incrementer): a sub-millisecond dial
// must clamp to exactly 1, the documented floor.
func TestMutD_ProbeVNC_LatencyFloorIsExactlyOne(t *testing.T) {
	dev := mutDDeviceServer(t, func(string) {})
	best := 0
	for i := 0; i < 8; i++ {
		lat, err := probeVNC(context.Background(), dev.URL)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if i == 0 || lat < best {
			best = lat
		}
		if best == 1 {
			break
		}
	}
	if best != 1 {
		t.Fatalf("best loopback latency = %d ms, want the floor of exactly 1 (loopback dials are sub-ms)", best)
	}
}

// mutDHangListener accepts TCP connections and never answers: the handshake
// timeout under test is the only way a dial to it terminates.
func mutDHangListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			if _, err := ln.Accept(); err != nil {
				return
			}
		}
	}()
	return "ws://" + ln.Addr().String()
}

// Kills remotedesktop.go:174 (composite/field-clear, arithmetic/base,
// numbers/decrementer, numbers/incrementer): the probe dialer's handshake
// timeout must fire at ~5s (mutants: 4s, 6s, or none until the 8s ctx cap).
func TestMutD_ProbeVNC_HandshakeTimeout5s(t *testing.T) {
	// No synctest here, measured: gorilla's HandshakeTimeout lands as a real
	// fd SetDeadline while a bubble-parked dial goroutine freezes the virtual
	// clock (wall 5s / bubble 0s), so the window must absorb scheduling
	// noise instead. The upper bound stays below 6s to keep killing the
	// 5s→6s HandshakeTimeout mutant (elapsed ≈6s).
	addr := mutDHangListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	start := time.Now() // REAL-TIME
	_, err := probeVNC(ctx, addr)
	elapsed := time.Since(start) // REAL-TIME
	if err == nil {
		t.Fatal("dial to a hanging server must fail")
	}
	if elapsed < 4500*time.Millisecond || elapsed > 5950*time.Millisecond {
		t.Fatalf("handshake timeout fired after %v, want ~5s", elapsed)
	}
}

// Kills remotedesktop.go:245 (composite/field-clear, arithmetic/base,
// numbers/decrementer, numbers/incrementer): the proxy's device dial must
// time out at ~10s and surface a 1011 close to the browser client (mutants:
// 9s, 11s, or none before the 12s read deadline).
func TestMutD_VNCProxy_DeviceDialTimeout10s(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	addr := mutDHangListener(t)
	seedSettings(t, `{"desktops":[{"name":"hang","addr":"`+addr+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	start := time.Now() // REAL-TIME
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/hang"), nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(12 * time.Second)) // REAL-TIME
	_, _, rerr := client.ReadMessage()
	elapsed := time.Since(start) // REAL-TIME
	ce, ok := rerr.(*websocket.CloseError)
	if !ok || ce.Code != websocket.CloseInternalServerErr {
		t.Fatalf("client read err = %v, want a 1011 close after the device dial fails", rerr)
	}
	if elapsed < 9300*time.Millisecond || elapsed > 10700*time.Millisecond {
		t.Fatalf("device dial failed after %v, want ~10s handshake timeout", elapsed)
	}
}

// Kills remotedesktop.go:199 (composite/field-clear: vncUpgrader
// subprotocols): the browser-facing upgrade must negotiate binary.
func TestMutD_VNCProxy_ClientNegotiatesBinary(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	dev := mutDDeviceServer(t, func(string) {})
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{Subprotocols: []string{"binary"}}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/echo"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if sub := client.Subprotocol(); sub != "binary" {
		t.Fatalf("client negotiated subprotocol %q, want binary", sub)
	}
}

// Kills remotedesktop.go:200 (composite/field-clear: CheckOrigin dropped,
// gorilla then rejects cross-origin upgrades by default): the proxy must
// accept browsers from any origin — it is same-origin by construction.
func TestMutD_VNCProxy_CrossOriginAccepted(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	dev := mutDDeviceServer(t, func(string) {})
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	header := http.Header{"Origin": []string{"http://elsewhere.example:8080"}}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/echo"), header)
	if err != nil {
		t.Fatalf("cross-origin dial must be accepted: %v", err)
	}
	_ = client.Close()
}

// Kills remotedesktop.go:244 (composite/field-clear: proxy dialer
// subprotocols): the proxy must dial the device with the binary subprotocol.
func TestMutD_VNCProxy_DeviceSeesBinarySubprotocol(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	subCh := make(chan string, 1)
	dev := mutDDeviceServer(t, func(s string) { subCh <- s })
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/echo"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	select {
	case sub := <-subCh:
		if sub != "binary" {
			t.Fatalf("device saw subprotocol %q, want binary", sub)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy never dialed the device")
	}
}

// Kills remotedesktop.go:223 (loop/break: break flipped to continue): with
// duplicate device names only settings.json can produce, the FIRST match must
// win; flipping to continue lets the last match hijack the lookup.
func TestMutD_VNCProxy_DuplicateNamesFirstMatchWins(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	dev := mutDDeviceServer(t, func(string) {})
	// First "dup" points at a refused port; second at the live echo device.
	// The loop's break must select the first, so the relay dies with 1011.
	seedSettings(t, `{"desktops":[
		{"name":"dup","addr":"ws://127.0.0.1:1"},
		{"name":"dup","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/dup"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second)) // REAL-TIME
	_, _, rerr := client.ReadMessage()
	ce, ok := rerr.(*websocket.CloseError)
	if !ok || ce.Code != websocket.CloseInternalServerErr {
		t.Fatalf("client read = %v, want 1011 close from the first (refused) device", rerr)
	}
	if !strings.Contains(ce.Text, "device dial failed") {
		t.Errorf("close text = %q, want it to name the device dial failure", ce.Text)
	}
}

// Kills remotedesktop.go:272 (statement/remove: trailing stop dropped): when
// the main pump exits, relayWS itself must close both conns — the surviving
// reverse pump would otherwise block on its read forever.
func TestMutD_RelayWS_ClosesBothConnsWhenMainPumpExits(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mkPair := func() (*websocket.Conn, *websocket.Conn, *httptest.Server) {
		srvConn := make(chan *websocket.Conn, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			srvConn <- ws
		}))
		client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatalf("pair dial: %v", err)
		}
		return client, <-srvConn, srv
	}
	aC, aS, srvA := mkPair()
	defer srvA.Close()
	bC, bS, srvB := mkPair()
	defer srvB.Close()

	relayDone := make(chan struct{})
	go func() {
		relayWS(aS, bS)
		close(relayDone)
	}()
	// Kill the b side: the MAIN pump (b→a) reads b and exits first, while the
	// goroutine pump stays parked reading aS.
	_ = bC.Close()
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relayWS did not return after the main pump exited")
	}
	_ = aC.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	_, _, rerr := aC.ReadMessage()
	if rerr == nil {
		t.Fatal("a side must be closed after relay teardown")
	}
	if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
		t.Fatal("a peer still open after 2s — relayWS must close both conns so the parked pump unblocks")
	}
	_ = aC.Close()
}

// Kills remotedesktop.go:282 (branch/if): when ReadMessage fails MID-MESSAGE
// it returns a valid message type plus partial data; the mutated pump falls
// through and forwards those partial bytes, the original must not. The
// mid-message error is produced deterministically with a read limit hit on
// the second frame of a fragmented message.
func TestMutD_PumpWS_ReadErrorForwardsNothing(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	// src side: the server pushes a fragmented binary message (2x64 bytes);
	// the client under a 64-byte read limit errors mid-message after the
	// first frame.
	srcSrvConn := make(chan *websocket.Conn, 1)
	srcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srcSrvConn <- ws
		frame1 := append([]byte{0x02, 0x40}, bytes.Repeat([]byte{0xA1}, 64)...) // binary, no FIN
		frame2 := append([]byte{0x80, 0x40}, bytes.Repeat([]byte{0xB2}, 64)...) // continuation, FIN
		if _, err := ws.UnderlyingConn().Write(append(frame1, frame2...)); err != nil {
			return
		}
	}))
	t.Cleanup(srcSrv.Close)

	dstSrvConn := make(chan *websocket.Conn, 1)
	dstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		dstSrvConn <- ws
	}))
	t.Cleanup(dstSrv.Close)

	src, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srcSrv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("src dial: %v", err)
	}
	defer src.Close()
	src.SetReadLimit(64)
	dst, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(dstSrv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dst dial: %v", err)
	}
	defer dst.Close()
	dstS := <-dstSrvConn

	pumped := make(chan struct{})
	go func() {
		pumpWS(src, dstS)
		close(pumped)
	}()

	_ = dst.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
	_, data, derr := dst.ReadMessage()
	if derr == nil {
		t.Fatalf("pump forwarded %d bytes after a failed read; the error path must write no frame", len(data))
	}
	if ne, ok := derr.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("dst read err = %v, want the 500ms deadline (silence, not a frame)", derr)
	}
	select {
	case <-pumped:
	case <-time.After(time.Second):
		t.Fatal("pumpWS did not return after the read error")
	}
}
