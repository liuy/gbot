package scrapers

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type atomFeed struct {
	Entry atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID         string         `xml:"id"`
	Title      string         `xml:"title"`
	Summary    string         `xml:"summary"`
	Authors    []atomAuthor   `xml:"author"`
	Published  string         `xml:"published"`
	Links      []atomLink     `xml:"link"`
	Categories []atomCategory `xml:"category"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Title string `xml:"title,attr"`
	Href  string `xml:"href,attr"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

func HandleArxiv(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	if u.Hostname() != "arxiv.org" {
		return nil, nil
	}

	path := u.Path

	// Match /abs/... or /pdf/... paths.
	var paperID string
	switch {
	case strings.HasPrefix(path, "/abs/"):
		paperID = strings.TrimPrefix(path, "/abs/")
	case strings.HasPrefix(path, "/pdf/"):
		paperID = strings.TrimPrefix(path, "/pdf/")
	default:
		return nil, nil
	}

	// Strip .pdf suffix if present.
	paperID = strings.TrimSuffix(paperID, ".pdf")
	if paperID == "" {
		return nil, nil
	}

	// Fetch metadata from arXiv API (returns Atom XML).
	apiURL := fmt.Sprintf("https://export.arxiv.org/api/query?id_list=%s", url.QueryEscape(paperID))
	data, err := fetchBytes(ctx, client, apiURL)
	if err != nil {
		return nil, err
	}

	var feed atomFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse arXiv API response: %w", err)
	}

	entry := feed.Entry
	if entry.ID == "" {
		return nil, nil
	}

	// Build markdown output.
	var md strings.Builder
	fmt.Fprintf(&md, "# %s\n\n", entry.Title)

	if len(entry.Authors) > 0 {
		authors := make([]string, len(entry.Authors))
		for i, a := range entry.Authors {
			authors[i] = a.Name
		}
		fmt.Fprintf(&md, "**Authors:** %s\n", strings.Join(authors, ", "))
	}

	// Extract just the YYYY-MM-DD part of the published date.
	pubDate := entry.Published
	if len(pubDate) >= 10 {
		pubDate = pubDate[:10]
	}
	fmt.Fprintf(&md, "**Published:** %s\n", pubDate)

	if len(entry.Categories) > 0 {
		cats := make([]string, len(entry.Categories))
		for i, c := range entry.Categories {
			cats[i] = c.Term
		}
		fmt.Fprintf(&md, "**Categories:** %s\n", strings.Join(cats, ", "))
	}

	fmt.Fprintf(&md, "**arXiv:** %s\n", paperID)

	md.WriteString("\n---\n\n## Abstract\n\n")
	md.WriteString(entry.Summary)
	md.WriteString("\n")

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "arxiv-api",
		Notes:       []string{"Fetched via arXiv OAI-PMH API"},
	}, nil
}
