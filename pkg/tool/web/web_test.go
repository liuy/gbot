package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/web/providers"
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
		{"a.b/c", false},                   // 1-char TLD too short
		{"x.y/z", false},                   // 1-char TLD too short
		{"golang.org", true},               // domain-only URL
		{"example.com", true},              // domain-only URL
		{"see foo.bar for details", false}, // has space before dot
		{"visit http://example.com for info", false},
		{"HTTPS://EXAMPLE.COM", true},     // uppercase scheme
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
		got := formatForLLM(resp, nil)
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
		got := formatForLLM(resp, nil)
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
		got := formatForLLM(resp, nil)
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
		got := formatForLLM(resp, nil)
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
		got := formatForLLM(resp, nil)
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

	t.Run("sources render as valid markdown list", func(t *testing.T) {
		resp := &providers.SearchResponse{
			Provider: "test",
			Answer:   "The answer.",
			Sources: []providers.SearchSource{
				{Title: "Go 1.18 Release Notes", URL: "https://go.dev/doc/go1.18"},
				{Title: "Go Generics Tutorial", URL: "https://go.dev/doc/tutorial/generics"},
			},
		}
		got := formatForLLM(resp, nil)
		if !strings.Contains(got, "- [1]") {
			t.Errorf("source items should use markdown list syntax '- [N] Title', got:\n%s", got)
		}
		if !strings.Contains(got, "- [2]") {
			t.Errorf("second source should use markdown list syntax '- [2] Title', got:\n%s", got)
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

func TestPluralWord(t *testing.T) {
	if pluralWord(1, "source") != "source" {
		t.Error("expected singular form")
	}
	if pluralWord(0, "source") != "sources" {
		t.Error("expected plural for 0")
	}
	if pluralWord(2, "source") != "sources" {
		t.Error("expected plural for 2")
	}
}

func TestTruncateRunes(t *testing.T) {
	// Short string passes through.
	if truncateRunes("hello", 10) != "hello" {
		t.Error("short string should pass through")
	}
	// ASCII truncation.
	if truncateRunes("hello world", 5) != "hello" {
		t.Error("ASCII truncation failed")
	}
	// CJK truncation — must not split a 3-byte rune.
	input := "你好世界"
	got := truncateRunes(input, 5) // mid-rune boundary
	if len(got) != 3 {
		t.Errorf("expected 3 bytes (one CJK char), got %d: %q", len(got), got)
	}
}

func TestFormatForLLM_SourcesOnly(t *testing.T) {
	resp := &providers.SearchResponse{
		Provider: "test",
		Sources: []providers.SearchSource{
			{Title: "Test", URL: "https://example.com"},
		},
	}
	got := formatForLLM(resp, nil)
	if !strings.Contains(got, "[1] Test") {
		t.Errorf("expected numbered source, got: %q", got)
	}
}

func TestFormatForLLM_AnswerWithSources(t *testing.T) {
	resp := &providers.SearchResponse{
		Provider: "test",
		Answer:   "The answer is 42",
		Sources: []providers.SearchSource{
			{Title: "Source1", URL: "https://example.com"},
		},
	}
	got := formatForLLM(resp, nil)
	if !strings.Contains(got, "## Sources") {
		t.Error("expected Sources header when answer has sources")
	}
	if !strings.Contains(got, "1 source") {
		t.Errorf("expected singular '1 source', got: %q", got)
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

func TestFormatForLLM_PublishedDateFallback(t *testing.T) {
	// When AgeSeconds <= 0 but PublishedDate is set, formatForLLM should
	// use PublishedDate in the title parenthetical instead of leaving it blank.
	resp := &providers.SearchResponse{
		Provider: "test",
		Sources: []providers.SearchSource{
			{Title: "Old Article", URL: "https://example.com/old", PublishedDate: "2020-01-15"},
		},
	}
	got := formatForLLM(resp, nil)
	if !strings.Contains(got, "Old Article (2020-01-15)") {
		t.Errorf("expected title with PublishedDate in parens, got: %q", got)
	}
}

func TestIsURL_DotAtEnd(t *testing.T) {
	// "foo." hits tldStart >= len(query) → false.
	if IsURL("foo.") {
		t.Error("IsURL(\"foo.\") = true, want false (tldStart out of range)")
	}
	if IsURL("a.") {
		t.Error("IsURL(\"a.\") = true, want false")
	}
}

func TestExtByMime(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"application/pdf", ".pdf"},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
		{"application/vnd.ms-excel", ".xls"},
		{"application/epub+zip", ".epub"},
		{"text/html", ""},
		{"", ""},
		{"application/octet-stream", ""},
	}
	for _, tt := range tests {
		got := extByMime(tt.mime)
		if got != tt.want {
			t.Errorf("extByMime(%q) = %q, want %q", tt.mime, got, tt.want)
		}
	}
}

func TestWithAPIKeys(t *testing.T) {
	keys := map[string]string{
		"anysearch": "test-key",
		"zhipu":     "zhipu-key",
	}
	opt := WithAPIKeys(keys)
	cfg := &webConfig{}
	opt(cfg)
	if cfg.apiKeys["anysearch"] != "test-key" {
		t.Errorf("expected anysearch key, got %q", cfg.apiKeys["anysearch"])
	}
	if cfg.apiKeys["zhipu"] != "zhipu-key" {
		t.Errorf("expected zhipu key, got %q", cfg.apiKeys["zhipu"])
	}
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

func TestFetchAndConvertDocument_DOCXViaHTTP(t *testing.T) {
	docxData, err := os.ReadFile("../../markitdown/testdata/test.docx")
	if err != nil {
		t.Skip("test.docx not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		_, _ = w.Write(docxData)
	}))
	defer server.Close()

	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, server.URL+"/paper.docx", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md == "" {
		t.Fatal("expected non-empty markdown from DOCX conversion")
	}
	if !strings.Contains(md, "AutoGen") {
		preview := md
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Errorf("expected DOCX content to contain 'AutoGen', got: %s", preview)
	}
}

func TestFetchAndConvertDocument_WrongContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not a pdf</html>"))
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

func TestFetchAndConvertDocument_HEADDetectsDOCX(t *testing.T) {
	docxData, err := os.ReadFile("../../markitdown/testdata/test.docx")
	if err != nil {
		t.Skip("test.docx not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		_, _ = w.Write(docxData)
	}))
	defer server.Close()

	// URL has no .docx extension — should use HEAD to detect type.
	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, server.URL+"/download/abc123", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md == "" {
		t.Fatal("expected non-empty markdown from HEAD-detected DOCX")
	}
	if !strings.Contains(md, "AutoGen") {
		t.Error("expected DOCX content from HEAD-detected conversion")
	}
}

func TestFetchAndConvertDocument_HEADSaysHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("Content-Type", "text/html")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not a doc</html>"))
	}))
	defer server.Close()

	// No extension, HEAD says HTML — should fall through.
	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, server.URL+"/download/abc123", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty markdown when HEAD says HTML, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_ConversionFailure(t *testing.T) {
	// Serve invalid DOCX bytes with correct Content-Type.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		_, _ = w.Write([]byte("not a real docx"))
	}))
	defer server.Close()

	ctx := context.Background()
	md, err := fetchAndConvertDocument(ctx, server.URL+"/bad.docx", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// markitdown may return empty or garbage for invalid input — either way no crash.
	// We only verify it didn't panic and returned no error.
	if md == "" {
		t.Log("markitdown returned empty for invalid PDF (acceptable)")
	}
}

