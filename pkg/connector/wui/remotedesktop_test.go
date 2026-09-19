package wui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
  "remote_desktop": [
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
	if !jsonEqual(t, file["remote_desktop"], json.RawMessage(`[{"name":"only","addr":"ws://127.0.0.1:6080"}]`)) {
		t.Errorf("remote_desktop = %s, want the PUT body", file["remote_desktop"])
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
	path, old := seedSettings(t, `{"providers":[{"name":"zhipu","url":"https://a","keys":["k"],"models":{"m":{}}}],"remote_desktop":[{"name":"keep","addr":"ws://127.0.0.1:8006"}]}`)

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
	if string(file["remote_desktop"]) != "[]" {
		t.Errorf("remote_desktop = %s, want []", file["remote_desktop"])
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
	seedSettings(t, `{"remote_desktop":[{"name":"keep","addr":"ws://127.0.0.1:8006"}]}`)

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
	seedSettings(t, `{"remote_desktop":[{"name":"echo","addr":"`+device.URL+`"}]}`)

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
	seedSettings(t, `{"remote_desktop":[{"name":"broken","addr":"ws://127.0.0.1:1"}]}`)

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
	seedSettings(t, `{"remote_desktop":[{"name":"ftpdev","addr":"ftp://x"}]}`)

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
	seedSettings(t, `{"remote_desktop":[{"name":"echo","addr":"ws://127.0.0.1:8006"}]}`)

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
