package wui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	if got := h.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	const wantCSP = "default-src 'self' 'unsafe-inline' data: blob:; connect-src 'self' http:; sandbox allow-scripts allow-same-origin allow-popups allow-modals allow-downloads allow-forms"
	if got := h.Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
	}
	if got := h.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	if got := h.Get("ETag"); got == "" {
		t.Error("ETag is empty, want a strong validator for 304 negotiation")
	}
	if got := h.Get("Last-Modified"); got == "" {
		t.Error("Last-Modified is empty, want the file modtime for 304 negotiation")
	}
	if got := h.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", got)
	}
}

func TestRegisterArtifactRoutes_ConditionalRequestNegotiates304(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "game.html", artifactGameHTML)
	srv := newArtifactTestServer(t, dir)

	first, err := http.Get(srv.URL + "/artifacts/game.html")
	if err != nil {
		t.Fatal(err)
	}
	etag := first.Header.Get("ETag")
	if _, err := io.Copy(io.Discard, first.Body); err != nil {
		t.Fatal(err)
	}
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != http.StatusOK || etag == "" {
		t.Fatalf("first GET = %d etag %q, want 200 with an ETag", first.StatusCode, etag)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/artifacts/game.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-None-Match", etag)
	revalidated, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer revalidated.Body.Close()
	body, err := io.ReadAll(revalidated.Body)
	if err != nil {
		t.Fatal(err)
	}
	if revalidated.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match round-trip = %d, want 304 (no re-download)", revalidated.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("304 body = %d bytes, want empty", len(body))
	}

	// The If-Modified-Since path must negotiate 304 from the modtime too.
	// Deterministic input: the file's own modtime (a moving time.Now() could
	// trail a coarser mtime and make the test flaky).
	fi, err := os.Stat(filepath.Join(dir, "game.html"))
	if err != nil {
		t.Fatal(err)
	}
	sinceReq, err := http.NewRequest(http.MethodGet, srv.URL+"/artifacts/game.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	sinceReq.Header.Set("If-Modified-Since", fi.ModTime().UTC().Format(http.TimeFormat))
	sinceResp, err := http.DefaultClient.Do(sinceReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sinceResp.Body.Close()
	if sinceResp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-Modified-Since = %d, want 304", sinceResp.StatusCode)
	}

	// A content change must produce a new validator and fresh bytes.
	writeArtifactFile(t, dir, "game.html", "v2 content")
	changed, err := http.Get(srv.URL + "/artifacts/game.html")
	if err != nil {
		t.Fatal(err)
	}
	defer changed.Body.Close()
	changedBody, err := io.ReadAll(changed.Body)
	if err != nil {
		t.Fatal(err)
	}
	if changed.StatusCode != http.StatusOK || string(changedBody) != "v2 content" {
		t.Fatalf("after edit: status %d body %q, want 200 with fresh bytes", changed.StatusCode, string(changedBody))
	}
	if changed.Header.Get("ETag") == etag {
		t.Error("ETag unchanged after file edit — clients would be served stale caches forever")
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
	inBubble(t, func(t *testing.T) {
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
		mux := http.NewServeMux()
		RegisterArtifactRoutes(mux, dir, observeStubFn(nil, "", false))
		RegisterStaticRoutes(mux)
		srv := startHermeticServer(mux)
		defer srv.close()

		noFollow := srv.client()
		noFollow.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		resp, err := noFollow.Get("http://wui-hermetic/artifacts/../secret")
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
		follow, err := srv.client().Get("http://wui-hermetic/artifacts/../secret")
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
	})
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
	const wantCSP = "default-src 'self' 'unsafe-inline' data: blob:; connect-src 'self' http:; sandbox allow-scripts allow-same-origin allow-popups allow-modals allow-downloads allow-forms"
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

// deleteArtifact issues a DELETE against url and returns the status code.
// The body is drained and closed so the test client's keep-alives stay sane.
func deleteArtifact(t *testing.T, url string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}
	return resp.StatusCode
}

func TestRegisterArtifactRoutes_DeleteAllClearsEveryFile(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "a.html", "a")
	writeArtifactFile(t, dir, "nested/b.html", "b")
	writeArtifactFile(t, dir, "nested/deep/c.html", "c")
	srv := newArtifactTestServer(t, dir)

	if status := deleteArtifact(t, srv.URL+"/api/artifacts"); status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Fatalf("the artifacts dir itself must survive clear-all (stat err = %v)", err)
	}
	var remaining []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			remaining = append(remaining, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk after clear-all: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("files remain after clear-all: %v", remaining)
	}
	// The listing must reflect the cleared state, not a stale cache.
	status, _, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK || string(body) != "[]" {
		t.Fatalf("list after clear-all = (%d, %q), want (200, [])", status, string(body))
	}
}

