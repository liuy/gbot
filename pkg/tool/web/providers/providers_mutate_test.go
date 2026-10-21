package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// toHTTPStatus mirrors an MCP numeric code into an HTTP-shaped status.
func TestToHTTPStatus_Boundaries(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want int }{
		{100, 100}, {429, 429}, {599, 599},
		{99, 500}, {600, 500}, {0, 500},
		{-429, 429}, {-100, 100}, {-99, 500},
	}
	for _, c := range cases {
		if got := toHTTPStatus(c.in); got != c.want {
			t.Errorf("toHTTPStatus(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// A Zhipu key is only usable when it looks like a JWT (contains a dot).
func TestZhipuIsAvailable_DotAtStart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		key  string
		want bool
	}{
		{".k", true},    // dot at index 0 still counts
		{"a.b.c", true}, // JWT-shaped
		{"nodot", false},
		{"", false},
	} {
		z := &ZhipuProvider{APIKey: c.key}
		if got := z.IsAvailable(); got != c.want {
			t.Errorf("ZhipuProvider{APIKey:%q}.IsAvailable() = %v, want %v", c.key, got, c.want)
		}
	}
}

// Each provider caps its request with its own timeout, observable through the
// deadline on the outgoing request.
func TestProviderRequestTimeouts(t *testing.T) {
	t.Parallel()
	var got time.Duration
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		dl, ok := req.Context().Deadline()
		if ok {
			got = time.Until(dl)
		} else {
			got = -1
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{"Content-Type": []string{"text/html"}},
		}, nil
	})

	ddg := &DuckDuckGoProvider{Client: &http.Client{Transport: transport}}
	if _, err := ddg.searchWithClient(context.Background(), SearchParams{Query: "q"}, "https://example.invalid/"); err != nil {
		t.Fatalf("ddg request: %v", err)
	}
	if got <= 19*time.Second || got > 20*time.Second {
		t.Errorf("ddg request deadline = %v, want ~20s", got)
	}

	a := &AnySearchProvider{Client: &http.Client{Transport: transport}}
	_, _ = a.Search(context.Background(), SearchParams{Query: "q"})
	if got <= 29*time.Second || got > 30*time.Second {
		t.Errorf("anysearch request deadline = %v, want ~30s", got)
	}

	// Tavily and jina get their own transports: the shared one above returns
	// an empty body, whose failed unmarshal would push both down their
	// parse-error SPEs and break the err check.
	tavilyTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		dl, ok := req.Context().Deadline()
		if ok {
			got = time.Until(dl)
		} else {
			got = -1
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})
	tv := &TavilyProvider{Client: &http.Client{Transport: tavilyTransport}, APIKey: "k"}
	if _, err := tv.Search(context.Background(), SearchParams{Query: "q"}); err != nil {
		t.Fatalf("tavily request: %v", err)
	}
	if got <= 59*time.Second || got > 60*time.Second {
		t.Errorf("tavily request deadline = %v, want ~60s", got)
	}

	jinaTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		dl, ok := req.Context().Deadline()
		if ok {
			got = time.Until(dl)
		} else {
			got = -1
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})
	jp := &JinaProvider{Client: &http.Client{Transport: jinaTransport}, APIKey: "k"}
	if _, err := jp.Search(context.Background(), SearchParams{Query: "q"}); err != nil {
		t.Fatalf("jina request: %v", err)
	}
	if got <= 59*time.Second || got > 60*time.Second {
		t.Errorf("jina request deadline = %v, want ~60s", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// A JSON-RPC response without a result carries the full provider error.
func TestParseZhipuResponse_MissingResultFields(t *testing.T) {
	t.Parallel()
	_, err := parseZhipuResponse(strings.NewReader("data: {\"jsonrpc\":\"2.0\"}\n"), 10)
	if err == nil {
		t.Fatal("missing result must be an error")
	}
	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("err type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "zhipu" || spe.Message != "missing result in JSON-RPC response" || spe.Status != 500 {
		t.Errorf("error = %+v, want zhipu / missing result in JSON-RPC response / 500", spe)
	}
}

// isError with no content must not index into an empty slice.
func TestParseZhipuResponse_IsErrorWithoutContent(t *testing.T) {
	t.Parallel()
	resp, err := parseZhipuResponse(strings.NewReader("data: {\"result\":{\"isError\":true,\"content\":[]}}\n"), 10)
	if err != nil {
		t.Fatalf("isError with empty content should fall through, got %v", err)
	}
	if len(resp.Sources) != 0 {
		t.Errorf("sources = %+v, want none", resp.Sources)
	}
}

// Content entries that are not non-empty text are skipped.
func TestParseZhipuResponse_ContentFilter(t *testing.T) {
	t.Parallel()
	body := `data: {"result":{"content":[` +
		`{"type":"image","text":"{\"x\":1}"},` +
		`{"type":"text","text":""},` +
		`{"type":"text","text":"[{\"link\":\"https://a\",\"title\":\"A\"}]"}]}}`
	resp, err := parseZhipuResponse(strings.NewReader(body+"\n"), 10)
	if err != nil {
		t.Fatalf("parseZhipuResponse: %v", err)
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("sources = %+v, want exactly the one text entry", resp.Sources)
	}
	if resp.Sources[0].URL != "https://a" || resp.Sources[0].Title != "A" {
		t.Errorf("source = %+v, want A / https://a", resp.Sources[0])
	}
}

// Provider errors keep their own text inside the chain failure list.
func TestSearchChain_ProviderErrorTextPreserved(t *testing.T) {
	t.Parallel()
	chain := &SearchChain{Providers: []SearchProvider{
		&mockProvider{id: "p1", available: true, err: &SearchProviderError{Provider: "zhipu", Message: "boom", Status: 429}},
	}}
	_, err := chain.Search(context.Background(), SearchParams{Query: "q"})
	if err == nil {
		t.Fatal("expected an aggregated error")
	}
	want := "all search providers failed: zhipu: boom (status 429)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}
