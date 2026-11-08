package web

// Kill tests for chromedp.go mutation escapes (census 2026-11-07).
// No-Chrome unit tests first; the Chrome-backed tests are guarded by

import (
	"net/http"
	"net/http/httptest"

	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func setNoChromeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CHROME_PATH", "")
	t.Setenv("PATH", "")
}

func TestCDKFindChromeBinPermBits(t *testing.T) {
	dir := t.TempDir()
	write := func(mode os.FileMode) string {
		p := filepath.Join(dir, "bin"+mode.String())
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		return p
	}

	// Exec-only bits are accepted; write-only bits are not. PATH is empty
	// so the fallback lookup cannot rescue a rejection.
	t.Setenv("PATH", "")
	t.Setenv("CHROME_PATH", write(0o002))
	if p, ok := findChromeBin(); ok {
		t.Errorf("findChromeBin() = %q, true for write-only binary; want not found", p)
	}

	t.Setenv("CHROME_PATH", write(0o001))
	if p, ok := findChromeBin(); !ok || p == "" {
		t.Errorf("findChromeBin() = %q, %v for exec-only binary; want found", p, ok)
	}
}

func TestCDKGetWithProxyFastPathReuse(t *testing.T) {
	// No-Chrome env so the fall-through paths fail loudly instead of
	// launching a real browser.
	setNoChromeEnv(t)
	pool := &ChromePool{ready: true, allocCtx: context.Background(), proxyURL: ""}
	for i := range 2 {
		ctx, cancel, err := pool.getWithProxy("")
		if err != nil {
			t.Fatalf("call %d: error %v", i+1, err)
		}
		if ctx == nil {
			t.Fatalf("call %d: ctx is nil", i+1)
		}
		if cancel == nil {
			t.Fatalf("call %d: cancel is nil", i+1)
		}
	}
}

func TestCDKGetWithProxyNoChrome(t *testing.T) {
	setNoChromeEnv(t)
	pool := &ChromePool{}
	_, _, err := pool.getWithProxy("")
	if err == nil {
		t.Fatal("expected error without any Chrome binary")
	}
	if !strings.Contains(err.Error(), "no Chrome/Chromium binary found") {
		t.Errorf("error = %q, want 'no Chrome/Chromium binary found'", err.Error())
	}
}

func TestCDKGetWithProxyProxyChangeTearsDown(t *testing.T) {
	setNoChromeEnv(t)
	var canceled bool
	pool := &ChromePool{
		ready:    true,
		allocCtx: context.Background(),
		proxyURL: "http://old:1",
		cancel:   func() { canceled = true },
	}
	_, _, err := pool.getWithProxy("http://new:2")
	if err == nil || !strings.Contains(err.Error(), "no Chrome/Chromium binary found") {
		t.Fatalf("error = %v, want no-chrome error", err)
	}
	if !canceled {
		t.Error("proxy change must cancel the old allocator")
	}
	if pool.ready {
		t.Error("pool must not stay ready after teardown")
	}
	if pool.allocCtx != nil || pool.cancel != nil {
		t.Errorf("pool state not cleared: %+v", pool)
	}
}

func TestCDKResetClearsState(t *testing.T) {
	var canceled bool
	pool := &ChromePool{
		ready:    true,
		allocCtx: context.Background(),
		proxyURL: "http://x:1",
		cancel:   func() { canceled = true },
	}
	pool.reset()
	pool.reset()
	if !canceled {
		t.Error("reset must cancel the allocator")
	}
	if pool.ready || pool.allocCtx != nil || pool.cancel != nil {
		t.Errorf("reset left state behind: %+v", pool)
	}
}

func TestCDKRealFetchNoChrome(t *testing.T) {
	setNoChromeEnv(t)
	defaultPool.reset()
	_, err := realChromedpFetch("http://example.com/", 5*time.Second, "")
	if err == nil {
		t.Fatal("expected error without Chrome")
	}
	if !strings.Contains(err.Error(), "chrome not available") {
		t.Errorf("error = %q, want 'chrome not available' prefix", err.Error())
	}
	inner := errors.Unwrap(err)
	if inner == nil || !strings.Contains(inner.Error(), "no Chrome/Chromium binary found") {
		t.Errorf("unwrap = %v, want the no-binary error (wrapped)", inner)
	}
}

func TestCDKRealFetchBadChrome(t *testing.T) {
	// A real executable that is not a browser: startup probing must fail
	// with a wrapped, unwrap-able error.
	t.Setenv("CHROME_PATH", "/bin/true")
	t.Setenv("PATH", "")
	defaultPool.reset()
	_, err := realChromedpFetch("http://example.com/", 5*time.Second, "")
	if err == nil {
		t.Fatal("expected error for non-browser CHROME_PATH")
	}
	if !strings.Contains(err.Error(), "chrome not available") {
		t.Errorf("error = %q", err.Error())
	}
	inner := errors.Unwrap(err)
	if inner == nil || !strings.Contains(inner.Error(), "chrome startup failed") {
		t.Fatalf("unwrap = %v, want startup failure (wrapped)", inner)
	}
	if errors.Unwrap(inner) == nil {
		t.Error("startup failure must wrap the probe error (%w)")
	}
}

