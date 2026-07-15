package wui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegisterStaticRoutes_ServesIndexHTML verifies the root path serves
// index.html with no-cache headers.
func TestRegisterStaticRoutes_ServesIndexHTML(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "<!DOCTYPE html>") && !strings.Contains(string(body), "<html") {
		t.Errorf("body does not contain HTML: first 200 chars = %q", truncBody(string(body), 200))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache, no-store, must-revalidate" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache, no-store, must-revalidate")
	}
}

// TestRegisterStaticRoutes_ServesIndexHTMLDirectly verifies /index.html is
// served as a real file.
func TestRegisterStaticRoutes_ServesIndexHTMLDirectly(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/index.html")
	if err != nil {
		t.Fatalf("GET /index.html: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "<html") {
			t.Errorf("body does not contain <html tag: first 200 chars = %q", truncBody(string(body), 200))
		}
	}
}

// TestRegisterStaticRoutes_SPARoutingFallback verifies that unknown paths
// fall back to index.html (SPA client-side routing).
func TestRegisterStaticRoutes_SPARoutingFallback(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/some/deep/nonexistent/route")
	if err != nil {
		t.Fatalf("GET /some/deep/nonexistent/route: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (SPA fallback)", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "<html") {
		t.Errorf("SPA fallback should serve index.html, got: first 200 chars = %q", truncBody(string(body), 200))
	}
}

// TestRegisterStaticRoutes_ServesCSSAsset verifies that real asset files
// (index.css) are served correctly.
func TestRegisterStaticRoutes_ServesCSSAsset(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/index.css")
	if err != nil {
		t.Fatalf("GET /index.css: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Error("CSS asset body is empty, want non-empty CSS content")
	}
}

// TestRegisterStaticRoutes_ServesJSAsset verifies that index.js is served.
func TestRegisterStaticRoutes_ServesJSAsset(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/index.js")
	if err != nil {
		t.Fatalf("GET /index.js: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Error("JS asset body is empty, want non-empty JS content")
	}
}

// TestRegisterStaticRoutes_DirectoryFallback verifies that directory paths
// (e.g. /assets/) fall back to index.html, not a 404 or directory listing.
func TestRegisterStaticRoutes_DirectoryFallback(t *testing.T) {
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/assets/")
	if err != nil {
		t.Fatalf("GET /assets/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (SPA fallback for directory)", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "<html") {
		t.Errorf("directory path should fall back to index.html: first 200 chars = %q", truncBody(string(body), 200))
	}
}

// truncBody returns the first n characters of s for error messages.
func truncBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
