package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// npmPackage maps the npm registry latest endpoint response.
type npmPackage struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	License      string            `json:"license"`
	Homepage     string            `json:"homepage"`
	Repository   json.RawMessage   `json:"repository"`
	Keywords     []string          `json:"keywords"`
	Dependencies map[string]string `json:"dependencies"`
	Readme       string            `json:"readme"`
}

// npmDownloadStats maps the npm download count API response.
type npmDownloadStats struct {
	Downloads int `json:"downloads"`
}

func HandleNpm(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	hostname := u.Hostname()
	if hostname != "www.npmjs.com" && hostname != "npmjs.com" {
		return nil, nil
	}

	path := u.Path

	// Match /package/{name} or /package/@scope/name
	if !strings.HasPrefix(path, "/package/") {
		return nil, nil
	}
	packageName := strings.TrimPrefix(path, "/package/")
	if packageName == "" {
		return nil, nil
	}

	// Fetch package info from npm registry.
	apiURL := fmt.Sprintf("https://registry.npmjs.org/%s/latest", url.PathEscape(packageName))
	raw, err := fetchJSON(ctx, client, apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch npm package: %w", err)
	}

	var pkg npmPackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("parse npm package: %w", err)
	}
	if pkg.Name == "" {
		return nil, nil
	}

	// Fetch download stats (best-effort).
	dlURL := fmt.Sprintf("https://api.npmjs.org/downloads/point/last-week/%s", url.PathEscape(packageName))
	dlRaw, dlErr := fetchJSON(ctx, client, dlURL)
	dlStr := "N/A"
	if dlErr == nil {
		var stats npmDownloadStats
		if err := json.Unmarshal(dlRaw, &stats); err == nil {
			dlStr = formatNumber(stats.Downloads)
		}
	}

	var md strings.Builder

	// Title.
	fmt.Fprintf(&md, "# %s\n\n", pkg.Name)

	// Description.
	if pkg.Description != "" {
		md.WriteString(pkg.Description)
		md.WriteString("\n\n")
	}

	// Metadata: version, license, downloads.
	fmt.Fprintf(&md, "**Latest:** %s · **License:** %s\n", pkg.Version, pkg.License)
	fmt.Fprintf(&md, "**Weekly Downloads:** %s\n\n", dlStr)

	// Homepage.
	if pkg.Homepage != "" {
		fmt.Fprintf(&md, "**Homepage:** %s\n", pkg.Homepage)
	}

	// Repository — can be a string (deprecated) or an object with `url` field.
	if pkg.Repository != nil {
		repoURL := extractRepoURL(pkg.Repository)
		if repoURL != "" {
			fmt.Fprintf(&md, "**Repository:** %s\n", repoURL)
		}
	}

	// Keywords.
	if len(pkg.Keywords) > 0 {
		fmt.Fprintf(&md, "**Keywords:** %s\n", strings.Join(pkg.Keywords, ", "))
	}

	md.WriteString("\n## Dependencies\n\n")
	if len(pkg.Dependencies) > 0 {
		for name, ver := range pkg.Dependencies {
			fmt.Fprintf(&md, "- %s: %s\n", name, ver)
		}
	} else {
		md.WriteString("None\n")
	}

	md.WriteString("\n---\n\n## README\n\n")
	if pkg.Readme != "" {
		md.WriteString(pkg.Readme)
		md.WriteString("\n")
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "npm-api",
		Notes:       []string{"Fetched via npm registry API"},
	}, nil
}

// extractRepoURL handles both string and object forms of the repository field.
func extractRepoURL(raw json.RawMessage) string {
	// Try string form first (deprecated).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s
	}

	// Try object form: { "url": "..." } or { "type": "...", "url": "..." }.
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.URL
	}

	return ""
}
