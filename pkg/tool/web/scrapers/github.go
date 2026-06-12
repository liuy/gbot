package scrapers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const ghAPIBase = "https://api.github.com"

// ghURL holds parsed GitHub URL components.
type ghURL struct {
	urlType string // repo, blob, tree, issue, pull, issues, actions-run, actions-job, discussion, discussions
	owner   string
	repo    string
	ref     string
	path    string
	number  int
	runID   int64
	jobID   int64
}

func HandleGitHub(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	if u.Hostname() != "github.com" {
		return nil, nil
	}

	parsed := parseGitHubURL(u)
	if parsed == nil {
		return nil, nil
	}

	switch parsed.urlType {
	case "repo":
		return renderRepo(ctx, client, parsed)
	case "blob":
		return renderBlob(ctx, client, parsed)
	case "tree":
		return renderTree(ctx, client, parsed)
	case "issue":
		return renderIssue(ctx, client, parsed, false)
	case "pull":
		return renderIssue(ctx, client, parsed, true)
	case "issues":
		return renderIssuesList(ctx, client, parsed)
	case "actions-run":
		return renderActionsRun(ctx, client, parsed)
	case "actions-job":
		return renderActionsJob(ctx, client, parsed)
	case "discussion":
		return nil, nil
	case "discussions":
		return nil, nil
	default:
		return nil, nil
	}
}

// parseGitHubURL extracts components from a github.com URL path.
func parseGitHubURL(u *url.URL) *ghURL {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	owner, repo := parts[0], parts[1]

	if len(parts) == 2 {
		return &ghURL{urlType: "repo", owner: owner, repo: repo}
	}

	switch parts[2] {
	case "blob":
		if len(parts) < 4 {
			return nil
		}
		return &ghURL{urlType: "blob", owner: owner, repo: repo, ref: parts[3], path: strings.Join(parts[4:], "/")}
	case "tree":
		if len(parts) < 4 {
			return nil
		}
		return &ghURL{urlType: "tree", owner: owner, repo: repo, ref: parts[3], path: strings.Join(parts[4:], "/")}
	case "issues":
		if len(parts) == 3 {
			return &ghURL{urlType: "issues", owner: owner, repo: repo}
		}
		if num, err := strconv.Atoi(parts[3]); err == nil {
			return &ghURL{urlType: "issue", owner: owner, repo: repo, number: num}
		}
	case "pull":
		if len(parts) < 4 {
			return nil
		}
		if num, err := strconv.Atoi(parts[3]); err == nil {
			return &ghURL{urlType: "pull", owner: owner, repo: repo, number: num}
		}
	case "actions":
		if len(parts) >= 5 && parts[3] == "runs" {
			runID, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil {
				return nil
			}
			if len(parts) >= 7 && parts[5] == "job" {
				jobID, err := strconv.ParseInt(parts[6], 10, 64)
				if err != nil {
					return nil
				}
				return &ghURL{urlType: "actions-job", owner: owner, repo: repo, runID: runID, jobID: jobID}
			}
			// Check for /attempts/{n}#summary-{jobID} pattern via fragment
			if len(parts) >= 6 && parts[5] == "attempts" && u.Fragment != "" {
				if jobID, err := extractJobFromFragment(u.Fragment); err == nil {
					return &ghURL{urlType: "actions-job", owner: owner, repo: repo, runID: runID, jobID: jobID}
				}
			}
			return &ghURL{urlType: "actions-run", owner: owner, repo: repo, runID: runID}
		}
	case "discussions":
		if len(parts) >= 4 {
			if num, err := strconv.Atoi(parts[3]); err == nil {
				return &ghURL{urlType: "discussion", owner: owner, repo: repo, number: num}
			}
		}
		return &ghURL{urlType: "discussions", owner: owner, repo: repo}
	}

	return nil
}

// extractJobFromFragment parses a URL fragment like "summary-12345" to extract the job ID.
func extractJobFromFragment(fragment string) (int64, error) {
	if !strings.HasPrefix(fragment, "summary-") {
		return 0, fmt.Errorf("not a job fragment")
	}
	return strconv.ParseInt(strings.TrimPrefix(fragment, "summary-"), 10, 64)
}

