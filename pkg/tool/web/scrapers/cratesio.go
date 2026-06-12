package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type cratesResponse struct {
	Crate struct {
		Name            string   `json:"name"`
		Description     string   `json:"description"`
		Homepage        string   `json:"homepage"`
		Documentation   string   `json:"documentation"`
		Repository      string   `json:"repository"`
		MaxVersion      string   `json:"max_version"`
		Downloads       int      `json:"downloads"`
		RecentDownloads int      `json:"recent_downloads"`
		Keywords        []string `json:"keywords"`
		Categories      []string `json:"categories"`
	} `json:"crate"`
	Versions []struct {
		Num     string `json:"num"`
		License string `json:"license"`
	} `json:"versions"`
}

func HandleCratesIo(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	if u.Hostname() != "crates.io" {
		return nil, nil
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "crates" {
		return nil, nil
	}
	name := parts[1]
	if name == "" {
		return nil, nil
	}

	apiURL := fmt.Sprintf("https://crates.io/api/v1/crates/%s", url.PathEscape(name))

	// The API may return a lot of data; we use fetchBytes and unmarshal manually.
	data, err := fetchBytes(ctx, client, apiURL)
	if err != nil {
		return nil, fmt.Errorf("crates.io API error for %s: %w", name, err)
	}

	var resp cratesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("invalid crates.io response for %s: %w", name, err)
	}

	c := resp.Crate
	var b strings.Builder

	fmt.Fprintf(&b, "# %s v%s\n\n", c.Name, c.MaxVersion)
	if c.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", c.Description)
	}

	fmt.Fprintf(&b, "**Downloads:** %d", c.Downloads)
	if resp.Versions != nil {
		fmt.Fprintf(&b, " · **Version Count:** %d", len(resp.Versions))
	}
	fmt.Fprintf(&b, " · **Recent Downloads:** %d\n", c.RecentDownloads)

	// Show license from the latest version.
	if len(resp.Versions) > 0 && resp.Versions[0].License != "" {
		fmt.Fprintf(&b, "**License:** %s\n", resp.Versions[0].License)
	}

	if c.Homepage != "" {
		fmt.Fprintf(&b, "**Homepage:** %s\n", c.Homepage)
	}
	if c.Repository != "" {
		fmt.Fprintf(&b, "**Repository:** %s\n", c.Repository)
	}
	if c.Documentation != "" {
		fmt.Fprintf(&b, "**Documentation:** %s\n", c.Documentation)
	}

	if len(c.Keywords) > 0 {
		fmt.Fprintf(&b, "**Keywords:** %s\n", strings.Join(c.Keywords, ", "))
	}
	if len(c.Categories) > 0 {
		fmt.Fprintf(&b, "**Categories:** %s\n", strings.Join(c.Categories, ", "))
	}

	// Best-effort fetch of README from docs.rs.
	readmeURL := fmt.Sprintf("https://docs.rs/crate/%s/%s", url.PathEscape(c.Name), url.PathEscape(c.MaxVersion))
	if readmeData, readmeErr := fetchBytes(ctx, client, readmeURL); readmeErr == nil {
		readmeStr := string(readmeData)
		// Try to extract content from the main readme section.
		readmeSection := regexp.MustCompile(`(?is)<div[^>]*class="[^"]*readme[^"]*"[^>]*>(.*?)</div>\s*$`)
		if m := readmeSection.FindStringSubmatch(readmeStr); len(m) > 1 {
			content := stripHTMLTags(m[1])
			if len(content) > 1000 {
				content = content[:1000] + "..."
			}
			if len(strings.TrimSpace(content)) > 50 {
				b.WriteString("\n---\n\n## README\n\n")
				b.WriteString(content)
				b.WriteString("\n")
			}
		}
	}

	return &Result{
		Content:     b.String(),
		ContentType: "text/markdown",
		Method:      "cratesio-api",
	}, nil
}