func TestRegisterArtifactRoutes_ClearAllMissingDirIsNoContent(t *testing.T) {
	// Fresh project before the first artifact write: nothing to clear is a
	// success, not an error.
	srv := newArtifactTestServer(t, filepath.Join(t.TempDir(), "artifacts"))

	if status := deleteArtifact(t, srv.URL+"/api/artifacts"); status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
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
			done:   make(chan struct{}),
			thumbs: newThumbCache(),
		}
		c.outQ.Store(newOutQueue(1))
		go c.wsWriter()
		t.Cleanup(c.Stop)
		_, _, ok := c.ObserveLLM()
		if ok {
			t.Fatal("ok = true, want false with no active engine")
		}
	})
}

// ---------------------------------------------------------------------------
// artifacts.go — HTTP surface
// ---------------------------------------------------------------------------

// TestMutC_ArtifactOpenPermErrorIs404 pins the "unexplainable open error is
// a plain 404" branch: only a missing file may fall through to the bundled
// game fallback.
func TestMutC_ArtifactOpenPermErrorIs404(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit fixtures do not fail as root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	srv := newArtifactTestServer(t, dir)

	resp, err := http.Get(srv.URL + "/artifacts/chess")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (EACCES must not fall through to the bundled page)", resp.StatusCode)
	}
	if got := strings.TrimSpace(string(body)); got != "not found" {
		t.Errorf("body = %q, want the handler's not-found text", got)
	}
}

func TestMutC_ArtifactBundledFallback(t *testing.T) {
	srv := newArtifactTestServer(t, t.TempDir())

	status, header, body := getArtifactFull(t, srv.URL+"/artifacts/chess")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (registry name serves the bundled page)", status)
	}
	if !strings.Contains(string(body), "var ZhChess") {
		t.Errorf("body lacks the bundled chess template (%d bytes)", len(body))
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on the fallback", got)
	}
}

func TestMutC_ArtifactListNoStoreHeader(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "a.html", "a")
	srv := newArtifactTestServer(t, dir)

	status, header, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (listing must reflect new writes)", got)
	}
	if string(body) == "" || string(body) == "null" {
		t.Fatalf("body = %q, want a JSON array", string(body))
	}
}

// TestMutC_ArtifactWalkErrorsSurface500 uses an unreadable artifacts dir to
// pin that walk failures (other than a missing dir) answer 500 on both the
// list and the clear route.
func TestMutC_ArtifactWalkErrorsSurface500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit fixtures do not fail as root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	srv := newArtifactTestServer(t, dir)

	status, _, _ := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusInternalServerError {
		t.Fatalf("list status = %d, want 500 (walk error must surface)", status)
	}
	if delStatus := deleteArtifact(t, srv.URL+"/api/artifacts"); delStatus != http.StatusInternalServerError {
		t.Fatalf("delete status = %d, want 500 (walk error must surface)", delStatus)
	}
}

// TestMutC_ArtifactDeleteRemoveFailure500 keeps the dir readable (r+x) but
// not writable so readdir works and os.Remove fails with EACCES: a half-done
// delete must answer 500, not 204.
func TestMutC_ArtifactDeleteRemoveFailure500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit fixtures do not fail as root")
	}
	dir := t.TempDir()
	writeArtifactFile(t, dir, "a.html", "a")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	srv := newArtifactTestServer(t, dir)

	if status := deleteArtifact(t, srv.URL+"/api/artifacts"); status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a failing file remove must abort the clear)", status)
	}
}

