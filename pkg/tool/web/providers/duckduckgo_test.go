package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

const ddgHTMLResponse = `
<!DOCTYPE html>
<html>
<body>
<div class="result">
	<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Fgo1.18&amp;rut=abc">Go 1.18 Release Notes</a>
	<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Fgo1.18">Go 1.18 adds support for generics...</a>
</div>
<div class="result">
	<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fgo-generics&amp;rut=def">Go Generics Tutorial</a>
	<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fgo-generics">Learn how to use generics in Go...</a>
</div>
<div class="result">
	<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fblog.com%2Fgo-generics-deep-dive&amp;rut=ghi">Deep Dive into Go Generics</a>
	<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fblog.com%2Fgo-generics-deep-dive">A comprehensive guide to type parameters...</a>
</div>
</body>
</html>
`

func TestDuckDuckGo_Search_Parsing(t *testing.T) {
	sources, err := parseDDGHTML(strings.NewReader(ddgHTMLResponse), 10)
	if err != nil {
		t.Fatalf("parseDDGHTML error: %v", err)
	}
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(sources))
	}
	if sources[0].Title != "Go 1.18 Release Notes" {
		t.Errorf("source[0].Title = %q, want %q", sources[0].Title, "Go 1.18 Release Notes")
	}
	if sources[0].URL != "https://go.dev/doc/go1.18" {
		t.Errorf("source[0].URL = %q, want %q", sources[0].URL, "https://go.dev/doc/go1.18")
	}
	if sources[0].Snippet != "Go 1.18 adds support for generics..." {
		t.Errorf("source[0].Snippet = %q", sources[0].Snippet)
	}
	if sources[1].URL != "https://example.com/go-generics" {
		t.Errorf("source[1].URL = %q, want %q", sources[1].URL, "https://example.com/go-generics")
	}
}

func TestDuckDuckGo_Search_EmptyResults(t *testing.T) {
	emptyHTML := `<!DOCTYPE html><html><body></body></html>`
	sources, err := parseDDGHTML(strings.NewReader(emptyHTML), 10)
	if err != nil {
		t.Fatalf("parseDDGHTML error: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("expected 0 sources, got %d", len(sources))
	}
}

func TestDuckDuckGo_Search_Limit(t *testing.T) {
	sources, err := parseDDGHTML(strings.NewReader(ddgHTMLResponse), 2)
	if err != nil {
		t.Fatalf("parseDDGHTML error: %v", err)
	}
	if len(sources) != 2 {
		t.Errorf("expected 2 sources (limit), got %d", len(sources))
	}
}

func TestDuckDuckGo_Search_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &DuckDuckGoProvider{}
	_, err := d.Search(ctx, SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("search should fail with cancelled context")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", ctx.Err())
	}
}

func TestExtractDDGURL(t *testing.T) {
	tests := []struct {
		name string
		href string
		want string
	}{
		{"empty", "", ""},
		{"redirect_url", "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=abc", "https://example.com"},
		{"double_slash", "//example.com/path", "https://example.com/path"},
		{"direct_url", "https://direct.com/page", "https://direct.com/page"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDDGURL(tt.href)
			if got != tt.want {
				t.Errorf("extractDDGURL(%q) = %q, want %q", tt.href, got, tt.want)
			}
		})
	}
}

func TestDuckDuckGo_ID(t *testing.T) {
	d := &DuckDuckGoProvider{}
	if d.ID() != "duckduckgo" {
		t.Errorf("ID() = %q, want %q", d.ID(), "duckduckgo")
	}
}

func TestDuckDuckGo_IsAvailable(t *testing.T) {
	d := &DuckDuckGoProvider{}
	if !d.IsAvailable() {
		t.Error("IsAvailable() should always be true for DDG")
	}
}

func TestDuckDuckGo_Search_HTTPSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("expected User-Agent header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, ddgHTMLResponse)
	}))
	defer server.Close()

	d := &DuckDuckGoProvider{Client: server.Client()}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "go generics", Limit: 10}, server.URL)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if resp.Provider != "duckduckgo" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "duckduckgo")
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Go 1.18 Release Notes" {
		t.Errorf("Sources[0].Title = %q", resp.Sources[0].Title)
	}
}