func TestFetchAndConvertDocument_GetNetworkError(t *testing.T) {
	// Extension-matched URL on an unreachable host triggers the GET client.Do
	// error path, which returns ("", nil).
	md, err := fetchAndConvertDocument(context.Background(), "http://127.0.0.1:1/doc.docx", http.DefaultClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty markdown for unreachable host, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_BodyReadError(t *testing.T) {
	// Trigger io.ReadAll error by hijacking the conn and declaring a large
	// Content-Length then closing early — io.ReadAll returns io.ErrUnexpectedEOF.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(bufrw, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\nContent-Type: application/pdf\r\n\r\nshort")
		_ = bufrw.Flush()
	}))
	defer server.Close()

	md, err := fetchAndConvertDocument(context.Background(), server.URL+"/file.pdf", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty markdown for body read error, got %d chars", len(md))
	}
}

func TestNew_NilClient(t *testing.T) {
	tl := New(nil)
	if tl.Name() != "Web" {
		t.Errorf("Name() = %q, want %q", tl.Name(), "Web")
	}
	aliases := tl.Aliases()
	if len(aliases) != 1 || aliases[0] != "web" {
		t.Errorf("Aliases() = %v, want [web]", aliases)
	}
}

func TestMockProvider_ID(t *testing.T) {
	m := &mockProvider{id: "test-provider"}
	if m.ID() != "test-provider" {
		t.Errorf("ID() = %q, want %q", m.ID(), "test-provider")
	}
}