// TestMutC_ArtifactGzipSizeFloor pins the exact eligibility floor around
// 1 MiB: below it identity, at it gzip.
func TestMutC_ArtifactGzipSizeFloor(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "sub-threshold.html", strings.Repeat("a", 600*1024))
	writeArtifactFile(t, dir, "one-under.html", strings.Repeat("a", 1<<20-1))
	writeArtifactFile(t, dir, "exact.html", strings.Repeat("a", 1<<20))
	srv := newArtifactTestServer(t, dir)

	for _, name := range []string{"sub-threshold.html", "one-under.html"} {
		status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/"+name, "gzip", nil)
		if status != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", name, status)
		}
		if got := h.Get("Content-Encoding"); got != "" {
			t.Errorf("%s Content-Encoding = %q, want identity below the 1MiB floor", name, got)
		}
		if len(body) < 600*1024 {
			t.Errorf("%s body = %d bytes, want the full identity body", name, len(body))
		}
	}
	status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/exact.html", "gzip", nil)
	if status != http.StatusOK {
		t.Fatalf("exact.html status = %d, want 200", status)
	}
	if got := h.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("exact.html Content-Encoding = %q, want gzip (size == floor is eligible)", got)
	}
	if plain := gunzipAll(t, body); len(plain) != 1<<20 {
		t.Errorf("exact.html gunzipped = %d bytes, want %d", len(plain), 1<<20)
	}
}

// TestMutC_ArtifactGzipEligibleText serves a >1MiB text artifact: the
// eligibility sniff must let plain text through and the octet-stream
// Content-Type fallback must be applied on the compressed representation.
func TestMutC_ArtifactGzipEligibleText(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("wui-gzip-text-payload\n", 80000) // > 1.5 MiB
	writeArtifactFile(t, dir, "big.txt", content)
	srv := newArtifactTestServer(t, dir)

	status, h, body, contentLength := probeArtifact(t, srv.URL+"/artifacts/big.txt", "gzip", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := h.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip for compressible text over the floor", got)
	}
	if got := h.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want the explicit octet-stream fallback", got)
	}
	if contentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d (exact compressed size on the wire)", contentLength, len(body))
	}
	if plain := gunzipAll(t, body); string(plain) != content {
		t.Errorf("gunzipped body = %d bytes, want the exact content (%d)", len(plain), len(content))
	}
}

// TestMutC_ArtifactGzipSkipsCompressedMedia pins the sniff blocklist: zip,
// gzip and video payloads must serve identity even when the client accepts
// gzip.
func TestMutC_ArtifactGzipSkipsCompressedMedia(t *testing.T) {
	dir := t.TempDir()
	zipBytes := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0}, 1<<20+512)...)
	gzBytes := append([]byte{0x1f, 0x8b, 0x08}, bytes.Repeat([]byte{0}, 1<<20+512)...)
	// A valid ftyp box (size 32, brands padded) is required for the mp4
	// sniff; the bytes inside the box must all be brand strings.
	mp4Bytes := append([]byte{
		0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		'm', 'p', '4', '2', 'm', 'p', '4', '2', 'm', 'p', '4', '2',
		'm', 'p', '4', '2', 'm', 'p', '4', '2',
	}, bytes.Repeat([]byte{0}, 1<<20+512)...)
	writeArtifactFile(t, dir, "a.zip", string(zipBytes))
	writeArtifactFile(t, dir, "a.gz", string(gzBytes))
	writeArtifactFile(t, dir, "a.mp4", string(mp4Bytes))
	srv := newArtifactTestServer(t, dir)

	for _, name := range []string{"a.zip", "a.gz", "a.mp4"} {
		status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/"+name, "gzip", nil)
		if status != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", name, status)
		}
		if got := h.Get("Content-Encoding"); got != "" {
			t.Errorf("%s Content-Encoding = %q, want identity (already-compressed medium)", name, got)
		}
		if len(body) < 1<<20 {
			t.Errorf("%s body = %d bytes, want the full identity body", name, len(body))
		}
	}
}

// TestMutC_ArtifactGzipValidators pins If-None-Match: "*" (a one-byte
// header value) matching any representation, and the 304 header diet.
func TestMutC_ArtifactGzipValidators(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "big.glb", bigGLB())
	srv := newArtifactTestServer(t, dir)

	status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/big.glb", "gzip",
		map[string]string{"If-None-Match": "*"})
	if status != http.StatusNotModified {
		t.Fatalf("INM * status = %d, want 304", status)
	}
	if len(body) != 0 {
		t.Errorf("304 body = %d bytes, want 0", len(body))
	}
	if got := h.Get("Content-Type"); got != "" {
		t.Errorf("304 Content-Type = %q, want empty (writeNotModified drops representation headers)", got)
	}
	if got := h.Get("Content-Encoding"); got != "" {
		t.Errorf("304 Content-Encoding = %q, want empty", got)
	}
	if !gzipETagRe.MatchString(h.Get("ETag")) {
		t.Errorf("304 ETag = %q, want the gz- validator", h.Get("ETag"))
	}
}