func TestDuckDuckGo_Search_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	d := &DuckDuckGoProvider{Client: server.Client()}
	_, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, server.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error should mention status 403, got: %v", err)
	}
	var pe *SearchProviderError
	if !isProviderError(err, &pe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if pe.Status != http.StatusForbidden {
		t.Errorf("Status = %d, want 403", pe.Status)
	}
}

// ddgAnomalyError pins the exact blocked-page error the plan mandates
// (omp duckduckgo.ts wording, provider examples adapted to gbot's chain).
const ddgAnomalyError = "duckduckgo: DuckDuckGo blocked the request with a bot-detection challenge. " +
	"DuckDuckGo throttles automated HTML searches from datacenter/shared-egress IPs; " +
	"configure a credentialed provider such as Jina or Tavily for reliable web search. (status 429)"

func TestDDGIsAnomalyResponse(t *testing.T) {
	tests := []struct {
		name string
		html string
		want bool
	}{
		{"anomaly-modal substring", "x anomaly-modal y", true},
		{"anomaly.js", "anomaly.js", true},
		{"results page", ddgHTMLResponse, false},
		{"case sensitive", "ANOMALY-MODAL", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAnomalyResponse(tt.html); got != tt.want {
				t.Errorf("isAnomalyResponse(%q) = %v, want %v", tt.html, got, tt.want)
			}
		})
	}
}

func TestDuckDuckGo_Search_AnomalyNoBrowse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<div class="anomaly-modal">challenge</div>`)
	}))
	defer server.Close()

	d := &DuckDuckGoProvider{Client: server.Client()}
	_, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, server.URL)
	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 429 {
		t.Errorf("Status = %d, want 429", spe.Status)
	}
	if !spe.IsRateLimit() {
		t.Error("IsRateLimit() = false, want true")
	}
	if spe.Error() != ddgAnomalyError {
		t.Errorf("Error() = %q, want %q", spe.Error(), ddgAnomalyError)
	}
}

func TestDuckDuckGo_Search_HTTP202Accepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, ddgHTMLResponse)
	}))
	defer server.Close()

	d := &DuckDuckGoProvider{Client: server.Client()}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "test", Limit: 10}, server.URL)
	if err != nil {
		t.Fatalf("202 with a results body must be accepted, got %v", err)
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
}

func TestDuckDuckGo_Search_ReadBodyError(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)),
			Header:     http.Header{"Content-Type": []string{"text/html"}},
		}, nil
	})

	d := &DuckDuckGoProvider{Client: &http.Client{Transport: transport}}
	_, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, "https://example.invalid/")
	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Provider != "duckduckgo" {
		t.Errorf("Provider = %q, want %q", spe.Provider, "duckduckgo")
	}
	if spe.Message != "read response: unexpected EOF" {
		t.Errorf("Message = %q, want %q", spe.Message, "read response: unexpected EOF")
	}
	if spe.Status != 200 {
		t.Errorf("Status = %d, want 200 (carries resp.StatusCode)", spe.Status)
	}
}

// browseRecorder mocks the Browse closure and records every call.
type browseRecorder struct {
	calls          int
	urls           []string
	timeouts       []time.Duration
	ctxs           []context.Context
	canceledAtCall bool
	html           string
	err            error
}

func (b *browseRecorder) browse(ctx context.Context, u string, timeout time.Duration) (string, error) {
	b.calls++
	b.urls = append(b.urls, u)
	b.timeouts = append(b.timeouts, timeout)
	b.ctxs = append(b.ctxs, ctx)
	b.canceledAtCall = ctx.Err() != nil
	return b.html, b.err
}

func TestDuckDuckGo_Search_FallbackOn403(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	rec := &browseRecorder{html: ddgHTMLResponse}
	d := &DuckDuckGoProvider{Client: server.Client(), Browse: rec.browse}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "go generics", Limit: 10}, server.URL)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("browse called %d times, want exactly 1", rec.calls)
	}
	if rec.urls[0] != server.URL {
		t.Errorf("browse URL = %q, want %q", rec.urls[0], server.URL)
	}
	if rec.timeouts[0] != 25*time.Second {
		t.Errorf("browse timeout = %v, want 25s", rec.timeouts[0])
	}
	// The render ctx must be live during Browse and canceled after return.
	if rec.canceledAtCall {
		t.Error("browse ctx already canceled at call time")
	}
	if err := rec.ctxs[0].Err(); err == nil {
		t.Error("browse ctx not canceled after search returned")
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Go 1.18 Release Notes" {
		t.Errorf("Sources[0].Title = %q, want %q", resp.Sources[0].Title, "Go 1.18 Release Notes")
	}
	if resp.Provider != "duckduckgo" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "duckduckgo")
	}
}

func TestDuckDuckGo_Search_FallbackOnAnomaly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<script src="anomaly.js"></script>`)
	}))
	defer server.Close()

	rec := &browseRecorder{html: ddgHTMLResponse}
	d := &DuckDuckGoProvider{Client: server.Client(), Browse: rec.browse}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "test", Limit: 10}, server.URL)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("browse called %d times, want exactly 1", rec.calls)
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
}

