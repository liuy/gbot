package web

import (
	"fmt"
	"time"
)

// Recency represents a time-window filter for search results.
type Recency int

const (
	RecencyUnlimited Recency = iota - 1
	RecencyDay
	RecencyWeek
	RecencyMonth
	RecencyYear
)

// ParseSince parses a human-readable recency string (e.g. "3d", "1w", "2m", "1y").
// Returns the Recency enum and the equivalent time.Duration for HTTP API usage.
func ParseSince(s string) (Recency, time.Duration, error) {
	if s == "" {
		return RecencyUnlimited, 0, nil
	}
	if len(s) < 2 {
		return RecencyUnlimited, 0, fmt.Errorf("invalid since format: %q (expected e.g. 3d, 1w, 2m, 1y)", s)
	}

	var count int
	_, err := fmt.Sscanf(s[:len(s)-1], "%d", &count)
	if err != nil || count <= 0 {
		return RecencyUnlimited, 0, fmt.Errorf("invalid since format: %q (expected e.g. 3d, 1w, 2m, 1y)", s)
	}

	unit := s[len(s)-1]
	switch unit {
	case 'd':
		return RecencyDay, time.Duration(count) * 24 * time.Hour, nil
	case 'w':
		return RecencyWeek, time.Duration(count) * 7 * 24 * time.Hour, nil
	case 'm':
		return RecencyMonth, time.Duration(count) * 30 * 24 * time.Hour, nil
	case 'y':
		return RecencyYear, time.Duration(count) * 365 * 24 * time.Hour, nil
	default:
		return RecencyUnlimited, 0, fmt.Errorf("invalid since unit: %q (use d/w/m/y)", unit)
	}
}

// SearchParams holds parameters for a search operation.
type SearchParams struct {
	Query string
	Limit int
	Since Recency
}

// SearchResponse is the unified result from any search provider.
type SearchResponse struct {
	Provider string
	Answer   string         // AI-synthesized answer (Z.AI has this)
	Sources  []SearchSource
}

// SearchSource is a single search result.
type SearchSource struct {
	Title         string
	URL           string
	Snippet       string
	PublishedDate string
	AgeSeconds    int64
	Author        string
}

// SearchProviderError classifies provider failures to drive fallback decisions.
// Source: omp providers/utils.ts — classifyProviderHttpError.
type SearchProviderError struct {
	Provider string
	Message  string
	Status   int // HTTP status: 401/403 → skip, 429 → retry, 500 → fallback
}

func (e *SearchProviderError) Error() string {
	return fmt.Sprintf("%s: %s (status %d)", e.Provider, e.Message, e.Status)
}

// IsAuthError returns true for authentication failures that should not be retried.
func (e *SearchProviderError) IsAuthError() bool {
	return e.Status == 401 || e.Status == 403
}

// IsRateLimit returns true for rate limiting that may be retried once.
func (e *SearchProviderError) IsRateLimit() bool {
	return e.Status == 429
}

// IsURL detects whether a query string is a URL rather than a search query.
// Rules: must have http/https scheme OR match domain.tld pattern (with optional path).
// "golang.org/x/text" → true, "golang.org" → true, "user/repo" → false, "golang generics" → false.
func IsURL(query string) bool {
	// Explicit scheme
	if len(query) > 8 && query[:8] == "https://" {
		return true
	}
	if len(query) > 7 && query[:7] == "http://" {
		return true
	}
	// Domain pattern: something.tld with optional /path
	// Must have a dot, TLD >= 2 chars, no spaces before the dot
	for i := 0; i < len(query); i++ {
		if query[i] == '.' {
			tldStart := i + 1
			if tldStart >= len(query) {
				return false
			}
			tldLen := 0
			for j := tldStart; j < len(query); j++ {
				if query[j] == '/' {
					break
				}
				if !isASCIILetter(query[j]) {
					break
				}
				tldLen++
			}
			if tldLen >= 2 {
				// No spaces before the dot
				for k := 0; k < i; k++ {
					if query[k] == ' ' {
						return false
					}
				}
				return true
			}
			return false
		}
	}
	return false
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