func TestMutC_ArtifactGzipHEADNoContentLength(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "big.glb", bigGLB())
	srv := newArtifactTestServer(t, dir)

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/artifacts/big.glb", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength != -1 {
		t.Errorf("HEAD ContentLength = %d, want -1 (HEAD returns before the compressed body is sized)", resp.ContentLength)
	}
	if got := resp.Header.Get("ETag"); !gzipETagRe.MatchString(got) {
		t.Errorf("HEAD ETag = %q, want the gz- validator", got)
	}
}

func TestMutC_ETagMatchesDirect(t *testing.T) {
	tests := []struct {
		name string
		inm  string
		etag string
		want bool
	}{
		{"star matches anything", "*", `"abc"`, true},
		{"exact", `"abc"`, `"abc"`, true},
		{"weak prefix counts", `W/"abc"`, `"abc"`, true},
		{"list with spaces", `"zzz", "abc" `, `"abc"`, true},
		{"no match", `"zzz"`, `"abc"`, false},
		{"empty header", ``, `"abc"`, false},
	}
	for _, tt := range tests {
		if got := etagMatches(tt.inm, tt.etag); got != tt.want {
			t.Errorf("%s: etagMatches(%q, %q) = %v, want %v", tt.name, tt.inm, tt.etag, got, tt.want)
		}
	}
}

func TestMutC_NotModifiedSinceDirect(t *testing.T) {
	mtime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	fresh := mtime.UTC().Format(http.TimeFormat)
	stale := mtime.Add(-time.Hour).UTC().Format(http.TimeFormat)
	tests := []struct {
		name    string
		ims     string
		modtime time.Time
		want    bool
	}{
		{"empty header", "", mtime, false},
		// A zero modtime is the bundled-template case; the guard must reject
		// before ParseTime runs, and a mutated guard flips this to true.
		{"zero modtime with valid ims", fresh, time.Time{}, false},
		{"equal second is fresh", fresh, mtime, true},
		{"older validator is fresh", stale, mtime, false},
		{"malformed ims", "not-a-date", mtime, false},
	}
	for _, tt := range tests {
		if got := notModifiedSince(tt.ims, tt.modtime); got != tt.want {
			t.Errorf("%s: notModifiedSince(%q, %v) = %v, want %v", tt.name, tt.ims, tt.modtime, got, tt.want)
		}
	}
}

func TestMutC_ArtifactContentTypeGltf(t *testing.T) {
	if got := artifactContentType("scene.GLTF"); got != "model/gltf+json" {
		t.Errorf("gltf = %q, want model/gltf+json", got)
	}
	if got := artifactContentType("scene.glb"); got != "model/gltf-binary" {
		t.Errorf("glb = %q, want model/gltf-binary", got)
	}
	if got := artifactContentType("scene.html"); got != "" {
		t.Errorf("html = %q, want empty (no explicit map)", got)
	}
}

func TestMutC_ArtifactListNewestFirstOrder(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "old.html", "old")
	writeArtifactFile(t, dir, "new.html", "newer")
	writeArtifactFile(t, dir, "mid.html", "mid")
	setArtifactMtime(t, filepath.Join(dir, "old.html"), time.UnixMilli(1000))
	setArtifactMtime(t, filepath.Join(dir, "mid.html"), time.UnixMilli(2000))
	setArtifactMtime(t, filepath.Join(dir, "new.html"), time.UnixMilli(3000))
	srv := newArtifactTestServer(t, dir)

	status, _, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if items[0].Name != "new.html" || items[1].Name != "mid.html" || items[2].Name != "old.html" {
		t.Fatalf("order = [%s %s %s], want newest first [new mid old]", items[0].Name, items[1].Name, items[2].Name)
	}

	// Equal mtimes must keep the walk order (a "<=" comparator reverses
	// equal-key items under SliceStable's insertion pass).
	writeArtifactFile(t, dir, "t1.html", "1")
	writeArtifactFile(t, dir, "t2.html", "2")
	eq := time.UnixMilli(5000)
	setArtifactMtime(t, filepath.Join(dir, "t1.html"), eq)
	setArtifactMtime(t, filepath.Join(dir, "t2.html"), eq)
	status, _, body = getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("second list status = %d, want 200", status)
	}
	items = nil
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	if items[0].Name != "t1.html" || items[1].Name != "t2.html" {
		t.Fatalf("equal-mtime order = [%s %s], want the stable walk order [t1 t2]", items[0].Name, items[1].Name)
	}
}

