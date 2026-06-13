package web

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type ChromePool struct {
	mu       sync.Mutex
	allocCtx context.Context
	cancel   context.CancelFunc
	ready    bool
	proxyURL string
}

var defaultPool = &ChromePool{}

var chromedpAvailable struct {
	once    sync.Once
	result  bool
	binPath string
}

func isChromedpAvailable() (bool, string) {
	chromedpAvailable.once.Do(func() {
		for _, name := range []string{"chromium-browser", "chromium", "google-chrome", "chrome"} {
			if p, err := exec.LookPath(name); err == nil {
				chromedpAvailable.result = true
				chromedpAvailable.binPath = p
				return
			}
		}
	})
	return chromedpAvailable.result, chromedpAvailable.binPath
}

// getWithProxy returns a chromedp context, restarting Chrome if proxy changed.
func (p *ChromePool) getWithProxy(parentCtx context.Context, proxyURL string) (context.Context, context.CancelFunc, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ready && p.proxyURL == proxyURL {
		ctx, cancel := chromedp.NewContext(p.allocCtx)
		return ctx, cancel, nil
	}

	if p.ready && p.proxyURL != proxyURL {
		if p.cancel != nil {
			p.cancel()
		}
		p.allocCtx = nil
		p.cancel = nil
		p.ready = false
	}

	available, binPath := isChromedpAvailable()
	if !available {
		return nil, nil, fmt.Errorf("no Chrome/Chromium binary found")
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(binPath),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1280, 800),
	)
	if proxyURL != "" {
		opts = append(opts, chromedp.Flag("proxy-server", proxyURL))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	testCtx, testCancel := chromedp.NewContext(allocCtx)
	defer testCancel()

	if err := chromedp.Run(testCtx, chromedp.Navigate("about:blank")); err != nil {
		allocCancel()
		return nil, nil, fmt.Errorf("chrome startup failed: %w", err)
	}

	p.allocCtx = allocCtx
	p.cancel = allocCancel
	p.ready = true
	p.proxyURL = proxyURL

	ctx, cancel := chromedp.NewContext(p.allocCtx)
	return ctx, cancel, nil
}

// reset marks the pool as not ready so next call re-creates Chrome.
func (p *ChromePool) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.allocCtx = nil
	p.cancel = nil
	p.ready = false
}

// chromedpFetch can be replaced in tests to avoid launching Chrome.
var chromedpFetch = realChromedpFetch

func realChromedpFetch(parentCtx context.Context, url string, timeout time.Duration, proxyURL string) (string, error) {
	ctx, cancel, err := defaultPool.getWithProxy(parentCtx, proxyURL)
	if err != nil {
		return "", fmt.Errorf("chrome not available: %w", err)
	}
	defer cancel()

	ctx, timeoutCancel := context.WithTimeout(ctx, timeout)
	defer timeoutCancel()

	var html string
	err = chromedp.Run(ctx,
		network.Enable(),
		page.Enable(),
		dom.Enable(),
		// Stealth: inject anti-detection scripts before any page loads
		chromedp.ActionFunc(func(ctx context.Context) error {
			stealthScript := buildStealthInjectionScript()
			if stealthScript != "" {
				if _, err := page.AddScriptToEvaluateOnNewDocument(stealthScript).Do(ctx); err != nil {
					slog.Warn("web: stealth injection failed", "error", err)
				}
			}
			return nil
		}),
		// UA override: replace HeadlessChrome with Chrome, Linux with Windows
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, ua, _, err := browser.GetVersion().Do(ctx)
			if err != nil {
				return nil
			}
			ua = strings.Replace(ua, "HeadlessChrome/", "Chrome/", 1)
			if strings.Contains(ua, "Linux") && !strings.Contains(ua, "Android") {
				ua = strings.Replace(ua, "X11; Linux x86_64", "Windows NT 10.0; Win64; x64", 1)
				ua = strings.Replace(ua, "Linux x86_64", "Windows NT 10.0; Win64; x64", 1)
			}
			return emulation.SetUserAgentOverride(ua).
				WithAcceptLanguage("en-US,en").
				WithPlatform("Win32").
				Do(ctx)
		}),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		// Poll until JS rendering settles: body has children and stops
		// changing, checked every 200ms, max 5s total.
		waitForJSRender(200*time.Millisecond, 5*time.Second),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)

	if err != nil {
		// Chrome process may have died; reset so next call creates a fresh one.
		if ctx.Err() == nil {
			defaultPool.reset()
		}
		return "", fmt.Errorf("chromedp fetch %s: %w", url, err)
	}

	if strings.TrimSpace(html) == "" {
		return "", fmt.Errorf("chromedp returned empty content for %s", url)
	}

	return html, nil
}

// waitForJSRender polls until the page's body has children and the
// child count stabilises (same count on two consecutive checks).
// Falls back to returning after maxWait if the content never settles.
func waitForJSRender(interval, maxWait time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(maxWait)
		var prevCount int
		for {
			if time.Now().After(deadline) {
				return nil
			}
			var count int64
			err := chromedp.Evaluate(`
				document.body && document.body.children ? document.body.children.length : 0
			`, &count).Do(ctx)
			if err != nil {
				return err
			}
			if count > 0 && int(count) == prevCount {
				return nil
			}
			prevCount = int(count)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
	})
}