func TestCDKCancelWithDeadline(t *testing.T) {
	// A cancel that never settles must be abandoned after ~5s.
	start := time.Now() // REAL-TIME: measuring the abandon deadline
	var wg sync.WaitGroup
	wg.Add(1)
	cancelWithDeadline(func() { wg.Wait() }, pageCloseTimeout)
	if d := time.Since(start); d < 4500*time.Millisecond || d > 5500*time.Millisecond {
		t.Errorf("blocking cancel abandoned after %v, want ~5s", d)
	}

	// A settling cancel returns immediately.
	start = time.Now() // REAL-TIME: fast-cancel latency
	cancelWithDeadline(func() {}, pageCloseTimeout)
	if d := time.Since(start); d > time.Second {
		t.Errorf("settling cancel took %v, want immediate", d)
	}
}

// --- Chrome-backed tests below; they need a real browser. ---

func cdkPage(t *testing.T, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestCDKFetchUAAndStealth(t *testing.T) {
	if !chromeAvailable {
		t.Skip("no Chrome/Chromium binary")
	}
	// The UA override turns HeadlessChrome/Linux into Chrome/Windows. The
	// hardwareConcurrency spoof rides the same payload but is NOT asserted
	// here — this host pins 4 natively, so the spoof is indistinguishable
	// (ledger RESIDUAL 165/166); only the three UA segments are pinned.
	url := cdkPage(t, `<html><body><h1 id="m"></h1><script>
document.getElementById('m').textContent = navigator.userAgent + '|' + navigator.hardwareConcurrency;
</script></body></html>`)
	html, err := realChromedpFetch(url, 20*time.Second, "")
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}
	if !strings.Contains(html, "Windows NT 10.0") {
		t.Errorf("user agent not overridden to Windows: %q", html)
	}
	if strings.Contains(html, "HeadlessChrome") {
		t.Errorf("HeadlessChrome leaked into the UA: %q", html)
	}
	if strings.Contains(html, "X11;") {
		t.Errorf("Linux platform leaked into the UA: %q", html)
	}
}

func TestCDKWaitForJSRenderDirect(t *testing.T) {
	if !chromeAvailable {
		t.Skip("no Chrome/Chromium binary")
	}
	// Load happens untimed; only the wait action is measured so page-load
	// latency cannot mask the settle windows.
	run := func(body string, waitMax, runTimeout time.Duration) (time.Duration, string, error) {
		url := cdkPage(t, body)
		ctx, cancel, err := defaultPool.getWithProxy("")
		if err != nil {
			t.Fatalf("pool: %v", err)
		}
		defer cancelWithDeadline(cancel, pageCloseTimeout)
		ctx, timeoutCancel := context.WithTimeout(ctx, runTimeout)
		defer timeoutCancel()
		if err := chromedp.Run(ctx,
			chromedp.Navigate(url),
			chromedp.WaitReady("body", chromedp.ByQuery),
		); err != nil {
			t.Fatalf("navigate: %v", err)
		}
		var html string
		start := time.Now() // REAL-TIME: measuring the render-settle window
		err = chromedp.Run(ctx,
			waitForJSRender(50*time.Millisecond, waitMax),
			chromedp.OuterHTML("html", &html, chromedp.ByQuery),
		)
		return time.Since(start), html, err
	}

	t.Run("empty body waits the full window", func(t *testing.T) {
		d, _, err := run("<html><head></head><body></body></html>", 400*time.Millisecond, 5*time.Second)
		if err != nil {
			t.Fatalf("run error: %v", err)
		}
		if d < 350*time.Millisecond {
			t.Errorf("returned after %v on an empty body; want the ~400ms window", d)
		}
	})

	t.Run("stable body settles fast", func(t *testing.T) {
		d, _, err := run("<html><head></head><body><p>x</p></body></html>", 400*time.Millisecond, 5*time.Second)
		if err != nil {
			t.Fatalf("run error: %v", err)
		}
		if d >= 250*time.Millisecond {
			t.Errorf("returned after %v on a stable body; want a quick settle (<250ms)", d)
		}
	})

	t.Run("first child must appear before returning", func(t *testing.T) {
		d, html, err := run(`<html><head><script>
setTimeout(function(){ var p = document.createElement('p'); p.textContent = 'LATE'; document.body.appendChild(p); }, 100);
</script></head><body></body></html>`, 2*time.Second, 5*time.Second)
		if err != nil {
			t.Fatalf("run error: %v", err)
		}
		// Floor is one full poll cycle (~50ms + eval latency), not the page's
		// 100ms timer: under load WaitReady can outlive the timer, so the
		// child is already rendered when measurement starts and a correct
		// two-read settle legitimately returns at ~70ms. The count>0 term
		// itself is pinned by the empty-body subtest (full-window wait) and
		// by the LATE assertion below.
		if d < 45*time.Millisecond {
			t.Errorf("returned after %v; the count>0 term must hold until the first child renders", d)
		}
		if !strings.Contains(html, "LATE") {
			t.Errorf("html missing late child; settled before render:\n%s", html)
		}
	})

	t.Run("never-settling page bounded by window", func(t *testing.T) {
		d, _, err := run(`<html><head><script>
setInterval(function(){ document.body.appendChild(document.createElement('span')); }, 40);
</script></head><body></body></html>`, 400*time.Millisecond, 3*time.Second)
		if err != nil {
			t.Fatalf("run error: %v (deadline return must be nil)", err)
		}
		if d < 360*time.Millisecond {
			t.Errorf("returned after %v; never-settling page must run the full window", d)
		}
		if d > time.Second {
			t.Errorf("returned after %v; window must bound the wait", d)
		}
	})
}