// TestMutC_ArtifactClearENOTDIR500 registers the routes under a path whose
// parent is a regular file: stat fails with ENOTDIR (not IsNotExist), which
// must surface as 500 rather than the nothing-to-clear 204.
func TestMutC_ArtifactClearENOTDIR500(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	mutCWriteFile(t, blocker, []byte("file"))
	bad := filepath.Join(blocker, "artifacts")
	mux := http.NewServeMux()
	RegisterArtifactRoutes(mux, bad, observeStubFn(nil, "", false))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if status := deleteArtifact(t, srv.URL+"/api/artifacts"); status != http.StatusInternalServerError {
		t.Fatalf("delete status = %d, want 500 (ENOTDIR is not the nothing-to-clear case)", status)
	}
	status, _, _ := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusInternalServerError {
		t.Fatalf("list status = %d, want 500 (ENOTDIR walk error surfaces)", status)
	}
}

func TestMutC_ArtifactFilePathDirect(t *testing.T) {
	dir := t.TempDir()
	if got, status := artifactFilePath(dir, ""); got != "" || status != http.StatusBadRequest {
		t.Errorf("empty name = (%q, %d), want (\"\", 400)", got, status)
	}
	full, status := artifactFilePath(dir, "sub/game.html")
	if status != 0 || full != filepath.Join(dir, "sub", "game.html") {
		t.Errorf("nested = (%q, %d), want the joined path", full, status)
	}
	if _, status := artifactFilePath(dir, "../secret"); status != http.StatusBadRequest {
		t.Errorf("traversal status = %d, want 400", status)
	}
}

// ---------------------------------------------------------------------------
// artifacts.go — gzip cache
// ---------------------------------------------------------------------------

// mutCCacheCounts wraps a deterministic payload producer that records calls.
type mutCCacheCounts struct {
	mu    sync.Mutex
	calls []string
}

func (cc *mutCCacheCounts) compress(key string, payload []byte) func() ([]byte, error) {
	return func() ([]byte, error) {
		cc.mu.Lock()
		cc.calls = append(cc.calls, key)
		cc.mu.Unlock()
		return payload, nil
	}
}

func (cc *mutCCacheCounts) count(key string) int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	n := 0
	for _, k := range cc.calls {
		if k == key {
			n++
		}
	}
	return n
}

func mutCCacheGet(t *testing.T, c *gzipCache, path string, mtime, size int64, compress func() ([]byte, error)) []byte {
	t.Helper()
	// A mutant that drops an unlock deadlocks here; fail fast instead of
	// stalling the whole suite.
	var gz []byte
	mutCDeadline(t, 5*time.Second, "gzipCache.get "+path, func() {
		out, err := c.get(path, mtime, size, compress)
		if err != nil {
			t.Errorf("get %s: %v", path, err)
			return
		}
		gz = out
	})
	return gz
}

// TestMutC_GzipCacheIdentity pins that a cached entry is keyed by the full
// (mtime, size) identity and that replacing an entry updates the byte
// accounting instead of accumulating.
func TestMutC_GzipCacheIdentity(t *testing.T) {
	c := newGzipCache(4, 1<<30)
	cc := &mutCCacheCounts{}

	gz1 := []byte(strings.Repeat("1", 10))
	gz2 := []byte(strings.Repeat("2", 30))
	if out := mutCCacheGet(t, c, "p", 100, 1, cc.compress("p", gz1)); !bytes.Equal(out, gz1) {
		t.Fatalf("first get = %q, want the compressed payload", out)
	}
	c.mu.Lock()
	bytes1, tick1 := c.bytes, c.tick
	c.mu.Unlock()
	if bytes1 != 10 {
		t.Fatalf("bytes after insert = %d, want 10", bytes1)
	}

	// Same path, new mtime: identity mismatch must re-compress.
	out := mutCCacheGet(t, c, "p", 200, 1, cc.compress("p", gz2))
	if cc.count("p") != 2 {
		t.Fatalf("compress calls = %d, want 2 (changed mtime invalidates)", cc.count("p"))
	}
	if !bytes.Equal(out, gz2) {
		t.Fatalf("second get = %q, want the fresh payload (stale identity must not be served)", out)
	}
	// Same path, new size: same rule.
	mutCCacheGet(t, c, "p", 200, 2, cc.compress("p", gz1))
	if cc.count("p") != 3 {
		t.Fatalf("compress calls = %d, want 3 (changed size invalidates)", cc.count("p"))
	}
	c.mu.Lock()
	bytes2, tick2 := c.bytes, c.tick
	entry := c.entries["p"]
	c.mu.Unlock()
	if bytes2 != 10 {
		t.Fatalf("bytes after replacement = %d, want 10 (old entry subtracted, new added)", bytes2)
	}
	if entry == nil || entry.size != 2 || entry.mtimeNano != 200 {
		t.Fatalf("entry = %+v, want the newest identity", entry)
	}
	if tick2 <= tick1 {
		t.Fatalf("tick = %d after more gets, must advance (was %d)", tick2, tick1)
	}
}