func TestNew_CallFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><h1>Fetched</h1></body></html>")
	}))
	defer server.Close()

	tl := New(server.Client())

	result, err := tl.Call(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL)), nil)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "Fetched") {
		t.Errorf("content should contain fetched text, got: %q", output.Content)
	}
}

func TestNew_CallEmptyQuery(t *testing.T) {
	tl := New(nil)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"query": ""}`), nil)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "query") {
		t.Fatalf("error should mention empty/query, got: %q", err.Error())
	}
}

func TestNew_WithClientAndKeys(t *testing.T) {
	tl := New(http.DefaultClient, WithAPIKeys(map[string]string{
		"anysearch":  "test-key",
		"zhipu":      "zhipu-key",
		"duckduckgo": "",
	}))
	if tl.Name() != "Web" {
		t.Errorf("Name() = %q, want %q", tl.Name(), "Web")
	}
	schema := tl.InputSchema()
	if !strings.Contains(string(schema), "query") {
		t.Error("schema should contain 'query' property")
	}
	if !strings.Contains(string(schema), "Available:") {
		t.Error("schema should list available providers when keys are provided")
	}
}

func TestNew_Description(t *testing.T) {
	tl := New(nil)
	desc, err := tl.Description(json.RawMessage(`{"query": "test search"}`))
	if err != nil {
		t.Fatalf("Description() error = %v", err)
	}
	if desc != "test search" {
		t.Errorf("Description() = %q, want %q", desc, "test search")
	}

	// Invalid JSON → fallback description
	desc, err = tl.Description(json.RawMessage(`bad json`))
	if err != nil {
		t.Fatalf("Description() with bad JSON should not error, got: %v", err)
	}
	if desc != "Search the web or fetch URLs" {
		t.Errorf("Description() fallback = %q, want default", desc)
	}
}

func TestNew_NilClientUsesDefault(t *testing.T) {
	tl := New(nil)
	output := tl.InputSchema()
	if !strings.Contains(string(output), "query") {
		t.Error("expected query in schema")
	}
}

func TestNew_NoProviderKeys(t *testing.T) {
	tl := New(http.DefaultClient)
	schema := string(tl.InputSchema())
	if !strings.Contains(schema, "query") {
		t.Error("expected query in schema")
	}
	// DDG needs no API key, so it's always available.
	if !strings.Contains(schema, "duckduckgo") {
		t.Error("expected duckduckgo as default provider (no key needed)")
	}
}

func TestExecuteFetch_DocumentConversion(t *testing.T) {
	docxData, err := os.ReadFile("../../markitdown/testdata/test.docx")
	if err != nil {
		t.Skip("test.docx not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		_, _ = w.Write(docxData)
	}))
	defer server.Close()

	result, err := execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": "%s/download/abc"}`, server.URL)), nil, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want fetch", output.Mode)
	}
	if !strings.Contains(output.Content, "AutoGen") {
		t.Errorf("expected DOCX content, got: %q", truncateRunes(output.Content, 200))
	}
}

func TestExecuteFetch_RedirectNote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><h1>Moved</h1></body></html>")
	}))
	defer server.Close()

	result, err := execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": "%s/old"}`, server.URL)), nil, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "Moved") {
		t.Errorf("expected content with Moved, got: %q", output.Content)
	}
}

func TestExecuteFetch_PlaintextPassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "plain text response")
	}))
	defer server.Close()

	result, err := execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL)), nil, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "plain text response") {
		t.Errorf("expected plain text, got: %q", output.Content)
	}
}

func TestExecuteFetch_FetchError(t *testing.T) {
	_, err := execute(context.Background(), json.RawMessage(`{"query": "http://127.0.0.1:1"}`), nil, http.DefaultClient, nil)
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}
	if !strings.Contains(err.Error(), "fetch failed") {
		t.Fatalf("error should mention fetch failed, got: %v", err)
	}
}

