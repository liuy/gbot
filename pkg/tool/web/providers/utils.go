package providers

import (
	"regexp"
	"strings"
	"time"
)

// Helpers shared by the search providers. toSearchSources remains unported:
// omp's callers (kagi/parallel/ollama) have no gbot counterparts and none of
// gbot's providers matches its shape.
var creditBodyPattern = regexp.MustCompile(`(?i)credits?\s*(?:exhausted|exceeded)|quota|insufficient`)

// SiteHosts returns deduplicated bare hosts from site: values
// (github.com/anthropics → github.com) for provider domain filters; path
// parts are enforced by the central lenient post-filter.
func SiteHosts(sites []string) []string {
	var hosts []string
	seen := make(map[string]bool)
	for _, site := range sites {
		host, _, _ := strings.Cut(site, "/")
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts
}

// classifyProviderHttpError returns nil when the response matches no known
// quota/auth signal; the caller then builds its provider-specific fallback.
// Messages carry no provider prefix: unlike omp's SPE, gbot's Error() renders
// the provider itself, so omp's baked-in prefix would double it.
func classifyProviderHttpError(provider string, status int, body string) *SearchProviderError {
	if creditBodyPattern.MatchString(body) {
		return &SearchProviderError{Provider: provider, Message: "credits exhausted", Status: status}
	}
	switch status {
	case 402:
		return &SearchProviderError{Provider: provider, Message: "402 credits exhausted", Status: status}
	case 401:
		return &SearchProviderError{Provider: provider, Message: "401 unauthorized", Status: status}
	case 403:
		return &SearchProviderError{Provider: provider, Message: "403 forbidden", Status: status}
	}
	return nil
}

// clampNumResults mirrors the TS source: only the absent/0 value selects the
// default; negatives clamp up to 1 (TS: -5 is truthy → max(1,-5)=1).
func clampNumResults(value, defaultVal, maxVal int) int {
	if value == 0 {
		return defaultVal
	}
	return min(maxVal, max(1, value))
}

// dateToAgeSeconds parses common API date formats; 0 = unknown (missing or
// unparseable), which callers render as empty and fall back to the raw string.
var dateAgeLayouts = []string{time.RFC3339, "2006-01-02", time.RFC1123Z, time.RFC1123}

func dateToAgeSeconds(s string) int64 {
	return ageSecondsWith(dateAgeLayouts, s)
}

// ageSecondsWith is dateToAgeSeconds's parse loop over a caller-supplied
// layout set, so DDG's no-offset timestamps get their own leniency without
// widening the shared set (jina/tavily parsing must not drift).
func ageSecondsWith(layouts []string, s string) int64 {
	if s == "" {
		return 0
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		secs := time.Since(t).Seconds()
		n := int64(secs)
		if secs < 0 && float64(n) != secs {
			// int64() truncates toward zero; TS Math.floor rounds down, and a
			// mis-floored future date would read as 0 = unknown.
			n--
		}
		return n
	}
	return 0
}
