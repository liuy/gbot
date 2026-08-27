package wui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newArtifactTestServer mounts artifact routes plus the SPA catch-all —
// the same coexistence the daemon mux uses, so route priority is exercised
// on every request, not just the dedicated priority case.
func newArtifactTestServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterArtifactRoutes(mux, dir)
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