func TestCDKResetOnErrorAndKeepOnTimeout(t *testing.T) {
	if !chromeAvailable {
		t.Skip("no Chrome/Chromium binary")
	}
	warm := cdkPage(t, "<html><body><p>ok</p></body></html>")
	if _, err := realChromedpFetch(warm, 20*time.Second, ""); err != nil {
		t.Fatalf("warm fetch: %v", err)
	}
	if !defaultPool.ready {
		t.Fatal("pool must be ready after a successful fetch")
	}
	if defaultPool.allocCtx == nil || defaultPool.cancel == nil {
		t.Fatal("pool must record its allocator and cancel after startup")
	}

	// A navigation-level failure with a live context resets the pool.
	_, err := realChromedpFetch("http://[::1]:namedport/", 10*time.Second, "")
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
	if !strings.Contains(err.Error(), "chromedp fetch") {
		t.Errorf("error = %q, want 'chromedp fetch' prefix", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Error("fetch error must wrap the chromedp error")
	}
	if defaultPool.ready {
		t.Error("pool must reset after a non-timeout failure")
	}

	// A timeout must keep the pool (Chrome itself is healthy).
	if _, err := realChromedpFetch(warm, 20*time.Second, ""); err != nil {
		t.Fatalf("rewarm fetch: %v", err)
	}
	neverSettles := cdkPage(t, `<html><body><div id="d"></div><script>
setInterval(function(){ document.getElementById('d').appendChild(document.createElement('span')); }, 40);
</script></body></html>`)
	_, err = realChromedpFetch(neverSettles, 300*time.Millisecond, "")
	if err == nil {
		t.Fatal("expected timeout error for never-settling page")
	}
	if defaultPool.ready != true {
		t.Error("pool must be kept after a timeout failure")
	}
}

func TestCDKPoolLockedDuringStartup(t *testing.T) {
	if !chromeAvailable {
		t.Skip("no Chrome/Chromium binary")
	}
	// The startup path must hold p.mu for its whole body: concurrent pool
	// calls must serialize. Only meaningful under -race; the mutant that
	// drops the defer unlocks immediately and races these writes.
	p := &ChromePool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel, err := p.getWithProxy("")
		if err == nil && cancel != nil {
			cancelWithDeadline(cancel, pageCloseTimeout)
		}
		_ = ctx
	}()
	// reset and a second get must serialize against the in-flight startup;
	// reset repeatedly because startup duration varies with machine load.
	for range 5 {
		p.reset()
		time.Sleep(100 * time.Millisecond) // REAL-TIME: overlap with startup
	}
	if _, _, err := p.getWithProxy(""); err != nil {
		t.Errorf("second call: %v", err)
	}
	<-done
}

func TestCDKFetchWindowDuration(t *testing.T) {
	if !chromeAvailable {
		t.Skip("no Chrome/Chromium binary")
	}
	// A never-settling page makes the fetch run waitForJSRender's full
	// 5s window, pinning the default maxWait.
	neverSettles := cdkPage(t, `<html><head><script>
setInterval(function(){ document.body.appendChild(document.createElement('span')); }, 40);
</script></head><body></body></html>`)
	start := time.Now() // REAL-TIME: measuring the fetch render window
	_, err := realChromedpFetch(neverSettles, 20*time.Second, "")
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}
	// 8s upper bound, not 6.2s: under make-check's full load (race detector
	// + parallel packages all spawning Chrome) the 5s render window can
	// stretch past 6.2s of wall time. The band's resolution lives at the
	// bottom — a dropped or shortened window lands under 4.6s — while the
	// top only needs to catch a doubled window (~10s).
	if d := time.Since(start); d < 4600*time.Millisecond || d > 8*time.Second {
		t.Errorf("fetch took %v; want the ~5s render window (4.6s-8s)", d)
	}
}
