package scrapers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"slices"
	"strings"
	"testing"
)

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

func mustParseURL(t *testing.T, s string) *neturl.URL {
	t.Helper()
	u, err := neturl.Parse(s)
	if err != nil {
		t.Fatalf("parse URL %q: %v", s, err)
	}
	return u
}

// Scraper tests differ only in what the fake site serves, how the client is
// pointed at it, which entry point is called and which substrings the markdown
// must contain; the server lifecycle, the env pinning and the error/result
// checks are identical everywhere, so they live here instead of in many copies.
type scraperTest struct {
	env     map[string]string // env vars pinned before the call (e.g. API tokens)
	fixture func(w http.ResponseWriter, r *http.Request)
	client  func(srv *httptest.Server) *http.Client
	handle  Handler // a Handler, or a closure into an internal render* when the test bypasses URL parsing
	url     string  // parsed and handed to handle; empty when handle ignores it
	want    []string
}

func runScraperTest(t *testing.T, tt scraperTest) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(tt.fixture))
	defer srv.Close()
	for k, v := range tt.env {
		t.Setenv(k, v)
	}
	result, err := tt.handle(context.Background(), mustParseURL(t, tt.url), tt.client(srv), nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	for _, want := range tt.want {
		if !strings.Contains(result.Content, want) {
			t.Errorf("expected %q, got: %q", want, result.Content)
		}
	}
}