// fetchGitHubAPI calls the GitHub REST API with auth headers.
func fetchGitHubAPI(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	u := ghAPIBase + endpoint
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "gbot/1.0")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if token := os.Getenv("GH_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// fetchGitHubURL fetches from an arbitrary URL with GitHub auth headers and follows redirects.
func fetchGitHubURL(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "gbot/1.0")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if token := os.Getenv("GH_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// commentItem is a minimal GitHub comment representation for pagination.
type commentItem struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	User      *struct {
		Login string `json:"login"`
	} `json:"user"`
}

// fetchAllComments fetches all comments for an issue/PR with pagination.
func fetchAllComments(ctx context.Context, client *http.Client, owner, repo string, issueNumber int) ([]commentItem, error) {
	var allComments []commentItem
	page := 1
	perPage := 100
	for {
		data, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d&page=%d", owner, repo, issueNumber, perPage, page))
		if err != nil {
			break
		}
		var pageComments []commentItem
		if err := json.Unmarshal(data, &pageComments); err != nil {
			break
		}
		if len(pageComments) == 0 {
			break
		}
		allComments = append(allComments, pageComments...)
		if len(pageComments) < perPage {
			break
		}
		page++
	}
	return allComments, nil
}

// --- Render functions ---

func renderRepo(ctx context.Context, client *http.Client, gh *ghURL) (*Result, error) {
	// Fetch repo info
	repoData, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s", gh.owner, gh.repo))
	if err != nil {
		return nil, err
	}
	var repo struct {
		Description string `json:"description"`
		Stars       int    `json:"stargazers_count"`
		Forks       int    `json:"forks_count"`
		OpenIssues  int    `json:"open_issues_count"`
		Language    string `json:"language"`
		License     *struct {
			SPDXID string `json:"spdx_id"`
		} `json:"license"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(repoData, &repo); err != nil {
		return nil, fmt.Errorf("parse repo: %w", err)
	}

	// Fetch README
	readmeData, readmeErr := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/readme", gh.owner, gh.repo))

	// Fetch file tree (top-level)
	treeData, treeErr := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", gh.owner, gh.repo, repo.DefaultBranch))

	var md strings.Builder
	fmt.Fprintf(&md, "# %s/%s\n\n", gh.owner, gh.repo)
	if repo.Description != "" {
		fmt.Fprintf(&md, "%s\n\n", repo.Description)
	}
	fmt.Fprintf(&md, "Stars: %d · Forks: %d · Issues: %d\n", repo.Stars, repo.Forks, repo.OpenIssues)
	if repo.Language != "" {
		fmt.Fprintf(&md, "Language: %s\n", repo.Language)
	}
	if repo.License != nil && repo.License.SPDXID != "" {
		fmt.Fprintf(&md, "License: %s\n", repo.License.SPDXID)
	}
	md.WriteString("\n---\n\n")

	// File tree
	md.WriteString("## Files\n\n")
	if treeErr == nil {
		var tree struct {
			Tree []struct {
				Path string `json:"path"`
				Type string `json:"type"`
			} `json:"tree"`
		}
		if err := json.Unmarshal(treeData, &tree); err == nil {
			// Count total files/dirs and show top-level entries
			dirCount := 0
			fileCount := 0
			for _, entry := range tree.Tree {
				if !strings.Contains(entry.Path, "/") {
					if entry.Type == "tree" {
						fmt.Fprintf(&md, "[dir] %s/\n", entry.Path)
					} else {
						fmt.Fprintf(&md, "      %s\n", entry.Path)
					}
				} else if entry.Type == "blob" {
					fileCount++
				} else {
					dirCount++
				}
			}
			if fileCount > 0 || dirCount > 0 {
				fmt.Fprintf(&md, "... and %d more files\n\n", fileCount+dirCount)
			}
		}
	} else {
		md.WriteString("(unable to fetch tree)\n\n")
	}

	// README
	md.WriteString("## README\n\n")
	if readmeErr == nil {
		var readme struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(readmeData, &readme); err == nil && readme.Encoding == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(readme.Content)
			if err == nil {
				md.WriteString(string(decoded))
				md.WriteString("\n")
			}
		}
	} else {
		md.WriteString("(no README found)\n")
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "github-api",
		Notes:       []string{fmt.Sprintf("Fetched via GitHub REST API: %s/%s", gh.owner, gh.repo)},
	}, nil
}

func renderBlob(ctx context.Context, client *http.Client, gh *ghURL) (*Result, error) {
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", gh.owner, gh.repo, gh.ref, gh.path)
	data, err := fetchBytes(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}

	content := string(data)
	// If it looks like binary, truncate the output.
	if isBinary(data) {
		content = "(binary file)"
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# %s/%s/%s\n\n", gh.owner, gh.repo, gh.path)
	fmt.Fprintf(&md, "**Branch:** %s\n\n", gh.ref)
	md.WriteString("```\n")
	md.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		md.WriteString("\n")
	}
	md.WriteString("```\n")

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "github-raw",
		Notes:       []string{fmt.Sprintf("Fetched raw content from %s/%s", gh.owner, gh.repo)},
	}, nil
}

