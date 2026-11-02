package scrapers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ghSrv routes api.github.com and raw.githubusercontent.com paths to canned
// bodies and records the request lines the scraper produced.
func ghSrv(t *testing.T, bodies map[string]string) (*http.Client, *[]string) {
	t.Helper()
	reqs := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		*reqs = append(*reqs, key)
		// Longest matching pattern wins, so a short prefix never shadows a
		// more specific route.
		best, bestLen := "", -1
		for pattern := range bodies {
			if strings.HasPrefix(key, pattern) && len(pattern) > bestLen {
				best, bestLen = pattern, len(pattern)
			}
		}
		if bestLen >= 0 {
			body := bodies[best]
			if body == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"Not Found"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &allowHostsTransport{server: srv,
		hosts: []string{"api.github.com", "raw.githubusercontent.com"}}}, reqs
}

func ghEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
}

func checkResultFields(t *testing.T, r *Result, method string, notes string) {
	t.Helper()
	if r.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", r.ContentType)
	}
	if r.Method != method {
		t.Errorf("Method = %q, want %q", r.Method, method)
	}
	if len(r.Notes) != 1 || r.Notes[0] != notes {
		t.Errorf("Notes = %q, want [%q]", r.Notes, notes)
	}
}

func TestHandleGitHub_RepoFull(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/git/trees/main": `{"tree":[{"path":"src","type":"tree"},{"path":"README.md","type":"blob"},` +
			`{"path":"a/b","type":"blob"},{"path":"c/d","type":"tree"}]}`,
		"/repos/owner/repo/readme": `{"content":"IyBNeSBSZWFkTWU=","encoding":"base64"}`,
		"/repos/owner/repo":        `{"description":"A cool project","stargazers_count":100,"forks_count":10,"open_issues_count":5,"language":"Go","license":{"spdx_id":"MIT"},"default_branch":"main"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo\n\nA cool project\n\nStars: 100 · Forks: 10 · Issues: 5\nLanguage: Go\nLicense: MIT\n" +
		"\n---\n\n## Files\n\n[dir] src/\n      README.md\n... and 2 more files\n\n## README\n\n# My ReadMe\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	checkResultFields(t, result, "github-api", "Fetched via GitHub REST API: owner/repo")
	if (*reqs)[2] != "/repos/owner/repo/git/trees/main?recursive=1" {
		t.Errorf("tree request = %q", (*reqs)[2])
	}
}

func TestHandleGitHub_RepoWithoutOptionalParts(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/git/trees/main": "",
		"/repos/owner/repo/readme":         "",
		"/repos/owner/repo":                `{"stargazers_count":0,"forks_count":0,"open_issues_count":0,"default_branch":"main"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo\n\nStars: 0 · Forks: 0 · Issues: 0\n\n---\n\n## Files\n\n(unable to fetch tree)\n\n" +
		"## README\n\n(no README found)\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// A tree with only top-level entries gets no "and N more files" line.
func TestHandleGitHub_RepoTreeWithoutNestedEntries(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/git/trees/main": `{"tree":[{"path":"src","type":"tree"},{"path":"README.md","type":"blob"}]}`,
		"/repos/owner/repo/readme":         `{"content":"IyBNeSBSZWFkTWU=","encoding":"not-base64"}`,
		"/repos/owner/repo":                `{"default_branch":"main"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo\n\nStars: 0 · Forks: 0 · Issues: 0\n\n---\n\n## Files\n\n[dir] src/\n      README.md\n## README\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleGitHub_BlobExact(t *testing.T) {
	ghEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()
	client := &http.Client{Transport: &allowHostsTransport{server: srv,
		hosts: []string{"api.github.com", "raw.githubusercontent.com"}}}

	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/dir/main.go"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo/dir/main.go\n\n**Branch:** main\n\n```\nhello\n```\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	checkResultFields(t, result, "github-raw", "Fetched raw content from owner/repo")
}

func TestHandleGitHub_BlobBinaryExact(t *testing.T) {
	ghEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "a\x00b")
	}))
	defer srv.Close()
	client := &http.Client{Transport: &allowHostsTransport{server: srv,
		hosts: []string{"api.github.com", "raw.githubusercontent.com"}}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/x.bin"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo/x.bin\n\n**Branch:** main\n\n```\n(binary file)\n```\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleGitHub_TreeWithReadme(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/contents/src": `[{"name":"main.go","type":"file"},{"name":"sub","type":"dir"}]`,
		"/repos/owner/repo/readme/src":   `{"content":"ZGlyIHJlYWRtZQ==","encoding":"base64"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/tree/main/src"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo/src\n\n**Branch:** main\n\n## Contents\n\n      main.go\n[dir] sub/\n" +
		"\n---\n\n## README\n\ndir readme\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	checkResultFields(t, result, "github-api", "Fetched directory listing for owner/repo/src")
	if (*reqs)[0] != "/repos/owner/repo/contents/src?ref=main" {
		t.Errorf("contents request = %q", (*reqs)[0])
	}
}

func TestHandleGitHub_TreeRootExact(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/contents": `[{"name":"README.md","type":"file"}]`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/tree/main"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo/root\n\n**Branch:** main\n\n## Contents\n\n      README.md\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if (*reqs)[0] != "/repos/owner/repo/contents/?ref=main" {
		t.Errorf("contents request = %q", (*reqs)[0])
	}
}

// A contents path that resolves to a single file is not a listing.
func TestHandleGitHub_TreeSingleFile(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/contents/main.go": `{"name":"main.go","type":"file"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/main.go"), client, nil)
	if err == nil && result != nil {
		t.Errorf("expected nil for a file listing, got %q", result.Content)
	}
}

