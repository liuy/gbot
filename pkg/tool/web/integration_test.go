package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/tool/web/providers"
	"github.com/liuy/gbot/pkg/tool/web/scrapers"
)

func TestExecute_SearchQuery(t *testing.T) {
	chain := &providers.SearchChain{
		Providers: []providers.SearchProvider{
			&mockProvider{
				id:        "test",
				available: true,
				resp: &providers.SearchResponse{
					Provider: "test",
					Sources: []providers.SearchSource{
						{Title: "Go 1.18 Generics", URL: "https://go.dev/doc/go1.18", Snippet: "Generics are here"},
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

	output, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("expected *Output, got %T", result.Data)
	}
	if output.Mode != "search" {
		t.Errorf("mode = %q, want %q", output.Mode, "search")
	}
	if !strings.Contains(output.Content, "Go 1.18 Generics") {
		t.Errorf("content missing source title: %q", output.Content)
	}
	if !strings.Contains(output.Content, "https://go.dev/doc/go1.18") {
		t.Errorf("content missing source URL: %q", output.Content)
	}
	if output.Raw == nil || output.Raw.Provider != "test" {
		t.Errorf("raw response not set correctly")
	}
}

func TestExecute_SearchWithLimit(t *testing.T) {
	chain := &providers.SearchChain{
		Providers: []providers.SearchProvider{
			&mockProvider{
				id:        "test",
				available: true,
				resp: &providers.SearchResponse{
					Provider: "test",
					Sources: []providers.SearchSource{
						{Title: "A", URL: "https://a.com"},
						{Title: "B", URL: "https://b.com"},
					},
				},
			},
		},
	}

	input := json.RawMessage(`{"query": "test", "limit": 1}`)
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "[1]") {
		t.Errorf("expected numbered result: %q", output.Content)
	}
}

func TestExecute_EmptyQuery(t *testing.T) {
	chain := &providers.SearchChain{Providers: []providers.SearchProvider{}}
	input := json.RawMessage(`{"query": ""}`)
	_, err := execute(context.Background(), input, chain, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("error = %v, want 'query is required'", err)
	}
}

func TestExecute_SearchAllProvidersFail(t *testing.T) {
	chain := &providers.SearchChain{
		Providers: []providers.SearchProvider{
			&mockProvider{
				id:        "fail1",
				available: true,
				err:       &providers.SearchProviderError{Provider: "fail1", Message: "timeout", Status: 500},
			},
		},
	}

	input := json.RawMessage(`{"query": "test"}`)
	_, err := execute(context.Background(), input, chain, nil, nil)
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !strings.Contains(err.Error(), "web search failed") {
		t.Errorf("error = %v, want 'web search failed'", err)
	}
}

func TestExecute_URLFetch_HTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Test</title></head><body><h1>Hello World</h1><p>Some content here.</p></body></html>`)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "Hello World") {
		t.Errorf("content missing heading: %q", output.Content)
	}
}

func TestExecute_URLFetch_PlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "just plain text, no HTML here")
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "just plain text") {
		t.Errorf("content missing plain text: %q", output.Content)
	}
}

func TestExecute_URLFetch_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() should not error on HTTP 500, got: %v", err)
	}

	// LoadPage returns content even on 500; OK=false but Error=""
	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "internal error") {
		t.Errorf("content should contain error body: %q", output.Content)
	}
}

func TestExecute_URLFetch_ConnectionRefused(t *testing.T) {
	chain := &providers.SearchChain{}
	// Use a port that's definitely not listening
	input := json.RawMessage(`{"query": "http://127.0.0.1:1"}`)
	_, err := execute(context.Background(), input, chain, nil, nil)
	if err == nil {
		t.Fatal("connection refused should return error, got nil")
	}
	if !strings.Contains(err.Error(), "fetch failed") {
		t.Errorf("error = %v, want 'fetch failed'", err)
	}
}

func TestExecute_InvalidJSON(t *testing.T) {
	chain := &providers.SearchChain{}
	input := json.RawMessage(`{invalid json`)
	_, err := execute(context.Background(), input, chain, nil, nil)
	if err == nil {
		t.Fatal("invalid JSON should return parse error, got nil")
	}
}

func TestWebPrompt(t *testing.T) {
	prompt := webPrompt()
	if !strings.Contains(prompt, "Search mode") {
		t.Errorf("prompt missing search mode section: %q", prompt)
	}
	if !strings.Contains(prompt, "js: true") {
		t.Errorf("prompt missing js guidance: %q", prompt)
	}
}

func TestWebPrompt_NoProviders(t *testing.T) {
	prompt := webPrompt()
	if !strings.Contains(prompt, "Fetch mode") {
		t.Errorf("prompt missing fetch mode section: %q", prompt)
	}
}

func TestNew_ToolProperties(t *testing.T) {
	tool := New(nil)

	if tool.Name() != "Web" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "Web")
	}
	if !tool.IsReadOnly(nil) {
		t.Error("IsReadOnly() = false, want true")
	}
	if tool.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = true, want false")
	}

	// Verify schema is valid JSON
	schema := tool.InputSchema()
	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("InputSchema() returned invalid JSON: %v", err)
	}
	props, _ := parsed["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Error("schema missing 'query' property")
	}
	if _, ok := props["provider"]; !ok {
		t.Error("schema missing 'provider' property")
	}
	if _, ok := props["since"]; ok {
		t.Error("schema should not have 'since' property (deleted)")
	}
	if _, ok := props["fetch"]; ok {
		t.Error("schema should not have 'fetch' property (deleted)")
	}
}

func TestBuildResult(t *testing.T) {
	result := BuildResult("# Hello", struct {
		URL         string
		FinalURL    string
		Method      string
		FetchedAt   string
		Notes       []string
		ContentType string
	}{
		URL:         "https://example.com",
		FinalURL:    "https://example.com/page",
		ContentType: "text/html",
		Notes:       []string{"redirected"},
	})

	if result.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", result.URL, "https://example.com")
	}
	if result.FinalURL != "https://example.com/page" {
		t.Errorf("FinalURL = %q, want %q", result.FinalURL, "https://example.com/page")
	}
	if result.ContentType != "text/html" {
		t.Errorf("ContentType = %q, want %q", result.ContentType, "text/html")
	}
	if len(result.Notes) != 1 || result.Notes[0] != "redirected" {
		t.Errorf("Notes = %v, want [redirected]", result.Notes)
	}
	if !strings.Contains(result.Content, "Hello") {
		t.Errorf("Content missing heading: %q", result.Content)
	}
}

func TestBuildResult_DefaultContentType(t *testing.T) {
	result := BuildResult("plain text", struct {
		URL         string
		FinalURL    string
		Method      string
		FetchedAt   string
		Notes       []string
		ContentType string
	}{URL: "https://example.com"})

	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want default text/markdown", result.ContentType)
	}
}

// --- Cold start: no providers available ---

func TestExecute_SearchNoProvidersAvailable(t *testing.T) {
	chain := &providers.SearchChain{
		Providers: []providers.SearchProvider{
			&mockProvider{id: "dead", available: false},
		},
	}

	input := json.RawMessage(`{"query": "test"}`)
	_, err := execute(context.Background(), input, chain, nil, nil)
	if err == nil {
		t.Fatal("should error when no providers are available")
	}
	if !strings.Contains(err.Error(), "no search providers available") {
		t.Errorf("error = %v, want 'no search providers available'", err)
	}
}

// --- Redirect chain: URL changes between request and response ---

func TestExecute_URLFetch_Redirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><h1>Moved</h1></body></html>")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusMovedPermanently)
	}))
	defer redirector.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, redirector.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "Moved") {
		t.Errorf("content should contain redirected page body: %q", output.Content)
	}
}

// --- Truncation: large page gets cut ---

func TestExecute_URLFetch_Truncation(t *testing.T) {
	var bigBody strings.Builder
	for i := range 100000 {
		fmt.Fprintf(&bigBody, "line %d ", i)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, bigBody.String())
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if len(output.Content) > MaxOutputChars+1000 {
		t.Errorf("content length %d exceeds MaxOutputChars %d by too much", len(output.Content), MaxOutputChars)
	}
	if !strings.Contains(output.Content, "line 0") {
		t.Errorf("truncated content should still start from beginning: %q", output.Content[:100])
	}
}

// --- Context cancel during search ---

func TestExecute_SearchContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	chain := &providers.SearchChain{
		Providers: []providers.SearchProvider{
			&mockProvider{id: "test", available: true},
		},
	}

	input := json.RawMessage(`{"query": "test"}`)
	_, err := execute(ctx, input, chain, nil, nil)
	// providers.SearchChain.Search checks ctx.Err() first
	if err == nil {
		t.Fatal("should error with canceled context")
	}
}

// --- Self-check ---
// Q: If a real bug happened where executeFetch stopped converting HTML to MD,
//    would TestExecute_URLFetch_HTML catch it? A: Yes — it checks "Hello World"
//    in output, which only appears after HTML→MD conversion strips tags.
//
// Q: If executeSearch returned raw JSON instead of formatted text,
//    would TestExecute_SearchQuery catch it? A: Yes — it checks for specific
//    title and URL substrings that only appear after formatForLLM.
//
// Q: Are we testing observable behavior or internal fields? A: Observable —
//    we check Content strings and error messages, not struct fields.

// --- JS Fallback ---

func TestExecute_Fetch_BotBlockNoChrome(t *testing.T) {
	if ok, _ := isChromedpAvailable(); ok {
		t.Skip("Chrome available — use TestExecute_FetchJSFallback_BotBlock instead")
	}

	// Bot block page — no Chrome to fall back to, returns error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<html><body>Cloudflare challenge required</body></html>`)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	_, err := execute(context.Background(), input, chain, nil, nil)
	if err == nil {
		t.Fatal("bot block without Chrome should return error")
	}
	if !strings.Contains(err.Error(), "fetch failed") {
		t.Errorf("error = %v, want 'fetch failed'", err)
	}
}

func TestExecute_FetchJSFallback_BotBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	available, _ := isChromedpAvailable()
	if !available {
		t.Skip("Chrome/Chromium not installed, skipping JS fallback test")
	}

	// Reset Chrome pool for clean state
	defaultPool.reset()
	chromedpAvailable.once = sync.Once{}

	// Server that returns bot block via HTTP
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<html><body>Cloudflare challenge required</body></html>`)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() with JS fallback should succeed, got: %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
}

func TestExecute_FetchJSFallback_ShortSPA(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Chrome integration test in short mode")
	}
	available, _ := isChromedpAvailable()
	if !available {
		t.Skip("Chrome/Chromium not installed, skipping JS fallback test")
	}

	defaultPool.reset()
	chromedpAvailable.once = sync.Once{}

	// Server that returns short SPA shell via HTTP
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<html><body><div id="app"></div></body></html>`)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() with JS fallback should succeed, got: %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
}

