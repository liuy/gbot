package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// redirectTransport rewrites all requests to point at the test server,
// preserving path and query string. Lets us mock APIs that scrapers
// hardcode to external URLs.
type redirectTransport struct {
	server *httptest.Server
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target := t.server.URL + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		for _, val := range v {
			newReq.Header.Add(k, val)
		}
	}
	return t.server.Client().Transport.RoundTrip(newReq)
}

func mustParseURL(t *testing.T, s string) *neturl.URL {
	t.Helper()
	u, err := neturl.Parse(s)
	if err != nil {
		t.Fatalf("parse URL %q: %v", s, err)
	}
	return u
}

// --- Registry tests ---

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

// --- utils tests ---

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, tt := range tests {
		got := formatNumber(tt.input)
		if got != tt.want {
			t.Errorf("formatNumber(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFetchBytes_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	data, err := fetchBytes(context.Background(), client, "https://hacker-news.firebaseio.com/v0/item/1.json")
	if err != nil {
		t.Fatalf("fetchBytes() error = %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("data = %q, want hello", string(data))
	}
}

func TestFetchBytes_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	_, err := fetchBytes(context.Background(), client, "https://example.com/missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestFetchJSON_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	_, err := fetchJSON(context.Background(), client, "https://example.com/data")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid JSON, got: %v", err)
	}
}

func TestFetchJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"key":"value"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	raw, err := fetchJSON(context.Background(), client, "https://example.com/data")
	if err != nil {
		t.Fatalf("fetchJSON() error = %v", err)
	}
	if !strings.Contains(string(raw), "value") {
		t.Errorf("raw = %q, want value", string(raw))
	}
}

func TestFetchBytes_NilClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	// We can't use http.DefaultClient with redirect transport easily.
	// Just verify the function signature accepts nil.
	_, _ = fetchBytes(context.Background(), nil, "https://hacker-news.firebaseio.com/v0/item/1.json")
	// We don't assert on success — http.DefaultClient will fail (no server at that domain).
	// The point is to confirm fetchBytes handles nil client without panic.
}

// rewriteTransport rewrites a hardcoded host to a test server URL.
// It only triggers when the request's host matches `host`, otherwise it
// passes through. This lets a scraper call e.g. "https://api.github.com/..." and
// have the request land on the test server.
type rewriteTransport struct {
	server *httptest.Server
	host   string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.host != "" && req.URL.Host != t.host {
		return nil, fmt.Errorf("unexpected host %q (expected %q)", req.URL.Host, t.host)
	}
	target := t.server.URL + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		for _, val := range v {
			newReq.Header.Add(k, val)
		}
	}
	return t.server.Client().Transport.RoundTrip(newReq)
}

// pathRoutingTransport routes requests from any of the given hosts to the
// test server. Used when a scraper calls multiple API endpoints (e.g.
// registry + downloads) that share path-based logic in the test handler.
type pathRoutingTransport struct {
	server *httptest.Server
	hosts  map[string]string
}

func (t *pathRoutingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := t.hosts[req.URL.Host]; !ok {
		return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
	}
	target := t.server.URL + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		for _, val := range v {
			newReq.Header.Add(k, val)
		}
	}
	return t.server.Client().Transport.RoundTrip(newReq)
}

// allowHostsTransport routes any of the listed hosts to the test server.
type allowHostsTransport struct {
	server *httptest.Server
	hosts  []string
}

func (t *allowHostsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	allowed := slices.Contains(t.hosts, req.URL.Host)
	if !allowed {
		return nil, fmt.Errorf("unexpected host %q (allowed: %v)", req.URL.Host, t.hosts)
	}
	target := t.server.URL + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		for _, val := range v {
			newReq.Header.Add(k, val)
		}
	}
	return t.server.Client().Transport.RoundTrip(newReq)
}

// --- Wikipedia ---

func TestHandleWikipedia_NoMatch(t *testing.T) {
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://example.com/wiki/Foo"), nil, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for non-wikipedia host, got %+v", result)
	}
}

func TestHandleWikipedia_NonWikiPath(t *testing.T) {
	result, _ := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/random/Foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/wiki/ path, got %+v", result)
	}
}

