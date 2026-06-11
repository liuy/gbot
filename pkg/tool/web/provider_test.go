package web

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
