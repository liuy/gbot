package wui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
)

// newArtifactTestServer mounts artifact routes plus the SPA catch-all —
// the same coexistence the daemon mux uses, so route priority is exercised
// on every request, not just the dedicated priority case. The optional
// observe fn stands in for the production ObserveLLM wiring; tests that only
// exercise GET pass nothing and get a fn that reports "no provider".
func newArtifactTestServer(t *testing.T, dir string, observe ...ObserveProviderFn) *httptest.Server {
	t.Helper()
	var observeFn ObserveProviderFn
	if len(observe) == 1 {
		observeFn = observe[0]
	} else {
		observeFn = observeStubFn(nil, "", false)
	}
	mux := http.NewServeMux()
	RegisterArtifactRoutes(mux, dir, observeFn)
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeArtifactFile creates dir/name with the given content, failing the
// test on setup errors so assertions never run against a missing fixture.
func writeArtifactFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

const artifactGameHTML = "<html><body>game</body></html>"

func TestRegisterArtifactRoutes_ServesFileWithHeaders(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "game.html", artifactGameHTML)
	srv := newArtifactTestServer(t, dir)

	resp, err := http.Get(srv.URL + "/artifacts/game.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != artifactGameHTML {
		t.Errorf("body = %q, want %q — SPA catch-all must not swallow the artifact route", string(body), artifactGameHTML)
	}
	h := resp.Header
	if got := h.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	const wantCSP = "default-src 'self' 'unsafe-inline' data:; sandbox allow-scripts allow-same-origin"
	if got := h.Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
	}
	if got := h.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	if got := h.Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want empty (zero modtime forbids 304 negotiation)", got)
	}
	if got := h.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", got)
	}
}

func TestRegisterArtifactRoutes_ConditionalRequestStill200(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "game.html", artifactGameHTML)
	srv := newArtifactTestServer(t, dir)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/artifacts/game.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-Modified-Since", "Mon, 01 Jan 2035 00:00:00 GMT")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (zero modtime must defeat 304)", resp.StatusCode)
	}
}

func TestRegisterArtifactRoutes_HEADReturnsContentLength(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "game.html", artifactGameHTML)
	srv := newArtifactTestServer(t, dir)

	resp, err := http.Head(srv.URL + "/artifacts/game.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength != int64(len(artifactGameHTML)) {
		t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(artifactGameHTML))
	}
}

