package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// FreeModelEntry represents a single free model from OpenRouter's /api/v1/models.
type FreeModelEntry struct {
	ID            string // e.g. "qwen/qwen3-coder:free"
	Name          string // display name
	ContextLength int    // tokens
}

// orModelsResponse is the JSON shape of OpenRouter's /api/v1/models response.
type orModelsResponse struct {
	Data []struct {
		ID                  string            `json:"id"`
		Name                string            `json:"name"`
		ContextLength       int               `json:"context_length"`
		Pricing             map[string]string `json:"pricing"`
		SupportedParameters []string          `json:"supported_parameters"`
	} `json:"data"`
}

// FetchFreeModels pulls free models from an OpenRouter-compatible API.
//
// baseURL is the provider's API root (e.g. "https://openrouter.ai/api/v1"),
// already including the /v1 prefix. We append "/models" — not "/api/v1/models"
// — otherwise the path becomes /api/v1/api/v1/models and returns 404.
//
// Filters:
//   - pricing.prompt == "0" && pricing.completion == "0" (free)
//   - supported_parameters contains "tools" (required for gbot)
//
// Sorts by most-popular (server-side) and caps at 10 results — more than
// that is noise in the model picker.
func FetchFreeModels(ctx context.Context, baseURL string) ([]FreeModelEntry, error) {
	url := strings.TrimRight(baseURL, "/") + "/models?max_price=0&sort=most-popular"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("openrouter fetch: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter fetch: HTTP %d", resp.StatusCode)
	}

	var parsed orModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("openrouter decode: %w", err)
	}

	var models []FreeModelEntry
	for _, m := range parsed.Data {
		// Both prompt and completion must be 0 (free).
		if m.Pricing["prompt"] != "0" || m.Pricing["completion"] != "0" {
			continue
		}
		// Must support tool calling.
		if !hasParam(m.SupportedParameters, "tools") {
			continue
		}
		models = append(models, FreeModelEntry{
			ID:            m.ID,
			Name:          m.Name,
			ContextLength: m.ContextLength,
		})
		if len(models) >= 10 {
			break
		}
	}
	return models, nil
}

func hasParam(params []string, want string) bool {
	return slices.Contains(params, want)
}
