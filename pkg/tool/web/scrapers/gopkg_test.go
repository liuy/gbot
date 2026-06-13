package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleGoPkg_NoMatch(t *testing.T) {
	result, _ := HandleGoPkg(context.Background(), mustParseURL(t, "https://example.com/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-pkg.go.dev host, got %+v", result)
	}
}

func TestHandleGoPkg_NotAPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "@latest") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>about page</body></html>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/about"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result, got nil")
	}
}

func TestHandleGoPkg_EmptyPath(t *testing.T) {
	result, _ := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty path, got %+v", result)
	}
}

func TestHandleGoPkg_Success(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<body>
	<div data-test-id="UnitHeader-version">v1.2.3</div>
	<div data-test-id="UnitHeader-license">MIT</div>
	<div data-test-id="UnitHeader-imports">github.com/owner/repo</div>
	<div data-test-id="UnitHeader-importedby">5 packages</div>
	<div data-test-id="Unit-readmeContent">
		<h1>Package foo</h1>
		<p>This package does cool things.</p>
	</div>
</body>
</html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Host, "proxy.golang.org") || strings.Contains(r.URL.Path, "@latest") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Version":"v1.2.3","Time":"2024-01-15T00:00:00Z"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "v1.2.3") {
		t.Errorf("expected version, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
}

func TestHandleGoPkg_FullContent(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<body>
	<div data-test-id="UnitHeader-version">v2.0.0</div>
	<div data-test-id="UnitHeader-license">BSD-3-Clause</div>
	<div data-test-id="UnitHeader-imports">9 packages</div>
	<div data-test-id="UnitHeader-importedby">100 packages</div>
	<div data-test-id="UnitHeader-commitTime">Published: Jan 15, 2024 License: BSD-3-Clause</div>
	<div data-test-id="Unit-readmeContent">
		<h1>Package</h1>
		<p>Synopsis here.</p>
		<a href="#Foo">Foo</a>
		<a href="#Bar">Bar</a>
		<a href="#lowercase">skip</a>
	</div>
</body>
</html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "@latest") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Version":"v2.0.0","Time":"2024-01-15T00:00:00Z"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/example.com/pkg"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "v2.0.0") {
		t.Errorf("expected version, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "BSD-3-Clause") {
		t.Errorf("expected license, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Foo") || !strings.Contains(result.Content, "Bar") {
		t.Errorf("expected exports, got: %q", result.Content)
	}
	if strings.Contains(result.Content, "lowercase") {
		t.Error("lowercase export should be skipped")
	}
}

func TestHandleGoPkg_NoReadmeContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "@latest") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Version":"v1.0.0","Time":"2024-01-15T00:00:00Z"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>no readme</body></html>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/example.com/pkg"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# example.com/pkg") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestExtractReadmeContent_NoMarker(t *testing.T) {
	synopsis, readme, exports := extractReadmeContent("<html><body>no readme marker</body></html>")
	if synopsis != "" || readme != "" || len(exports) != 0 {
		t.Errorf("expected empty results for no marker, got synopsis=%q readme=%q exports=%v", synopsis, readme, exports)
	}
}

func TestExtractReadmeContent_WithExports(t *testing.T) {
	html := `<div data-test-id="Unit-readmeContent">
		<p>This is the synopsis text.</p>
		<p>More content here.</p>
		<a href="#Foo">Foo</a>
		<a href="#Bar">Bar</a>
		<a href="#lowercase">skip</a>
	</div>`
	synopsis, readme, exports := extractReadmeContent(html)
	if synopsis == "" {
		t.Error("expected synopsis, got empty")
	}
	if !strings.Contains(synopsis, "synopsis") {
		t.Errorf("expected synopsis text, got %q", synopsis)
	}
	if len(exports) != 2 {
		t.Errorf("expected 2 exports, got %v", exports)
	}
	if readme == "" {
		t.Error("expected readme, got empty")
	}
}

func TestExtractReadmeContent_LongSynopsis(t *testing.T) {
	var longText strings.Builder
	for range 50 {
		longText.WriteString("word ")
	}
	html := `<div data-test-id="Unit-readmeContent"><p>` + longText.String() + `</p></div>`
	synopsis, readme, _ := extractReadmeContent(html)
	if synopsis == "" {
		t.Error("expected synopsis, got empty")
	}
	if !strings.Contains(synopsis, "word") {
		t.Errorf("expected synopsis to contain words, got %q", synopsis)
	}
	if readme == "" {
		t.Errorf("expected readme content, got empty")
	}
}

func TestHandleGoPkg_ProxyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html>no proxy data</html>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/example.com/pkg"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	// Should still produce output even with proxy error (version just won't be there).
	if !strings.Contains(result.Content, "# example.com/pkg") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleGoPkg_OverridesViaCommitTime(t *testing.T) {
	html := `<html>
	<body>
		<div data-test-id="UnitHeader-version">v1.0.0</div>
		<div data-test-id="UnitHeader-commitTime">Published: Feb 28, 2026</div>
		<div data-test-id="Unit-readmeContent"><p>Readme.</p></div>
	</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "@latest") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Version":"v1.0.0","Time":"2024-01-15T00:00:00Z"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/example.com/pkg"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
}
