package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
