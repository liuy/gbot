package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func zhipuSSEResponse(t *testing.T, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal SSE payload: %v", err)
	}
	return fmt.Sprintf("id:1\nevent:message\ndata:%s\n", string(data))
}

func zhipuSearchResults(t *testing.T, results []zhipuSearchResult) string {
	t.Helper()
	inner, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("marshal results: %v", err)
	}
	escaped, err := json.Marshal(string(inner))
	if err != nil {
		t.Fatalf("marshal escaped: %v", err)
	}
	return string(escaped)
}

func TestZhipuIsAvailable(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"empty key", "", false},
		{"no dot", "justakey123", false},
		{"valid id.secret format", "abc123.secret456", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			z := &ZhipuProvider{APIKey: tt.key}
			if got := z.IsAvailable(); got != tt.want {
				t.Errorf("IsAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestZhipuSearchSuccess(t *testing.T) {
	results := []zhipuSearchResult{
		{Title: "Go Generics", Link: "https://go.dev/blog/generics", Content: "An introduction to generics"},
		{Title: "GORM Generics", Link: "https://gorm.io/generics", Content: "GORM generics support"},
	}
	textContent := zhipuSearchResults(t, results)
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test-session",
		"result": map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": textContent},
			},
		},
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test.key" {
			t.Errorf("expected Authorization 'Bearer test.key', got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Mcp-Session-Id") == "" {
			t.Error("expected Mcp-Session-Id header")
		}
		w.Header().Set("Content-Type", "text/event-stream;charset=UTF-8")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	origURL := zhipuMCPURL
	zhipuMCPURL = server.URL
	defer func() { zhipuMCPURL = origURL }()

	z := &ZhipuProvider{Client: server.Client(), APIKey: "test.key"}
	resp, err := z.Search(context.Background(), SearchParams{Query: "golang generics", Limit: 10})
	if err != nil {
		t.Fatalf("parseZhipuResponse error: %v", err)
	}

	if resp.Provider != "zhipu" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "zhipu")
	}
	if len(resp.Sources) != 2 {
		t.Fatalf("len(Sources) = %d, want 2", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Go Generics" {
		t.Errorf("Sources[0].Title = %q, want %q", resp.Sources[0].Title, "Go Generics")
	}
	if resp.Sources[0].URL != "https://go.dev/blog/generics" {
		t.Errorf("Sources[0].URL = %q, want %q", resp.Sources[0].URL, "https://go.dev/blog/generics")
	}
	if resp.Sources[0].Snippet != "An introduction to generics" {
		t.Errorf("Sources[0].Snippet = %q, want %q", resp.Sources[0].Snippet, "An introduction to generics")
	}
	if resp.Sources[1].Title != "GORM Generics" {
		t.Errorf("Sources[1].Title = %q, want %q", resp.Sources[1].Title, "GORM Generics")
	}
}

func TestZhipuSearchMCPError401(t *testing.T) {
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
		"result": map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "MCP error -401: Api key not found"},
			},
			"isError": true,
		},
	})

	resp, err := parseZhipuResponse(strings.NewReader(sseBody), 10)
	if err == nil {
		t.Fatalf("expected error for 401 MCP error, got nil")
	}

	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 401 {
		t.Errorf("Status = %d, want 401", spe.Status)
	}
	if !spe.IsAuthError() {
		t.Error("expected IsAuthError() = true")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

func TestZhipuSearchMCPError429(t *testing.T) {
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
		"result": map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": `MCP error -429: {"error":{"code":"1310","message":"Weekly/Monthly Limit Exhausted"}}`},
			},
			"isError": true,
		},
	})

	resp, err := parseZhipuResponse(strings.NewReader(sseBody), 10)
	if err == nil {
		t.Fatalf("expected error for 429 MCP error, got nil")
	}

	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 429 {
		t.Errorf("Status = %d, want 429", spe.Status)
	}
	if !spe.IsRateLimit() {
		t.Error("expected IsRateLimit() = true")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

func TestZhipuSearchJSONRPCError(t *testing.T) {
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
		"error": map[string]any{
			"code":    -32603,
			"message": "Tool not found: bad_tool",
		},
	})

	_, err := parseZhipuResponse(strings.NewReader(sseBody), 10)
	if err == nil {
		t.Fatalf("expected error for JSON-RPC error, got nil")
	}

	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	// -32603 is a JSON-RPC error code, not HTTP → maps to 500
	if spe.Status != 500 {
		t.Errorf("Status = %d, want 500 (non-HTTP JSON-RPC code maps to 500)", spe.Status)
	}
}

func TestZhipuSearchEmptySSE(t *testing.T) {
	_, err := parseZhipuResponse(strings.NewReader("id:1\nevent:message\n"), 10)
	if err == nil {
		t.Fatalf("expected error for empty SSE, got nil")
	}

	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 500 {
		t.Errorf("Status = %d, want 500", spe.Status)
	}
}

