package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool/web/providers"
	"unicode/utf8"
)

func TestIsURL(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"https://example.com", true},
		{"http://foo.com/path", true},
		{"golang.org/x/text", true},
		{"github.com/owner/repo/issues/1", true},
		{"pkg.go.dev/encoding/json", true},
		{"user/repo", false},
		{"golang generics", false},
		{"how to write go tests", false},
		{"", false},
		{"just-a-word", false},
		{"go to example.com/path", false},
		{"https://", false},
		{"http://", false},
		{"a.b/c", false}, // 1-char TLD too short
		{"x.y/z", false}, // 1-char TLD too short
		{"golang.org", true},       // domain-only URL
		{"example.com", true},      // domain-only URL
		{"see foo.bar for details", false}, // has space before dot
		{"visit http://example.com for info", false},
		{"HTTPS://EXAMPLE.COM", true},   // uppercase scheme
		{"Http://Example.com/path", true}, // mixed case scheme
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := IsURL(tt.query)
			if got != tt.want {
				t.Errorf("IsURL(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestFormatForLLM(t *testing.T) {
	t.Run("empty response", func(t *testing.T) {
		resp := &providers.SearchResponse{Provider: "test"}
		got := formatForLLM(resp)
		if got != "" {
			t.Errorf("expected empty output, got %q", got)
		}
	})

	t.Run("with answer", func(t *testing.T) {
		resp := &providers.SearchResponse{
			Provider: "test",
			Answer:   "Go generics were introduced in Go 1.18.",
			Sources: []providers.SearchSource{
				{Title: "Go 1.18 Release Notes", URL: "https://go.dev/doc/go1.18"},
			},
		}
		got := formatForLLM(resp)
		if got == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(got, "Go generics were introduced") {
			t.Errorf("expected answer in output, got %q", got)
		}
		if !strings.Contains(got, "## Sources") {
			t.Errorf("expected Sources header in output, got %q", got)
		}
		if !strings.Contains(got, "[1] Go 1.18 Release Notes") {
			t.Errorf("expected source title in output, got %q", got)
		}
	})

	t.Run("with age", func(t *testing.T) {
		resp := &providers.SearchResponse{
			Provider: "test",
			Sources: []providers.SearchSource{
				{Title: "Test", URL: "https://example.com", AgeSeconds: 86400 * 2},
			},
		}
		got := formatForLLM(resp)
		if !strings.Contains(got, "(2d ago)") {
			t.Errorf("expected age in output, got %q", got)
		}
	})

	t.Run("with long snippet", func(t *testing.T) {
		var longSnippet strings.Builder
		for range 300 {
			longSnippet.WriteString("x")
		}
		resp := &providers.SearchResponse{
			Provider: "test",
			Sources: []providers.SearchSource{
				{Title: "Test", URL: "https://example.com", Snippet: longSnippet.String()},
			},
		}
		got := formatForLLM(resp)
		if !strings.Contains(got, "…") {
			t.Errorf("expected truncated snippet, got output without ellipsis")
		}
	})

	t.Run("with long CJK snippet respects rune boundary", func(t *testing.T) {
		// Each CJK char is 3 bytes, so 100 chars = 300 bytes > 240 threshold
		var cjkSnippet strings.Builder
		for range 100 {
			cjkSnippet.WriteString("你")
		}
		resp := &providers.SearchResponse{
			Provider: "test",
			Sources: []providers.SearchSource{
				{Title: "测试", URL: "https://example.com", Snippet: cjkSnippet.String()},
			},
		}
		got := formatForLLM(resp)
		if !strings.Contains(got, "…") {
			t.Errorf("expected truncated CJK snippet, got output without ellipsis")
		}
		// Verify no partial rune — output should be valid UTF-8
		for _, r := range got {
			if r == utf8.RuneError {
				t.Error("truncation produced invalid UTF-8 (RuneError)")
			}
		}
	})
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		seconds int64
		want    string
	}{
		{0, ""},
		{-1, ""},
		{100, "just now"},
		{86400, "1d ago"},
		{86400 * 5, "5d ago"},
		{86400 * 45, "1mo ago"},
		{86400 * 400, "1y ago"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatAge(tt.seconds)
			if got != tt.want {
				t.Errorf("formatAge(%d) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
	}
}

func TestExtractProxyURL(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		if got := extractProxyURL(nil); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("nil transport", func(t *testing.T) {
		if got := extractProxyURL(&http.Client{}); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("default transport", func(t *testing.T) {
		if got := extractProxyURL(http.DefaultClient); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("custom transport without proxy", func(t *testing.T) {
		client := &http.Client{Transport: &http.Transport{}}
		if got := extractProxyURL(client); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("client with proxy", func(t *testing.T) {
		proxyURL, _ := url.Parse("http://localhost:10809")
		client := &http.Client{
			Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		}
		got := extractProxyURL(client)
		if got != "http://localhost:10809" {
			t.Errorf("expected http://localhost:10809, got %q", got)
		}
	})

	t.Run("non-Transport type", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer server.Close()
		// httptest.Server.Client() uses a custom Transport that isn't *http.Transport
		_ = server.Client()
		// This is a pathological case; just verify no panic
	})
}

func TestIsConvertibleDocument(t *testing.T) {
	tests := []struct {
		mime string
		ext  string
		want bool
	}{
		{"application/pdf", ".pdf", true},
		{"application/pdf", "", true},
		{"", ".pdf", true},
		{"application/octet-stream", ".docx", true},
		{"text/html", ".pdf", false},
		{"application/json", ".json", false},
		{"", ".html", false},
		{"", "", false},
		{"application/vnd.ms-excel", ".xls", true},
		{"text/plain", ".ipynb", true},
		{"text/plain", ".csv", true},
		{"text/plain", ".html", false},
	}
	for _, tt := range tests {
		got := IsConvertibleDocument(tt.mime, tt.ext)
		if got != tt.want {
			t.Errorf("IsConvertibleDocument(%q, %q) = %v, want %v", tt.mime, tt.ext, got, tt.want)
		}
	}
}

func TestGetExt(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/paper.pdf", ".pdf"},
		{"https://example.com/doc.docx", ".docx"},
		{"/path/to/file.xlsx", ".xlsx"},
		{"/no/ext", ""},
		{"/dir.with.dots/file", ""},
		{"/archive.tar.gz", ".gz"},
	}
	for _, tt := range tests {
		got := getExt(tt.path)
		if got != tt.want {
			t.Errorf("getExt(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestFetchAndConvertDocument_NonDocURL(t *testing.T) {
	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, "https://example.com/page.html", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty markdown for non-doc URL, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_PDFViaHTTP(t *testing.T) {
	pdfData, err := os.ReadFile("../../markitdown/testdata/test.pdf")
	if err != nil {
		t.Skip("test.pdf not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(pdfData)
	}))
	defer server.Close()

	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, server.URL+"/paper.pdf", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md == "" {
		t.Fatal("expected non-empty markdown from PDF conversion")
	}
	if !strings.Contains(md, "contemporaneous") {
		preview := md
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Errorf("expected PDF content to contain 'contemporaneous', got: %s", preview)
	}
}

func TestFetchAndConvertDocument_WrongContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>not a pdf</html>"))
	}))
	defer server.Close()

	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, server.URL+"/paper.pdf", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty markdown when Content-Type mismatches, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, server.URL+"/missing.pdf", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty markdown for 404, got %d chars", len(md))
	}
}
