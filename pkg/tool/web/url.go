package web

import (
	"net/url"
	"strings"
)

// IsURL returns true if the query looks like an HTTP(S) URL or bare domain.
func IsURL(query string) bool {
	if u, err := url.Parse(query); err == nil {
		if u.Scheme == "http" || u.Scheme == "https" {
			return u.Host != ""
		}
	}
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
