package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

var (
	anySearchAPIURL  = "https://api.anysearch.com/v1/search"
	anySearchTimeout = 30 * time.Second
)

type AnySearchProvider struct {
	Client *http.Client
	APIKey string // Optional — anonymous mode if empty.
}

func (a *AnySearchProvider) ID() string { return "anysearch" }

func (a *AnySearchProvider) IsAvailable() bool {
	// Always available — anonymous mode works without an API key.
	return true
}

func (a *AnySearchProvider) apiKey() string {
	if a.APIKey != "" {
		return a.APIKey
	}
	return os.Getenv("ANYSEARCH_API_KEY")
}

func (a *AnySearchProvider) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a *AnySearchProvider) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	body := map[string]any{
		"query":       params.Query,
		"max_results": params.Limit,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anysearch: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, anySearchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, anySearchAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("anysearch: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := a.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := a.client().Do(req)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "anysearch",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "anysearch",
			Message:  fmt.Sprintf("read response: %v", err),
			Status:   resp.StatusCode,
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &SearchProviderError{
			Provider: "anysearch",
			Message:  string(respBytes),
			Status:   resp.StatusCode,
		}
	}

	var apiResp anySearchAPIResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("anysearch: parse response: %w", err)
	}

	if apiResp.Code != 0 {
		return nil, &SearchProviderError{
			Provider: "anysearch",
			Message:  apiResp.Message,
			Status:   resp.StatusCode,
		}
	}

	var sources []SearchSource
	for _, r := range apiResp.Data.Results {
		sources = append(sources, SearchSource{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Snippet,
		})
	}

	var answer string
	if len(apiResp.Data.Results) > 0 && apiResp.Data.Results[0].Content != "" {
		answer = apiResp.Data.Results[0].Content
	}

	return &SearchResponse{
		Provider: "anysearch",
		Answer:   answer,
		Sources:  sources,
	}, nil
}

type anySearchAPIResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Content string `json:"content"`
		} `json:"results"`
		Metadata struct {
			RequestID    string `json:"request_id"`
			TotalResults int    `json:"total_results"`
			SearchTimeMs int    `json:"search_time_ms"`
		} `json:"metadata"`
	} `json:"data"`
}
