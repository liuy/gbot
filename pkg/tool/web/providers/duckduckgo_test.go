package providers

import (
	"context"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool/web"
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
	_, err := d.Search(ctx, web.SearchParams{Query: "test"})
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