// TestMutC_GzipCacheJoinError pins single-flight error propagation: a joiner
// must receive the leader's error, not start its own compression.
func TestMutC_GzipCacheJoinError(t *testing.T) {
	c := newGzipCache(2, 1<<30)
	leaderStarted := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	compress := func() ([]byte, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(leaderStarted)
			<-release
			return nil, errors.New("leader boom")
		}
		return nil, errors.New("joiner boom")
	}

	errs := make(chan error, 2)
	go func() {
		_, err := c.get("p", 1, 1, compress)
		errs <- err
	}()
	<-leaderStarted
	go func() {
		_, err := c.get("p", 1, 1, compress)
		errs <- err
	}()
	// Give the joiner a moment to observe the in-flight call before releasing.
	time.Sleep(50 * time.Millisecond) // REAL-TIME: joins must observe the in-flight call before release
	close(release)

	var joinerErr error
	mutCDeadline(t, 5*time.Second, "joined get", func() {
		e1, e2 := <-errs, <-errs
		if e1 == nil || e2 == nil {
			t.Errorf("errors = %v / %v, want both non-nil", e1, e2)
		}
		if e1.Error() != "leader boom" {
			t.Errorf("first error = %v, want leader boom", e1)
		}
		joinerErr = e2
	})
	if joinerErr == nil || joinerErr.Error() != "leader boom" {
		t.Fatalf("joiner error = %v, want the leader's error (single flight)", joinerErr)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("compress calls = %d, want 1 (joiner must not re-compress)", got)
	}
}

// TestMutC_GzipCacheLRUTickAndVictim pins the LRU clock: inserts and hits
// advance tick, lastUsed reflects recency, and eviction picks the true LRU.
func TestMutC_GzipCacheLRUTickAndVictim(t *testing.T) {
	c := newGzipCache(2, 1<<30)
	cc := &mutCCacheCounts{}

	mutCCacheGet(t, c, "a", 1, 1, cc.compress("a", []byte("aaaaaa")))
	mutCCacheGet(t, c, "b", 1, 1, cc.compress("b", []byte("bbbbbb")))
	c.mu.Lock()
	usedA, usedB := c.entries["a"].lastUsed, c.entries["b"].lastUsed
	c.mu.Unlock()
	if usedA != 1 || usedB != 2 {
		t.Fatalf("lastUsed = a:%d b:%d, want 1 and 2 (insert stamps recency)", usedA, usedB)
	}
	// Touch a: it becomes most recent, b the victim.
	mutCCacheGet(t, c, "a", 1, 1, cc.compress("a", nil))
	mutCCacheGet(t, c, "c", 1, 1, cc.compress("c", []byte("cccccc")))

	c.mu.Lock()
	tick := c.tick
	_, hasB := c.entries["b"]
	_, hasA := c.entries["a"]
	_, hasC := c.entries["c"]
	c.mu.Unlock()
	if !hasA || hasB || !hasC {
		t.Fatalf("entries after eviction: a=%v b=%v c=%v, want b evicted (LRU) and a,c resident", hasA, hasB, hasC)
	}
	// a-insert + b-insert + a-hit + c-insert = 4 ticks.
	if tick != 4 {
		t.Fatalf("tick = %d, want 4 (every insert and hit advances the LRU clock)", tick)
	}
	if cc.count("b") != 1 {
		t.Fatalf("b compress calls = %d, want 1", cc.count("b"))
	}
}