func TestRegisterArtifactRoutes_MissingFile404(t *testing.T) {
	srv := newArtifactTestServer(t, t.TempDir())

	resp, err := http.Get(srv.URL + "/artifacts/nope.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRegisterArtifactRoutes_EmptyName400(t *testing.T) {
	srv := newArtifactTestServer(t, t.TempDir())

	resp, err := http.Get(srv.URL + "/artifacts/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRegisterArtifactRoutes_EncodedTraversal400(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "game.html", artifactGameHTML)
	// Marker one level above the artifacts dir: any traversal that slips
	// through would serve this.
	markerPath := filepath.Join(dir, "..", "secret")
	if err := os.WriteFile(markerPath, []byte("TOP-SECRET-MARKER"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(markerPath); err != nil {
			t.Logf("remove marker: %v", err)
		}
	})
	srv := newArtifactTestServer(t, dir)

	// %2f escapes stay within one path segment, so the mux does NOT
	// clean-path-redirect them; they reach the handler where PathValue is
	// decoded to "../secret" and caught by the ".." segment check.
	for _, raw := range []string{"/artifacts/..%2fsecret", "/artifacts/%2e%2e%2fsecret"} {
		resp, err := http.Get(srv.URL + raw)
		if err != nil {
			t.Fatalf("GET %s: %v", raw, err)
		}
		body, err := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatalf("GET %s close body: %v", raw, cerr)
		}
		if err != nil {
			t.Fatalf("GET %s read body: %v", raw, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", raw, resp.StatusCode)
		}
		if strings.Contains(string(body), "TOP-SECRET-MARKER") {
			t.Errorf("GET %s leaked content above the artifacts dir", raw)
		}
	}
}

func TestRegisterArtifactRoutes_LiteralTraversalRedirectedByMux(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "..", "secret")
	if err := os.WriteFile(markerPath, []byte("TOP-SECRET-MARKER"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(markerPath); err != nil {
			t.Logf("remove marker: %v", err)
		}
	})
	srv := newArtifactTestServer(t, dir)

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noFollow.Get(srv.URL + "/artifacts/../secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		t.Fatalf("status = %d, want 3xx (mux clean-path redirect, handler never reached)", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/secret" {
		t.Errorf("Location = %q, want /secret", got)
	}

	// Following the redirect lands on the SPA catch-all, never on the file.
	follow, err := http.Get(srv.URL + "/artifacts/../secret")
	if err != nil {
		t.Fatal(err)
	}
	defer follow.Body.Close()
	body, err := io.ReadAll(follow.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "TOP-SECRET-MARKER") {
		t.Error("followed redirect leaked content above the artifacts dir")
	}
}

func TestRegisterArtifactRoutes_Directory404(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	srv := newArtifactTestServer(t, dir)

	resp, err := http.Get(srv.URL + "/artifacts/sub")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRegisterArtifactRoutes_NestedPath(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "sub/game.html", artifactGameHTML)
	srv := newArtifactTestServer(t, dir)

	resp, err := http.Get(srv.URL + "/artifacts/sub/game.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != artifactGameHTML {
		t.Errorf("body = %q, want %q", string(body), artifactGameHTML)
	}
}

// setArtifactMtime pins a file's mtime so the descending sort order is
// observable regardless of filesystem write timing.
func setArtifactMtime(t *testing.T, full string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(full, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", full, err)
	}
}

func getArtifactList(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

func TestRegisterArtifactRoutes_ListReturnsEntriesNewestFirst(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "old.html", "old")
	writeArtifactFile(t, dir, "new.html", "newer-content")
	writeArtifactFile(t, dir, "nested/game.html", artifactGameHTML)
	setArtifactMtime(t, filepath.Join(dir, "old.html"), time.UnixMilli(1700000000000))
	setArtifactMtime(t, filepath.Join(dir, "new.html"), time.UnixMilli(1700000100000))
	setArtifactMtime(t, filepath.Join(dir, "nested", "game.html"), time.UnixMilli(1700000050000))
	srv := newArtifactTestServer(t, dir)

	status, header, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — SPA catch-all must not swallow the list route", status)
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var items []struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		Mtime int64  `json:"mtime"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("unmarshal %q: %v", string(body), err)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3 (body %q)", len(items), string(body))
	}
	// Newest first, so freshly written artifacts surface at the top.
	wantOrder := []struct {
		name  string
		mtime int64
	}{
		{"new.html", 1700000100000},
		{"nested/game.html", 1700000050000},
		{"old.html", 1700000000000},
	}
	for i, w := range wantOrder {
		if items[i].Name != w.name || items[i].Mtime != w.mtime {
			t.Errorf("items[%d] = {%s %d}, want {%s %d}", i, items[i].Name, items[i].Mtime, w.name, w.mtime)
		}
	}
	if items[0].Size != int64(len("newer-content")) {
		t.Errorf("new.html size = %d, want %d", items[0].Size, len("newer-content"))
	}
	if items[1].Size != int64(len(artifactGameHTML)) {
		t.Errorf("nested/game.html size = %d, want %d", items[1].Size, len(artifactGameHTML))
	}
}

func TestRegisterArtifactRoutes_ListEmptyDirReturnsEmptyArray(t *testing.T) {
	srv := newArtifactTestServer(t, t.TempDir())

	status, _, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(body) != "[]" {
		t.Errorf("body = %q, want [] — the client renders its empty state from [] not null", string(body))
	}
}

func TestRegisterArtifactRoutes_ListMissingDirReturnsEmptyArray(t *testing.T) {
	// Fresh project before the first artifact write: the directory does not
	// exist yet, which must present as an empty list, not an error.
	srv := newArtifactTestServer(t, filepath.Join(t.TempDir(), "artifacts"))

	status, _, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(body) != "[]" {
		t.Errorf("body = %q, want []", string(body))
	}
}

func TestRegisterArtifactRoutes_ListUnreadableSubdirReturns500(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "sub/game.html", artifactGameHTML)
	sub := filepath.Join(dir, "sub")
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", sub, err)
	}
	// Restore readability so t.TempDir's RemoveAll succeeds after the test.
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	srv := newArtifactTestServer(t, dir)

	status, _, _ := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — walk errors other than a missing dir must surface", status)
	}
}

func TestArtifactFilePath(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		in     string
		want   string
		status int
	}{
		{name: "simple", in: "game.html", want: filepath.Join(dir, "game.html")},
		{name: "nested", in: "sub/game.html", want: filepath.Join(dir, "sub", "game.html")},
		{name: "empty", in: "", status: http.StatusBadRequest},
		{name: "dotdot", in: "..", status: http.StatusBadRequest},
		{name: "dotdot segment", in: "a/../b", status: http.StatusBadRequest},
		{name: "encoded-decoded dotdot", in: "../secret", status: http.StatusBadRequest},
		{name: "dot only", in: ".", status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		got, status := artifactFilePath(dir, tt.in)
		if status != tt.status {
			t.Errorf("%s: status = %d, want %d", tt.name, status, tt.status)
		}
		if got != tt.want {
			t.Errorf("%s: path = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// getArtifactFull fetches an artifact URL returning status, headers and body
// so fallback scenarios can diff the full observable surface against the
// on-disk serve path.
func getArtifactFull(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

func TestRegisterArtifactRoutes_BundledChessFallbackMatchesFileServe(t *testing.T) {
	srv := newArtifactTestServer(t, t.TempDir())

	status, header, body := getArtifactFull(t, srv.URL+"/artifacts/chess")
	if status != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200", status, string(body))
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	const wantCSP = "default-src 'self' 'unsafe-inline' data:; sandbox allow-scripts allow-same-origin"
	if got := header.Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
	}
	if got := header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	if got := header.Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want empty (bundled serve must forbid 304 too)", got)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", got)
	}
	if !strings.Contains(string(body), "var ZhChess") {
		t.Error("bundled fallback body does not contain the inlined zh-chess lib")
	}
}

func TestRegisterArtifactRoutes_DiskFileWinsOverBundledChess(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "chess", "<html>disk version</html>")
	srv := newArtifactTestServer(t, dir)

	status, _, body := getArtifactFull(t, srv.URL+"/artifacts/chess")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(body) != "<html>disk version</html>" {
		t.Fatalf("body = %q, want the on-disk file content", string(body))
	}
}

func TestRegisterArtifactRoutes_UnknownNameStill404WithBundled(t *testing.T) {
	srv := newArtifactTestServer(t, t.TempDir())

	resp, err := http.Get(srv.URL + "/artifacts/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestObserveRouteWiredViaRegisterArtifactRoutes(t *testing.T) {
	sp := &stubProvider{script: []stubScript{{text: "马8进7"}}}
	srv := newArtifactTestServer(t, t.TempDir(), observeStubFn(sp, "glm-5.2", true))

	body := observeBody(t, observeTestPrompt, observeTestState)
	resp, err := http.Post(srv.URL+"/artifacts/chess", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200", resp.StatusCode, string(respBytes))
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", got)
	}
	r := decodeObserveResp(t, respBytes)
	if r.Move != "马8进7" {
		t.Errorf("Move = %q, want 马8进7", r.Move)
	}
	if len(sp.recorded()) != 1 {
		t.Errorf("Complete calls = %d, want 1", len(sp.recorded()))
	}

	// POST to a non-game name must 404 even though the handler is mounted.
	noResp, err := http.Post(srv.URL+"/artifacts/nope", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer noResp.Body.Close()
	if noResp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST nope status = %d, want 404", noResp.StatusCode)
	}
}

func TestObserveLLM(t *testing.T) {
	t.Run("returns active provider and model", func(t *testing.T) {
		c := newTestConnector(t)
		sp := &stubProvider{}
		c.mock().providerFn = func() llm.Provider { return sp }
		p, model, ok := c.ObserveLLM()
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if p == nil {
			t.Fatal("provider = nil, want stub")
		}
		if _, isStub := p.(*stubProvider); !isStub {
			t.Fatalf("provider = %T, want *stubProvider", p)
		}
		if model != "glm-5.2" {
			t.Errorf("model = %q, want glm-5.2", model)
		}
	})
	t.Run("nil provider reports not ok", func(t *testing.T) {
		c := newTestConnector(t)
		p, model, ok := c.ObserveLLM()
		if ok {
			t.Fatal("ok = true, want false for nil provider")
		}
		if p != nil {
			t.Errorf("provider = %v, want nil", p)
		}
		if model != "" {
			t.Errorf("model = %q, want empty", model)
		}
	})
	t.Run("empty model reports not ok", func(t *testing.T) {
		c := newTestConnector(t)
		sp := &stubProvider{}
		c.mock().providerFn = func() llm.Provider { return sp }
		c.mock().modelFn = func() string { return "" }
		_, _, ok := c.ObserveLLM()
		if ok {
			t.Fatal("ok = true, want false for empty model")
		}
	})
	t.Run("nil engine reports not ok", func(t *testing.T) {
		c := &WUIConnector{
			slots:  make(map[string]*engineSlot),
			wsCh:   make(chan wsMsg, 1),
			done:   make(chan struct{}),
			thumbs: newThumbCache(),
		}
		go c.wsWriter()
		t.Cleanup(c.Stop)
		_, _, ok := c.ObserveLLM()
		if ok {
			t.Fatal("ok = true, want false with no active engine")
		}
	})
}