func TestHandleWikipedia_SummaryOnly(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path)
		mu.Unlock()
		if strings.Contains(r.URL.Path, "/summary/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Go","description":"language","extract":"Go is a statically typed, compiled language."}`))
			return
		}
		// mobile-html returns error → falls back to summary-only.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "en.wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Go_(programming_language)"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Go is a statically typed") {
		t.Errorf("expected extract in content, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "# Go") {
		t.Errorf("expected title heading, got: %q", result.Content)
	}
	if result.Method != "wikipedia-api" {
		t.Errorf("Method = %q, want wikipedia-api", result.Method)
	}
}

func TestHandleWikipedia_FullContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/summary/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Tea","description":"drink","extract":"Tea is a drink."}`))
			return
		}
		if strings.Contains(r.URL.Path, "/mobile-html/") {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body><section><h2>History</h2><p>Tea originated in China.</p></section></body></html>`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "en.wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Tea"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "History") {
		t.Errorf("expected History section, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "originated in China") {
		t.Errorf("expected paragraph text, got: %q", result.Content)
	}
}

func TestHandleWikipedia_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "en.wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Go"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for bad JSON, got %+v", result)
	}
}

// --- Stack Overflow ---

func TestHandleStackOverflow_NoMatch(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://github.com/questions/123"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-SE host, got %+v", result)
	}
}

func TestHandleStackOverflow_NonQuestionPath(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/users/123"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/questions/ path, got %+v", result)
	}
}

func TestHandleStackOverflow_NonNumericID(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/abc/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-numeric ID, got %+v", result)
	}
}

func TestHandleStackOverflow_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/questions/123/answers") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"body":"<p>Use map[int]int</p>","score":5,"is_accepted":true,"owner":{"display_name":"alice"},"creation_date":1700000000}]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/questions/123") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"title":"How to use maps","body":"<p>I need help</p>","score":3,"owner":{"display_name":"bob"},"creation_date":1699000000,"tags":["go","map"],"answer_count":1,"is_answered":true}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/123/how-to-use-maps"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "How to use maps") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Use map[int]int") {
		t.Errorf("expected answer body, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "(Accepted)") {
		t.Errorf("expected (Accepted) marker, got: %q", result.Content)
	}
	if result.Method != "stackexchange" {
		t.Errorf("Method = %q, want stackexchange", result.Method)
	}
}

func TestHandleStackOverflow_StackExchangeSubdomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"title":"Question","body":"<p>body</p>","score":1,"owner":{"display_name":"x"},"creation_date":1700000000,"tags":[],"answer_count":0,"is_answered":false}],"items":[{"body":"<p>ans</p>","score":0,"is_accepted":false,"owner":{"display_name":"y"},"creation_date":1700000001}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://unix.stackexchange.com/questions/456/q"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Question") {
		t.Errorf("expected content, got: %q", result.Content)
	}
}

// --- Hacker News ---

func TestHandleHackerNews_NoMatch(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://example.com/item?id=1"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-HN host, got %+v", result)
	}
}

func TestHandleHackerNews_ItemByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /v0/item/{id}.json → comment fetch
		// /v0/topstories.json → listing
		// Empty for item.
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/item/1.json" {
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"title":"Test Story","url":"https://example.com","score":42,"descendants":3,"kids":[]}`))
			return
		}
		_, _ = w.Write([]byte(`null`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test Story") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "alice") {
		t.Errorf("expected by-line, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ItemWithComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/item/1.json":
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"title":"Story","url":"","score":10,"descendants":2,"kids":[2,3]}`))
		case "/v0/item/2.json":
			_, _ = w.Write([]byte(`{"id":2,"type":"comment","by":"bob","time":1700001000,"text":"<p>nice</p>"}`))
		case "/v0/item/3.json":
			_, _ = w.Write([]byte(`{"id":3,"type":"comment","by":"carol","time":1700002000,"text":"<p>cool</p>"}`))
		default:
			_, _ = w.Write([]byte(`null`))
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Comments") {
		t.Errorf("expected Comments section, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ItemDeleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"deleted":true}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "[deleted]") {
		t.Errorf("expected [deleted] marker, got: %q", result.Content)
	}
}

func TestHandleHackerNews_TopStories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/topstories.json" {
			_, _ = w.Write([]byte(`[1,2]`))
			return
		}
		if r.URL.Path == "/v0/item/1.json" {
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"title":"First Story","url":"https://example.com/a","score":50,"descendants":10}`))
			return
		}
		if r.URL.Path == "/v0/item/2.json" {
			_, _ = w.Write([]byte(`{"id":2,"type":"story","by":"bob","time":1700001000,"title":"Second Story","url":"https://example.com/b","score":30,"descendants":5}`))
			return
		}
		_, _ = w.Write([]byte(`null`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "First Story") {
		t.Errorf("expected first story, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Second Story") {
		t.Errorf("expected second story, got: %q", result.Content)
	}
}

func TestHandleHackerNews_BadItemID(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-numeric ID, got %+v", result)
	}
}

// --- arXiv ---

