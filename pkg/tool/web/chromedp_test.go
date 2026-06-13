package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool/web/scrapers"
)

// ---------------------------------------------------------------------------
// Shared Chrome pool setup — all Chrome tests reuse the same instance.
// ---------------------------------------------------------------------------

var chromeAvailable bool

func init() {
	chromeAvailable, _ = isChromedpAvailable()
}

func skipNoChrome(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	if !chromeAvailable {
		t.Skip("Chrome/Chromium not installed")
	}
}

// ---------------------------------------------------------------------------
// isChromedpAvailable — caching behavior
// ---------------------------------------------------------------------------

func TestIsChromedpAvailable_Cached(t *testing.T) {
	a1, p1 := isChromedpAvailable()
	a2, p2 := isChromedpAvailable()
	if a1 != a2 {
		t.Errorf("availability changed between calls: %v -> %v", a1, a2)
	}
	if p1 != p2 {
		t.Errorf("path changed between calls: %s -> %s", p1, p2)
	}
}

// ---------------------------------------------------------------------------
// ChromePool — lifecycle
// ---------------------------------------------------------------------------

func TestChromePool_ResetAllowsNewInstance(t *testing.T) {
	pool := &ChromePool{}

	pool.mu.Lock()
	if pool.ready {
		t.Error("pool should start not ready")
	}
	pool.mu.Unlock()

	pool.reset()

	pool.mu.Lock()
	if pool.ready {
		t.Error("pool should still not be ready after reset")
	}
	pool.mu.Unlock()
}

func TestChromePool_GetWithCanceledContext(t *testing.T) {
	skipNoChrome(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pool := &ChromePool{}
	_, _, err := pool.getWithProxy(ctx, "")
	if err != nil {
		t.Fatalf("getWithProxy should work with canceled context since allocator uses Background: %v", err)
	}
	pool.reset()
}

// ---------------------------------------------------------------------------
// chromedpFetch — integration tests (requires Chrome)
// ---------------------------------------------------------------------------

func TestChromedpFetch_RealPage(t *testing.T) {
	skipNoChrome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head></head><body><div id="app"></div><script>document.getElementById('app').textContent='JS Rendered Content';</script></body></html>`)
	}))
	defer server.Close()

	html, err := chromedpFetch(context.Background(), server.URL, 10*time.Second, "")
	if err != nil {
		t.Fatalf("chromedpFetch() error = %v", err)
	}
	if !strings.Contains(html, "JS Rendered Content") {
		t.Errorf("chromedp should have executed JS, got: %s", html[:min(500, len(html))])
	}
}

func TestChromedpFetch_InvalidURL(t *testing.T) {
	skipNoChrome(t)

	_, err := chromedpFetch(context.Background(), "http://127.0.0.1:1", 5*time.Second, "")
	if err == nil {
		t.Fatal("should error for unreachable URL")
	}
}

func TestChromedpFetch_ConcurrentAccess_Mock(t *testing.T) {
	orig := chromedpFetch
	var mu sync.Mutex
	seen := map[string]bool{}
	chromedpFetch = func(ctx context.Context, url string, timeout time.Duration, proxyURL string) (string, error) {
		mu.Lock()
		seen[url] = true
		mu.Unlock()
		return "<html><body>" + url + "</body></html>", nil
	}
	defer func() { chromedpFetch = orig }()

	const n = 3
	errs := make(chan error, n)
	for i := range n {
		go func(i int) {
			_, err := chromedpFetch(context.Background(), fmt.Sprintf("http://example.com/%d", i), 10*time.Second, "")
			errs <- err
		}(i)
	}

	for range n {
		if err := <-errs; err != nil {
			t.Errorf("concurrent fetch failed: %v", err)
		}
	}
	if len(seen) != n {
		t.Errorf("expected %d unique URLs, got %d", n, len(seen))
	}
}

// ---------------------------------------------------------------------------
// fetchWithChrome + executeFetch JS mode — integration (requires Chrome)
// ---------------------------------------------------------------------------

func TestFetchWithChrome_HTMLPage(t *testing.T) {
	skipNoChrome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body><h1>Hello Chrome</h1></body></html>`)
	}))
	defer server.Close()

	result, err := fetchWithChrome(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("fetchWithChrome() error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "Hello Chrome") {
		t.Errorf("content should contain page text, got: %q", truncateStr(output.Content, 200))
	}
}

