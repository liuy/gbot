package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func fetchJSON(ctx context.Context, client *http.Client, url string) (json.RawMessage, error) {
	data, err := fetchBytes(ctx, client, url)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON from %s: %w", url, err)
	}
	return raw, nil
}

// formatNumber formats an integer with comma separators (e.g. 1234567 -> "1,234,567").
func formatNumber(n int) string {
	if n < 0 {
		return "-" + formatNumber(-n)
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return formatNumber(n/1000) + fmt.Sprintf(",%03d", n%1000)
}

func fetchBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gbot/1.0")
	req.Header.Set("Accept", "application/json, text/html")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}

	return body, nil
}