func TestZhipuSearchMissingResult(t *testing.T) {
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
	})

	_, err := parseZhipuResponse(strings.NewReader(sseBody), 10)
	if err == nil {
		t.Fatalf("expected error for missing result, got nil")
	}

	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if !strings.Contains(spe.Message, "missing result") {
		t.Errorf("Message = %q, want to contain 'missing result'", spe.Message)
	}
}

func TestZhipuSearchLimit(t *testing.T) {
	results := make([]zhipuSearchResult, 5)
	for i := range results {
		results[i] = zhipuSearchResult{
			Title:   fmt.Sprintf("Result %d", i+1),
			Link:    fmt.Sprintf("https://example.com/%d", i+1),
			Content: fmt.Sprintf("Snippet %d", i+1),
		}
	}
	textContent := zhipuSearchResults(t, results)
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
		"result": map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": textContent},
			},
		},
	})

	resp, err := parseZhipuResponse(strings.NewReader(sseBody), 3)
	if err != nil {
		t.Fatalf("parseZhipuResponse error: %v", err)
	}
	if len(resp.Sources) != 3 {
		t.Errorf("len(Sources) = %d, want 3 (limited)", len(resp.Sources))
	}
}

func TestZhipuSearchHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway"))
	}))
	defer server.Close()

	// Test via the actual Search method with a custom URL
	origURL := zhipuMCPURL
	zhipuMCPURL = server.URL
	defer func() { zhipuMCPURL = origURL }()

	z := &ZhipuProvider{Client: server.Client(), APIKey: "test.key"}
	_, err := z.Search(context.Background(), SearchParams{Query: "test", Limit: 3})
	if err == nil {
		t.Fatalf("expected error for HTTP 502, got nil")
	}

	var spe *SearchProviderError
	if !errors.As(err, &spe) {
		t.Fatalf("expected SearchProviderError, got %T: %v", err, err)
	}
	if spe.Status != 502 {
		t.Errorf("Status = %d, want 502", spe.Status)
	}
}

func TestZhipuSearchContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	z := &ZhipuProvider{Client: http.DefaultClient, APIKey: "test.key"}
	_, err := z.Search(ctx, SearchParams{Query: "test", Limit: 3})
	if err == nil {
		t.Fatalf("expected error for cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") && !strings.Contains(err.Error(), "context deadline") {
		t.Errorf("error = %v, want context cancellation error", err)
	}
}

func TestZhipuSearchSkipEmptyLink(t *testing.T) {
	results := []zhipuSearchResult{
		{Title: "No Link", Link: "", Content: "Missing link"},
		{Title: "Has Link", Link: "https://example.com", Content: "Valid"},
	}
	textContent := zhipuSearchResults(t, results)
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
		"result": map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": textContent},
			},
		},
	})

	resp, err := parseZhipuResponse(strings.NewReader(sseBody), 10)
	if err != nil {
		t.Fatalf("parseZhipuResponse error: %v", err)
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("len(Sources) = %d, want 1 (empty link skipped)", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Has Link" {
		t.Errorf("Sources[0].Title = %q, want %q", resp.Sources[0].Title, "Has Link")
	}
}

func TestExtractMCPError(t *testing.T) {
	tests := []struct {
		input    string
		wantCode int
		wantMsg  string
	}{
		{"MCP error -401: Api key not found", -401, "Api key not found"},
		{"MCP error -429: limit exceeded", -429, "limit exceeded"},
		{"not an MCP error", 0, ""},
		{"MCP error nocode", 0, "MCP error nocode"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			code, msg := extractMCPError(tt.input)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if msg != tt.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}

func TestZhipuID(t *testing.T) {
	z := &ZhipuProvider{APIKey: "test.key"}
	if got := z.ID(); got != "zhipu" {
		t.Errorf("ID() = %q, want %q", got, "zhipu")
	}
}

func TestZhipuClientFallback(t *testing.T) {
	z := &ZhipuProvider{Client: nil, APIKey: "test.key"}
	if z.client() != http.DefaultClient {
		t.Error("expected http.DefaultClient when Client is nil")
	}
}

func TestZhipuSearchRawTextFallback(t *testing.T) {
	// When text is a raw JSON array (not double-encoded), parseZhipuResponse should still work.
	rawResults := `[{"title":"Raw Result","link":"https://example.com","content":"direct json","refer":"ref_1"}]`
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
		"result": map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": rawResults},
			},
		},
	})

	resp, err := parseZhipuResponse(strings.NewReader(sseBody), 10)
	if err != nil {
		t.Fatalf("parseZhipuResponse error: %v", err)
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("len(Sources) = %d, want 1", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Raw Result" {
		t.Errorf("Sources[0].Title = %q, want %q", resp.Sources[0].Title, "Raw Result")
	}
}

func TestZhipuSearchEmptyResults(t *testing.T) {
	sseBody := zhipuSSEResponse(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "test",
		"result": map[string]any{
			"content": []any{},
		},
	})

	resp, err := parseZhipuResponse(strings.NewReader(sseBody), 10)
	if err != nil {
		t.Fatalf("parseZhipuResponse error: %v", err)
	}
	if len(resp.Sources) != 0 {
		t.Errorf("len(Sources) = %d, want 0", len(resp.Sources))
	}
}
