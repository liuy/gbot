package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