func renderTree(ctx context.Context, client *http.Client, gh *ghURL) (*Result, error) {
	// Fetch directory contents via GitHub API
	ref := gh.ref
	if ref == "" {
		ref = "HEAD"
	}
	path := gh.path
	if path == "" {
		path = ""
	}

	contentsData, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", gh.owner, gh.repo, url.PathEscape(path), url.QueryEscape(ref)))
	if err != nil {
		return nil, err
	}

	displayPath := path
	if displayPath == "" {
		displayPath = "root"
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# %s/%s/%s\n\n", gh.owner, gh.repo, displayPath)
	fmt.Fprintf(&md, "**Branch:** %s\n\n", ref)
	md.WriteString("## Contents\n\n")

	// The API returns a JSON array for directories
	var items []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(contentsData, &items); err != nil {
		// Might be a single file, not a directory
		return nil, nil
	}

	for _, item := range items {
		if item.Type == "dir" {
			fmt.Fprintf(&md, "[dir] %s/\n", item.Name)
		} else {
			fmt.Fprintf(&md, "      %s\n", item.Name)
		}
	}

	// Try to find and render a README in the same directory
	if path != "" {
		readmeData, readmeErr := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/readme/%s?ref=%s", gh.owner, gh.repo, url.PathEscape(path), url.QueryEscape(ref)))
		if readmeErr == nil {
			var readme struct {
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
			}
			if err := json.Unmarshal(readmeData, &readme); err == nil && readme.Encoding == "base64" {
				decoded, err := base64.StdEncoding.DecodeString(readme.Content)
				if err == nil {
					md.WriteString("\n---\n\n## README\n\n")
					md.WriteString(string(decoded))
					md.WriteString("\n")
				}
			}
		}
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "github-api",
		Notes:       []string{fmt.Sprintf("Fetched directory listing for %s/%s/%s", gh.owner, gh.repo, displayPath)},
	}, nil
}

func renderIssue(ctx context.Context, client *http.Client, gh *ghURL, isPR bool) (*Result, error) {
	// For PRs, use /pulls endpoint for the main item but /issues for comments
	itemType := "issues"
	if isPR {
		itemType = "pulls"
	}

	itemData, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/%s/%d", gh.owner, gh.repo, itemType, gh.number))
	if err != nil {
		return nil, err
	}

	var item struct {
		Title     string `json:"title"`
		State     string `json:"state"`
		Number    int    `json:"number"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		User      *struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(itemData, &item); err != nil {
		return nil, fmt.Errorf("parse issue: %w", err)
	}

	// Fetch all comments (with pagination).
	comments, err := fetchAllComments(ctx, client, gh.owner, gh.repo, gh.number)
	if err != nil {
		comments = nil
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# %s\n\n", item.Title)
	fmt.Fprintf(&md, "**#%d** · %s", item.Number, item.State)

	user := "unknown"
	if item.User != nil {
		user = item.User.Login
	}
	fmt.Fprintf(&md, " · opened by @%s\n", user)

	createdAt := formatGHTime(item.CreatedAt)
	updatedAt := formatGHTime(item.UpdatedAt)
	fmt.Fprintf(&md, "Created: %s · Updated: %s\n", createdAt, updatedAt)

	if len(item.Labels) > 0 {
		labels := make([]string, len(item.Labels))
		for i, l := range item.Labels {
			labels[i] = l.Name
		}
		fmt.Fprintf(&md, "Labels: %s\n", strings.Join(labels, ", "))
	}

	md.WriteString("\n---\n\n")
	if item.Body != "" {
		md.WriteString(item.Body)
		md.WriteString("\n")
	} else {
		md.WriteString("(no description)\n")
	}

	md.WriteString("\n---\n\n")
	fmt.Fprintf(&md, "## Comments (%d)\n\n", len(comments))

	for _, c := range comments {
		commentUser := "unknown"
		if c.User != nil {
			commentUser = c.User.Login
		}
		fmt.Fprintf(&md, "### @%s · %s\n\n", commentUser, formatGHTime(c.CreatedAt))
		md.WriteString(c.Body)
		md.WriteString("\n\n---\n\n")
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "github-api",
		Notes:       []string{fmt.Sprintf("Fetched issue/PR #%d from %s/%s", gh.number, gh.owner, gh.repo)},
	}, nil
}

func renderIssuesList(ctx context.Context, client *http.Client, gh *ghURL) (*Result, error) {
	data, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/issues?state=open&per_page=30", gh.owner, gh.repo))
	if err != nil {
		return nil, err
	}

	var issues []struct {
		Title     string `json:"title"`
		Number    int    `json:"number"`
		CreatedAt string `json:"created_at"`
		User      *struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Comments int `json:"comments"`
	}
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("parse issues: %w", err)
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# %s/%s - Open Issues\n\n", gh.owner, gh.repo)

	for _, issue := range issues {
		user := "unknown"
		if issue.User != nil {
			user = issue.User.Login
		}
		labels := make([]string, len(issue.Labels))
		for i, l := range issue.Labels {
			labels[i] = l.Name
		}
		labelStr := ""
		if len(labels) > 0 {
			labelStr = fmt.Sprintf(" [%s]", strings.Join(labels, ", "))
		}
		fmt.Fprintf(&md, "- **#%d** %s%s\n", issue.Number, issue.Title, labelStr)
		fmt.Fprintf(&md, "  by @%s · %d comments · %s\n", user, issue.Comments, formatGHTime(issue.CreatedAt))
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "github-api",
		Notes:       []string{fmt.Sprintf("Listed open issues for %s/%s", gh.owner, gh.repo)},
	}, nil
}

func renderActionsRun(ctx context.Context, client *http.Client, gh *ghURL) (*Result, error) {
	runData, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/actions/runs/%d", gh.owner, gh.repo, gh.runID))
	if err != nil {
		return nil, err
	}

	var run struct {
		Name       string `json:"name"`
		RunNumber  int    `json:"run_number"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HeadBranch string `json:"head_branch"`
		HeadSha    string `json:"head_sha"`
		Event      string `json:"event"`
		Actor      *struct {
			Login string `json:"login"`
		} `json:"actor"`
		CreatedAt    string `json:"created_at"`
		UpdatedAt    string `json:"updated_at"`
		HTMLURL      string `json:"html_url"`
		DisplayTitle string `json:"display_title"`
	}
	if err := json.Unmarshal(runData, &run); err != nil {
		return nil, fmt.Errorf("parse run: %w", err)
	}

	// Fetch jobs
	jobsData, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=100", gh.owner, gh.repo, gh.runID))
	var jobsResp struct {
		Jobs []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Status      string `json:"status"`
			Conclusion  string `json:"conclusion"`
			StartedAt   string `json:"started_at"`
			CompletedAt string `json:"completed_at"`
			Steps       []struct {
				Number      int    `json:"number"`
				Name        string `json:"name"`
				Status      string `json:"status"`
				Conclusion  string `json:"conclusion"`
				StartedAt   string `json:"started_at"`
				CompletedAt string `json:"completed_at"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err == nil {
		_ = json.Unmarshal(jobsData, &jobsResp)
	}

	var md strings.Builder
	title := run.DisplayTitle
	if title == "" {
		title = run.Name
	}
	fmt.Fprintf(&md, "# %s\n\n", title)

	fmt.Fprintf(&md, "**Workflow:** %s\n", run.Name)
	conclusion := run.Status
	if run.Conclusion != "" {
		conclusion = run.Status + " (" + run.Conclusion + ")"
	}
	fmt.Fprintf(&md, "**Run:** #%d · %s\n", run.RunNumber, conclusion)

	sha := run.HeadSha
	if len(sha) > 7 {
		sha = sha[:7]
	}
	fmt.Fprintf(&md, "**Branch:** %s @ %s\n", run.HeadBranch, sha)

	actor := "unknown"
	if run.Actor != nil {
		actor = run.Actor.Login
	}
	fmt.Fprintf(&md, "**Event:** %s · by @%s\n", run.Event, actor)

	startedAt := formatGHTime(run.CreatedAt)
	duration := computeDuration(run.CreatedAt, run.UpdatedAt)
	fmt.Fprintf(&md, "Started: %s · Duration: %s\n", startedAt, duration)

	if run.HTMLURL != "" {
		fmt.Fprintf(&md, "URL: %s\n", run.HTMLURL)
	}

	md.WriteString("\n---\n\n")
	fmt.Fprintf(&md, "## Jobs (%d)\n\n", len(jobsResp.Jobs))

	for _, job := range jobsResp.Jobs {
		jobDuration := computeDuration(job.StartedAt, job.CompletedAt)
		jobConclusion := job.Status
		if job.Conclusion != "" {
			jobConclusion = job.Status + " (" + job.Conclusion + ")"
		}
		fmt.Fprintf(&md, "### %s — %s (%s)\n\n", job.Name, jobConclusion, jobDuration)

		if len(job.Steps) > 0 {
			md.WriteString("| # | Step | Status | Conclusion | Duration |\n")
			md.WriteString("|---|------|--------|------------|----------|\n")
			for _, step := range job.Steps {
				stepDuration := computeDuration(step.StartedAt, step.CompletedAt)
				fmt.Fprintf(&md, "| %d | %s | %s | %s | %s |\n",
					step.Number, step.Name, step.Status, step.Conclusion, stepDuration)
			}
		}
		md.WriteString("\n")
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "github-api",
		Notes:       []string{fmt.Sprintf("Fetched actions run #%d from %s/%s", gh.runID, gh.owner, gh.repo)},
	}, nil
}

func renderActionsJob(ctx context.Context, client *http.Client, gh *ghURL) (*Result, error) {
	jobData, err := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/actions/jobs/%d", gh.owner, gh.repo, gh.jobID))
	if err != nil {
		return nil, err
	}

	var job struct {
		Name         string `json:"name"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		StartedAt    string `json:"started_at"`
		CompletedAt  string `json:"completed_at"`
		RunnerName   string `json:"runner_name"`
		RunID        int64  `json:"run_id"`
		WorkflowName string `json:"workflow_name"`
		RunNumber    int    `json:"run_number"`
		HeadBranch   string `json:"head_branch"`
		Steps        []struct {
			Number      int    `json:"number"`
			Name        string `json:"name"`
			Status      string `json:"status"`
			Conclusion  string `json:"conclusion"`
			StartedAt   string `json:"started_at"`
			CompletedAt string `json:"completed_at"`
		} `json:"steps"`
		HTMLURL string `json:"html_url"`
	}
	// Some fields may be nested in the run; try fetching run-level info
	if err := json.Unmarshal(jobData, &job); err != nil {
		return nil, fmt.Errorf("parse job: %w", err)
	}

	// Try fetching run info for context
	runData, runErr := fetchGitHubAPI(ctx, client, fmt.Sprintf("/repos/%s/%s/actions/runs/%d", gh.owner, gh.repo, job.RunID))
	var runInfo struct {
		Name         string `json:"name"`
		RunNumber    int    `json:"run_number"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		DisplayTitle string `json:"display_title"`
	}
	if runErr == nil {
		_ = json.Unmarshal(runData, &runInfo)
	}

	// Fetch logs — the logs endpoint returns a 302 redirect to a signed URL,
	// so use fetchGitHubURL which follows redirects with auth headers.
	logData, logErr := fetchGitHubURL(ctx, client, fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%d/logs", ghAPIBase, gh.owner, gh.repo, gh.jobID))

	var md strings.Builder
	fmt.Fprintf(&md, "# %s\n\n", job.Name)

	workflowName := job.WorkflowName
	if workflowName == "" {
		workflowName = runInfo.Name
	}
	if workflowName != "" {
		fmt.Fprintf(&md, "**Workflow:** %s\n", workflowName)
	}

	runNumber := job.RunNumber
	if runNumber == 0 {
		runNumber = runInfo.RunNumber
	}
	runStatus := runInfo.Status
	runConclusion := runInfo.Conclusion
	if runStatus != "" {
		conclusion := runStatus
		if runConclusion != "" {
			conclusion = runStatus + " (" + runConclusion + ")"
		}
		fmt.Fprintf(&md, "**Run:** #%d · %s\n", runNumber, conclusion)
	}

	fmt.Fprintf(&md, "**Branch:** %s\n", job.HeadBranch)
	jobConclusion := job.Status
	if job.Conclusion != "" {
		jobConclusion = job.Status + " (" + job.Conclusion + ")"
	}
	jobDuration := computeDuration(job.StartedAt, job.CompletedAt)
	fmt.Fprintf(&md, "**Job:** %s · %s · %s\n", job.Name, jobConclusion, jobDuration)
	if job.RunnerName != "" {
		fmt.Fprintf(&md, "**Runner:** %s\n", job.RunnerName)
	}

	md.WriteString("URL: https://github.com/")
	md.WriteString(gh.owner)
	md.WriteString("/")
	md.WriteString(gh.repo)
	md.WriteString("/actions/runs/")
	md.WriteString(strconv.FormatInt(job.RunID, 10))
	md.WriteString("\n")

	md.WriteString("\n---\n\n## Steps\n\n")
	md.WriteString("| # | Step | Status | Conclusion | Duration |\n")
	md.WriteString("|---|------|--------|------------|----------|\n")
	for _, step := range job.Steps {
		stepDuration := computeDuration(step.StartedAt, step.CompletedAt)
		fmt.Fprintf(&md, "| %d | %s | %s | %s | %s |\n",
			step.Number, step.Name, step.Status, step.Conclusion, stepDuration)
	}

	if logErr == nil && len(logData) > 0 {
		md.WriteString("\n## Logs\n\n")
		md.WriteString("```\n")
		lines := strings.SplitSeq(string(logData), "\n")
		for line := range lines {
			// Strip ISO timestamp prefix like "2024-01-15T00:00:00.0000000Z "
			line = stripLogTimestamp(line)
			md.WriteString(line)
			md.WriteString("\n")
		}
		md.WriteString("```\n")
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "github-api",
		Notes:       []string{fmt.Sprintf("Fetched job %d from %s/%s", gh.jobID, gh.owner, gh.repo)},
	}, nil
}

// --- Helpers ---

func formatGHTime(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		// Try without timezone
		t, err = time.Parse("2006-01-02T15:04:05Z", iso)
		if err != nil {
			return iso
		}
	}
	return t.Format("2006-01-02")
}

func computeDuration(start, end string) string {
	if start == "" || end == "" {
		return ""
	}
	startT, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return ""
	}
	endT, err := time.Parse(time.RFC3339, end)
	if err != nil {
		return ""
	}
	d := max(endT.Sub(startT).Round(time.Second), 0)
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	if mins >= 60 {
		return fmt.Sprintf("%d:%02d:%02d", mins/60, mins%60, secs)
	}
	return fmt.Sprintf("%d:%02d", mins, secs)
}

// stripLogTimestamp removes ISO timestamp prefix from action log lines.
func stripLogTimestamp(line string) string {
	// GitHub log timestamps look like: "2024-01-15T00:00:00.0000000Z message"
	if len(line) < 30 {
		return line
	}
	// Check if line starts with an ISO-like timestamp
	if line[4] == '-' && line[7] == '-' && line[10] == 'T' {
		if idx := strings.Index(line, " "); idx > 0 && idx < 35 {
			return line[idx+1:]
		}
	}
	return line
}

// isBinary does a simple check for null bytes (binary content indicator).
func isBinary(data []byte) bool {
	// Check first 8KB for null bytes
	checkLen := min(len(data), 8192)
	for i := range checkLen {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