// TestMutC_GzipCacheOversizedEntryKept pins the documented cap escape: a
// single entry larger than maxBytes stays servable instead of evicting
// itself.
func TestMutC_GzipCacheOversizedEntryKept(t *testing.T) {
	c := newGzipCache(3, 16)
	cc := &mutCCacheCounts{}
	payload := []byte(strings.Repeat("z", 40))

	mutCCacheGet(t, c, "huge", 1, 1, cc.compress("huge", payload))
	c.mu.Lock()
	bytes, entry := c.bytes, c.entries["huge"]
	c.mu.Unlock()
	if entry == nil {
		t.Fatal("oversized entry was evicted, want it servable (documented cap escape)")
	}
	if bytes != 40 {
		t.Fatalf("bytes = %d, want 40", bytes)
	}
	mutCCacheGet(t, c, "huge", 1, 1, cc.compress("huge", payload))
	if cc.count("huge") != 1 {
		t.Fatalf("compress calls = %d, want 1 (oversized entry is cached)", cc.count("huge"))
	}
}

// TestMutC_GzipCacheEvictionBytesAccounting pins that evicting a victim
// subtracts (not assigns) its size.
func TestMutC_GzipCacheEvictionBytesAccounting(t *testing.T) {
	c := newGzipCache(3, 1<<30)
	cc := &mutCCacheCounts{}
	mutCCacheGet(t, c, "a", 1, 1, cc.compress("a", bytes.Repeat([]byte{1}, 10)))
	mutCCacheGet(t, c, "b", 1, 1, cc.compress("b", bytes.Repeat([]byte{2}, 20)))
	mutCCacheGet(t, c, "c", 1, 1, cc.compress("c", bytes.Repeat([]byte{3}, 30)))
	mutCCacheGet(t, c, "d", 1, 1, cc.compress("d", bytes.Repeat([]byte{4}, 40)))

	c.mu.Lock()
	bytes := c.bytes
	_, hasA := c.entries["a"]
	c.mu.Unlock()
	if hasA {
		t.Fatal("a was not evicted at the entry cap")
	}
	if bytes != 20+30+40 {
		t.Fatalf("bytes after eviction = %d, want %d (victim size subtracted)", bytes, 20+30+40)
	}
}

// TestMutC_GzipCacheBudgetConstants pins the documented daemon-wide memory
// budget. The byte cap is only observable with ~400MiB of resident entries,
// which no hermetic test can produce; the constant itself is the contract.
func TestMutC_GzipCacheBudgetConstants(t *testing.T) {
	if artifactGzipCacheMaxEntries != 3 {
		t.Errorf("artifactGzipCacheMaxEntries = %d, want 3 (documented resident-model budget)", artifactGzipCacheMaxEntries)
	}
	if artifactGzipCacheMaxBytes != 400<<20 {
		t.Errorf("artifactGzipCacheMaxBytes = %d, want %d (documented pinned-memory budget)", artifactGzipCacheMaxBytes, 400<<20)
	}
	if artifactGzipMinSize != 1<<20 {
		t.Errorf("artifactGzipMinSize = %d, want %d (floor under which gzip costs more than it saves)", artifactGzipMinSize, 1<<20)
	}
}

// mutCFailWriter is an http.ResponseWriter whose Write always fails, so the
// gzip write-error path fires deterministically.
type mutCFailWriter struct {
	hdr http.Header
}

func (w *mutCFailWriter) Header() http.Header { return w.hdr }

func (w *mutCFailWriter) Write([]byte) (int, error) { return 0, errors.New("peer went away") }

func (w *mutCFailWriter) WriteHeader(int) {}

// TestMutC_ArtifactGzipWriteErrorWarns pins that a failed body write logs
// instead of passing silently (the only externally visible effect).
func TestMutC_ArtifactGzipWriteErrorWarns(t *testing.T) {
	rec := mutCCaptureSlog(t)
	resetArtifactGzipCache(t)
	path := filepath.Join(t.TempDir(), "warn.txt")
	mutCWriteFile(t, path, []byte(strings.Repeat("w", 2048)))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	mutCDeadline(t, 5*time.Second, "serveArtifactGzipped", func() {
		serveArtifactGzipped(&mutCFailWriter{hdr: http.Header{}}, r, f, st)
	})
	if !rec.hasMessage("artifact gzip write") {
		t.Fatalf("log messages = %v, want an \"artifact gzip write\" warn", rec.messages())
	}
}