func TestHandleArxiv_NoMatch(t *testing.T) {
	result, _ := HandleArxiv(context.Background(), mustParseURL(t, "https://example.com/abs/1234"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-arxiv host, got %+v", result)
	}
}

func TestHandleArxiv_NonAbsPath(t *testing.T) {
	result, _ := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/list/cs/1234"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-abs/pdf path, got %+v", result)
	}
}

func TestHandleArxiv_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<feed>
  <entry>
    <id>http://arxiv.org/abs/2401.12345v1</id>
    <title>Test Paper Title</title>
    <summary>This is the abstract of the test paper.</summary>
    <author><name>Alice Smith</name></author>
    <author><name>Bob Jones</name></author>
    <published>2024-01-15T00:00:00Z</published>
    <category term="cs.AI"/>
    <category term="cs.LG"/>
  </entry>
</feed>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2401.12345"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test Paper Title") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Alice Smith") {
		t.Errorf("expected author, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "2024-01-15") {
		t.Errorf("expected date, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "cs.AI") {
		t.Errorf("expected category, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "This is the abstract") {
		t.Errorf("expected abstract, got: %q", result.Content)
	}
	if result.Method != "arxiv-api" {
		t.Errorf("Method = %q, want arxiv-api", result.Method)
	}
}

func TestHandleArxiv_PdfPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<feed><entry><id>x</id><title>From PDF Path</title><summary>abs</summary><author><name>Y</name></author></entry></feed>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/pdf/2401.12345.pdf"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "From PDF Path") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleArxiv_EmptyFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<feed></feed>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/9999.99999"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty feed, got %+v", result)
	}
}

func TestHandleArxiv_BadXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not xml"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	_, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/1234"), client, nil)
	if err == nil {
		t.Fatal("expected error for bad XML")
	}
	if !strings.Contains(err.Error(), "parse arXiv") {
		t.Fatalf("error should mention parse failure, got: %v", err)
	}
}

// --- NPM ---

func TestHandleNpm_NoMatch(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://example.com/package/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-npm host, got %+v", result)
	}
}

func TestHandleNpm_NonPackagePath(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/search?q=react"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/package/ path, got %+v", result)
	}
}

func TestHandleNpm_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/downloads/point/") {
			_, _ = w.Write([]byte(`{"downloads":1234567}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"express","version":"4.18.0","description":"Fast web framework","license":"MIT","homepage":"https://expressjs.com","repository":{"type":"git","url":"git+https://github.com/expressjs/express.git"}}`))
	}))
	defer srv.Close()

	// Use a single host mapping for both registry.npmjs.org and api.npmjs.org.
	// api.npmjs.org and registry.npmjs.org have different paths; rewriteTransport
	// routes by path, so we accept both.
	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/express"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# express") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Fast web framework") {
		t.Errorf("expected description, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "1,234,567") {
		t.Errorf("expected formatted download count, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
}

func TestHandleNpm_EmptyName(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty package name, got %+v", result)
	}
}

// --- PyPI ---

func TestHandlePyPI_NoMatch(t *testing.T) {
	result, _ := HandlePyPI(context.Background(), mustParseURL(t, "https://example.com/project/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-pypi host, got %+v", result)
	}
}

func TestHandlePyPI_NonProjectPath(t *testing.T) {
	result, _ := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/search?q=requests"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/project/ path, got %+v", result)
	}
}

func TestHandlePyPI_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"info": {
				"name": "requests",
				"version": "2.31.0",
				"summary": "HTTP library",
				"description_content_type": "text/markdown",
				"author": "Kenneth Reitz",
				"author_email": "me@kennethreitz.org",
				"home_page": "https://requests.readthedocs.io",
				"project_urls": {"Documentation": "https://docs.example.com"},
				"license": "Apache-2.0",
				"requires_dist": ["urllib3>=1.21.1"]
			}
		}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "pypi.org"}}
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/requests/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# requests") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "HTTP library") {
		t.Errorf("expected summary, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Apache-2.0") {
		t.Errorf("expected license, got: %q", result.Content)
	}
}

func TestHandlePyPI_EmptyName(t *testing.T) {
	result, _ := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project//"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty package name, got %+v", result)
	}
}

// --- crates.io ---

func TestHandleCratesIo_NoMatch(t *testing.T) {
	result, _ := HandleCratesIo(context.Background(), mustParseURL(t, "https://example.com/crates/serde"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-crates.io host, got %+v", result)
	}
}

func TestHandleCratesIo_BadPath(t *testing.T) {
	result, _ := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty path, got %+v", result)
	}
}

