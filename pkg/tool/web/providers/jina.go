package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// A1: omp requests s.jina.ai/<escaped-query>?count=n; the /search?q=&num=
// form is the user-verified one (live key, envelope {"code":200,...}).
var jinaSearchURL = "https://s.jina.ai/search"

const (
	jinaDefaultResults = 5
	jinaMaxResults     = 20
	jinaTimeout        = 60 * time.Second
)

type JinaProvider struct {
	Client *http.Client
	APIKey string
}

func (j *JinaProvider) ID() string        { return "jina" }
func (j *JinaProvider) IsAvailable() bool { return j.apiKey() != "" }

func (j *JinaProvider) apiKey() string {
	if j.APIKey != "" {
		return j.APIKey
	}
	return os.Getenv("JINA_API_KEY")
}

func (j *JinaProvider) client() *http.Client {
	if j.Client != nil {
		return j.Client
	}
	return http.DefaultClient
}

func (j *JinaProvider) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	key := j.apiKey()
	if key == "" {
		return nil, &SearchProviderError{
			Provider: "jina",
			Message:  `Jina credentials not found. Set JINA_API_KEY or configure an API key for provider "jina".`,
			Status:   0,
		}
	}

	numResults := clampNumResults(params.Limit, jinaDefaultResults, jinaMaxResults)

	parsed := params.Parsed()
	query := params.Query
	site := ""
	if parsed.HasDirectives {
		// Jina's X-Site header takes a single domain; with exactly one
		// include site, send its host there and strip site: tokens from
		// the query. Multiple sites stay inline (Bing-backed, parses them).
		if len(parsed.Sites) == 1 {
			site = strings.SplitN(parsed.Sites[0], "/", 2)[0]
		}
		query = FormatQuery(*parsed, QuerySyntax{
			Phrases:  true,
			Negation: true,
			Site:     site == "",
			InTitle:  true,
			InURL:    true,
			Filetype: true,
		})
	}

	q := url.Values{}
	q.Set("q", query)
	q.Set("num", strconv.Itoa(numResults))
	requestURL := jinaSearchURL + "?" + q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, jinaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jina: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	if site != "" {
		req.Header.Set("X-Site", site)
	}
	// Both X- headers keep payloads lean (omp parity).
	req.Header.Set("X-Respond-With", "no-content")
	req.Header.Set("X-Retain-Images", "none")

	resp, err := j.client().Do(req)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "jina",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, &SearchProviderError{
				Provider: "jina",
				Message:  fmt.Sprintf("read response: %v", readErr),
				Status:   resp.StatusCode,
			}
		}
		// omp reads the body before classifying; raw body, no JSON extraction
		// and no statusText fallback here — deliberately unlike Tavily.
		body := string(bodyBytes)
		if spe := classifyProviderHttpError("jina", resp.StatusCode, body); spe != nil {
			return nil, spe
		}
		return nil, &SearchProviderError{
			Provider: "jina",
			Message:  fmt.Sprintf("Jina API error (%d): %s", resp.StatusCode, body),
			Status:   resp.StatusCode,
		}
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "jina",
			Message:  fmt.Sprintf("read response: %v", err),
			Status:   resp.StatusCode,
		}
	}

	var v any
	if err := json.Unmarshal(respBytes, &v); err != nil {
		return nil, &SearchProviderError{
			Provider: "jina",
			Message:  fmt.Sprintf("parse response: %v", err),
			Status:   0,
		}
	}

	var results []any
	switch payload := v.(type) {
	case []any:
		results = payload
	case map[string]any:
		// The envelope code is the API-level status: HTTP 200 with a 429
		// envelope is still a rate limit for chain failover.
		if code, ok := payload["code"].(float64); ok && int(code) != 200 {
			return nil, &SearchProviderError{
				Provider: "jina",
				Message:  fmt.Sprintf("Jina API response reported failure (%d)", int(code)),
				Status:   int(code),
			}
		}
		data, ok := payload["data"].([]any)
		if !ok {
			return nil, &SearchProviderError{
				Provider: "jina",
				Message:  "Jina API returned invalid response: expected data array",
				Status:   0,
			}
		}
		results = data
	default:
		return nil, &SearchProviderError{
			Provider: "jina",
			Message:  "Jina API returned invalid response: expected an object or array",
			Status:   0,
		}
	}

	var sources []SearchSource
	for _, item := range results {
		result, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemURL, ok := result["url"].(string)
		if !ok || itemURL == "" {
			continue
		}
		// ?? semantics: an empty-string title is kept; only absent/null falls
		// back to the URL (unlike Tavily's truthiness check).
		title := itemURL
		if t, ok := result["title"].(string); ok {
			title = t
		}
		source := SearchSource{Title: title, URL: itemURL}
		snippet := ""
		if d, ok := result["description"].(string); ok {
			snippet = strings.TrimSpace(d)
		}
		if snippet == "" {
			if c, ok := result["content"].(string); ok {
				snippet = strings.TrimSpace(c)
			}
		}
		source.Snippet = snippet
		if date, ok := result["date"].(string); ok && date != "" {
			source.PublishedDate = date
			source.AgeSeconds = dateToAgeSeconds(date)
		}
		sources = append(sources, source)
	}

	if len(sources) > numResults {
		sources = sources[:numResults]
	}

	return &SearchResponse{Provider: "jina", Sources: sources}, nil
}
