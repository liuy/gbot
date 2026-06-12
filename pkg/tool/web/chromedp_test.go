package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// isChromedpAvailable — caching behavior
// ---------------------------------------------------------------------------

func TestIsChromedpAvailable_Cached(t *testing.T) {
	// Call twice — must return same result without panic
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

	// Pool starts not ready
	pool.mu.Lock()
	if pool.ready {
		t.Error("pool should start not ready")
	}
	pool.mu.Unlock()

	// Reset on empty pool should not panic
	pool.reset()

	pool.mu.Lock()
	if pool.ready {
		t.Error("pool should still not be ready after reset")
	}
	pool.mu.Unlock()
}

func TestChromePool_GetWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pool := &ChromePool{}
	_, _, err := pool.getWithProxy(ctx, "")
	if err == nil {
		t.Fatal("should error with canceled context")
	}
}

// ---------------------------------------------------------------------------
// chromedpFetch — integration test (requires Chrome)
// ---------------------------------------------------------------------------

func TestChromedpFetch_RealPage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	available, _ := isChromedpAvailable()
	if !available {
		t.Skip("Chrome/Chromium not installed, skipping integration test")
	}

	// Reset pool to ensure clean state
	defaultPool.reset()
	// Restore chromedpAvailable cache so it re-detects
	chromedpAvailable.once = sync.Once{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head></head><body><div id="app"></div><script>document.getElementById('app').textContent='JS Rendered Content';</script></body></html>`)
	}))
	defer server.Close()

	html, err := chromedpFetch(context.Background(), server.URL, 20*time.Second, "")
	if err != nil {
		t.Fatalf("chromedpFetch() error = %v", err)
	}
	if !strings.Contains(html, "JS Rendered Content") {
		t.Errorf("chromedp should have executed JS, got: %s", html[:min(500, len(html))])
	}
}

func TestChromedpFetch_InvalidURL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	available, _ := isChromedpAvailable()
	if !available {
		t.Skip("Chrome/Chromium not installed, skipping integration test")
	}

	defaultPool.reset()
	chromedpAvailable.once = sync.Once{}

	_, err := chromedpFetch(context.Background(), "http://127.0.0.1:1", 5*time.Second, "")
	if err == nil {
		t.Fatal("should error for unreachable URL")
	}
}

func TestChromedpFetch_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	available, _ := isChromedpAvailable()
	if !available {
		t.Skip("Chrome/Chromium not installed, skipping integration test")
	}

	defaultPool.reset()
	chromedpAvailable.once = sync.Once{}

	// Server that never responds
	neverRespond := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-neverRespond
	}))
	defer close(neverRespond)
	defer server.Close()

	_, err := chromedpFetch(context.Background(), server.URL, 1*time.Second, "")
	if err == nil {
		t.Fatal("should error on timeout")
	}
}

func TestChromedpFetch_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	available, _ := isChromedpAvailable()
	if !available {
		t.Skip("Chrome/Chromium not installed, skipping integration test")
	}

	defaultPool.reset()
	chromedpAvailable.once = sync.Once{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, "<html><body>%s</body></html>", r.URL.Path)
	}))
	defer server.Close()

	const n = 3
	errs := make(chan error, n)
	for i := range n {
		go func(i int) {
			_, err := chromedpFetch(context.Background(), fmt.Sprintf("%s/page/%d", server.URL, i), 20*time.Second, "")
			errs <- err
		}(i)
	}

	for range n {
		if err := <-errs; err != nil {
			t.Errorf("concurrent fetch %d failed: %v", 0, err)
		}
	}
}

func TestChromedpFetch_CrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	available, _ := isChromedpAvailable()
	if !available {
		t.Skip("Chrome/Chromium not installed, skipping integration test")
	}

	defaultPool.reset()
	chromedpAvailable.once = sync.Once{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body>Hello</body></html>")
	}))
	defer server.Close()

	// First fetch should work
	_, err := chromedpFetch(context.Background(), server.URL, 20*time.Second, "")
	if err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}

	// Simulate Chrome crash by resetting pool
	defaultPool.reset()

	// Second fetch should start a new Chrome and still work
	_, err = chromedpFetch(context.Background(), server.URL, 20*time.Second, "")
	if err != nil {
		t.Fatalf("fetch after crash recovery failed: %v", err)
	}
}
