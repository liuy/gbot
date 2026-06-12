package providers

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type mockProvider struct {
	id        string
	available bool
	resp      *SearchResponse
	err       error
}

func (m *mockProvider) ID() string        { return m.id }
func (m *mockProvider) IsAvailable() bool { return m.available }
func (m *mockProvider) Search(_ context.Context, _ SearchParams) (*SearchResponse, error) {
	return m.resp, m.err
}

func TestSearchChain_AllFail(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true, err: &SearchProviderError{Provider: "p1", Message: "fail", Status: 500}},
			&mockProvider{id: "p2", available: true, err: &SearchProviderError{Provider: "p2", Message: "fail", Status: 500}},
		},
	}
	_, err := chain.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !strings.Contains(err.Error(), "all search providers failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSearchChain_FallbackOnAuthError(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true, err: &SearchProviderError{Provider: "p1", Message: "unauthorized", Status: 401}},
			&mockProvider{id: "p2", available: true, resp: &SearchResponse{Provider: "p2", Sources: []SearchSource{{Title: "found"}}}},
		},
	}
	resp, err := chain.Search(context.Background(), SearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Provider != "p2" {
		t.Errorf("expected fallback to p2, got %s", resp.Provider)
	}
}

func TestSearchChain_FallbackOnRateLimit(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true, err: &SearchProviderError{Provider: "p1", Message: "rate limited", Status: 429}},
			&mockProvider{id: "p2", available: true, resp: &SearchResponse{Provider: "p2", Sources: []SearchSource{{Title: "found"}}}},
		},
	}
	resp, err := chain.Search(context.Background(), SearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Provider != "p2" {
		t.Errorf("expected fallback to p2, got %s", resp.Provider)
	}
}

func TestSearchChain_FirstSuccess(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true, resp: &SearchResponse{Provider: "p1", Sources: []SearchSource{{Title: "first"}}}},
			&mockProvider{id: "p2", available: true, resp: &SearchResponse{Provider: "p2"}},
		},
	}
	resp, err := chain.Search(context.Background(), SearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Provider != "p1" {
		t.Errorf("expected p1, got %s", resp.Provider)
	}
}

func TestSearchChain_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true, err: fmt.Errorf("should not reach")},
		},
	}
	_, err := chain.Search(ctx, SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("search should fail with cancelled context")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", ctx.Err())
	}
}

func TestSearchChain_NoProviders(t *testing.T) {
	chain := &SearchChain{}
	_, err := chain.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected error with no providers")
	}
	if !strings.Contains(err.Error(), "no search providers") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSearchChain_AllUnavailable(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: false},
			&mockProvider{id: "p2", available: false},
		},
	}
	_, err := chain.Search(context.Background(), SearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected error when all providers unavailable")
	}
	if !strings.Contains(err.Error(), "all 2 providers report unavailable") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSearchProviderError_Classification(t *testing.T) {
	spe := &SearchProviderError{Provider: "test", Message: "fail", Status: 401}
	if !spe.IsAuthError() {
		t.Error("401 should be auth error")
	}
	if spe.IsRateLimit() {
		t.Error("401 should not be rate limit")
	}

	spe.Status = 403
	if !spe.IsAuthError() {
		t.Error("403 should be auth error")
	}

	spe.Status = 429
	if !spe.IsRateLimit() {
		t.Error("429 should be rate limit")
	}
	if spe.IsAuthError() {
		t.Error("429 should not be auth error")
	}
}

func TestSearchChain_SkipsUnavailable(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: false},
			&mockProvider{id: "p2", available: true, resp: &SearchResponse{Provider: "p2", Sources: []SearchSource{{Title: "found"}}}},
		},
	}
	resp, err := chain.Search(context.Background(), SearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Provider != "p2" {
		t.Errorf("expected p2 (p1 unavailable), got %s", resp.Provider)
	}
}

func TestSearchChain_AvailableProviders(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "zhipu", available: true},
			&mockProvider{id: "duckduckgo", available: false},
		},
	}
	avail := chain.AvailableProviders()
	if len(avail) != 1 || avail[0] != "zhipu" {
		t.Errorf("expected [zhipu], got %v", avail)
	}
}

func TestSearchWithProvider_Auto(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true, resp: &SearchResponse{Provider: "p1"}},
		},
	}
	for _, provider := range []string{"", "auto"} {
		resp, err := chain.SearchWithProvider(context.Background(), SearchParams{Query: "test"}, provider)
		if err != nil {
			t.Fatalf("provider=%q: %v", provider, err)
		}
		if resp.Provider != "p1" {
			t.Errorf("provider=%q: expected p1, got %s", provider, resp.Provider)
		}
	}
}

func TestSearchWithProvider_Specific(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true, resp: &SearchResponse{Provider: "p1"}},
			&mockProvider{id: "p2", available: true, resp: &SearchResponse{Provider: "p2"}},
		},
	}
	resp, err := chain.SearchWithProvider(context.Background(), SearchParams{Query: "test"}, "p2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Provider != "p2" {
		t.Errorf("expected p2, got %s", resp.Provider)
	}
}

func TestSearchWithProvider_Unknown(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: true},
		},
	}
	_, err := chain.SearchWithProvider(context.Background(), SearchParams{Query: "test"}, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") || !strings.Contains(err.Error(), "available:") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSearchWithProvider_Unavailable(t *testing.T) {
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "p1", available: false},
		},
	}
	_, err := chain.SearchWithProvider(context.Background(), SearchParams{Query: "test"}, "p1")
	if err == nil {
		t.Fatal("expected error for unavailable provider")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSearchChain_AvailableProviders_ZhipuNoKey(t *testing.T) {
	// Zhipu without key is unavailable, DDG always available
	chain := &SearchChain{
		Providers: []SearchProvider{
			&mockProvider{id: "zhipu", available: false},
			&mockProvider{id: "duckduckgo", available: true, resp: &SearchResponse{Provider: "duckduckgo"}},
		},
	}
	avail := chain.AvailableProviders()
	if len(avail) != 1 || avail[0] != "duckduckgo" {
		t.Fatalf("expected only [duckduckgo], got %v", avail)
	}

	// auto should skip zhipu and use duckduckgo
	resp, err := chain.SearchWithProvider(context.Background(), SearchParams{Query: "test"}, "")
	if err != nil {
		t.Fatalf("auto search: %v", err)
	}
	if resp.Provider != "duckduckgo" {
		t.Errorf("auto should fallback to duckduckgo, got %s", resp.Provider)
	}
}