func TestFetchAndConvertDocument_HeadNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))
	defer server.Close()

	md, err := fetchAndConvertDocument(context.Background(), server.URL+"/download/abc", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty for HEAD 404, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_ExtMatchNonDocContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<xml>not a pdf</xml>`))
	}))
	defer server.Close()

	md, err := fetchAndConvertDocument(context.Background(), server.URL+"/file.pdf", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty for xml content-type with .pdf ext, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_InvalidURL(t *testing.T) {
	md, err := fetchAndConvertDocument(context.Background(), "http://[::1]:namedport/file.pdf", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty for invalid URL, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_GetNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	md, err := fetchAndConvertDocument(context.Background(), server.URL+"/file.pdf", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty for GET 500, got %d chars", len(md))
	}
}

func TestFetchAndConvertDocument_NilClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not a doc</html>"))
	}))
	defer server.Close()

	md, err := fetchAndConvertDocument(context.Background(), server.URL+"/download/abc", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" {
		t.Errorf("expected empty for HEAD-says-HTML, got %d chars", len(md))
	}
}

func TestTruncateStringToRunes(t *testing.T) {
	// ASCII: exact truncation
	if got := truncateStringToRunes("hello world", 5); got != "hello" {
		t.Errorf("truncate ASCII = %q, want %q", got, "hello")
	}
	// Short string passes through
	if got := truncateStringToRunes("hi", 10); got != "hi" {
		t.Errorf("short string = %q, want %q", got, "hi")
	}
	// CJK: no partial rune
	input := "你好世界"
	got := truncateStringToRunes(input, 3)
	for _, r := range got {
		if r == utf8.RuneError {
			t.Error("truncation produced invalid UTF-8")
		}
	}
	if len(got) != 9 { // 3 CJK chars × 3 bytes
		t.Errorf("expected 9 bytes for 3 CJK chars, got %d", len(got))
	}
	// Empty string
	if got := truncateStringToRunes("", 5); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	// Exact length
	if got := truncateStringToRunes("abc", 3); got != "abc" {
		t.Errorf("exact length = %q, want %q", got, "abc")
	}
}

func TestBuildResult_Truncation(t *testing.T) {
	long := strings.Repeat("x", MaxOutputChars+100)
	result := BuildResult(long, FetchResultOptions{URL: "http://example.com"})
	if len(result.Content) > MaxOutputChars {
		t.Errorf("BuildResult should truncate, got %d chars", len(result.Content))
	}
}

func TestBuildResult_WithNotes(t *testing.T) {
	result := BuildResult("hello", FetchResultOptions{
		URL:      "http://example.com",
		FinalURL: "http://example.com/final",
		Notes:    []string{"redirected", "converted"},
	})
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected content to contain hello, got: %q", result.Content)
	}
}

func TestNew_CallSearch(t *testing.T) {
	// Test the search path through execute() with a mock chain to avoid
	// real DDG network calls. The actual DDG provider is tested separately
	// in the providers package.
	chain := &providers.SearchChain{
		Providers: []providers.SearchProvider{
			&mockProvider{
				id:        "test",
				available: true,
				resp: &providers.SearchResponse{
					Provider: "test",
					Answer:   "test answer",
					Sources: []providers.SearchSource{
						{Title: "Test", URL: "https://example.com", Snippet: "snippet"},
					},
				},
			},
		},
	}
	input := json.RawMessage(`{"query": "go generics"}`)
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if output.Mode != "search" {
		t.Errorf("mode = %q, want search", output.Mode)
	}
	if output.Raw == nil {
		t.Fatal("expected Raw SearchResponse, got nil")
	}
	if output.Raw.Provider != "test" {
		t.Errorf("provider = %q, want test", output.Raw.Provider)
	}
}

func TestNew_RenderResult_Output(t *testing.T) {
	tl := New(nil)
	// Use reflection-free path: call via Call_ and inspect result.Data,
	// then call RenderResult_ via Tool interface.
	// Tool interface exposes RenderResult(data any) string.
	type renderer interface {
		RenderResult(data any) string
	}
	r, ok := tl.(renderer)
	if !ok {
		t.Fatalf("tool must implement RenderResult")
	}

	// Case *Output
	got := r.RenderResult(&Output{Mode: "fetch", Content: "hello-output"})
	if got != "hello-output" {
		t.Errorf("RenderResult(*Output) = %q, want %q", got, "hello-output")
	}
}

func TestNew_RenderResult_JSONRawMessage(t *testing.T) {
	tl := New(nil)

	// Valid JSON Output → content (wrapped in array form)
	inner := `{"Mode":"fetch","Content":"hello-json"}`
	textBytes, _ := json.Marshal(inner)
	valid := json.RawMessage(`[{"type":"text","text":` + string(textBytes) + `}]`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(valid)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	got := tl.RenderResult(v)
	if got != "hello-json" {
		t.Errorf("RenderResult(valid json) = %q, want %q", got, "hello-json")
	}

	// Invalid JSON inside text block → DecodeResult returns error
	invalidBytes, _ := json.Marshal("not json")
	invalid := json.RawMessage(`[{"type":"text","text":` + string(invalidBytes) + `}]`)
	_, err = tl.(tool.ToolWithDecodeResult).DecodeResult(invalid)
	if err == nil {
		t.Error("DecodeResult(invalid json inner) should return error")
	}
}

func TestWeb_DecodeResult_RejectsBareStruct(t *testing.T) {
	t.Parallel()

	tl := New(nil)
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{"Mode":"fetch","Content":"x"}`))
	if err == nil {
		t.Error("DecodeResult must reject bare struct form")
	}
}