func TestDuckDuckGo_Search_FallbackStillAnomaly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	rec := &browseRecorder{html: `<div class="anomaly-modal">challenge</div>`}
	d := &DuckDuckGoProvider{Client: server.Client(), Browse: rec.browse}
	_, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, server.URL)
	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 429 {
		t.Errorf("Status = %d, want 429", spe.Status)
	}
	if !spe.IsRateLimit() {
		t.Error("IsRateLimit() = false, want true")
	}
	if spe.Error() != ddgAnomalyError {
		t.Errorf("Error() = %q, want %q", spe.Error(), ddgAnomalyError)
	}
}

func TestDuckDuckGo_Search_FallbackBrowseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	rec := &browseRecorder{err: errors.New("no chrome")}
	d := &DuckDuckGoProvider{Client: server.Client(), Browse: rec.browse}
	_, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, server.URL)
	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 503 {
		t.Errorf("Status = %d, want 503", spe.Status)
	}
	want := "duckduckgo: DuckDuckGo browser fallback failed: no chrome (status 503)"
	if spe.Error() != want {
		t.Errorf("Error() = %q, want %q", spe.Error(), want)
	}
}

func TestDuckDuckGo_Search_FallbackTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // close to force connection failure

	rec := &browseRecorder{html: ddgHTMLResponse}
	d := &DuckDuckGoProvider{Client: server.Client(), Browse: rec.browse}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "test", Limit: 10}, server.URL)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("browse called %d times, want exactly 1", rec.calls)
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Go 1.18 Release Notes" {
		t.Errorf("Sources[0].Title = %q, want %q", resp.Sources[0].Title, "Go 1.18 Release Notes")
	}
}

func TestDuckDuckGo_Search_ContextCancelNoBrowse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &DuckDuckGoProvider{
		Browse: func(ctx context.Context, u string, timeout time.Duration) (string, error) {
			t.Errorf("browse must not run on canceled parent ctx (url %s)", u)
			return "", nil
		},
	}
	_, err := d.Search(ctx, SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected error with canceled context")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v, want the cancellation to surface", err)
	}
}

func TestDuckDuckGo_Search_NoBrowserTouchOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, ddgHTMLResponse)
	}))
	defer server.Close()

	d := &DuckDuckGoProvider{
		Client: server.Client(),
		Browse: func(ctx context.Context, u string, timeout time.Duration) (string, error) {
			t.Errorf("browse must not run on plain success (url %s)", u)
			return "", nil
		},
	}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "test", Limit: 10}, server.URL)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
}

func TestDuckDuckGo_Search_FallbackRenderedEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	rec := &browseRecorder{html: "not html"}
	d := &DuckDuckGoProvider{Client: server.Client(), Browse: rec.browse}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "test", Limit: 10}, server.URL)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Sources) != 0 {
		t.Fatalf("expected 0 sources, got %d", len(resp.Sources))
	}
}