func TestHandleGitHub_IssueFull(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/issues/42/comments": `[{"user":{"login":"alice"},"body":"Comment 1","created_at":"2024-01-02T00:00:00Z"}]`,
		"/repos/owner/repo/issues/42":          `{"title":"Test issue","state":"open","number":42,"body":"Issue body","created_at":"2024-01-01T10:00:00Z","updated_at":"2024-01-02T10:00:00Z","user":{"login":"bob"},"labels":[{"name":"bug"},{"name":"docs"}]}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Test issue\n\n**#42** · open · opened by @bob\nCreated: 2024-01-01 · Updated: 2024-01-02\n" +
		"Labels: bug, docs\n\n---\n\nIssue body\n\n---\n\n## Comments (1)\n\n" +
		"### @alice · 2024-01-02\n\nComment 1\n\n---\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	checkResultFields(t, result, "github-api", "Fetched issue/PR #42 from owner/repo")
}

func TestHandleGitHub_IssueWithoutBodyOrUser(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/issues/42/comments": `[]`,
		"/repos/owner/repo/issues/42":          `{"title":"Bare","state":"closed","number":42,"body":"","user":null,"labels":[]}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Bare\n\n**#42** · closed · opened by @unknown\nCreated:  · Updated: \n\n---\n\n(no description)\n" +
		"\n---\n\n## Comments (0)\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// Comment pages are followed until a short page arrives.
func TestHandleGitHub_IssueCommentPagination(t *testing.T) {
	ghEnv(t)
	full := make([]string, 100)
	for i := range full {
		full[i] = `{"body":"c","created_at":"2024-01-01T00:00:00Z"}`
	}
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/issues/42/comments?per_page=100&page=1": "[" + strings.Join(full, ",") + "]",
		"/repos/owner/repo/issues/42/comments?per_page=100&page=2": `[{"body":"last","created_at":"2024-01-01T00:00:00Z"}]`,
		"/repos/owner/repo/issues/42":                              `{"title":"T","number":42,"body":"b"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "## Comments (101)\n\n") {
		t.Errorf("missing paginated comment count in %q", result.Content)
	}
	if len(*reqs) != 3 {
		t.Errorf("requests = %q, want issue + two comment pages", *reqs)
	}
}

func TestHandleGitHub_PullUsesPullsEndpoint(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/pulls/42":           `{"title":"PR title","state":"open","number":42,"body":"pr body"}`,
		"/repos/owner/repo/issues/42/comments": `[]`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/pull/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if (*reqs)[0] != "/repos/owner/repo/pulls/42" {
		t.Errorf("first request = %q, want /repos/owner/repo/pulls/42", (*reqs)[0])
	}
	if !strings.Contains(result.Content, "# PR title\n\n**#42** · open · opened by @unknown\n") {
		t.Errorf("Content = %q", result.Content)
	}
}

func TestHandleGitHub_IssuesListExact(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/issues?state=open&per_page=30": `[{"title":"First issue","number":1,"created_at":"2024-01-01T00:00:00Z","user":{"login":"alice"},"labels":[{"name":"bug"}],"comments":3},` +
			`{"title":"Second","number":2,"created_at":"2024-01-02T00:00:00Z","user":null,"labels":[],"comments":0}]`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# owner/repo - Open Issues\n\n- **#1** First issue [bug]\n  by @alice · 3 comments · 2024-01-01\n" +
		"- **#2** Second\n  by @unknown · 0 comments · 2024-01-02\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	checkResultFields(t, result, "github-api", "Listed open issues for owner/repo")
	if (*reqs)[0] != "/repos/owner/repo/issues?state=open&per_page=30" {
		t.Errorf("request = %q", (*reqs)[0])
	}
}

func TestHandleGitHub_ActionsRunExact(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/actions/runs/7/jobs": `{"jobs":[{"id":1,"name":"build","status":"completed","conclusion":"failure",` +
			`"started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:01:30Z","steps":[{"number":1,"name":"checkout",` +
			`"status":"completed","conclusion":"success","started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:00:10Z"}]}]}`,
		"/repos/owner/repo/actions/runs/7": `{"name":"CI","run_number":12,"status":"completed","conclusion":"success",` +
			`"head_branch":"main","head_sha":"abcdef0123456789","event":"push","actor":{"login":"octo"},` +
			`"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T01:02:03Z",` +
			`"html_url":"https://github.com/o/r/actions/runs/7","display_title":"Fix thing"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/7"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Fix thing\n\n**Workflow:** CI\n**Run:** #12 · completed (success)\n**Branch:** main @ abcdef0\n" +
		"**Event:** push · by @octo\nStarted: 2024-01-01 · Duration: 1:02:03\n" +
		"URL: https://github.com/o/r/actions/runs/7\n\n---\n\n## Jobs (1)\n\n" +
		"### build — completed (failure) (1:30)\n\n" +
		"| # | Step | Status | Conclusion | Duration |\n|---|------|--------|------------|----------|\n" +
		"| 1 | checkout | completed | success | 0:10 |\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	checkResultFields(t, result, "github-api", "Fetched actions run #7 from owner/repo")
}

func TestHandleGitHub_ActionsRunMinimal(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/actions/runs/7/jobs": `{"jobs":[]}`,
		"/repos/owner/repo/actions/runs/7":      `{"name":"CI","run_number":1,"status":"queued","head_sha":"abc"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/7"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# CI\n\n**Workflow:** CI\n**Run:** #1 · queued\n**Branch:**  @ abc\n**Event:**  · by @unknown\n" +
		"Started:  · Duration: \n\n---\n\n## Jobs (0)\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleGitHub_ActionsJobFull(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/actions/jobs/5/logs": "2024-01-15T00:00:00.0000000Z first line\nsecond line\n",
		"/repos/owner/repo/actions/jobs/5": `{"name":"build","status":"completed","conclusion":"success",` +
			`"started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:02:00Z","runner_name":"ubuntu","run_id":9,` +
			`"workflow_name":"CI","run_number":3,"head_branch":"main","steps":[{"number":1,"name":"checkout",` +
			`"status":"completed","conclusion":"success","started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:00:05Z"}]}`,
		"/repos/owner/repo/actions/runs/9": `{"name":"CI","run_number":3,"status":"completed","conclusion":"success"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/7/job/5"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# build\n\n**Workflow:** CI\n**Run:** #3 · completed (success)\n**Branch:** main\n" +
		"**Job:** build · completed (success) · 2:00\n**Runner:** ubuntu\n" +
		"URL: https://github.com/owner/repo/actions/runs/9\n\n---\n\n## Steps\n\n" +
		"| # | Step | Status | Conclusion | Duration |\n|---|------|--------|------------|----------|\n" +
		"| 1 | checkout | completed | success | 0:05 |\n\n## Logs\n\n```\nfirst line\nsecond line\n\n```\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	checkResultFields(t, result, "github-api", "Fetched job 5 from owner/repo")
	if (*reqs)[2] != "/repos/owner/repo/actions/jobs/5/logs" {
		t.Errorf("logs request = %q", (*reqs)[2])
	}
}

// The attempts/#summary-NNN fragment selects the job endpoint.
func TestHandleGitHub_ActionsJobFragmentRoute(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/owner/repo/actions/jobs/5": `{"name":"j","status":"completed","run_id":9}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/7/attempts/1#summary-5"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if (*reqs)[0] != "/repos/owner/repo/actions/jobs/5" {
		t.Errorf("first request = %q", (*reqs)[0])
	}
}

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		raw     string
		urlType string
		owner   string
		repo    string
		ref     string
		path    string
		number  int
		runID   int64
		jobID   int64
		nil_    bool
	}{
		{"https://github.com/owner/repo", "repo", "owner", "repo", "", "", 0, 0, 0, false},
		{"https://github.com/owner/repo/blob/main/a/b.go", "blob", "owner", "repo", "main", "a/b.go", 0, 0, 0, false},
		{"https://github.com/owner/repo/tree/main/src", "tree", "owner", "repo", "main", "src", 0, 0, 0, false},
		{"https://github.com/owner/repo/issues/42", "issue", "owner", "repo", "", "", 42, 0, 0, false},
		{"https://github.com/owner/repo/issues", "issues", "owner", "repo", "", "", 0, 0, 0, false},
		{"https://github.com/owner/repo/issues/notanumber", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com/owner/repo/pull/7", "pull", "owner", "repo", "", "", 7, 0, 0, false},
		{"https://github.com/owner/repo/actions/runs/12", "actions-run", "owner", "repo", "", "", 0, 12, 0, false},
		{"https://github.com/owner/repo/actions/runs/12/job/34", "actions-job", "owner", "repo", "", "", 0, 12, 34, false},
		{"https://github.com/owner/repo/actions/runs/12/attempts/1#summary-34", "actions-job", "owner", "repo", "", "", 0, 12, 34, false},
		{"https://github.com/owner/repo/actions/runs/notanumber", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com/owner/repo/actions/runs/12/job/notanumber", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com/owner/repo/actions/other", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com/owner/repo/discussions/9", "discussion", "owner", "repo", "", "", 9, 0, 0, false},
		{"https://github.com/owner/repo/discussions", "discussions", "owner", "repo", "", "", 0, 0, 0, false},
		{"https://github.com/owner/repo/blob/main", "blob", "owner", "repo", "main", "", 0, 0, 0, false},
		{"https://github.com/owner/repo/tree", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com/owner/repo/pull", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com/owner", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com/owner/", "", "", "", "", "", 0, 0, 0, true},
		{"https://github.com//repo", "", "", "", "", "", 0, 0, 0, true},
		{"https://gist.github.com/owner/repo", "repo", "owner", "repo", "", "", 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := parseGitHubURL(mustParseURL(t, tt.raw))
			if tt.nil_ {
				if got != nil {
					t.Errorf("parseGitHubURL(%q) = %+v, want nil", tt.raw, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseGitHubURL(%q) = nil", tt.raw)
			}
			if got.urlType != tt.urlType || got.owner != tt.owner || got.repo != tt.repo ||
				got.ref != tt.ref || got.path != tt.path || got.number != tt.number ||
				got.runID != tt.runID || got.jobID != tt.jobID {
				t.Errorf("parseGitHubURL(%q) = %+v", tt.raw, got)
			}
		})
	}
}

func TestExtractJobFromFragment_Variants(t *testing.T) {
	id, err := extractJobFromFragment("summary-12345")
	if err != nil || id != 12345 {
		t.Errorf("extractJobFromFragment() = %d, %v", id, err)
	}
	id, err = extractJobFromFragment("jobs-1")
	if err == nil {
		t.Errorf("extractJobFromFragment(jobs-1) = %d, want an error", id)
	}
	id, err = extractJobFromFragment("summary-abc")
	if err == nil {
		t.Errorf("extractJobFromFragment(summary-abc) = %d, want an error", id)
	}
}

func TestFetchGitHubAPI_AuthHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.github.com"}}

	t.Setenv("GITHUB_TOKEN", "tok-a")
	t.Setenv("GH_TOKEN", "tok-b")
	if _, err := fetchGitHubAPI(context.Background(), client, "/repos/o/r"); err != nil {
		t.Fatalf("error = %v", err)
	}
	if got.Get("Authorization") != "Bearer tok-a" {
		t.Errorf("Authorization = %q, want Bearer tok-a", got.Get("Authorization"))
	}
	// GitHub answers 403 without a User-Agent and selects the API revision from
	// Accept, so both are part of the request contract, not decoration.
	if got.Get("Accept") != "application/vnd.github.v3+json" {
		t.Errorf("Accept = %q, want application/vnd.github.v3+json", got.Get("Accept"))
	}
	if got.Get("User-Agent") != "gbot/1.0" {
		t.Errorf("User-Agent = %q, want gbot/1.0", got.Get("User-Agent"))
	}

	t.Setenv("GITHUB_TOKEN", "")
	if _, err := fetchGitHubAPI(context.Background(), client, "/repos/o/r"); err != nil {
		t.Fatalf("error = %v", err)
	}
	if got.Get("Authorization") != "Bearer tok-b" {
		t.Errorf("Authorization = %q, want Bearer tok-b (GH_TOKEN fallback)", got.Get("Authorization"))
	}

	t.Setenv("GH_TOKEN", "")
	if _, err := fetchGitHubAPI(context.Background(), client, "/repos/o/r"); err != nil {
		t.Fatalf("error = %v", err)
	}
	if got.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want empty", got.Get("Authorization"))
	}
}

// fetchGitHubURL talks to hosts other than api.github.com (raw content, redirects),
// so its header set is a separate contract from fetchGitHubAPI's.
func TestFetchGitHubURL_Headers(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "tok-a")
	t.Setenv("GH_TOKEN", "tok-b")
	if _, err := fetchGitHubURL(context.Background(), srv.Client(), srv.URL+"/x"); err != nil {
		t.Fatalf("fetchGitHubURL() error = %v", err)
	}
	if got.Get("Accept") != "application/vnd.github.v3+json" {
		t.Errorf("Accept = %q, want application/vnd.github.v3+json", got.Get("Accept"))
	}
	if got.Get("User-Agent") != "gbot/1.0" {
		t.Errorf("User-Agent = %q, want gbot/1.0", got.Get("User-Agent"))
	}
	if got.Get("Authorization") != "Bearer tok-a" {
		t.Errorf("Authorization = %q, want Bearer tok-a", got.Get("Authorization"))
	}

	t.Setenv("GITHUB_TOKEN", "")
	if _, err := fetchGitHubURL(context.Background(), srv.Client(), srv.URL+"/x"); err != nil {
		t.Fatalf("fetchGitHubURL() error = %v", err)
	}
	if got.Get("Authorization") != "Bearer tok-b" {
		t.Errorf("Authorization = %q, want Bearer tok-b (GH_TOKEN fallback)", got.Get("Authorization"))
	}

	t.Setenv("GH_TOKEN", "")
	if _, err := fetchGitHubURL(context.Background(), srv.Client(), srv.URL+"/x"); err != nil {
		t.Fatalf("fetchGitHubURL() error = %v", err)
	}
	if got.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want empty", got.Get("Authorization"))
	}
}

func TestFetchGitHubAPI_ErrorIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "  rate limited  ")
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.github.com"}}
	ghEnv(t)

	_, err := fetchGitHubAPI(context.Background(), client, "/repos/o/r")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "GitHub API 403: rate limited" {
		t.Errorf("error = %q, want %q", err.Error(), "GitHub API 403: rate limited")
	}
}