func TestNew_RenderResult_StringAndDefault(t *testing.T) {
	tl := New(nil)
	type renderer interface {
		RenderResult(data any) string
	}
	r, _ := tl.(renderer)

	// case string
	if got := r.RenderResult("plain-string"); got != "plain-string" {
		t.Errorf("RenderResult(string) = %q, want %q", got, "plain-string")
	}

	// default case (int) → JSON marshal
	got := r.RenderResult(42)
	if !strings.Contains(got, "42") {
		t.Errorf("RenderResult(42) = %q, want containing 42", got)
	}
}

func TestNew_IsSearchOrRead(t *testing.T) {
	tl := New(nil)
	type searchOrReader interface {
		IsSearchOrRead(input json.RawMessage) interface{ IsCollapsible() bool }
	}
	// The Tool interface returns SearchReadKind; verify via concrete cast.
	type searchOrReadConcrete interface {
		IsSearchOrRead(input json.RawMessage) tool.SearchReadKind
	}
	_ = searchOrReader(nil) // keep interface name referenced
	r, ok := tl.(searchOrReadConcrete)
	if !ok {
		t.Fatalf("tool must implement IsSearchOrRead")
	}
	kind := r.IsSearchOrRead(json.RawMessage(`{"query": "test"}`))
	if !kind.IsSearch {
		t.Errorf("IsSearchOrRead().IsSearch = false, want true")
	}
	if kind.IsRead || kind.IsList {
		t.Errorf("IsRead/IsList should be false")
	}
}

func TestExecuteFetch_TruncationNote(t *testing.T) {
	// Trigger result.Truncated via a > MaxBytes (50MB) response through executeFetch.
	// Serve a body that exceeds MaxBytes so LoadPage marks Truncated=true and
	// executeFetch appends the "output truncated" note to the FetchResult.
	big := strings.Repeat("x", 51*1024*1024+100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(big))
	}))
	defer server.Close()

	result, err := execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL)), nil, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want fetch", output.Mode)
	}
	// The truncated content should be at most MaxBytes (50MB) and likely
	// trimmed further by FinalizeOutput (MaxOutputChars). At minimum,
	// the response should be well under the raw 51MB.
	if len(output.Content) > 51*1024*1024 {
		t.Errorf("content len = %d, should be truncated below 51MB", len(output.Content))
	}
	// Content must be non-empty and consist of 'x' chars (not garbled).
	if len(output.Content) == 0 {
		t.Fatal("content should be non-empty after truncation")
	}
	if output.Content[0] != 'x' {
		t.Errorf("first char = %q, want 'x'", output.Content[0])
	}
}

func TestExtractProxyURL_ProxyReturnsError(t *testing.T) {
	// A transport whose Proxy func returns an error should yield "".
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: func(req *http.Request) (*url.URL, error) {
				return nil, fmt.Errorf("proxy lookup failed")
			},
		},
	}
	if got := extractProxyURL(client); got != "" {
		t.Errorf("expected empty when Proxy returns error, got %q", got)
	}
}

func TestExtractProxyURL_ProxyReturnsNil(t *testing.T) {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: func(req *http.Request) (*url.URL, error) {
				return nil, nil
			},
		},
	}
	if got := extractProxyURL(client); got != "" {
		t.Errorf("expected empty when Proxy returns nil, got %q", got)
	}
}

func TestSearchChain_Composition(t *testing.T) {
	client := &http.Client{}
	chain := searchChain(client, map[string]string{"anysearch": "", "jina": "jk", "tavily": "tk", "zhipu": "a.b"})
	want := []string{"anysearch", "jina", "tavily", "zhipu", "duckduckgo"}
	if got := chain.AvailableProviders(); !slices.Equal(got, want) {
		t.Errorf("full keys: AvailableProviders() = %v, want %v", got, want)
	}

	// Wiring: each keyed provider must carry the injected client and key.
	var sawJina, sawTavily bool
	for _, p := range chain.Providers {
		switch v := p.(type) {
		case *providers.JinaProvider:
			sawJina = true
			if v.Client != client || v.APIKey != "jk" {
				t.Errorf("jina wiring = (%p, %q), want injected client and jk", v.Client, v.APIKey)
			}
		case *providers.TavilyProvider:
			sawTavily = true
			if v.Client != client || v.APIKey != "tk" {
				t.Errorf("tavily wiring = (%p, %q), want injected client and tk", v.Client, v.APIKey)
			}
		}
	}
	if !sawJina || !sawTavily {
		t.Errorf("chain lacks jina/tavily registrations (jina=%v tavily=%v)", sawJina, sawTavily)
	}

	chain = searchChain(nil, map[string]string{"tavily": "tk"})
	want = []string{"tavily", "duckduckgo"}
	if got := chain.AvailableProviders(); !slices.Equal(got, want) {
		t.Errorf("tavily only: AvailableProviders() = %v, want %v", got, want)
	}

	chain = searchChain(nil, nil)
	want = []string{"duckduckgo"}
	if got := chain.AvailableProviders(); !slices.Equal(got, want) {
		t.Errorf("no keys: AvailableProviders() = %v, want %v", got, want)
	}
}

