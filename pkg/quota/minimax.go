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

// MinimaxFetcher queries the MiniMax Token Plan 5h quota.
// Endpoint: GET {BaseURL}/v1/token_plan/remains
//
// Documented at platform.minimax.io/docs/token-plan/faq.
// Response is a single record (not wrapped in a limits array).
// `current_interval_remaining_percent` is the 5h window remaining %;
// `end_time` is the epoch-ms window rollover.
type MinimaxFetcher struct {
	BaseURL string // e.g. "https://api.minimax.io" or "https://www.minimax.io"
	APIKey  string
	Client  *http.Client
}

// NewMinimaxFetcher returns a MinimaxFetcher with a default 10s timeout.
func NewMinimaxFetcher(baseURL, apiKey string) *MinimaxFetcher {
	if baseURL == "" {
		baseURL = "https://api.minimax.io"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &MinimaxFetcher{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 10 * time.Second},
	}
}

type minimaxModelRemains struct {
	ModelName                       string `json:"model_name"`
	StartTime                       int64  `json:"start_time"`
	EndTime                         int64  `json:"end_time"`
	CurrentIntervalRemainingPercent int    `json:"current_interval_remaining_percent"`
	CurrentIntervalStatus           int    `json:"current_interval_status"`
	CurrentWeeklyRemainingPercent   int    `json:"current_weekly_remaining_percent"`
}

type minimaxResponse struct {
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
	// Real response is wrapped in a model_remains array, one entry per
	// quota bucket (e.g. "general" for coding, "video", "audio").
	ModelRemains []minimaxModelRemains `json:"model_remains"`
}

func (f *MinimaxFetcher) Fetch(ctx context.Context) (Info, error) {
	url := f.BaseURL + "/v1/token_plan/remains"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Info{}, fmt.Errorf("minimax quota: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.Client.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("minimax quota: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Info{}, fmt.Errorf("minimax quota: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("minimax quota: HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}

	var parsed minimaxResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Info{}, fmt.Errorf("minimax quota: parse JSON: %w", err)
	}

	if parsed.BaseResp.StatusCode != 0 {
		return Info{}, fmt.Errorf("minimax quota: API status=%d msg=%q",
			parsed.BaseResp.StatusCode, parsed.BaseResp.StatusMsg)
	}

	// Pick the most relevant entry: prefer "general" (covers coding-plan
	// usage), fall back to the first available entry. Some accounts may
	// have only a single bucket.
	picked := pickMinimaxBucket(parsed.ModelRemains)
	if picked == nil {
		return Info{}, fmt.Errorf("minimax quota: empty model_remains")
	}

	// Remaining% comes from the API; convert to used% for the unified Info shape.
	used := clamp100(100 - picked.CurrentIntervalRemainingPercent)

	return Info{
		Used:    used,
		ResetAt: time.UnixMilli(picked.EndTime),
	}, nil
}

// pickMinimaxBucket returns the most relevant model_remains entry for
// a coding-plan user. Prefers the "general" bucket; falls back to
// the first available entry. Returns nil if the slice is empty.
func pickMinimaxBucket(entries []minimaxModelRemains) *minimaxModelRemains {
	if len(entries) == 0 {
		return nil
	}
	for i := range entries {
		if entries[i].ModelName == "general" {
			return &entries[i]
		}
	}
	return &entries[0]
}

// clamp100 constrains v to [0, 100] (defensive against malformed responses).
func clamp100(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