func TestFetchGitHubAPI_TransportError(t *testing.T) {
	sentinel := errors.New("gh unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	_, err := fetchGitHubAPI(context.Background(), client, "/repos/o/r")
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
}

func TestFetchGitHubURL_ErrorAndChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.github.com"}}
	ghEnv(t)
	_, err := fetchGitHubURL(context.Background(), client, "https://api.github.com/repos/o/r/actions/jobs/1/logs")
	if err == nil {
		t.Fatal("expected error")
	}
	want := `HTTP 404 from https://api.github.com/repos/o/r/actions/jobs/1/logs`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestFormatGHTime(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"2024-01-15T10:20:30Z", "2024-01-15"},
		{"2024-01-15T10:20:30+02:00", "2024-01-15"},
		{"2024-01-15T10:20:30", "2024-01-15T10:20:30"},
		{"2024-01-15T10:20:30Z", "2024-01-15"},
		{"not a time", "not a time"},
		{"", ""},
	}
	for _, tt := range tests {
		got := formatGHTime(tt.in)
		if got != tt.want {
			t.Errorf("formatGHTime(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestComputeDuration(t *testing.T) {
	tests := []struct {
		start string
		end   string
		want  string
	}{
		{"2024-01-01T00:00:00Z", "2024-01-01T00:00:09Z", "0:09"},
		{"2024-01-01T00:00:00Z", "2024-01-01T00:01:30Z", "1:30"},
		{"2024-01-01T00:00:00Z", "2024-01-01T00:59:59Z", "59:59"},
		// Exactly 60 minutes is the hour boundary: it must roll into h:mm:ss
		// and the minute field must be reduced modulo 60.
		{"2024-01-01T00:00:00Z", "2024-01-01T01:00:00Z", "1:00:00"},
		// 118 minutes separates the hour divisor from any nearby divisor.
		{"2024-01-01T00:00:00Z", "2024-01-01T01:58:00Z", "1:58:00"},
		{"2024-01-01T00:00:00Z", "2024-01-01T01:02:03Z", "1:02:03"},
		{"2024-01-01T00:00:00Z", "2024-01-02T01:02:03Z", "25:02:03"},
		{"2024-01-01T00:01:00Z", "2024-01-01T00:00:00Z", "0:00"},
		{"", "2024-01-01T00:00:00Z", ""},
		{"2024-01-01T00:00:00Z", "", ""},
		{"bad", "2024-01-01T00:00:00Z", ""},
		{"2024-01-01T00:00:00Z", "bad", ""},
	}
	for _, tt := range tests {
		got := computeDuration(tt.start, tt.end)
		if got != tt.want {
			t.Errorf("computeDuration(%q, %q) = %q, want %q", tt.start, tt.end, got, tt.want)
		}
	}
}

func TestStripLogTimestamp_Boundaries(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"2024-01-15T00:00:00.0000000Z first line", "first line"},
		{"2024-01-15T00:00:00.0000000Z", "2024-01-15T00:00:00.0000000Z"},
		{"2024-01-15T00:00:00.0000000Z " + strings.Repeat("x", 40), strings.Repeat("x", 40)},
		{"20240115T00:00:00.0000000Z not a stamp", "20240115T00:00:00.0000000Z not a stamp"},
		{"short", "short"},
		// The stamp shape is three fixed positions; each one has to fail on its
		// own for the line to be left alone.
		{"aaaaaaaaaaT bbbbbbbbbbbbbbbbbbbbbbbb", "aaaaaaaaaaT bbbbbbbbbbbbbbbbbbbbbbbb"},
		{"2024-xxx15T00:00:00Z message padding", "2024-xxx15T00:00:00Z message padding"},
		{"2024-01-15 00:00:00Z message padding!!", "2024-01-15 00:00:00Z message padding!!"},
		{"2024x01-15T00:00:00Z message padding!!", "2024x01-15T00:00:00Z message padding!!"},
		// The separator window is (0, 35): index 1 and index 34 are inside it,
		// index 0 and index 35 are outside, and a stamp longer than the window
		// is not a stamp at all.
		{"a bc-de-fgTijklmnopqrstuvwxyz0123456789", "bc-de-fgTijklmnopqrstuvwxyz0123456789"},
		{"2024-01-15T00:00:00.0000000000000Z msg", "msg"},
		{" 123-56-89T00:00:00Z message padding", " 123-56-89T00:00:00Z message padding"},
		{"2024-01-15T00:00:00.00000000000000Z msg", "2024-01-15T00:00:00.00000000000000Z msg"},
		{"2024-01-15T00:00:00.0000000000000000000Z msg", "2024-01-15T00:00:00.0000000000000000000Z msg"},
	}
	for _, tt := range tests {
		got := stripLogTimestamp(tt.in)
		if got != tt.want {
			t.Errorf("stripLogTimestamp(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsBinaryNullByteWindow(t *testing.T) {
	within := append([]byte(strings.Repeat("a", 8191)), 0)
	if !isBinary(within) {
		t.Error("null byte at index 8191 should count as binary")
	}
	outside := append([]byte(strings.Repeat("a", 8192)), 0)
	if isBinary(outside) {
		t.Error("null byte at index 8192 is outside the window")
	}
}

func TestHandleGitHub_UnhandledShapes(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{})
	for _, raw := range []string{
		"https://example.com/owner/repo",
		"https://github.com/owner",
		"https://github.com/owner/repo/discussions/9",
		"https://github.com/owner/repo/discussions",
		"https://github.com/owner/repo/actions/other",
		"https://github.com/owner/repo/releases",
	} {
		result, err := HandleGitHub(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestFetchAllComments_StopsOnShortPage(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/o/r/issues/1/comments?per_page=100&page=1": `[{"body":"a"},{"body":"b"}]`,
	})
	got, err := fetchAllComments(context.Background(), client, "o", "r", 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(got) != 2 || got[0].Body != "a" || got[1].Body != "b" {
		t.Errorf("comments = %+v", got)
	}
	if len(*reqs) != 1 {
		t.Errorf("requests = %q, want a single page", *reqs)
	}
}

func TestFetchAllComments_BadJSONStops(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{"/repos/o/r/issues/1/comments": `5`})
	got, err := fetchAllComments(context.Background(), client, "o", "r", 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got != nil {
		t.Errorf("comments = %+v, want nil", got)
	}
}

func TestFetchAllComments_TransportErrorStops(t *testing.T) {
	ghEnv(t)
	client := &http.Client{Transport: errTransport{err: errors.New("down")}}
	got, err := fetchAllComments(context.Background(), client, "o", "r", 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got != nil {
		t.Errorf("comments = %+v, want nil", got)
	}
}

func TestHandleGitHub_APIFetchErrors(t *testing.T) {
	ghEnv(t)
	cases := []struct {
		name    string
		url     string
		pattern string
	}{
		{"tree", "https://github.com/owner/repo/tree/main/src", "/repos/owner/repo/contents/src"},
		{"issue", "https://github.com/owner/repo/issues/42", "/repos/owner/repo/issues/42"},
		{"issues_list", "https://github.com/owner/repo/issues", "/repos/owner/repo/issues?"},
		{"run", "https://github.com/o/r/actions/runs/7", "/repos/o/r/actions/runs/7"},
		{"job", "https://github.com/o/r/actions/runs/7/job/5", "/repos/o/r/actions/jobs/5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := ghSrv(t, map[string]string{tc.pattern: ""})
			result, err := HandleGitHub(context.Background(), mustParseURL(t, tc.url), client, nil)
			if result != nil {
				t.Fatalf("result = %+v, want nil on API failure", result)
			}
			want := `GitHub API 404: {"message":"Not Found"}`
			if err == nil || err.Error() != want {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestHandleGitHub_ParseErrors(t *testing.T) {
	ghEnv(t)
	cases := []struct {
		name    string
		url     string
		pattern string
		want    string
	}{
		{"repo", "https://github.com/owner/repo", "/repos/owner/repo", "parse repo: "},
		{"issue", "https://github.com/owner/repo/issues/42", "/repos/owner/repo/issues/42", "parse issue: "},
		{"issues_list", "https://github.com/owner/repo/issues", "/repos/owner/repo/issues?", "parse issues: "},
		{"run", "https://github.com/o/r/actions/runs/7", "/repos/o/r/actions/runs/7", "parse run: "},
		{"job", "https://github.com/o/r/actions/runs/7/job/5", "/repos/o/r/actions/jobs/5", "parse job: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := ghSrv(t, map[string]string{tc.pattern: `5`})
			result, err := HandleGitHub(context.Background(), mustParseURL(t, tc.url), client, nil)
			if result != nil {
				t.Fatalf("result = %+v, want nil on parse failure", result)
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("err = %v, want prefix %q", err, tc.want)
			}
			if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
				t.Errorf("err = %v, want a wrapped *json.UnmarshalTypeError", err)
			}
		})
	}
}

func TestHandleGitHub_IssueCommentFetchError(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/owner/repo/issues/42/comments": "",
		"/repos/owner/repo/issues/42":          `{"number":42,"title":"Fix thing","state":"open","user":{"login":"alice"}}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues/42"), client, nil)
	if err != nil {
		t.Fatalf("HandleGitHub() error = %v", err)
	}
	want := "# Fix thing\n\n**#42** · open · opened by @alice\nCreated:  · Updated: \n\n---\n\n(no description)\n\n---\n\n## Comments (0)\n\n"
	if result == nil || result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// A jobs fetch that fails must not render as a run that has no jobs; the two
// states have to stay distinguishable in the markdown.
func TestHandleGitHub_ActionsRunJobsFetchError(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/o/r/actions/runs/7/jobs": "",
		"/repos/o/r/actions/runs/7":      `{"run_number":12,"name":"CI","status":"completed","conclusion":"success"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/actions/runs/7"), client, nil)
	if err != nil {
		t.Fatalf("HandleGitHub() error = %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	want := "# CI\n\n**Workflow:** CI\n**Run:** #12 · completed (success)\n**Branch:**  @ \n" +
		"**Event:**  · by @unknown\nStarted:  · Duration: \n\n---\n\n## Jobs (unknown — fetch failed)\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// A 200 response whose jobs payload cannot be parsed is the other way the jobs
// list can be unknowable; it must not fall back to the "no jobs" rendering.
func TestHandleGitHub_ActionsRunJobsUnparseablePayload(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/o/r/actions/runs/7/jobs": `{"jobs":`,
		"/repos/o/r/actions/runs/7":      `{"run_number":12,"name":"CI","status":"completed","conclusion":"success"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/actions/runs/7"), client, nil)
	if err != nil {
		t.Fatalf("HandleGitHub() error = %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	want := "# CI\n\n**Workflow:** CI\n**Run:** #12 · completed (success)\n**Branch:**  @ \n" +
		"**Event:**  · by @unknown\nStarted:  · Duration: \n\n---\n\n## Jobs (unknown — fetch failed)\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// GitHub hands out run and job IDs that exceed 2^62, so the parse must keep the
// full int64 width; narrowing it turns a real actions URL into an unparsed one.
func TestParseGitHubURL_FullWidthActionIDs(t *testing.T) {
	tests := []struct {
		raw     string
		urlType string
		runID   int64
		jobID   int64
	}{
		{"https://github.com/o/r/actions/runs/4611686018427387904", "actions-run", 4611686018427387904, 0},
		{"https://github.com/o/r/actions/runs/7/job/4611686018427387904", "actions-job", 7, 4611686018427387904},
		{"https://github.com/o/r/actions/runs/7/attempts/1#summary-4611686018427387904", "actions-job", 7, 4611686018427387904},
	}
	for _, tt := range tests {
		got := parseGitHubURL(mustParseURL(t, tt.raw))
		if got == nil {
			t.Fatalf("parseGitHubURL(%q) = nil", tt.raw)
		}
		if got.urlType != tt.urlType || got.runID != tt.runID || got.jobID != tt.jobID {
			t.Errorf("parseGitHubURL(%q) = %+v, want {%s %d %d}", tt.raw, got, tt.urlType, tt.runID, tt.jobID)
		}
	}
}

// An empty path segment is not a repository name; the guard has to look at both
// halves of the split, otherwise "/o//issues/5" becomes an issue in repo "".
func TestParseGitHubURL_EmptySecondSegment(t *testing.T) {
	if got := parseGitHubURL(mustParseURL(t, "https://github.com/o//issues/5")); got != nil {
		t.Errorf("parseGitHubURL(/o//issues/5) = %+v, want nil", got)
	}
}

// The actions branch indexes parts[3]..parts[6]; each length guard is what keeps
// a short path from reading past the slice or from claiming a run that is not
// there.
func TestParseGitHubURL_ActionsShapeGuards(t *testing.T) {
	tests := []struct {
		raw     string
		urlType string
		nil_    bool
	}{
		{"https://github.com/o/r/actions", "", true},
		{"https://github.com/o/r/actions/runs", "", true},
		{"https://github.com/o/r/actions/x/5", "", true},
		{"https://github.com/o/r/actions/runs/5", "actions-run", false},
		{"https://github.com/o/r/actions/runs/5#summary-7", "actions-run", false},
		{"https://github.com/o/r/actions/runs/5/foo/7#summary-9", "actions-run", false},
	}
	for _, tt := range tests {
		got := parseGitHubURL(mustParseURL(t, tt.raw))
		if tt.nil_ {
			if got != nil {
				t.Errorf("parseGitHubURL(%q) = %+v, want nil", tt.raw, got)
			}
			continue
		}
		if got == nil || got.urlType != tt.urlType {
			t.Errorf("parseGitHubURL(%q) = %+v, want urlType %s", tt.raw, got, tt.urlType)
		}
	}
}

// A rejected request URL must be reported, not swallowed: the nil *http.Request
// it leaves behind would otherwise be dereferenced by the header setup below.
func TestFetchGitHubAPI_InvalidEndpoint(t *testing.T) {
	client := &http.Client{Transport: errTransport{err: errors.New("must not be reached")}}
	_, err := fetchGitHubAPI(context.Background(), client, "/repos/a\x00b/c")
	want := `parse "https://api.github.com/repos/a\x00b/c": net/url: invalid control character in URL`
	if err == nil {
		t.Fatalf("error = nil, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func ghBodyClient(status int, body string) *http.Client {
	return &http.Client{Transport: fakeTransport{resp: &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}}}
}

// The error-body reader is capped at 4 KiB: a longer body must be cut there, and
// a shorter one must survive whole.
func TestFetchGitHubAPI_ErrorBodyTruncatedAt4KiB(t *testing.T) {
	long := strings.Repeat("x", 5000)
	_, err := fetchGitHubAPI(context.Background(), ghBodyClient(http.StatusInternalServerError, long), "/repos/o/r")
	want := "GitHub API 500: " + strings.Repeat("x", 4096)
	if err == nil {
		t.Fatal("error = nil, want truncated-body error")
	}
	if err.Error() != want {
		t.Errorf("error length = %d, want %d (body cut at 4096)", len(err.Error()), len(want))
	}
	short := strings.Repeat("y", 4096)
	_, err = fetchGitHubAPI(context.Background(), ghBodyClient(http.StatusInternalServerError, short), "/repos/o/r")
	if err == nil {
		t.Fatal("error = nil, want truncated-body error")
	}
	if err.Error() != "GitHub API 500: "+strings.Repeat("y", 4096) {
		t.Errorf("error = %q, want the full 4096-byte body", err.Error())
	}
}

// Success bodies are capped at 10 MiB; the cap has to be exactly 10<<20 so a
// slightly larger payload is cut and a slightly smaller one is not.
func TestFetchGitHubAPI_BodyCappedAt10MiB(t *testing.T) {
	body := strings.Repeat("z", (10<<20)+1)
	data, err := fetchGitHubAPI(context.Background(), ghBodyClient(http.StatusOK, body), "/repos/o/r")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(data) != 10<<20 {
		t.Errorf("len(data) = %d, want %d", len(data), 10<<20)
	}
	small := strings.Repeat("z", (10<<20)-1)
	data, err = fetchGitHubAPI(context.Background(), ghBodyClient(http.StatusOK, small), "/repos/o/r")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(data) != (10<<20)-1 {
		t.Errorf("len(data) = %d, want %d", len(data), (10<<20)-1)
	}
}

func TestFetchGitHubURL_BodyCappedAt10MiB(t *testing.T) {
	body := strings.Repeat("w", (10<<20)+1)
	data, err := fetchGitHubURL(context.Background(), ghBodyClient(http.StatusOK, body), "https://raw.githubusercontent.com/o/r/main/f")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(data) != 10<<20 {
		t.Errorf("len(data) = %d, want %d", len(data), 10<<20)
	}
	small := strings.Repeat("w", (10<<20)-1)
	data, err = fetchGitHubURL(context.Background(), ghBodyClient(http.StatusOK, small), "https://raw.githubusercontent.com/o/r/main/f")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(data) != (10<<20)-1 {
		t.Errorf("len(data) = %d, want %d", len(data), (10<<20)-1)
	}
}

// The "... and N more files" line counts nested entries only; it must appear for
// a single nested file and for a single nested directory, and never when every
// entry sits at the top level.
func TestHandleGitHub_RepoTreeMoreFilesLine(t *testing.T) {
	tests := []struct {
		name string
		tree string
		tail string
	}{
		{"nested file", `[{"path":"a/b","type":"blob"}]`, "... and 1 more files\n\n"},
		{"nested directory", `[{"path":"c/d","type":"tree"}]`, "... and 1 more files\n\n"},
		{"nested symlink", `[{"path":"e/f","type":"symlink"}]`, "... and 1 more files\n\n"},
		{"top level only", `[{"path":"README.md","type":"blob"}]`, "      README.md\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghEnv(t)
			client, _ := ghSrv(t, map[string]string{
				"/repos/o/r/git/trees/main": `{"tree":` + tt.tree + `}`,
				"/repos/o/r/readme":         "",
				"/repos/o/r":                `{"default_branch":"main"}`,
			})
			result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("result is nil")
			}
			want := "# o/r\n\nStars: 0 · Forks: 0 · Issues: 0\n\n---\n\n## Files\n\n" +
				tt.tail + "## README\n\n(no README found)\n"
			if result.Content != want {
				t.Errorf("Content = %q, want %q", result.Content, want)
			}
		})
	}
}

// A license object with an empty spdx_id carries nothing worth printing.
func TestHandleGitHub_RepoLicenseEmptySPDXID(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/o/r/git/trees/main": `{"tree":[]}`,
		"/repos/o/r/readme":         "",
		"/repos/o/r":                `{"default_branch":"main","license":{"spdx_id":""}}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	want := "# o/r\n\nStars: 0 · Forks: 0 · Issues: 0\n\n---\n\n## Files\n\n## README\n\n(no README found)\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// A tree URL whose ref segment is empty still has to ask for HEAD, and the ref
// it asks for is only visible in the query string.
func TestHandleGitHub_TreeEmptyRefFallsBackToHEAD(t *testing.T) {
	ghEnv(t)
	client, reqs := ghSrv(t, map[string]string{
		"/repos/o/r/contents": `[{"name":"a.go","type":"file"}]`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/tree//x"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if (*reqs)[0] != "/repos/o/r/contents/x?ref=HEAD" {
		t.Errorf("contents request = %q, want %q", (*reqs)[0], "/repos/o/r/contents/x?ref=HEAD")
	}
	want := "# o/r/x\n\n**Branch:** HEAD\n\n## Contents\n\n      a.go\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// The contents endpoint answers with an object for a file, not an array; that
// shape mismatch is the "not a directory" signal and must stop the render.
func TestHandleGitHub_TreeContentsIsSingleFile(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/o/r/contents": `{"name":"README.md","type":"file"}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/tree/main/README.md"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil", result)
	}
}

// A README is only rendered when it parses AND says it is base64; either half
// failing must leave the section out.
func TestHandleGitHub_TreeReadmeRejected(t *testing.T) {
	tests := []struct {
		name   string
		readme string
	}{
		{"parse failure", `{"content":123,"encoding":"base64"}`},
		{"not base64", `{"content":"aGk=","encoding":"utf-8"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghEnv(t)
			client, _ := ghSrv(t, map[string]string{
				"/repos/o/r/contents": `[{"name":"a.go","type":"file"}]`,
				"/repos/o/r/readme":   tt.readme,
			})
			result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/tree/main/src"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("result is nil")
			}
			want := "# o/r/src\n\n**Branch:** main\n\n## Contents\n\n      a.go\n"
			if result.Content != want {
				t.Errorf("Content = %q, want %q", result.Content, want)
			}
		})
	}
}

// The run link in a job page is decimal; a narrowed format base rewrites the
// digits of every run id above ten.
func TestHandleGitHub_ActionsJobRunIDIsDecimal(t *testing.T) {
	ghEnv(t)
	client, _ := ghSrv(t, map[string]string{
		"/repos/o/r/actions/jobs/5/logs": "",
		"/repos/o/r/actions/jobs/5":      `{"name":"j","status":"completed","run_id":12345}`,
	})
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/actions/runs/7/job/5"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	want := "# j\n\n**Branch:** \n**Job:** j · completed · \n" +
		"URL: https://github.com/o/r/actions/runs/12345\n\n---\n\n## Steps\n\n" +
		"| # | Step | Status | Conclusion | Duration |\n|---|------|--------|------------|----------|\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// A logs endpoint that answers with an empty body has no logs; the section must
// stay absent rather than render an empty fence.
func TestHandleGitHub_ActionsJobEmptyLogs(t *testing.T) {
	ghEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/actions/jobs/5" {
			_, _ = io.WriteString(w, `{"name":"j","status":"completed","run_id":9}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: &allowHostsTransport{server: srv, hosts: []string{"api.github.com"}}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/actions/runs/7/job/5"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	want := "# j\n\n**Branch:** \n**Job:** j · completed · \n" +
		"URL: https://github.com/o/r/actions/runs/9\n\n---\n\n## Steps\n\n" +
		"| # | Step | Status | Conclusion | Duration |\n|---|------|--------|------------|----------|\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}