// The ddg registration must carry the headless-Chrome escalation closure
// and the injected client.
func TestSearchChain_DDGBrowseWired(t *testing.T) {
	client := &http.Client{}
	chain := searchChain(client, nil)
	for _, p := range chain.Providers {
		ddg, ok := p.(*providers.DuckDuckGoProvider)
		if !ok {
			continue
		}
		if ddg.Browse == nil {
			t.Fatal("duckduckgo provider has nil Browse closure")
		}
		if ddg.Client != client {
			t.Errorf("duckduckgo Client = %p, want the injected client %p", ddg.Client, client)
		}
		return
	}
	t.Fatal("chain lacks a duckduckgo registration")
}

// Full escalation seam: provider → chromedpFetch → real Chrome → parse.
// Dual-role server: the plain UA gets 403, any other UA (the stealth-rendered
// browser) gets results. Counters track only real search requests (q!=""),
// because Chrome fires automatic favicon/subresource fetches that would
// otherwise pollute the browser-side count.
func TestDDGBrowserFallback_RealChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	if !chromeAvailable {
		t.Skip("Chrome/Chromium not installed")
	}

	const plainUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	resultsHTML := `<!DOCTYPE html><html><body>
<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Fgo1.18&amp;rut=abc">Go 1.18 Release Notes</a><a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Fgo1.18">Go 1.18 adds support for generics...</a></div>
<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fgo-generics&amp;rut=def">Go Generics Tutorial</a><a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fgo-generics">Learn how to use generics in Go...</a></div>
<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fblog.com%2Fgo-generics-deep-dive&amp;rut=ghi">Deep Dive into Go Generics</a><a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fblog.com%2Fgo-generics-deep-dive">A comprehensive guide to type parameters...</a></div>
</body></html>`

	var plainHits, chromeHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") == plainUA {
			plainHits.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		chromeHits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, resultsHTML)
	}))
	defer server.Close()

	saved := providers.DDGSearchURL
	providers.DDGSearchURL = server.URL
	t.Cleanup(func() { providers.DDGSearchURL = saved })

	defaultPool.reset()
	t.Cleanup(func() { defaultPool.reset() })

	d := &providers.DuckDuckGoProvider{
		Browse: func(ctx context.Context, u string, timeout time.Duration) (string, error) {
			return chromedpFetch(ctx, u, timeout, "")
		},
	}
	resp, err := d.Search(context.Background(), providers.SearchParams{Query: "go generics", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Go 1.18 Release Notes" {
		t.Errorf("Sources[0].Title = %q, want %q", resp.Sources[0].Title, "Go 1.18 Release Notes")
	}
	if got := plainHits.Load(); got != 1 {
		t.Errorf("plain hits = %d, want exactly 1", got)
	}
	if got := chromeHits.Load(); got != 1 {
		t.Errorf("chrome hits = %d, want exactly 1", got)
	}
}

// An empty settings key must not register the provider, even when the env
// fallback could make it available.
func TestSearchChain_EmptyKeySkipsProvider(t *testing.T) {
	t.Setenv("JINA_API_KEY", "env-key")
	t.Setenv("TAVILY_API_KEY", "env-key")

	chain := searchChain(nil, map[string]string{"jina": "", "tavily": ""})
	want := []string{"duckduckgo"}
	if got := chain.AvailableProviders(); !slices.Equal(got, want) {
		t.Errorf("AvailableProviders() = %v, want %v (empty keys skip registration)", got, want)
	}
}

