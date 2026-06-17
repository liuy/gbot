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
//   - provider name contains "zhipu"/"zai"/"glm"  → ZhipuFetcher
//   - provider name or URL contains "minimax"      → MinimaxFetcher
//   - otherwise                                    → nil (no quota shown)
func Detect(p *config.Provider) Fetcher {
	if p == nil {
		return nil
	}

	key := p.ResolveKey()
	if key == "" {
		return nil
	}

	name := strings.ToLower(p.Name)
	// hostOnly strips any path suffix (e.g. "/api/coding/paas/v4") —
	// quota endpoints live under the host root, not under model paths.
	host := hostOnly(p.URL)

	switch {
	case strings.Contains(name, "zhipu") || strings.Contains(name, "zai") ||
		strings.Contains(name, "glm") || strings.Contains(host, "z.ai") ||
		strings.Contains(host, "bigmodel"):
		return NewZhipuFetcher(host, key)
	case strings.Contains(name, "minimax") || strings.Contains(host, "minimax"):
		return NewMinimaxFetcher(host, key)
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
