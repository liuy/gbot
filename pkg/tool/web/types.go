package web

import (
	"fmt"
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
	lq := strings.ToLower(query)
	if len(lq) > 8 && lq[:8] == "https://" {
		return true
	}
	if len(lq) > 7 && lq[:7] == "http://" {
		return true
	}
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