func TestHandleCratesIo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Host, "docs.rs") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"crate": {
				"name": "serde",
				"description": "Serialization framework",
				"homepage": "https://serde.rs",
				"documentation": "https://docs.rs/serde",
				"repository": "https://github.com/serde-rs/serde",
				"max_version": "1.0.0",
				"downloads": 1000000,
				"recent_downloads": 50000,
				"keywords": ["serialization", "no_std"],
				"categories": ["encoding"]
			},
			"versions": [{"num": "1.0.0", "license": "MIT"}]
		}`))
	}))
	defer srv.Close()

	// The scraper hits two domains: crates.io and docs.rs. Use redirectTransport
	// that ignores host check and routes all to srv.
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# serde") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Serialization framework") {
		t.Errorf("expected description, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
}

// --- WeChat ---

func TestHandleWeixin_NoMatch(t *testing.T) {
	result, _ := HandleWeixin(context.Background(), mustParseURL(t, "https://example.com/s/abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-weixin host, got %+v", result)
	}
}

func TestHandleWeixin_NilJS(t *testing.T) {
	result, _ := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil when JSFetcher is nil, got %+v", result)
	}
}

func TestHandleWeixin_Success(t *testing.T) {
	html := `<html>
		<head><title>Test Article</title></head>
		<body>
			<h1 id="activity-name">Test Article Title</h1>
			<div id="js_content">
				<p>This is the article content.</p>
				<p>It has multiple paragraphs.</p>
			</div>
		</body>
	</html>`

	js := func(ctx context.Context, u string) (string, error) {
		return html, nil
	}

	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc123"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test Article Title") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "article content") {
		t.Errorf("expected content, got: %q", result.Content)
	}
}

func TestHandleWeixin_JSError(t *testing.T) {
	js := func(ctx context.Context, u string) (string, error) {
		return "", fmt.Errorf("chromedp failed")
	}
	_, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, js)
	if err == nil {
		t.Fatal("expected error when JSFetcher fails")
	}
	if !strings.Contains(err.Error(), "chromedp failed") {
		t.Errorf("error should mention chromedp failure, got: %v", err)
	}
}

// --- Hugging Face ---

func TestHandleHuggingFace_NoMatch(t *testing.T) {
	result, _ := HandleHuggingFace(context.Background(), mustParseURL(t, "https://example.com/models/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-HF host, got %+v", result)
	}
}

func TestHandleHuggingFace_Model(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/models/") {
			_, _ = w.Write([]byte(`{"id":"bert-base","modelId":"bert-base","author":"google","downloads":1000000,"likes":50,"tags":["transformer","bert"],"pipeline_tag":"fill-mask","library_name":"transformers"}`))
			return
		}
		// README
		_, _ = w.Write([]byte("# BERT\n\nBidirectional encoder."))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/bert-base"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "bert-base") {
		t.Errorf("expected model name, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "1,000,000") {
		t.Errorf("expected downloads, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_Dataset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/datasets/") {
			_, _ = w.Write([]byte(`{"id":"squad","author":"rajpurkar","downloads":500000}`))
			return
		}
		_, _ = w.Write([]byte("# SQuAD"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/datasets/squad"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "squad") {
		t.Errorf("expected dataset name, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_Space(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/spaces/") {
			_, _ = w.Write([]byte(`{"id":"gradio/chatbot","author":"gradio","likes":100}`))
			return
		}
		_, _ = w.Write([]byte("# Chatbot"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/spaces/gradio/chatbot"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "gradio") {
		t.Errorf("expected author, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_User(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// First request: /api/models/{id} returns 404
		if strings.Contains(r.URL.Path, "/api/models/nouser") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Second: /api/users/{id} returns user
		if strings.Contains(r.URL.Path, "/api/users/nouser") {
			_, _ = w.Write([]byte(`{"user":"nouser","fullname":"No User","numModels":5,"numDatasets":3,"numSpaces":2,"orgs":[{"name":"org1"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/nouser"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "nouser") {
		t.Errorf("expected username, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "org1") {
		t.Errorf("expected org, got: %q", result.Content)
	}
}

// --- GitHub ---

func TestHandleGitHub_NoMatch(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://example.com/foo/bar"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-github host, got %+v", result)
	}
}

func TestHandleGitHub_BadPath(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty path, got %+v", result)
	}
}

func TestHandleGitHub_DiscussionsList(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/discussions"), nil, nil)
	if result != nil {
		t.Errorf("discussions list should return nil, got %+v", result)
	}
}

func TestHandleGitHub_DiscussionItem(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/discussions/42"), nil, nil)
	if result != nil {
		t.Errorf("discussion items return nil, got %+v", result)
	}
}

