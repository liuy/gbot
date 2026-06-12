package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SearchProvider is the interface for search engine backends.
type SearchProvider interface {
	ID() string
	IsAvailable() bool
	Search(ctx context.Context, params SearchParams) (*SearchResponse, error)
}

// SearchChain tries multiple providers in order, falling back on failure.
// Source: omp search/provider.ts — resolveProviderChain + executeSearch.
type SearchChain struct {
	Providers []SearchProvider
}

// Search executes the fallback chain: tries each provider in order.
// Auth errors (401/403) skip to next. Rate limits (429) and server errors
// (500/timeout) fall back. Context cancellation stops immediately.
func (sc *SearchChain) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	if len(sc.Providers) == 0 {
		return nil, fmt.Errorf("no search providers available")
	}

	var failures []string
	var attempted bool
	for _, p := range sc.Providers {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if !p.IsAvailable() {
			continue
		}
		attempted = true

		resp, err := p.Search(ctx, params)
		if err == nil {
			return resp, nil
		}

		var spe *SearchProviderError
		if errors.As(err, &spe) {
			failures = append(failures, spe.Error())
			continue
		}

		failures = append(failures, fmt.Sprintf("%s: %s", p.ID(), err.Error()))
	}

	if !attempted {
		return nil, fmt.Errorf("no search providers available (all %d providers report unavailable)", len(sc.Providers))
	}
	return nil, fmt.Errorf("all search providers failed: %s", strings.Join(failures, "; "))
}

// AvailableProviders returns IDs of currently available providers.
func (sc *SearchChain) AvailableProviders() []string {
	var ids []string
	for _, p := range sc.Providers {
		if p.IsAvailable() {
			ids = append(ids, p.ID())
		}
	}
	return ids
}

// SearchWithProvider uses a specific provider by ID, or falls back to
// Search (auto chain) when provider is "" or "auto".
func (sc *SearchChain) SearchWithProvider(ctx context.Context, params SearchParams, provider string) (*SearchResponse, error) {
	if provider == "" || provider == "auto" {
		return sc.Search(ctx, params)
	}

	for _, p := range sc.Providers {
		if p.ID() == provider {
			if !p.IsAvailable() {
				return nil, fmt.Errorf("provider %q is not available", provider)
			}
			return p.Search(ctx, params)
		}
	}
	return nil, fmt.Errorf("unknown provider %q (available: %s)", provider, strings.Join(sc.AvailableProviders(), ", "))
}
