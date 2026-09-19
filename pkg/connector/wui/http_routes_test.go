package wui

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gzipBody decompresses a gzip-encoded response body and returns the plaintext.
func gzipBody(t *testing.T, body io.Reader) string {
	t.Helper()
	gzr, err := gzip.NewReader(body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gzr.Close()
	data, err := io.ReadAll(gzr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	return string(data)
}

// getDecompressed fetches a URL with gzip transport disabled (so we can
// verify the raw Content-Encoding header), then decompresses the body.
func getDecompressed(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DisableCompression: true,
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("Content-Encoding = %q, want %q", resp.Header.Get("Content-Encoding"), "gzip")
	}
	body := gzipBody(t, resp.Body)
	defer resp.Body.Close()
	return resp, body
}

// TestRegisterStaticRoutes_ServesGzipIndexHTML verifies the root path serves
// gzip-compressed index.html with correct headers.
func TestRegisterStaticRoutes_ServesGzipIndexHTML(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, body := getDecompressed(t, srv.URL+"/")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(body, "<!DOCTYPE html>") && !strings.Contains(body, "<html") {
		t.Errorf("body does not contain HTML: first 200 chars = %q", truncBody(body, 200))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache, no-store, must-revalidate" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache, no-store, must-revalidate")
	}
}

// TestRegisterStaticRoutes_SPAFallback verifies that any path serves the
// same single-file index.html (SPA client-side routing).
func TestRegisterStaticRoutes_SPAFallback(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, body1 := getDecompressed(t, srv.URL+"/")
	_, body2 := getDecompressed(t, srv.URL+"/some/deep/nonexistent/route")
	_, body3 := getDecompressed(t, srv.URL+"/index.css")
	_, body4 := getDecompressed(t, srv.URL+"/assets/")

	if body1 != body2 || body2 != body3 || body3 != body4 {
		t.Error("all paths should serve identical content (single-file mode)")
	}
}

// TestRegisterStaticRoutes_ServesPrebundledNovnc verifies the exact-path
// noVNC prebundle route: it must NOT fall through to the SPA catch-all.
func TestRegisterStaticRoutes_ServesPrebundledNovnc(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, body := getDecompressed(t, srv.URL+"/assets/novnc.esm.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", ct)
	}
	if len(body) < 1000 {
		t.Errorf("body length = %d, want a real bundle", len(body))
	}
	if !strings.Contains(body, "prefer-software") {
		t.Error("body lacks the software-decode patch")
	}
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("body is the SPA fallback, not the prebundle")
	}

	// Query strings (cache-busting) must select the same route.
	respQ, bodyQ := getDecompressed(t, srv.URL+"/assets/novnc.esm.js?v=1")
	if respQ.StatusCode != http.StatusOK || bodyQ != body {
		t.Errorf("query variant served differently: status=%d same=%v", respQ.StatusCode, bodyQ == body)
	}
}

// TestRegisterStaticRoutes_NovncHead verifies HEAD reaches the route (Go 1.22
// "GET" patterns also match HEAD) rather than the catch-all.
func TestRegisterStaticRoutes_NovncHead(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Head(srv.URL + "/assets/novnc.esm.js")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript (SPA catch-all?)", ct)
	}
}

func truncBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
