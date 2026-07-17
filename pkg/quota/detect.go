package quota

import (
	"net/url"
	"strings"

	"github.com/liuy/gbot/pkg/config"
)

// Detect returns a Fetcher for the given provider, or nil if the provider
// has no quota endpoint (e.g. Anthropic/OpenAI direct, OpenRouter).
//
// Detection logic:
//   - provider name or URL contains "minimax"  → MinimaxFetcher
//   - provider name or URL contains "zhipu" or "z.ai" → ZhipuFetcher
//   - otherwise                                → nil (no quota shown)
func Detect(p *config.Provider) Fetcher {
	if p == nil {
		return nil
	}

	key := p.ResolveKey()
	if key == "" {
		return nil
	}

	name := strings.ToLower(p.Name)
	host := hostOnly(p.URL)

	switch {
	case strings.Contains(name, "minimax") || strings.Contains(host, "minimax"):
		return NewMinimaxFetcher(host, key)
	case strings.Contains(name, "zhipu") || strings.Contains(host, "zhipu") ||
		strings.Contains(name, "z.ai") || strings.Contains(host, "z.ai"):
		return NewZhipuFetcher(host, key)
	default:
		return nil
	}
}

// hostOnly returns the scheme://host[:port] part of rawURL, dropping any path.
// Falls back to rawURL unchanged on parse error.
func hostOnly(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Scheme + "://" + u.Host
}