func TestExecuteSearch_RendersAnswerAndDates(t *testing.T) {
	chain := &providers.SearchChain{
		Providers: []providers.SearchProvider{
			&mockProvider{
				id:        "dated",
				available: true,
				resp: &providers.SearchResponse{
					Provider: "dated",
					Answer:   "Go is a statically typed language",
					Sources: []providers.SearchSource{
						{Title: "Go", URL: "https://go.dev", Snippet: "types", PublishedDate: "2019-06-01", AgeSeconds: 3 * 86400},
					},
				},
			},
		},
	}

	result, err := executeSearch(context.Background(), Input{Query: "go"}, chain)
	if err != nil {
		t.Fatalf("executeSearch() error = %v", err)
	}
	output := result.Data.(*Output)
	if !strings.HasPrefix(output.Content, "Go is a statically typed language\n") {
		t.Errorf("content should lead with the answer line, got: %q", output.Content)
	}
	if !strings.Contains(output.Content, "- [1] Go (3d ago)\n  https://go.dev") {
		t.Errorf("content should render the dated source with an age suffix, got: %q", output.Content)
	}
}

func TestExecuteSearch_SiteDirectiveFilters(t *testing.T) {
	m := &mockProvider{
		id: "mock", available: true,
		resp: &providers.SearchResponse{
			Provider: "mock",
			Sources: []providers.SearchSource{
				{Title: "GH One", URL: "https://github.com/one"},
				{Title: "Other", URL: "https://example.com/two"},
				{Title: "GH Two", URL: "https://github.com/two"},
			},
		},
	}
	chain := &providers.SearchChain{Providers: []providers.SearchProvider{m}}

	result, err := executeSearch(context.Background(), Input{Query: "site:github.com stuff"}, chain)
	if err != nil {
		t.Fatalf("executeSearch() error = %v", err)
	}
	output := result.Data.(*Output)
	if output.Raw == nil || len(output.Raw.Sources) != 2 {
		t.Fatalf("Raw.Sources = %+v, want exactly the 2 github sources", output.Raw)
	}
	if !strings.Contains(output.Content, "GH One") || !strings.Contains(output.Content, "GH Two") {
		t.Errorf("Content should list both github sources, got: %q", output.Content)
	}
	if strings.Contains(output.Content, "example.com/two") {
		t.Errorf("Content should exclude the non-matching source, got: %q", output.Content)
	}
	if strings.Contains(output.Content, "Note:") {
		t.Errorf("Content should carry no relaxation note when the filter kept results, got: %q", output.Content)
	}
}

func TestExecuteSearch_LenientRelaxationNote(t *testing.T) {
	m := &mockProvider{
		id: "mock", available: true,
		resp: &providers.SearchResponse{
			Provider: "mock",
			Answer:   "An answer.",
			Sources: []providers.SearchSource{
				{Title: "Other", URL: "https://example.com/one"},
			},
		},
	}
	chain := &providers.SearchChain{Providers: []providers.SearchProvider{m}}

	result, err := executeSearch(context.Background(), Input{Query: "site:github.com stuff"}, chain)
	if err != nil {
		t.Fatalf("executeSearch() error = %v", err)
	}
	output := result.Data.(*Output)
	// Non-empty input must never yield zero results: the dimension relaxes.
	if output.Raw == nil || len(output.Raw.Sources) != 1 {
		t.Fatalf("Raw.Sources = %+v, want the source kept despite the mismatch", output.Raw)
	}
	wantFirst := "Note: no results matched `site:github.com`; the constraint was relaxed"
	lines := strings.Split(output.Content, "\n")
	if lines[0] != wantFirst {
		t.Fatalf("first line = %q, want %q", lines[0], wantFirst)
	}
	if !strings.Contains(output.Content, "An answer.") {
		t.Errorf("Content should keep the answer after the note, got: %q", output.Content)
	}
}

func TestExecuteSearch_DateDirectiveFilters(t *testing.T) {
	m := &mockProvider{
		id: "mock", available: true,
		resp: &providers.SearchResponse{
			Provider: "mock",
			Sources: []providers.SearchSource{
				{Title: "Old", URL: "https://old.com", PublishedDate: "2023-01-01"},
				{Title: "New", URL: "https://new.com", PublishedDate: "2024-06-01"},
				{Title: "Undated", URL: "https://undated.com"},
			},
		},
	}
	chain := &providers.SearchChain{Providers: []providers.SearchProvider{m}}

	result, err := executeSearch(context.Background(), Input{Query: "x after:2024-01-01"}, chain)
	if err != nil {
		t.Fatalf("executeSearch() error = %v", err)
	}
	output := result.Data.(*Output)
	if len(output.Raw.Sources) != 2 {
		t.Fatalf("Raw.Sources = %+v, want in-range + undated only", output.Raw)
	}
	if output.Raw.Sources[0].Title != "New" || output.Raw.Sources[1].Title != "Undated" {
		t.Errorf("Raw.Sources = %+v, want New then Undated", output.Raw.Sources)
	}
}

