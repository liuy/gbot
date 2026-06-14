package scrapers

import (
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
)

func TestRegistry_RegisterAndTry(t *testing.T) {
	reg := New()
	called := false
	reg.Register(func(ctx context.Context, u *neturl.URL, c *http.Client, js JSFetcher) (*Result, error) {
		called = true
		return &Result{Content: "matched"}, nil
	})
	result, err := reg.Try(context.Background(), mustParseURL(t, "https://example.com"), nil, nil)
	if err != nil {
		t.Fatalf("Try() error = %v", err)
	}
	if !called {
		t.Error("handler not called")
	}
	if result.Content != "matched" {
		t.Errorf("Content = %q, want matched", result.Content)
	}
}

func TestRegistry_NoMatch(t *testing.T) {
	reg := New()
	reg.Register(func(ctx context.Context, u *neturl.URL, c *http.Client, js JSFetcher) (*Result, error) {
		return nil, nil
	})
	result, err := reg.Try(context.Background(), mustParseURL(t, "https://example.com"), nil, nil)
	if err != nil {
		t.Fatalf("Try() error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %+v", result)
	}
}

func TestRegistry_HandlerError(t *testing.T) {
	reg := New()
	reg.Register(func(ctx context.Context, u *neturl.URL, c *http.Client, js JSFetcher) (*Result, error) {
		return nil, fmt.Errorf("boom")
	})
	_, err := reg.Try(context.Background(), mustParseURL(t, "https://example.com"), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should contain 'boom', got: %v", err)
	}
}

func TestRegistry_FirstHandlerWins(t *testing.T) {
	reg := New()
	reg.Register(func(ctx context.Context, u *neturl.URL, c *http.Client, js JSFetcher) (*Result, error) {
		return &Result{Content: "first"}, nil
	})
	reg.Register(func(ctx context.Context, u *neturl.URL, c *http.Client, js JSFetcher) (*Result, error) {
		return &Result{Content: "second"}, nil
	})
	result, err := reg.Try(context.Background(), mustParseURL(t, "https://example.com"), nil, nil)
	if err != nil {
		t.Fatalf("Try() error = %v", err)
	}
	if result.Content != "first" {
		t.Errorf("Content = %q, want first", result.Content)
	}
}

func TestRegistry_FirstNilFallsThrough(t *testing.T) {
	reg := New()
	reg.Register(func(ctx context.Context, u *neturl.URL, c *http.Client, js JSFetcher) (*Result, error) {
		return nil, nil
	})
	reg.Register(func(ctx context.Context, u *neturl.URL, c *http.Client, js JSFetcher) (*Result, error) {
		return &Result{Content: "second"}, nil
	})
	result, _ := reg.Try(context.Background(), mustParseURL(t, "https://example.com"), nil, nil)
	if result == nil || result.Content != "second" {
		t.Errorf("expected second handler result, got %+v", result)
	}
}
