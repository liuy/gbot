package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ZhipuFetcher queries the zhipu/z.ai 5h token quota.
// Endpoint: GET {BaseURL}/api/monitor/usage/quota/limit
//
// Reverse-engineered from the web console (not in public docs).
// Response contains a "limits" array; we pick the entry whose unit=3,number=5
// (5-hour rolling window).
type ZhipuFetcher struct {
	BaseURL string // e.g. "https://open.bigmodel.cn" or "https://api.z.ai"
	APIKey  string
	Client  *http.Client
}

// NewZhipuFetcher returns a ZhipuFetcher with a default 10s timeout.
func NewZhipuFetcher(baseURL, apiKey string) *ZhipuFetcher {
	if baseURL == "" {
		baseURL = "https://open.bigmodel.cn"
	}
	// Normalize: strip trailing slash.
	baseURL = strings.TrimRight(baseURL, "/")
	return &ZhipuFetcher{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// zhipuLimit matches one entry in the response limits array.
type zhipuLimit struct {
	Type        string `json:"type"`
	Unit        int    `json:"unit"`
	Number      int    `json:"number"`
	Percentage  int    `json:"percentage"`
	NextResetMs int64  `json:"nextResetTime"`
}

type zhipuResponse struct {
	Code    int  `json:"code"`
	Success bool `json:"success"`
	Data    struct {
		Limits []zhipuLimit `json:"limits"`
	} `json:"data"`
	Msg string `json:"msg"`
}

func (f *ZhipuFetcher) Fetch(ctx context.Context) (Info, error) {
	url := f.BaseURL + "/api/monitor/usage/quota/limit"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Info{}, fmt.Errorf("zhipu quota: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := f.Client.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("zhipu quota: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Info{}, fmt.Errorf("zhipu quota: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("zhipu quota: HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}

	var parsed zhipuResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Info{}, fmt.Errorf("zhipu quota: parse JSON: %w", err)
	}
	if !parsed.Success || parsed.Code != 200 {
		return Info{}, fmt.Errorf("zhipu quota: API returned code=%d success=%v msg=%q",
			parsed.Code, parsed.Success, parsed.Msg)
	}

	// Pick the 5-hour window: unit=3, number=5.
	// Fallback: first TOKENS_LIMIT entry if no exact match.
	var picked *zhipuLimit
	for i := range parsed.Data.Limits {
		l := &parsed.Data.Limits[i]
		if l.Type == "TOKENS_LIMIT" && l.Unit == 3 && l.Number == 5 {
			picked = l
			break
		}
	}
	if picked == nil {
		for i := range parsed.Data.Limits {
			if parsed.Data.Limits[i].Type == "TOKENS_LIMIT" {
				picked = &parsed.Data.Limits[i]
				break
			}
		}
	}
	if picked == nil {
		return Info{}, fmt.Errorf("zhipu quota: no TOKENS_LIMIT in response")
	}

	return Info{
		Used:    picked.Percentage,
		ResetAt: time.UnixMilli(picked.NextResetMs),
	}, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
