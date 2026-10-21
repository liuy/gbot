package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type SearchParams struct {
	Query string
	// ParsedQuery is the structured view of Query, parsed once by the search
	// pipeline: Google-style directives (site:, before:/after:, inurl:,
	// intitle:, filetype:, quoted phrases, OR groups, -exclusions) extracted
	// into fields.
	//
	// Providers should map constraints onto native API parameters
	// (domain/date filters) or engine query syntax (FormatQuery) where the
	// upstream supports them, and lean lenient otherwise: the pipeline
	// post-filters every response with ApplyQueryConstraints, which relaxes
	// any constraint that would eliminate all results — so a best-effort
	// search always beats an empty one. When absent (direct provider calls),
	// parse with Parsed().
	ParsedQuery *StructuredQuery
	Limit       int
	// Recency narrows results to a time window: day, week, month, or year
	// ("" = unset). It is a pure time filter — providers without time support
	// must ignore it silently, never approximating it by rewriting the query
	// or altering any other request parameter.
	Recency string
}

type SearchResponse struct {
	Provider string
	Answer   string
	Sources  []SearchSource
}

type SearchSource struct {
	Title         string
	URL           string
	Snippet       string
	PublishedDate string
	AgeSeconds    int64
	Author        string
}

type SearchProviderError struct {
	Provider string
	Message  string
	Status   int
}

func (e *SearchProviderError) Error() string {
	return fmt.Sprintf("%s: %s (status %d)", e.Provider, e.Message, e.Status)
}

func (e *SearchProviderError) IsAuthError() bool {
	return e.Status == 401 || e.Status == 403
}

func (e *SearchProviderError) IsRateLimit() bool {
	return e.Status == 429
}

type SearchProvider interface {
	ID() string
	IsAvailable() bool
	Search(ctx context.Context, params SearchParams) (*SearchResponse, error)
}

// Parsed returns the structured view of Query, parsing lazily when the
// pipeline did not supply one (omp: params.parsedQuery ?? parseSearchQuery).
func (p SearchParams) Parsed() *StructuredQuery {
	if p.ParsedQuery != nil {
		return p.ParsedQuery
	}
	q := ParseSearchQuery(p.Query)
	return &q
}

type SearchChain struct {
	Providers []SearchProvider
}

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

		if spe, ok := errors.AsType[*SearchProviderError](err); ok {
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

func (sc *SearchChain) AvailableProviders() []string {
	var ids []string
	for _, p := range sc.Providers {
		if p.IsAvailable() {
			ids = append(ids, p.ID())
		}
	}
	return ids
}

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
