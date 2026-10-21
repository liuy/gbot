package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var tavilyAPIURL = "https://api.tavily.com/search"

const (
	tavilyDefaultResults = 5
	tavilyMaxResults     = 20
	// omp DEFAULT_WEB_SEARCH_TIMEOUT_SECONDS=60: the "advanced" answer is
	// LLM-mediated and slow, unlike the 30s gbot house style.
	tavilyTimeout = 60 * time.Second
)

type TavilyProvider struct {
	Client *http.Client
	APIKey string
}

func (t *TavilyProvider) ID() string        { return "tavily" }
func (t *TavilyProvider) IsAvailable() bool { return t.apiKey() != "" }

func (t *TavilyProvider) apiKey() string {
	if t.APIKey != "" {
		return t.APIKey
	}
	return os.Getenv("TAVILY_API_KEY")
}

func (t *TavilyProvider) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

func (t *TavilyProvider) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	key := t.apiKey()
	if key == "" {
		return nil, &SearchProviderError{
			Provider: "tavily",
			Message:  `Tavily credentials not found. Set TAVILY_API_KEY or configure an API key for provider "tavily".`,
			Status:   0,
		}
	}

	numResults := clampNumResults(params.Limit, tavilyDefaultResults, tavilyMaxResults)

	// Anonymous struct so the wire field order stays deterministic, matching
	// the omp request body. Directives (include/exclude_domains, date bounds)
	// arrive with the batch-2 query layer.
	reqBody := struct {
		Query             string `json:"query"`
		SearchDepth       string `json:"search_depth"`
		MaxResults        int    `json:"max_results"`
		IncludeAnswer     string `json:"include_answer"`
		IncludeRawContent bool   `json:"include_raw_content"`
	}{
		Query:             params.Query,
		SearchDepth:       "basic",
		MaxResults:        numResults,
		IncludeAnswer:     "advanced",
		IncludeRawContent: false,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("tavily: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, tavilyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, tavilyAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := t.client().Do(req)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "tavily",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, &SearchProviderError{
				Provider: "tavily",
				Message:  fmt.Sprintf("read response: %v", readErr),
				Status:   resp.StatusCode,
			}
		}
		errorText := string(body)
		if spe := classifyProviderHttpError("tavily", resp.StatusCode, errorText); spe != nil {
			return nil, spe
		}
		message := strings.TrimSpace(errorText)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		} else {
			// Both invalid JSON and extraction-empty keep the raw trimmed
			// text: omp only replaces the message when extraction returns a
			// non-null value.
			var parsed any
			if json.Unmarshal(body, &parsed) == nil {
				if extracted := extractErrorMessage(parsed); extracted != "" {
					message = extracted
				}
			}
		}
		return nil, &SearchProviderError{
			Provider: "tavily",
			Message:  fmt.Sprintf("Tavily API error (%d): %s", resp.StatusCode, message),
			Status:   resp.StatusCode,
		}
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "tavily",
			Message:  fmt.Sprintf("read response: %v", err),
			Status:   resp.StatusCode,
		}
	}

	var v any
	if err := json.Unmarshal(respBytes, &v); err != nil {
		return nil, &SearchProviderError{
			Provider: "tavily",
			Message:  fmt.Sprintf("parse response: %v", err),
			Status:   0,
		}
	}

	// asRecord(payload) ?? {}: any non-map JSON (array, string, number, bool,
	// null) maps to an empty record — zero sources, no answer, still success.
	// A struct-typed unmarshal would error here and trigger failover.
	record, _ := v.(map[string]any)

	var sources []SearchSource
	if results, ok := record["results"].([]any); ok {
		for _, item := range results {
			result, ok := item.(map[string]any)
			if !ok {
				continue
			}
			url, ok := result["url"].(string)
			if !ok || url == "" {
				continue
			}
			// Tavily uses title truthiness: an empty string falls back to the
			// URL (unlike Jina's ?? which keeps it).
			title := url
			if t, ok := result["title"].(string); ok && t != "" {
				title = t
			}
			source := SearchSource{Title: title, URL: url}
			if snippet, ok := result["content"].(string); ok {
				source.Snippet = snippet
			}
			if published, ok := result["published_date"].(string); ok {
				source.PublishedDate = published
				source.AgeSeconds = dateToAgeSeconds(published)
			}
			sources = append(sources, source)
		}
	}

	var answer string
	if a, ok := record["answer"].(string); ok {
		answer = strings.TrimSpace(a)
	}

	if len(sources) > numResults {
		sources = sources[:numResults]
	}

	return &SearchResponse{Provider: "tavily", Answer: answer, Sources: sources}, nil
}

// extractErrorMessage mirrors omp getErrorMessage: a trimmed non-empty string
// wins; otherwise recurse detail/error/message keys in order.
func extractErrorMessage(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	record, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"detail", "error", "message"} {
		if msg := extractErrorMessage(record[key]); msg != "" {
			return msg
		}
	}
	return ""
}
