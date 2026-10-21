package providers

import (
	"regexp"
	"time"
)

// Helpers shared by the tavily/jina providers. siteHosts/toSearchSources have
// no callers until the batch-2 directive layer lands; they port with it.
var creditBodyPattern = regexp.MustCompile(`(?i)credits?\s*(?:exhausted|exceeded)|quota|insufficient`)

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
	if s == "" {
		return 0
	}
	for _, layout := range dateAgeLayouts {
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
