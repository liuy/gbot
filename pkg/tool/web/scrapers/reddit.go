package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Reddit JSON API types.

type redditListing struct {
	Kind string `json:"kind"`
	Data struct {
		Children []redditThing `json:"children"`
	} `json:"data"`
}

type redditThing struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type redditPost struct {
	Title     string `json:"title"`
	SelfText  string `json:"selftext"`
	Author    string `json:"author"`
	Score     int    `json:"score"`
	Comments  int    `json:"num_comments"`
	Created   int64  `json:"created_utc"`
	Subreddit string `json:"subreddit"`
	URL       string `json:"url"`
	IsSelf    bool   `json:"is_self"`
}

type redditComment struct {
	Body     string `json:"body"`
	Author   string `json:"author"`
	Score    int    `json:"score"`
	Created  int64  `json:"created_utc"`
	Replies  json.RawMessage `json:"replies"`
}

func HandleReddit(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	if !strings.Contains(u.Hostname(), "reddit.com") {
		return nil, nil
	}

	// Build .json URL.
	cleanURL := strings.TrimRight(u.String(), "/")
	jsonURL := cleanURL + ".json"
	if u.RawQuery != "" {
		base := strings.TrimRight(strings.TrimSuffix(cleanURL, u.RawQuery), "?&")
		jsonURL = base + ".json?" + u.RawQuery
	}

	data, err := fetchBytes(ctx, client, jsonURL)
	if err != nil {
		return nil, nil
	}

	// Reddit returns either an array (post+comments) or object (listing).
	var listingArr []json.RawMessage
	if err := json.Unmarshal(data, &listingArr); err == nil && len(listingArr) > 0 {
		md, ok := renderRedditPost(listingArr)
		if !ok {
			return nil, nil
		}
		return &Result{
			Content:     md,
			ContentType: "text/markdown",
			Method:      "reddit-json",
			Notes:       []string{"Fetched via Reddit JSON API"},
		}, nil
	}

	// Single listing (subreddit page).
	var listing redditListing
	if err := json.Unmarshal(data, &listing); err != nil {
		return nil, nil
	}
	md := renderRedditListing(listing)
	if md == "" {
		return nil, nil
	}
	return &Result{
		Content:     md,
		ContentType: "text/markdown",
		Method:      "reddit-json",
		Notes:       []string{"Fetched via Reddit JSON API"},
	}, nil
}

func renderRedditPost(parts []json.RawMessage) (string, bool) {
	// Part 0 = post listing, part 1 = comments listing.
	var postListing redditListing
	if err := json.Unmarshal(parts[0], &postListing); err != nil {
		return "", false
	}
	if len(postListing.Data.Children) == 0 || postListing.Data.Children[0].Kind != "t3" {
		return "", false
	}

	var post redditPost
	if err := json.Unmarshal(postListing.Data.Children[0].Data, &post); err != nil {
		return "", false
	}

	var sb strings.Builder
	sb.WriteString("# ")
	sb.WriteString(post.Title)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "**r/%s** · u/%s · %s points · %s comments\n",
		post.Subreddit, post.Author, formatNumber(post.Score), formatNumber(post.Comments))
	fmt.Fprintf(&sb, "*%s*\n\n", formatRedditTime(post.Created))

	if post.IsSelf && post.SelfText != "" {
		sb.WriteString("---\n\n")
		sb.WriteString(post.SelfText)
		sb.WriteString("\n\n")
	} else if !post.IsSelf {
		fmt.Fprintf(&sb, "**Link:** %s\n\n", post.URL)
	}

	// Comments.
	if len(parts) >= 2 {
		var commentListing redditListing
		if err := json.Unmarshal(parts[1], &commentListing); err == nil {
			comments := filterThings(commentListing.Data.Children, "t1")
			if len(comments) > 0 {
				sb.WriteString("---\n\n## Top Comments\n\n")
				limit := 10
				if len(comments) < limit {
					limit = len(comments)
				}
				for i := 0; i < limit; i++ {
					var c redditComment
					if err := json.Unmarshal(comments[i].Data, &c); err != nil {
						continue
					}
					fmt.Fprintf(&sb, "**u/%s** · %s points\n\n%s\n\n---\n\n",
						c.Author, formatNumber(c.Score), c.Body)
				}
			}
		}
	}

	return sb.String(), true
}

func renderRedditListing(listing redditListing) string {
	posts := filterThings(listing.Data.Children, "t3")
	if len(posts) == 0 {
		return ""
	}

	var first redditPost
	if err := json.Unmarshal(posts[0].Data, &first); err != nil {
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# r/%s\n\n", first.Subreddit)

	limit := 20
	if len(posts) < limit {
		limit = len(posts)
	}
	for i := 0; i < limit; i++ {
		var p redditPost
		if err := json.Unmarshal(posts[i].Data, &p); err != nil {
			continue
		}
		fmt.Fprintf(&sb, "- **%s** (%s pts, %s comments)\n  by u/%s\n\n",
			p.Title, formatNumber(p.Score), formatNumber(p.Comments), p.Author)
	}
	return sb.String()
}

func filterThings(things []redditThing, kind string) []redditThing {
	var out []redditThing
	for _, t := range things {
		if t.Kind == kind {
			out = append(out, t)
		}
	}
	return out
}

func formatRedditTime(utc int64) string {
	t := time.Unix(utc, 0).UTC()
	days := int(time.Since(t).Hours() / 24)
	switch {
	case days < 1:
		hours := int(time.Since(t).Hours())
		if hours < 1 {
			return "just now"
		}
		return fmt.Sprintf("%dh ago", hours)
	case days < 30:
		return fmt.Sprintf("%dd ago", days)
	case days < 365:
		return fmt.Sprintf("%dmo ago", days/30)
	default:
		return t.Format("2006-01-02")
	}
}
