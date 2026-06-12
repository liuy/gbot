package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// pypiInfo maps the PyPI JSON API info section.
type pypiInfo struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Summary      string            `json:"summary"`
	Description  string            `json:"description"`
	Author       string            `json:"author"`
	AuthorEmail  string            `json:"author_email"`
	HomePage     string            `json:"home_page"`
	ProjectURLs  map[string]string `json:"project_urls"`
	License      string            `json:"license"`
	Keywords     string            `json:"keywords"`
	Classifiers  []string          `json:"classifiers"`
	RequiresDist []string          `json:"requires_dist"`
}

// pypiResponse maps the full PyPI JSON API response.
type pypiResponse struct {
	Info pypiInfo `json:"info"`
}

func HandlePyPI(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	hostname := u.Hostname()
	if hostname != "pypi.org" {
		return nil, nil
	}

	path := u.Path

	// Match /project/{name}/ or /project/{name}/
	if !strings.HasPrefix(path, "/project/") {
		return nil, nil
	}
	packageName := strings.TrimPrefix(path, "/project/")
	packageName = strings.TrimSuffix(packageName, "/")
	if packageName == "" {
		return nil, nil
	}

	// Fetch package info from PyPI JSON API.
	apiURL := fmt.Sprintf("https://pypi.org/pypi/%s/json", url.PathEscape(packageName))
	raw, err := fetchJSON(ctx, client, apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch PyPI package: %w", err)
	}

	var resp pypiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse PyPI package: %w", err)
	}

	info := resp.Info
	if info.Name == "" {
		return nil, nil
	}

	// Best-effort fetch of weekly download stats from PyPI Stats API.
	var weeklyDownloads int
	statsURL := fmt.Sprintf("https://pypistats.org/api/packages/%s/recent", url.PathEscape(packageName))
	if statsData, statsErr := fetchBytes(ctx, client, statsURL); statsErr == nil {
		var statsResp struct {
			Data struct {
				LastWeek int `json:"last_week"`
			} `json:"data"`
		}
		if err := json.Unmarshal(statsData, &statsResp); err == nil {
			weeklyDownloads = statsResp.Data.LastWeek
		}
	}

	var md strings.Builder

	// Title.
	fmt.Fprintf(&md, "# %s v%s\n\n", info.Name, info.Version)

	// Summary.
	if info.Summary != "" {
		md.WriteString(info.Summary)
		md.WriteString("\n\n")
	}

	// Author.
	if info.Author != "" || info.AuthorEmail != "" {
		fmt.Fprintf(&md, "**Author:** %s <%s>\n", info.Author, info.AuthorEmail)
	}

	// License.
	if info.License != "" {
		fmt.Fprintf(&md, "**License:** %s\n", info.License)
	}

	// Homepage.
	if info.HomePage != "" {
		fmt.Fprintf(&md, "**Homepage:** %s\n", info.HomePage)
	}

	// Weekly downloads
	if weeklyDownloads > 0 {
		fmt.Fprintf(&md, "**Weekly Downloads:** %s\n", formatNumber(weeklyDownloads))
	}

	md.WriteString("\n")

	// Links (project_urls).
	if len(info.ProjectURLs) > 0 {
		md.WriteString("## Links\n\n")
		for label, link := range info.ProjectURLs {
			fmt.Fprintf(&md, "- %s: %s\n", label, link)
		}
		md.WriteString("\n")
	}

	// Dependencies.
	md.WriteString("## Dependencies\n\n")
	if len(info.RequiresDist) > 0 {
		for _, dep := range info.RequiresDist {
			fmt.Fprintf(&md, "- %s\n", dep)
		}
	} else {
		md.WriteString("None\n")
	}

	md.WriteString("\n---\n\n## Description\n\n")
	if info.Description != "" {
		md.WriteString(info.Description)
		md.WriteString("\n")
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "pypi-api",
		Notes:       []string{"Fetched via PyPI JSON API"},
	}, nil
}