func TestExecuteSearch_NoConstraintPassthrough(t *testing.T) {
	m := &mockProvider{
		id: "mock", available: true,
		resp: &providers.SearchResponse{
			Provider: "mock",
			Sources: []providers.SearchSource{
				{Title: "A", URL: "https://a.com"},
				{Title: "B", URL: "https://b.com"},
				{Title: "C", URL: "https://c.com"},
			},
		},
	}
	chain := &providers.SearchChain{Providers: []providers.SearchProvider{m}}

	result, err := executeSearch(context.Background(), Input{Query: "plain query"}, chain)
	if err != nil {
		t.Fatalf("executeSearch() error = %v", err)
	}
	output := result.Data.(*Output)
	if len(output.Raw.Sources) != 3 {
		t.Fatalf("len(Raw.Sources) = %d, want 3 (untouched)", len(output.Raw.Sources))
	}
	if strings.Contains(output.Content, "Note:") {
		t.Errorf("Content should carry no note for a constraint-free query, got: %q", output.Content)
	}
}

func TestExecuteSearch_RecencyValidation(t *testing.T) {
	m := &mockProvider{id: "mock", available: true, resp: &providers.SearchResponse{Provider: "mock"}}
	chain := &providers.SearchChain{Providers: []providers.SearchProvider{m}}

	_, err := executeSearch(context.Background(), Input{Query: "x", Recency: "hour"}, chain)
	if err == nil {
		t.Fatal("expected an error for an invalid recency value")
	}
	want := `invalid recency "hour" (want day, week, month, or year)`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	m.resp = &providers.SearchResponse{Provider: "mock", Sources: []providers.SearchSource{{Title: "t", URL: "https://t.com"}}}
	if _, err := executeSearch(context.Background(), Input{Query: "x", Recency: "day"}, chain); err != nil {
		t.Fatalf("Recency=day: executeSearch() error = %v", err)
	}
	if m.recorded == nil {
		t.Fatal("provider did not record params")
	}
	if m.recorded.Recency != "day" {
		t.Errorf("params.Recency = %q, want day", m.recorded.Recency)
	}
	if m.recorded.ParsedQuery == nil {
		t.Error("params.ParsedQuery = nil, want the pipeline-parsed query")
	}
	if m.recorded.Query != "x" {
		t.Errorf("params.Query = %q, want the raw query", m.recorded.Query)
	}
	if m.recorded.Limit != defaultLimit {
		t.Errorf("params.Limit = %d, want %d", m.recorded.Limit, defaultLimit)
	}
}

func TestWebSchema_RecencyEnum(t *testing.T) {
	tl := New(nil)
	schema := string(tl.InputSchema())
	for _, want := range []string{`"recency"`, `"day"`, `"week"`, `"month"`, `"year"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema should contain %s, got: %s", want, schema)
		}
	}
}

func TestWebPrompt_MentionsDirectives(t *testing.T) {
	prompt := webPrompt()
	for _, want := range []string{"site:", "after:", "recency", "filetype:"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt should mention %q, got: %q", want, prompt)
		}
	}
}

func TestExecute_FetchModeUnaffectedByRecency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "plain text response")
	}))
	defer server.Close()

	result, err := execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": %q, "recency": "day"}`, server.URL)), nil, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want fetch (URL routing ignores recency)", output.Mode)
	}
}

func TestFormatForLLM_NotesLead(t *testing.T) {
	resp := &providers.SearchResponse{
		Provider: "test",
		Answer:   "The answer.",
		Sources:  []providers.SearchSource{{Title: "S", URL: "https://s.com"}},
	}
	got := formatForLLM(resp, []string{"no results matched `site:x`; the constraint was relaxed"})
	lines := strings.Split(got, "\n")
	if lines[0] != "Note: no results matched `site:x`; the constraint was relaxed" {
		t.Fatalf("first line = %q, want the note line", lines[0])
	}
	if !strings.Contains(got, "The answer.") {
		t.Errorf("answer should follow the note, got: %q", got)
	}
	// No notes: byte-identical to the plain rendering.
	if formatForLLM(resp, nil) != formatForLLM(resp, []string{}) {
		t.Error("nil and empty notes must render identically")
	}
	if strings.Contains(formatForLLM(resp, nil), "Note:") {
		t.Errorf("no-notes rendering must not contain Note:, got: %q", formatForLLM(resp, nil))
	}
}
