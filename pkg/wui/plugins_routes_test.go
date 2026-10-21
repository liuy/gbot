package wui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/transport"
)

// setupPluginFixture swaps route discovery to a temp tree with one live
// plugin (declared service + a live.js UI module) and returns the mux
// serving the plugin routes.
func setupPluginFixture(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	old := discoverPluginsFn
	discoverPluginsFn = func() ([]*plugins.ResolvedPlugin, error) {
		return plugins.DiscoverPluginsFromDir(dir)
	}
	t.Cleanup(func() { discoverPluginsFn = old })

	plug := filepath.Join(dir, "browser")
	if err := os.MkdirAll(plug, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{
	  "name": "browser", "version": "1.0", "ui": "./live.js", "protocol": "./protocol.js"
	}`
	if err := os.WriteFile(filepath.Join(plug, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	uiModule := "export function mount(c, ctx) { c.textContent = 'browser ui'; }"
	if err := os.WriteFile(filepath.Join(plug, "live.js"), []byte(uiModule), 0o600); err != nil {
		t.Fatal(err)
	}

	// A plugin without protocol must not appear in the list.
	plain := filepath.Join(dir, "plain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "plugin.json"), []byte(`{"name":"plain","version":"1.0","ui":"./x.js"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A protocol plugin without ui: listed, but its /ui route 404s.
	noUI := filepath.Join(dir, "nohead")
	if err := os.MkdirAll(noUI, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noUI, "plugin.json"), []byte(`{"name":"nohead","version":"1.0","protocol":"./protocol.js"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	RegisterPluginRoutes(mux, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPluginListOnlyServicePlugins(t *testing.T) {
	srv := setupPluginFixture(t)

	resp, err := http.Get(srv.URL + "/api/plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var cards []pluginCard
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %+v, want browser and nohead", cards)
	}
	if cards[0].Name != "browser" || cards[0].UI != "/plugins/browser/ui" || cards[0].Running {
		t.Fatalf("browser card wrong: %+v", cards[0])
	}
	if cards[1].Name != "nohead" || cards[1].UI != "/plugins/nohead/ui" {
		t.Fatalf("nohead card wrong: %+v", cards[1])
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("list must not be cached: %q", resp.Header.Get("Cache-Control"))
	}
}

func TestPluginListRunningFollowsHub(t *testing.T) {
	setupPluginFixture(t)
	oldState := runningState
	runningState = func(hub *transport.Hub, plugin string) bool { return plugin == "browser" }
	t.Cleanup(func() { runningState = oldState })

	cards, err := discoverPluginCards(nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]pluginCard{}
	for _, c := range cards {
		byName[c.Name] = c
	}
	if !byName["browser"].Running {
		t.Fatalf("browser must be running, got %+v", byName["browser"])
	}
	if byName["nohead"].Running {
		t.Fatalf("nohead must not be running, got %+v", byName["nohead"])
	}
}

// TestRunningStateRealHub pins the production runningState against a real
// hub: nil hub and unknown plugin stay false, a registered provider flips
// its plugin true.
func TestRunningStateRealHub(t *testing.T) {
	setupPluginFixture(t)
	if runningState(nil, "browser") {
		t.Fatal("nil hub must report not running")
	}
	hub := transport.New()
	t.Cleanup(hub.Close)
	if runningState(hub, "browser") {
		t.Fatal("no provider yet: must report not running")
	}
	handle := hub.RegisterInproc("browser", func([]byte) {}, nil)
	if handle == nil {
		t.Fatal("RegisterInproc nil handle")
	}
	defer handle.Close()
	if !runningState(hub, "browser") {
		t.Fatal("registered provider must report running")
	}
}

func TestPluginUIServedSameOrigin(t *testing.T) {
	srv := setupPluginFixture(t)

	resp, err := http.Get(srv.URL + "/plugins/browser/ui")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/javascript") {
		t.Fatalf("content-type = %q", ct)
	}
	body := make([]byte, 128)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "export function mount") {
		t.Fatalf("ui body wrong: %q", string(body[:n]))
	}
}

func TestPluginUIRejectsUnknownAndMissingDecl(t *testing.T) {
	srv := setupPluginFixture(t)

	// unknown plugin / traversal / serviceless plugin / ui-less plugin
	for _, path := range []string{
		"/plugins/missing/ui", "/plugins/../browser/ui", "/plugins/plain/ui", "/plugins/nohead/ui",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("path %q: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestResolveUIFileConfinement(t *testing.T) {
	root := "/plugins/browser"
	cases := map[string]string{
		"./live.js":  filepath.Join(root, "live.js"),
		"live.js":    filepath.Join(root, "live.js"),
		"sub/a.js":   filepath.Join(root, "sub", "a.js"),
		"../x.js":    "",
		"../../x.js": "",
		"/abs/x.js":  "",
		".":          "",
		"..":         "",
		"./../ok.js": "",
	}
	for ui, want := range cases {
		if got := resolveUIFile(root, ui); got != want {
			t.Fatalf("resolveUIFile(%q) = %q, want %q", ui, got, want)
		}
	}
}

func TestFindPluginByNameValidation(t *testing.T) {
	setupPluginFixture(t)

	for _, name := range []string{"", "a/b", "../browser", "."} {
		if _, ok := findPluginByName(name); ok {
			t.Fatalf("name %q must be rejected", name)
		}
	}
	if p, ok := findPluginByName("browser"); !ok || p.Name != "browser" {
		t.Fatalf("browser must resolve, got (%v, %v)", p, ok)
	}
}

func TestPluginListDiscoveryFailure(t *testing.T) {
	old := discoverPluginsFn
	discoverPluginsFn = func() ([]*plugins.ResolvedPlugin, error) {
		return nil, errors.New("scan boom")
	}
	t.Cleanup(func() { discoverPluginsFn = old })

	mux := http.NewServeMux()
	RegisterPluginRoutes(mux, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("scan failure = %d, want 500", resp.StatusCode)
	}
	// The ui route degrades to 404 on the same discovery error.
	resp2, err := http.Get(srv.URL + "/plugins/browser/ui")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("ui on scan failure = %d, want 404", resp2.StatusCode)
	}
}

func TestDiscoverPluginCardsSkipsDegenerateEntries(t *testing.T) {
	old := discoverPluginsFn
	discoverPluginsFn = func() ([]*plugins.ResolvedPlugin, error) {
		return []*plugins.ResolvedPlugin{
			{Name: "nil-manifest"}, // Manifest nil → skipped
			{Name: "no-protocol", Manifest: &plugins.PluginManifest{
				UI: "./ui.js",
			}}, // protocol-less plugin → no live card
			{Name: "good", Manifest: &plugins.PluginManifest{
				Protocol: "./protocol.js",
				UI:       "./ui.js",
			}},
		}, nil
	}
	t.Cleanup(func() { discoverPluginsFn = old })

	cards, err := discoverPluginCards(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %+v, want only the good plugin", cards)
	}
	if cards[0].Name != "good" || cards[0].UI != "/plugins/good/ui" || cards[0].Running {
		t.Fatalf("good card wrong: %+v", cards[0])
	}
}

// failWriterRecorder fails every Write while still recording headers, so the
// encode/write error branches fire without crashing the handler.
type failWriterRecorder struct {
	*httptest.ResponseRecorder
	wrote bool
}

func (w *failWriterRecorder) Write(b []byte) (int, error) {
	w.wrote = true
	return 0, errors.New("sink broken")
}

func TestPluginRoutesWriteFailureLogsAndSurvives(t *testing.T) {
	setupPluginFixture(t)
	rec := mutCCaptureSlog(t)

	mux := http.NewServeMux()
	RegisterPluginRoutes(mux, nil)

	// servePluginList: Encode fails → warn, no panic.
	w1 := &failWriterRecorder{ResponseRecorder: httptest.NewRecorder()}
	servePluginList(nil)(w1, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if !rec.hasMessage("wui: plugin list encode") {
		t.Fatalf("encode failure must warn, got %v", rec.messages())
	}

	// servePluginUI: body Write fails → warn, no panic. The request
	// must go through the mux so the {name} path variable is populated.
	w2 := &failWriterRecorder{ResponseRecorder: httptest.NewRecorder()}
	mux.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/plugins/browser/ui", nil))
	if !rec.hasMessage("wui: plugin ui write") {
		t.Fatalf("ui write failure must warn, got %v", rec.messages())
	}
	if !w2.wrote {
		t.Fatal("ui body write must have been attempted")
	}
}

func TestPluginListContentType(t *testing.T) {
	srv := setupPluginFixture(t)
	resp, err := http.Get(srv.URL + "/api/plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("list content-type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("list cache-control = %q", cc)
	}
}
