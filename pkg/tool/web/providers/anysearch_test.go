package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnySearch_ID(t *testing.T) {
	a := &AnySearchProvider{}
	if got := a.ID(); got != "anysearch" {
		t.Errorf("ID() = %q, want %q", got, "anysearch")
	}
}

func TestAnySearch_IsAvailable(t *testing.T) {
	a := &AnySearchProvider{}
	if !a.IsAvailable() {
		t.Error("IsAvailable() should always be true (anonymous mode)")
	}
}

func TestAnySearch_APIKeyPriority(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		envKey   string
		expected string
	}{
		{"struct key wins", "struct-key", "env-key", "struct-key"},
		{"env fallback", "", "env-key", "env-key"},
		{"no key (anonymous)", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envKey != "" {
				t.Setenv("ANYSEARCH_API_KEY", tt.envKey)
			}
			a := &AnySearchProvider{APIKey: tt.apiKey}
			if got := a.apiKey(); got != tt.expected {
				t.Errorf("apiKey() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestAnySearch_Search_Anonymous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("anonymous request should not have Authorization header")
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["query"] != "test query" {
			t.Errorf("query = %v, want %q", body["query"], "test query")
		}

		resp := `{"code":0,"message":"success","data":{"results":[{"title":"Test Result","url":"https://example.com","snippet":"test snippet","content":"full content here"}],"metadata":{"request_id":"req_123","total_results":1,"search_time_ms":100}}}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	a := &AnySearchProvider{Client: server.Client()}
	// Override URL for test.
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	resp, err := a.Search(context.Background(), SearchParams{Query: "test query", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if resp.Provider != "anysearch" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "anysearch")
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("len(Sources) = %d, want 1", len(resp.Sources))
	}
	if resp.Sources[0].Title != "Test Result" {
		t.Errorf("Title = %q, want %q", resp.Sources[0].Title, "Test Result")
	}
	if resp.Sources[0].URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", resp.Sources[0].URL, "https://example.com")
	}
	if resp.Answer != "full content here" {
		t.Errorf("Answer = %q, want %q", resp.Answer, "full content here")
	}
}

func TestAnySearch_Search_WithAPIKey(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		resp := `{"code":0,"message":"success","data":{"results":[],"metadata":{"request_id":"req_456","total_results":0,"search_time_ms":50}}}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	a := &AnySearchProvider{Client: server.Client(), APIKey: "my-secret-key"}
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	_, err := a.Search(context.Background(), SearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if gotAuth != "Bearer my-secret-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer my-secret-key")
	}
}

func TestAnySearch_Search_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limit exceeded`))
	}))
	defer server.Close()

	a := &AnySearchProvider{Client: server.Client()}
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	_, err := a.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("should fail on 429")
	}
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Status != 429 {
		t.Errorf("Status = %d, want 429", spe.Status)
	}
	if spe.Provider != "anysearch" {
		t.Errorf("Provider = %q, want %q", spe.Provider, "anysearch")
	}
}

func TestAnySearch_Search_NonZeroCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":40202,"message":"quota exhausted","data":{}}`))
	}))
	defer server.Close()

	a := &AnySearchProvider{Client: server.Client()}
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	_, err := a.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("should fail on non-zero code")
	}
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Message != "quota exhausted" {
		t.Errorf("Message = %q, want %q", spe.Message, "quota exhausted")
	}
}

func TestAnySearch_Search_EmptyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"code":0,"message":"success","data":{"results":[],"metadata":{"request_id":"req_789","total_results":0,"search_time_ms":50}}}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	a := &AnySearchProvider{Client: server.Client()}
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	resp, err := a.Search(context.Background(), SearchParams{Query: "obscure query"})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(resp.Sources) != 0 {
		t.Errorf("len(Sources) = %d, want 0", len(resp.Sources))
	}
	if resp.Answer != "" {
		t.Errorf("Answer = %q, want empty", resp.Answer)
	}
}

func TestAnySearch_client_Default(t *testing.T) {
	a := &AnySearchProvider{}
	if a.client() != http.DefaultClient {
		t.Fatal("expected DefaultClient when Client is nil")
	}
}

func TestAnySearch_Search_RequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // Force the connection to fail.

	a := &AnySearchProvider{Client: server.Client()}
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	_, err := a.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("should fail when server is closed")
	}
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "anysearch" {
		t.Errorf("Provider = %q, want %q", spe.Provider, "anysearch")
	}
	if spe.Status != 0 {
		t.Errorf("Status = %d, want 0 (request failed)", spe.Status)
	}
	if !strings.Contains(spe.Message, "request failed") {
		t.Errorf("Message = %q, want to contain 'request failed'", spe.Message)
	}
}

func TestAnySearch_Search_ReadBodyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write headers then immediately close the connection mid-stream to break ReadAll.
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if hijacker, ok := w.(http.Hijacker); ok {
			conn, _, _ := hijacker.Hijack()
			_ = conn.Close()
		}
	}))
	defer server.Close()

	a := &AnySearchProvider{Client: server.Client()}
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	_, err := a.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("should fail when response body read fails")
	}
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "anysearch" {
		t.Errorf("Provider = %q, want %q", spe.Provider, "anysearch")
	}
	if !strings.Contains(spe.Message, "read response") {
		t.Errorf("Message = %q, want to contain 'read response'", spe.Message)
	}
}

func TestAnySearch_Search_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))
	defer server.Close()

	a := &AnySearchProvider{Client: server.Client()}
	oldURL := anySearchAPIURL
	anySearchAPIURL = server.URL
	defer func() { anySearchAPIURL = oldURL }()

	_, err := a.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %v, want to contain 'parse response'", err)
	}
}

func TestAnySearch_Search_BuildRequestError(t *testing.T) {
	// Invalid URL triggers NewRequestWithContext failure.
	a := &AnySearchProvider{Client: http.DefaultClient}
	oldURL := anySearchAPIURL
	anySearchAPIURL = "http://[::1]:namedport" // invalid host
	defer func() { anySearchAPIURL = oldURL }()

	_, err := a.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
	if !strings.Contains(err.Error(), "build request") {
		t.Errorf("error = %v, want to contain 'build request'", err)
	}
}