func TestHandleGitHub_ActionsRunNoJobID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"https://github.com/owner/repo/actions/runs/1","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:01:00Z","run_attempt":1,"jobs_url":"https://api.github.com/repos/owner/repo/actions/runs/1/jobs"}`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "CI") {
		t.Errorf("expected workflow name, got: %q", result.Content)
	}
}

func TestHandleGitHub_Repo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_, _ = w.Write([]byte(`{"truncated":false,"tree":[{"path":"src","type":"directory"},{"path":"README.md","type":"blob"}]}`))
		case strings.Contains(r.URL.Path, "/readme"):
			_, _ = w.Write([]byte(`"# My Project\n\nA cool project."`))
		case strings.Contains(r.URL.Path, "/repos/owner/repo"):
			_, _ = w.Write([]byte(`{"description":"A cool project","stargazers_count":100,"forks_count":10,"open_issues_count":5,"language":"Go","license":{"spdx_id":"MIT"},"default_branch":"main"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	// GitHub scraper hits api.github.com and possibly raw.githubusercontent.com.
	client := &http.Client{Transport: &allowHostsTransport{server: srv, hosts: []string{"api.github.com", "raw.githubusercontent.com"}}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# owner/repo") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "100") {
		t.Errorf("expected stars, got: %q", result.Content)
	}
}

