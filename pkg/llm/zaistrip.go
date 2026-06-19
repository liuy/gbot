package llm

import (
	"encoding/json"
	"net/url"
	"strings"
)

// isZAIHost reports whether the base URL points to Z.AI / Zhipu's
// anthropic-compatible endpoint. Their implementation does not honor
// nested cache_control fields (Antigravity-Manager #290), so we strip
// them before sending requests to avoid silent cache misses.
func isZAIHost(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "z.ai") || strings.Contains(host, "bigmodel.cn")
}

// deepRemoveCacheControl walks any JSON value and removes every
// "cache_control" key, including nested ones inside arrays and objects.
// Source port: Antigravity-Manager fix #290 (zai_anthropic.rs deep_remove_cache_control).
func deepRemoveCacheControl(v any) any {
	switch val := v.(type) {
	case map[string]any:
		for k := range val {
			if k == "cache_control" {
				delete(val, k)
				continue
			}
			val[k] = deepRemoveCacheControl(val[k])
		}
		return val
	case []any:
		for i := range val {
			val[i] = deepRemoveCacheControl(val[i])
		}
		return val
	default:
		return v
	}
}

// stripCacheControlForZAI removes all cache_control fields from a JSON
// body if the request is going to Z.AI / Zhipu's anthropic endpoint.
// Other hosts are returned unchanged.
func stripCacheControlForZAI(baseURL string, body []byte) []byte {
	if !isZAIHost(baseURL) {
		return body
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body // malformed JSON — leave unchanged
	}
	stripped := deepRemoveCacheControl(v)
	out, err := json.Marshal(stripped)
	if err != nil {
		return body
	}
	return out
}
