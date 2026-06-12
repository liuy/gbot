package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// siteMapping maps well-known Stack Exchange hostnames to their API site parameter.
var siteMapping = map[string]string{
	"stackoverflow.com": "stackoverflow",
	"superuser.com":     "superuser",
	"serverfault.com":   "serverfault",
	"askubuntu.com":     "askubuntu",
	"mathoverflow.net":  "mathoverflow",
	"stackapps.com":     "stackapps",
}

func HandleStackOverflow(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	hostname := strings.TrimPrefix(u.Hostname(), "www.")

	// Check direct mapping first.
	site := siteMapping[hostname]
	if site == "" {
		// Check for *.stackexchange.com subdomain.
		if before, ok := strings.CutSuffix(hostname, ".stackexchange.com"); ok {
			subdomain := before
			// Reject nested subdomains like foo.bar.stackexchange.com.
			if subdomain == "" || strings.Contains(subdomain, ".") {
				return nil, nil
			}
			site = subdomain
		} else {
			return nil, nil
		}
	}

	// Extract question ID from /questions/{id}/...
	path := u.Path
	if !strings.HasPrefix(path, "/questions/") {
		return nil, nil
	}
	rest := strings.TrimPrefix(path, "/questions/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return nil, nil
	}
	questionID := parts[0]
	if _, err := strconv.Atoi(questionID); err != nil {
		return nil, nil
	}

	// Fetch question details from Stack Exchange API.
	qURL := fmt.Sprintf("https://api.stackexchange.com/2.3/questions/%s?order=desc&sort=votes&site=%s&filter=withbody", questionID, site)
	qData, err := fetchJSON(ctx, client, qURL)
	if err != nil {
		return nil, fmt.Errorf("fetch question: %w", err)
	}

	var qResp struct {
		Items []struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			Score int    `json:"score"`
			Owner *struct {
				DisplayName string `json:"display_name"`
			} `json:"owner"`
			CreationDate int64    `json:"creation_date"`
			Tags         []string `json:"tags"`
			AnswerCount  int      `json:"answer_count"`
			IsAnswered   bool     `json:"is_answered"`
		} `json:"items"`
	}
	if err := json.Unmarshal(qData, &qResp); err != nil {
		return nil, fmt.Errorf("parse question: %w", err)
	}
	if len(qResp.Items) == 0 {
		return nil, nil
	}
	q := qResp.Items[0]

	// Fetch answers from Stack Exchange API.
	aURL := fmt.Sprintf("https://api.stackexchange.com/2.3/questions/%s/answers?order=desc&sort=votes&site=%s&filter=withbody", questionID, site)
	aData, err := fetchJSON(ctx, client, aURL)
	if err != nil {
		return nil, fmt.Errorf("fetch answers: %w", err)
	}

	var aResp struct {
		Items []struct {
			Body       string `json:"body"`
			Score      int    `json:"score"`
			IsAccepted bool   `json:"is_accepted"`
			Owner      *struct {
				DisplayName string `json:"display_name"`
			} `json:"owner"`
			CreationDate int64 `json:"creation_date"`
		} `json:"items"`
	}
	if err := json.Unmarshal(aData, &aResp); err != nil {
		return nil, fmt.Errorf("parse answers: %w", err)
	}

	var md strings.Builder

	// Title
	fmt.Fprintf(&md, "# %s\n\n", q.Title)

	// Metadata: score, answer count, answered status.
	answered := "No"
	if q.IsAnswered {
		answered = "Yes"
	}
	fmt.Fprintf(&md, "**Score:** %d · **Answers:** %d (%s)\n", q.Score, q.AnswerCount, answered)

	// Tags
	if len(q.Tags) > 0 {
		fmt.Fprintf(&md, "**Tags:** %s\n", strings.Join(q.Tags, ", "))
	}

	// Asked by — owner can be nil for deleted users.
	userName := "anonymous"
	if q.Owner != nil && q.Owner.DisplayName != "" {
		userName = q.Owner.DisplayName
	}
	date := time.Unix(q.CreationDate, 0).Format("2006-01-02")
	fmt.Fprintf(&md, "**Asked by:** %s · %s\n\n", userName, date)

	md.WriteString("---\n\n## Question\n\n")
	md.WriteString(htmlToBasicMarkdown(q.Body))
	md.WriteString("\n\n---\n\n## Answers\n\n")

	// API returns answers sorted by votes desc; limit to top 5.
	limit := min(len(aResp.Items), 5)
	for i := range limit {
		a := aResp.Items[i]
		accepted := ""
		if a.IsAccepted {
			accepted = " (Accepted)"
		}
		aUser := "anonymous"
		if a.Owner != nil && a.Owner.DisplayName != "" {
			aUser = a.Owner.DisplayName
		}
		aDate := time.Unix(a.CreationDate, 0).Format("2006-01-02")

		fmt.Fprintf(&md, "### Score: %d%s · by %s · %s\n\n", a.Score, accepted, aUser, aDate)
		md.WriteString(htmlToBasicMarkdown(a.Body))
		md.WriteString("\n\n---\n\n")
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "stackexchange",
		Notes:       []string{fmt.Sprintf("Fetched via Stack Exchange API (site: %s)", site)},
	}, nil
}