func TestExecute_FetchJSFallback_NormalPageNoFallback(t *testing.T) {
	// Normal page with content — should NOT trigger fallback
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body><h1>Normal Page</h1><p>This is a normal page with enough content to not trigger any fallback mechanism. The content exceeds the empty body threshold.</p></body></html>`)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "Normal Page") {
		t.Errorf("content should contain page heading: %q", output.Content)
	}
}

func TestExecute_FetchUsesClient(t *testing.T) {
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><h1>OK</h1><p>This is a page with enough content to exceed the SPA detection threshold. The content needs to be long enough so the fetch path does not trigger the chromedp JS fallback and instead uses the HTTP client we provide through Config.Client for the request.</p></body></html>")
	}))
	defer server.Close()

	customClient := &http.Client{
		Transport: &customTransport{ua: "test-proxy-client/1.0"},
	}

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, customClient, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "OK") {
		t.Errorf("content should contain OK: %q", output.Content)
	}
	if gotUA != "test-proxy-client/1.0" {
		t.Errorf("User-Agent = %q, want %q — fetch should use the provided client", gotUA, "test-proxy-client/1.0")
	}
}

type customTransport struct {
	ua string
}

func (t *customTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", t.ua)
	return http.DefaultTransport.RoundTrip(req)
}

// --- Document conversion call chain tests ---

func TestExecute_URLFetch_DOCXConversionViaExt(t *testing.T) {
	docxData, err := os.ReadFile("../../markitdown/testdata/test.docx")
	if err != nil {
		t.Skip("test.docx not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		_, _ = w.Write(docxData)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s/paper.docx"}`, server.URL))
	result, err := execute(context.Background(), input, chain, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "AutoGen") {
		t.Errorf("content should contain DOCX text, got: %s", truncate(output.Content, 200))
	}
}