func TestHandleGitHub_Issue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/issues/42/comments"):
			_, _ = w.Write([]byte(`[{"user":{"login":"alice"},"body":"Comment 1","created_at":"2024-01-02T00:00:00Z"}]`))
		case strings.Contains(r.URL.Path, "/issues/42"):
			_, _ = w.Write([]byte(`{"title":"Test issue","body":"Issue body","state":"open","number":42,"user":{"login":"bob"},"labels":[{"name":"bug"}],"created_at":"2024-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test issue") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Comment 1") {
		t.Errorf("expected comment, got: %q", result.Content)
	}
}

func TestHandleGitHub_RepoAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestHandleGitHub_Blob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// raw.githubusercontent.com is used for blob
		_, _ = w.Write([]byte("hello\n"))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/main.go"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected content, got: %q", result.Content)
	}
}

// --- gopkg ---

func TestHandleGoPkg_NoMatch(t *testing.T) {
	result, _ := HandleGoPkg(context.Background(), mustParseURL(t, "https://example.com/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-pkg.go.dev host, got %+v", result)
	}
}

func TestHandleGoPkg_NotAPackage(t *testing.T) {
	// gopkg scraper treats any non-empty path as a package — even /about
	// gets rendered (just with no proxy/version data). This is the actual
	// behavior; it doesn't reject non-package paths.
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
	// Result is non-nil but content is mostly empty.
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
		// proxy.golang.org response
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

// --- Extra coverage: helpers, edge cases ---

func TestIsBinary(t *testing.T) {
	if isBinary([]byte("plain text")) {
		t.Error("plain text should not be binary")
	}
	if !isBinary([]byte{0x00, 0x01, 0x02}) {
		t.Error("data with null byte should be binary")
	}
	if isBinary(nil) {
		t.Error("empty data should not be binary")
	}
}

func TestStripLogTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2024-01-15T00:00:00.0000000Z hello world", "hello world"},
		{"short line", "short line"},
		{"not a timestamp just text", "not a timestamp just text"},
	}
	for _, tt := range tests {
		got := stripLogTimestamp(tt.input)
		if got != tt.want {
			t.Errorf("stripLogTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractJobFromFragment(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		want    int64
	}{
		{"summary-123", false, 123},
		{"summary-987654", false, 987654},
		{"job-123", true, 0},
		{"summary-abc", true, 0},
		{"", true, 0},
	}
	for _, tt := range tests {
		got, err := extractJobFromFragment(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("extractJobFromFragment(%q) err = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("extractJobFromFragment(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Now() // REAL-TIME: test uses current time for relative formatting
	tests := []struct {
		delta time.Duration
		want  string
	}{
		{-30 * time.Minute, "30m ago"},
		{-2 * time.Hour, "2h ago"},
	}
	for _, tt := range tests {
		ts := now.Add(tt.delta).Unix()
		got := formatRelativeTime(ts)
		if got != tt.want {
			t.Errorf("formatRelativeTime(%v) = %q, want %q", tt.delta, got, tt.want)
		}
	}
	// Older dates return formatted date string, not relative.
	oldTS := now.Add(-365 * 24 * time.Hour).Unix()
	got := formatRelativeTime(oldTS)
	if !strings.Contains(got, "-") {
		t.Errorf("expected date format for old timestamp, got: %q", got)
	}
}

func TestFormatRelativeTime_VeryRecent(t *testing.T) {
	now := time.Now() // REAL-TIME: test uses current time for relative formatting
	// 0 minutes ago → "0m ago"
	got := formatRelativeTime(now.Add(-1 * time.Second).Unix())
	if !strings.HasSuffix(got, "m ago") {
		t.Errorf("expected 'Xm ago', got %q", got)
	}
}

func TestDecodeHNText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>hello</p>", "hello"},
		{"<p>para 1</p><p>para 2</p>", "para 1\n\npara 2"},
		{"<i>italic</i>", "*italic*"},
		{"plain", "plain"},
		{"", ""},
		{"<pre><code>x := 1</code></pre>", "```\nx := 1\n```"},
	}
	for _, tt := range tests {
		got := decodeHNText(tt.input)
		if got != tt.want {
			t.Errorf("decodeHNText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReplaceAnchorTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`<a href="https://example.com">link</a>`, "[link](https://example.com)"},
		{"no anchors here", "no anchors here"},
		{`<a href="https://a.com">a</a> and <a href="https://b.com">b</a>`, "[a](https://a.com) and [b](https://b.com)"},
		{`<a href="x">y</a> trailing`, "[y](x) trailing"},
	}
	for _, tt := range tests {
		got := replaceAnchorTags(tt.input)
		if got != tt.want {
			t.Errorf("replaceAnchorTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDecodeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"foo_bar", "foo bar"},
		{"hello%20world", "hello world"},
		{"%E4%B8%AD%E6%96%87", "中文"},
	}
	for _, tt := range tests {
		got := decodeURL(tt.input)
		if got != tt.want {
			t.Errorf("decodeURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHandleGitHub_Tree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"main.go","path":"src/main.go","type":"file","size":1024},{"name":"README.md","path":"src/README.md","type":"file","size":256}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/tree/main/src"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "main.go") {
		t.Errorf("expected file name, got: %q", result.Content)
	}
}

func TestHandleGitHub_TreeRoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"README.md","path":"README.md","type":"file","size":1024}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/tree/main"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "root") {
		t.Errorf("expected 'root' marker, got: %q", result.Content)
	}
}

func TestHandleGitHub_Pull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/pulls/42"):
			_, _ = w.Write([]byte(`{"title":"PR title","body":"PR body","state":"open","number":42,"user":{"login":"alice"},"labels":[],"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z"}`))
		case strings.Contains(r.URL.Path, "/issues/42/comments"):
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/pull/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "PR title") {
		t.Errorf("expected PR title, got: %q", result.Content)
	}
}

func TestHandleGitHub_IssuesList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/issues") {
			_, _ = w.Write([]byte(`[{"title":"First issue","number":1,"created_at":"2024-01-01T00:00:00Z","user":{"login":"alice"},"labels":[{"name":"bug"}]},{"title":"Second issue","number":2,"created_at":"2024-01-02T00:00:00Z","user":{"login":"bob"},"labels":[]}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "First issue") {
		t.Errorf("expected first issue, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "bug") {
		t.Errorf("expected label, got: %q", result.Content)
	}
}

func TestHandleGitHub_ActionsJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/jobs/100"):
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","runner_name":"runner1","steps":[{"name":"Checkout","status":"completed","conclusion":"success","number":1}]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01","updated_at":"2024-01-01","run_attempt":1,"jobs_url":"x"}`))
		case strings.Contains(r.URL.Path, "/logs"):
			_, _ = w.Write([]byte("2024-01-15T00:00:00.0000000Z log line 1\n2024-01-15T00:00:01.0000000Z log line 2\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1/job/100"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "build") {
		t.Errorf("expected job name, got: %q", result.Content)
	}
}

func TestHandleGitHub_BlobBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Binary file (has null byte)
		_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03})
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/image.png"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "binary file") {
		t.Errorf("expected binary file marker, got: %q", result.Content)
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
		// No readme marker
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

func TestHandleHuggingFace_DatasetWithCardData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/datasets/") {
			_, _ = w.Write([]byte(`{"id":"squad","description":"Reading comprehension dataset","downloads":50000,"likes":10,"private":true,"cardData":{"license":"CC-BY-SA-4.0","task_categories":["question-answering"],"size_categories":["100K<n<1M"]},"tags":["nlp"]}`))
			return
		}
		_, _ = w.Write([]byte("# SQuAD"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/datasets/squad"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "CC-BY-SA-4.0") {
		t.Errorf("expected license, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "question-answering") {
		t.Errorf("expected task, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Private") {
		t.Errorf("expected private marker, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_SpaceFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/spaces/") {
			_, _ = w.Write([]byte(`{"id":"gradio/chatbot","author":"gradio","likes":100,"tags":["gradio","chatbot"],"sdk":"gradio","private":true,"gated":false}`))
			return
		}
		_, _ = w.Write([]byte("# Chatbot"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/spaces/gradio/chatbot"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "gradio") {
		t.Errorf("expected author, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_ModelFallsToUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api/models/ returns 404
		if strings.Contains(r.URL.Path, "/api/models/foo") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// /api/users/foo returns user
		if strings.Contains(r.URL.Path, "/api/users/foo") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":"foo","fullname":"Foo Bar","numModels":3,"numDatasets":2,"numSpaces":1,"orgs":[{"name":"org1"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Foo Bar") {
		t.Errorf("expected fullname, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_BothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when both API calls fail, got %+v", result)
	}
}

func TestParseHuggingFaceURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://huggingface.co/bert-base", "modelOrUser"},
		{"https://huggingface.co/owner/bert-base", "model"},
		{"https://huggingface.co/datasets/squad", "dataset"},
		{"https://huggingface.co/datasets/owner/squad", "dataset"},
		{"https://huggingface.co/spaces/gradio/chatbot", "space"},
		{"https://huggingface.co/owner", "modelOrUser"},
		{"https://huggingface.co/", "none"},
		{"https://example.com/foo", "none"},
		{"https://huggingface.co/docs/transformers", "none"},
	}
	for _, tt := range tests {
		u := mustParseURL(t, tt.url)
		got := parseHuggingFaceURL(u)
		switch tt.want {
		case "none":
			if got != nil {
				t.Errorf("parseHuggingFaceURL(%q) = %+v, want nil", tt.url, got)
			}
		case "model":
			if got == nil || got.kind != hfKindModel {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want model", tt.url, got)
			}
		case "modelOrUser":
			if got == nil || got.kind != hfKindModelOrUser {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want modelOrUser", tt.url, got)
			}
		case "dataset":
			if got == nil || got.kind != hfKindDataset {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want dataset", tt.url, got)
			}
		case "space":
			if got == nil || got.kind != hfKindSpace {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want space", tt.url, got)
			}
		}
	}
}

func TestHandleNpm_NoName(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty name, got %+v", result)
	}
}

func TestHandlePyPI_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "pypi.org"}}
	_, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/missing/"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestHandleNpm_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	_, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/missing"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>hello</p>", "hello"},
		{"<a href=\"x\">link</a>", "link"},
		{"a &amp; b", "a & b"},
		{"&lt;tag&gt;", "<tag>"},
		{"&quot;quoted&quot;", `"quoted"`},
		{"it&#39;s", "it's"},
		{"a&nbsp;b", "a b"},
		{"<p>a</p>\n\n<p>b</p>", "a b"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripHTMLTags(tt.input)
		if got != tt.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRenderLanguageField(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		var sb strings.Builder
		renderLanguageField(&sb, nil)
		if sb.Len() != 0 {
			t.Errorf("expected no output for nil, got %q", sb.String())
		}
	})
	t.Run("string", func(t *testing.T) {
		var sb strings.Builder
		renderLanguageField(&sb, "en")
		if !strings.Contains(sb.String(), "en") {
			t.Errorf("expected language output, got %q", sb.String())
		}
	})
	t.Run("string slice", func(t *testing.T) {
		var sb strings.Builder
		renderLanguageField(&sb, []any{"en", "fr"})
		if !strings.Contains(sb.String(), "en, fr") {
			t.Errorf("expected joined languages, got %q", sb.String())
		}
	})
}

func TestHandleHuggingFace_ModelGated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/models/") {
			_, _ = w.Write([]byte(`{"id":"gated-model","modelId":"gated-model","gated":"manual","private":true,"downloads":100,"library_name":"transformers","tags":["nlp"]}`))
			return
		}
		_, _ = w.Write([]byte("# Model Card"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/owner/gated-model"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Gated") {
		t.Errorf("expected Gated marker, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Private") {
		t.Errorf("expected Private marker, got: %q", result.Content)
	}
}

func TestHandleWeixin_NoTitleNoContent(t *testing.T) {
	html := `<html><body>no markers</body></html>`
	js := func(ctx context.Context, u string) (string, error) { return html, nil }
	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	// No title marker → result is nil.
	if result != nil {
		t.Errorf("expected nil for missing title, got %+v", result)
	}
}

func TestHandleCratesIo_NoReadme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"crate":{"name":"foo","description":"test","max_version":"1.0.0","downloads":100,"recent_downloads":10,"keywords":[],"categories":[]},"versions":[]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# foo") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleCratesIo_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/foo"), client, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error should mention invalid, got: %v", err)
	}
}

func TestHandleGitHub_ActionsRunFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/runs/1/jobs"):
			_, _ = w.Write([]byte(`{"jobs":[{"id":100,"name":"build","status":"completed","conclusion":"success","runner_name":"runner1"}]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T01:00:00Z","run_attempt":1,"jobs_url":"x"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "build") {
		t.Errorf("expected job name, got: %q", result.Content)
	}
}

