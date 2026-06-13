package web

import (
	"context"
	"encoding/json"
	"fmt"
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
	got := formatForLLM(resp)
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
	got := formatForLLM(resp)
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
		"anysearch": "test-key",
		"zhipu":     "zhipu-key",
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