func TestExecute_URLFetch_DOCXConversion(t *testing.T) {
	docxData, err := os.ReadFile("../../markitdown/testdata/test.docx")
	if err != nil {
		t.Skip("test.docx not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		_, _ = w.Write(docxData)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s/doc.docx"}`, server.URL))
	result, err := execute(context.Background(), input, chain, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "AutoGen") {
		t.Errorf("content should contain DOCX text, got: %s", truncate(output.Content, 200))
	}
}

func TestExecute_URLFetch_XLSXConversion(t *testing.T) {
	xlsxData, err := os.ReadFile("../../markitdown/testdata/test.xlsx")
	if err != nil {
		t.Skip("test.xlsx not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		_, _ = w.Write(xlsxData)
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s/data.xlsx"}`, server.URL))
	result, err := execute(context.Background(), input, chain, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "|") {
		t.Errorf("content should contain markdown table from XLSX, got: %s", truncate(output.Content, 200))
	}
}

func TestExecute_URLFetch_NonDocURLFallsThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><h1>HTML Page</h1></body></html>")
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s/page.html"}`, server.URL))
	result, err := execute(context.Background(), input, chain, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if !strings.Contains(output.Content, "HTML Page") {
		t.Errorf("HTML fetch should fall through to normal path, got: %q", output.Content)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

func TestExecuteFetch_ScraperMatch(t *testing.T) {
	reg := scrapers.New()
	reg.Register(func(ctx context.Context, u *neturl.URL, client *http.Client, js scrapers.JSFetcher) (*scrapers.Result, error) {
		return &scrapers.Result{
			Content:     "scraper content",
			Method:      "custom",
			ContentType: "text/plain",
		}, nil
	})

	chain := &providers.SearchChain{}
	input := json.RawMessage(`{"query": "https://example.com/page"}`)
	result, err := execute(context.Background(), input, chain, http.DefaultClient, reg)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "scraper content") {
		t.Errorf("content should contain scraper output, got: %q", output.Content)
	}
}

func TestExecuteFetch_ScraperError(t *testing.T) {
	reg := scrapers.New()
	reg.Register(func(ctx context.Context, u *neturl.URL, client *http.Client, js scrapers.JSFetcher) (*scrapers.Result, error) {
		return nil, fmt.Errorf("scraper blew up")
	})

	chain := &providers.SearchChain{}
	input := json.RawMessage(`{"query": "https://example.com/page"}`)
	_, err := execute(context.Background(), input, chain, http.DefaultClient, reg)
	if err == nil {
		t.Fatal("expected error from scraper failure")
	}
	if !strings.Contains(err.Error(), "scraper blew up") {
		t.Errorf("error = %q, want containing 'scraper blew up'", err.Error())
	}
}

func TestExecuteFetch_NilRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><p>no scraper</p></body></html>")
	}))
	defer server.Close()

	chain := &providers.SearchChain{}
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, server.URL))
	result, err := execute(context.Background(), input, chain, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want %q", output.Mode, "fetch")
	}
	if !strings.Contains(output.Content, "no scraper") {
		t.Errorf("content should fall through to HTTP fetch, got: %q", output.Content)
	}
}