func TestHandleGitHub_ActionsRunViaFragment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/jobs/100/logs"):
			_, _ = w.Write([]byte("2024-01-15T00:00:00.0000000Z log line\n"))
		case strings.Contains(r.URL.Path, "/actions/jobs/100"):
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","steps":[{"name":"step1","status":"completed","conclusion":"success","number":1}]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T01:00:00Z","run_attempt":2,"jobs_url":"x"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	// URL with /attempts/N#summary-JOBID pattern
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1/attempts/2#summary-100"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
}

func TestFetchAllComments_MultiplePages(t *testing.T) {
	page1Served := false
	page2Served := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !page1Served {
			page1Served = true
			// Return 100 comments to trigger next page
			comments := make([]map[string]any, 100)
			for i := range comments {
				comments[i] = map[string]any{"body": fmt.Sprintf("c%d", i)}
			}
			_ = json.NewEncoder(w).Encode(comments)
			return
		}
		if !page2Served {
			page2Served = true
			_, _ = w.Write([]byte(`[{"body":"page2"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	comments, err := fetchAllComments(context.Background(), client, "owner", "repo", 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(comments) < 100 {
		t.Errorf("expected at least 100 comments, got %d", len(comments))
	}
}

func TestStripTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>hello</p>", "hello"},
		{"plain", "plain"},
		{"<a href=\"x\">link</a>", "link"},
		{"", ""},
		{"<p>nested <b>bold</b></p>", "nested bold"},
	}
	for _, tt := range tests {
		got := stripTags(tt.input)
		if got != tt.want {
			t.Errorf("stripTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractByIDText(t *testing.T) {
	html := `<div id="title">Hello</div><div id="other">World</div>`
	got := extractByIDText(html, "title")
	if got != "Hello" {
		t.Errorf("got %q, want Hello", got)
	}
	got = extractByIDText(html, "nonexistent")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractByIDInner(t *testing.T) {
	html := `<div id="content">main text</div>`
	got := extractByIDInner(html, "content")
	if !strings.Contains(got, "main text") {
		t.Errorf("got %q, want to contain 'main text'", got)
	}
	got = extractByIDInner(html, "missing")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestHandleWeixin_WithAllFields(t *testing.T) {
	html := `<html>
		<body>
			<h1 id="activity-name">Title</h1>
			<div id="js_name">Author</div>
			<span id="publish_time">2024-01-15</span>
			<div id="js_content">
				<p>First paragraph.</p>
				<p>Second paragraph with <a href="https://example.com">link</a>.</p>
			</div>
		</body>
	</html>`
	js := func(ctx context.Context, u string) (string, error) { return html, nil }
	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Author") {
		t.Errorf("expected author, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "2024-01-15") {
		t.Errorf("expected date, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "First paragraph") {
		t.Errorf("expected first paragraph, got: %q", result.Content)
	}
}

func TestHandleGitHub_RepoLicense(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_, _ = w.Write([]byte(`{"truncated":true,"tree":[]}`))
		case strings.Contains(r.URL.Path, "/readme"):
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/repos/owner/repo"):
			_, _ = w.Write([]byte(`{"description":"X","stargazers_count":1,"forks_count":0,"open_issues_count":0,"default_branch":"main"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	// No license, no language — should still render.
	if !strings.Contains(result.Content, "# owner/repo") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleGitHub_Blob_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/missing.go"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestHandleGitHub_ActionInvalidRunID(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for invalid run ID, got %+v", result)
	}
}

func TestHandleGitHub_ActionsJobFromFragment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/jobs/100/logs"):
			_, _ = w.Write([]byte("2024-01-15T00:00:00.0000000Z line 1\nplain line\n"))
		case strings.Contains(r.URL.Path, "/actions/jobs/100"):
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","runner_name":"r1","steps":[]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T01:00:00Z","run_attempt":1,"jobs_url":"x"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1/attempts/1#summary-100"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
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

func TestHandleNpm_DependenciesSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/downloads/point/") {
			_, _ = w.Write([]byte(`{"downloads":100}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"my-pkg","version":"1.0.0","description":"d","license":"MIT","dependencies":{"express":"^4.0.0","lodash":"^4.17.0"}}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/my-pkg"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "express") {
		t.Errorf("expected dependency, got: %q", result.Content)
	}
}

func TestHandleHackerNews_BadIDFormat(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id="), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty id, got %+v", result)
	}
}

func TestHandleHackerNews_NoIDQuery(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for missing id query, got %+v", result)
	}
}