func TestFetchWithChrome_Error(t *testing.T) {
	skipNoChrome(t)

	_, err := fetchWithChrome(context.Background(), "http://127.0.0.1:1", nil)
	if err == nil {
		t.Fatal("should error for unreachable URL")
	}
}

func TestExecuteFetch_JSMode(t *testing.T) {
	skipNoChrome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body><div id="app"></div><script>document.getElementById('app').textContent='Dynamic Content';</script></body></html>`)
	}))
	defer server.Close()

	result, err := execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": "%s", "js": true}`, server.URL)), nil, nil, nil)
	if err != nil {
		t.Fatalf("execute() with js=true error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "Dynamic Content") {
		t.Errorf("JS mode should render dynamic content, got: %q", truncateStr(output.Content, 200))
	}
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ---------------------------------------------------------------------------
// Mock-based tests — replace chromedpFetch to avoid launching Chrome.
// These cover fetchWithChrome and executeFetch JS paths instantly.
// ---------------------------------------------------------------------------

func TestFetchWithChrome_MockSuccess(t *testing.T) {
	orig := chromedpFetch
	chromedpFetch = func(ctx context.Context, url string, timeout time.Duration, proxyURL string) (string, error) {
		return "<html><body><h1>Mock Page</h1></body></html>", nil
	}
	defer func() { chromedpFetch = orig }()

	result, err := fetchWithChrome(context.Background(), "https://example.com", nil)
	if err != nil {
		t.Fatalf("fetchWithChrome() error = %v", err)
	}
	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "Mock Page") {
		t.Errorf("content should contain mock text, got: %q", output.Content)
	}
}

func TestFetchWithChrome_MockError(t *testing.T) {
	orig := chromedpFetch
	chromedpFetch = func(ctx context.Context, url string, timeout time.Duration, proxyURL string) (string, error) {
		return "", fmt.Errorf("mock chrome failure")
	}
	defer func() { chromedpFetch = orig }()

	_, err := fetchWithChrome(context.Background(), "https://example.com", nil)
	if err == nil {
		t.Fatal("expected error from mock chrome failure")
	}
	if !strings.Contains(err.Error(), "mock chrome failure") {
		t.Errorf("error = %q, want containing 'mock chrome failure'", err.Error())
	}
}

func TestExecuteFetch_JSMode_Mock(t *testing.T) {
	orig := chromedpFetch
	chromedpFetch = func(ctx context.Context, url string, timeout time.Duration, proxyURL string) (string, error) {
		return "<html><body><div>JS Rendered</div></body></html>", nil
	}
	defer func() { chromedpFetch = orig }()

	result, err := execute(context.Background(), json.RawMessage(`{"query": "https://example.com", "js": true}`), nil, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "JS Rendered") {
		t.Errorf("mock JS mode should contain rendered text, got: %q", output.Content)
	}
}

func TestExecuteFetch_ScraperJSFetcher_Mock(t *testing.T) {
	jsCalled := false
	orig := chromedpFetch
	chromedpFetch = func(ctx context.Context, url string, timeout time.Duration, proxyURL string) (string, error) {
		jsCalled = true
		return "<html><body>JS content</body></html>", nil
	}
	defer func() { chromedpFetch = orig }()

	// Register a scraper that calls the JSFetcher.
	reg := scrapers.New()
	reg.Register(func(ctx context.Context, u *neturl.URL, client *http.Client, js scrapers.JSFetcher) (*scrapers.Result, error) {
		if js != nil {
			html, err := js(ctx, "https://example.com/js")
			if err != nil {
				return nil, err
			}
			return &scrapers.Result{Content: html, Method: "js-scraper", ContentType: "text/html"}, nil
		}
		return nil, nil
	})

	result, err := execute(context.Background(), json.RawMessage(`{"query": "https://example.com/page"}`), nil, http.DefaultClient, reg)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "JS content") {
		t.Errorf("expected JS content from scraper, got: %q", output.Content)
	}
	if !jsCalled {		t.Error("expected JSFetcher to be called")
	}
}