func TestDuckDuckGo_Search_URLAndQueryViaSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	saved := DDGSearchURL
	DDGSearchURL = server.URL
	t.Cleanup(func() { DDGSearchURL = saved })

	rec := &browseRecorder{html: ddgHTMLResponse}
	d := &DuckDuckGoProvider{Client: server.Client(), Browse: rec.browse}
	resp, err := d.Search(context.Background(), SearchParams{Query: "go generics", Limit: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("browse called %d times, want exactly 1", rec.calls)
	}
	wantURL := server.URL + "?q=go+generics"
	if rec.urls[0] != wantURL {
		t.Errorf("browse URL = %q, want %q", rec.urls[0], wantURL)
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
}

func TestDuckDuckGo_client_Default(t *testing.T) {
	d := &DuckDuckGoProvider{}
	if d.client() != http.DefaultClient {
		t.Error("expected DefaultClient when Client is nil")
	}
}

func TestDuckDuckGo_client_Custom(t *testing.T) {
	custom := &http.Client{Timeout: 5 * time.Second}
	d := &DuckDuckGoProvider{Client: custom}
	if d.client() != custom {
		t.Error("expected custom client")
	}
}

func isProviderError(err error, target **SearchProviderError) bool {
	pe, ok := err.(*SearchProviderError)
	if ok && target != nil {
		*target = pe
	}
	return ok
}

func TestDuckDuckGo_Search_TitleFallback(t *testing.T) {
	// Result with URL but no title → title should be the URL.
	html := `<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com"></a><a class="result__snippet">snippet</a></div>`
	sources, err := parseDDGHTML(strings.NewReader(html), 10)
	if err != nil {
		t.Fatalf("parseDDGHTML error: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].Title != "https://example.com" {
		t.Errorf("Title = %q, want URL as fallback", sources[0].Title)
	}
}

func TestDuckDuckGo_Search_SkipEmptyResult(t *testing.T) {
	html := `<div class="result"><a class="result__a" href=""></a><a class="result__snippet"></a></div>`
	sources, err := parseDDGHTML(strings.NewReader(html), 10)
	if err != nil {
		t.Fatalf("parseDDGHTML error: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("expected 0 sources for empty result, got %d", len(sources))
	}
}

func TestDuckDuckGo_searchWithClient_BuildRequestError(t *testing.T) {
	d := &DuckDuckGoProvider{Client: http.DefaultClient}
	// Malformed URL with invalid host triggers NewRequestWithContext error.
	_, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, "http://[::1]:namedport")
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
	if !strings.Contains(err.Error(), "build request") {
		t.Errorf("error = %v, want to contain 'build request'", err)
	}
}

func TestDuckDuckGo_searchWithClient_RequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // close to force connection failure

	d := &DuckDuckGoProvider{Client: server.Client()}
	_, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, server.URL)
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 0 {
		t.Errorf("Status = %d, want 0 (request failed)", spe.Status)
	}
	if !strings.Contains(spe.Message, "request failed") {
		t.Errorf("Message = %q, want to contain 'request failed'", spe.Message)
	}
}

func TestDuckDuckGo_searchWithClient_ParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not html"))
	}))
	defer server.Close()

	d := &DuckDuckGoProvider{Client: server.Client()}
	resp, err := d.searchWithClient(context.Background(), SearchParams{Query: "test"}, server.URL)
	// goquery is permissive and won't error on plain text — it parses it as a
	// single text node. The function returns no sources but no error.
	if err != nil {
		t.Fatalf("goquery should accept plain text, got error: %v", err)
	}
	if resp.Provider != "duckduckgo" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "duckduckgo")
	}
	if len(resp.Sources) != 0 {
		t.Errorf("len(Sources) = %d, want 0 for plain text body", len(resp.Sources))
	}
}

func TestParseDDGHTML_Error(t *testing.T) {
	// A reader that always errors triggers goquery's NewDocumentFromReader error path.
	_, err := parseDDGHTML(&errReader{}, 10)
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "parse HTML") {
		t.Errorf("error = %v, want to contain 'parse HTML'", err)
	}
}

type errReader struct{}

func (errReader) Read(p []byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestExtractDDGURL_MalformedURL(t *testing.T) {
	// A URL containing "uddg=" that url.Parse rejects.
	// Per RFC 3986, a literal control character in the host is invalid.
	got := extractDDGURL("http://exam\x00ple.com/?uddg=https%3A%2F%2Ftarget.com")
	// url.Parse with control char is rejected → fall through to return href as-is.
	if got == "" {
		t.Errorf("extractDDGURL returned empty for malformed URL; expected href fallback")
	}
}

func TestExtractDDGURL_QueryUnescapeError(t *testing.T) {
	// Construct an href whose uddg query value is invalid percent-encoding.
	// url.QueryUnescape fails on a stray '%' that doesn't form valid hex pairs.
	// Force encoded!= "" and QueryUnescape failure to hit the `return encoded` branch.
	href := "https://duckduckgo.com/l/?uddg=%zz"
	got := extractDDGURL(href)
	if got == "" {
		t.Errorf("expected non-empty fallback to raw encoded value")
	}
}
