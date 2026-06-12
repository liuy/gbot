package web

import (
	"fmt"
	"net/url"
	"strings"
)

type SearchParams struct {
	Query string
	Limit int
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

func IsURL(query string) bool {
	if u, err := url.Parse(query); err == nil {
		if u.Scheme == "http" || u.Scheme == "https" {
			return u.Host != ""
		}
	}
	// Bare domain heuristic: no spaces, has dot, TLD >= 2 chars
	if strings.Contains(query, " ") {
		return false
	}
	for i := 0; i < len(query); i++ {
		if query[i] == '.' {
			tldStart := i + 1
			if tldStart >= len(query) {
				return false
			}
			tldLen := 0
			for j := tldStart; j < len(query); j++ {
				if query[j] == '/' || query[j] == ':' {
					break
				}
				if !isASCIILetter(query[j]) {
					break
				}
				tldLen++
			}
			return tldLen >= 2
		}
	}
	return false
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
