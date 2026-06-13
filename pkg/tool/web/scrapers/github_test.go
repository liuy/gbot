package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleGitHub_NoMatch(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://example.com/foo/bar"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-github host, got %+v", result)
	}
}

func TestHandleGitHub_BadPath(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty path, got %+v", result)
	}
}

func TestHandleGitHub_DiscussionsList(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/discussions"), nil, nil)
	if result != nil {
		t.Errorf("discussions list should return nil, got %+v", result)
	}
}

func TestHandleGitHub_DiscussionItem(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/discussions/42"), nil, nil)
	if result != nil {
		t.Errorf("discussion items return nil, got %+v", result)
	}
}

func TestHandleGitHub_ActionsRunNoJobID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"https://github.com/owner/repo/actions/runs/1","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:01:00Z","run_attempt":1,"jobs_url":"https://api.github.com/repos/owner/repo/actions/runs/1/jobs"}`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "CI") {
		t.Errorf("expected workflow name, got: %q", result.Content)
	}
}

func TestHandleGitHub_Repo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_, _ = w.Write([]byte(`{"truncated":false,"tree":[{"path":"src","type":"directory"},{"path":"README.md","type":"blob"}]}`))
		case strings.Contains(r.URL.Path, "/readme"):
			_, _ = w.Write([]byte(`"# My Project\n\nA cool project."`))
		case strings.Contains(r.URL.Path, "/repos/owner/repo"):
			_, _ = w.Write([]byte(`{"description":"A cool project","stargazers_count":100,"forks_count":10,"open_issues_count":5,"language":"Go","license":{"spdx_id":"MIT"},"default_branch":"main"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &allowHostsTransport{server: srv, hosts: []string{"api.github.com", "raw.githubusercontent.com"}}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# owner/repo") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "100") {
		t.Errorf("expected stars, got: %q", result.Content)
	}
}

func TestHandleGitHub_Issue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/issues/42/comments"):
			_, _ = w.Write([]byte(`[{"user":{"login":"alice"},"body":"Comment 1","created_at":"2024-01-02T00:00:00Z"}]`))
		case strings.Contains(r.URL.Path, "/issues/42"):
			_, _ = w.Write([]byte(`{"title":"Test issue","body":"Issue body","state":"open","number":42,"user":{"login":"bob"},"labels":[{"name":"bug"}],"created_at":"2024-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test issue") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Comment 1") {
		t.Errorf("expected comment, got: %q", result.Content)
	}
}

func TestHandleGitHub_RepoAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestHandleGitHub_Blob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello\n"))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/main.go"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected content, got: %q", result.Content)
	}
}

func TestHandleGitHub_Tree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"main.go","path":"src/main.go","type":"file","size":1024},{"name":"README.md","path":"src/README.md","type":"file","size":256}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/tree/main/src"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "main.go") {
		t.Errorf("expected file name, got: %q", result.Content)
	}
}

func TestHandleGitHub_TreeRoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"README.md","path":"README.md","type":"file","size":1024}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/tree/main"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "root") {
		t.Errorf("expected 'root' marker, got: %q", result.Content)
	}
}

func TestHandleGitHub_Pull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/pulls/42"):
			_, _ = w.Write([]byte(`{"title":"PR title","body":"PR body","state":"open","number":42,"user":{"login":"alice"},"labels":[],"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z"}`))
		case strings.Contains(r.URL.Path, "/issues/42/comments"):
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/pull/42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "PR title") {
		t.Errorf("expected PR title, got: %q", result.Content)
	}
}

func TestHandleGitHub_IssuesList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/issues") {
			_, _ = w.Write([]byte(`[{"title":"First issue","number":1,"created_at":"2024-01-01T00:00:00Z","user":{"login":"alice"},"labels":[{"name":"bug"}]},{"title":"Second issue","number":2,"created_at":"2024-01-02T00:00:00Z","user":{"login":"bob"},"labels":[]}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/issues"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "First issue") {
		t.Errorf("expected first issue, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "bug") {
		t.Errorf("expected label, got: %q", result.Content)
	}
}

func TestHandleGitHub_ActionsJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/jobs/100"):
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","runner_name":"runner1","steps":[{"name":"Checkout","status":"completed","conclusion":"success","number":1}]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01","updated_at":"2024-01-01","run_attempt":1,"jobs_url":"x"}`))
		case strings.Contains(r.URL.Path, "/logs"):
			_, _ = w.Write([]byte("2024-01-15T00:00:00.0000000Z log line 1\n2024-01-15T00:00:01.0000000Z log line 2\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1/job/100"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "build") {
		t.Errorf("expected job name, got: %q", result.Content)
	}
}

func TestHandleGitHub_BlobBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03})
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/image.png"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "binary file") {
		t.Errorf("expected binary file marker, got: %q", result.Content)
	}
}

func TestHandleGitHub_RepoLicense(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_, _ = w.Write([]byte(`{"truncated":true,"tree":[]}`))
		case strings.Contains(r.URL.Path, "/readme"):
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/repos/owner/repo"):
			_, _ = w.Write([]byte(`{"description":"X","stargazers_count":1,"forks_count":0,"open_issues_count":0,"default_branch":"main"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# owner/repo") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleGitHub_Blob_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/blob/main/missing.go"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestHandleGitHub_ActionInvalidRunID(t *testing.T) {
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for invalid run ID, got %+v", result)
	}
}

func TestHandleGitHub_ActionsJobFromFragment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/jobs/100/logs"):
			_, _ = w.Write([]byte("2024-01-15T00:00:00.0000000Z line 1\nplain line\n"))
		case strings.Contains(r.URL.Path, "/actions/jobs/100"):
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","runner_name":"r1","steps":[]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T01:00:00Z","run_attempt":1,"jobs_url":"x"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1/attempts/1#summary-100"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
}

func TestHandleGitHub_ActionsRunFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/runs/1/jobs"):
			_, _ = w.Write([]byte(`{"jobs":[{"id":100,"name":"build","status":"completed","conclusion":"success","runner_name":"runner1"}]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T01:00:00Z","run_attempt":1,"jobs_url":"x"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "build") {
		t.Errorf("expected job name, got: %q", result.Content)
	}
}

func TestHandleGitHub_ActionsRunViaFragment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/jobs/100/logs"):
			_, _ = w.Write([]byte("2024-01-15T00:00:00.0000000Z log line\n"))
		case strings.Contains(r.URL.Path, "/actions/jobs/100"):
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","steps":[{"name":"step1","status":"completed","conclusion":"success","number":1}]}`))
		case strings.Contains(r.URL.Path, "/actions/runs/1"):
			_, _ = w.Write([]byte(`{"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push","html_url":"x","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T01:00:00Z","run_attempt":2,"jobs_url":"x"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/owner/repo/actions/runs/1/attempts/2#summary-100"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
}

func TestFetchAllComments_MultiplePages(t *testing.T) {
	page1Served := false
	page2Served := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !page1Served {
			page1Served = true
			comments := make([]map[string]any, 100)
			for i := range comments {
				comments[i] = map[string]any{"body": fmt.Sprintf("c%d", i)}
			}
			_ = json.NewEncoder(w).Encode(comments)
			return
		}
		if !page2Served {
			page2Served = true
			_, _ = w.Write([]byte(`[{"body":"page2"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: srv}}
	comments, err := fetchAllComments(context.Background(), client, "owner", "repo", 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(comments) < 100 {
		t.Errorf("expected at least 100 comments, got %d", len(comments))
	}
}

func TestParseGitHubURL_RepoOnly(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo")
	got := parseGitHubURL(u)
	if got == nil || got.urlType != "repo" || got.owner != "owner" || got.repo != "repo" {
		t.Errorf("parseGitHubURL(repo) = %+v, want repo/owner/repo", got)
	}
}

func TestParseGitHubURL_TooShort(t *testing.T) {
	cases := []string{
		"https://github.com/owner",
		"https://github.com/",
		"https://github.com",
	}
	for _, c := range cases {
		u := mustParseURL(t, c)
		if got := parseGitHubURL(u); got != nil {
			t.Errorf("parseGitHubURL(%q) = %+v, want nil", c, got)
		}
	}
}

func TestParseGitHubURL_BlobShortForm(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/blob")
	if got := parseGitHubURL(u); got != nil {
		t.Errorf("parseGitHubURL(blob short) = %+v, want nil", got)
	}
}

func TestParseGitHubURL_TreeShortForm(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/tree")
	if got := parseGitHubURL(u); got != nil {
		t.Errorf("parseGitHubURL(tree short) = %+v, want nil", got)
	}
}

func TestParseGitHubURL_IssuesList(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/issues")
	got := parseGitHubURL(u)
	if got == nil || got.urlType != "issues" {
		t.Errorf("parseGitHubURL(issues) = %+v, want issues", got)
	}
}

func TestParseGitHubURL_IssueNumberBadAtoi(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/issues/xyz")
	if got := parseGitHubURL(u); got != nil {
		t.Errorf("parseGitHubURL(issue bad atoi) = %+v, want nil", got)
	}
}

func TestParseGitHubURL_PullNumberBadAtoi(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/pull/notanumber")
	if got := parseGitHubURL(u); got != nil {
		t.Errorf("parseGitHubURL(pull bad atoi) = %+v, want nil", got)
	}
}

func TestParseGitHubURL_ActionsRunBadID(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/actions/runs/notanumber")
	if got := parseGitHubURL(u); got != nil {
		t.Errorf("parseGitHubURL(actions run bad) = %+v, want nil", got)
	}
}

func TestParseGitHubURL_ActionsJobBadID(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/actions/runs/1/job/abc")
	if got := parseGitHubURL(u); got != nil {
		t.Errorf("parseGitHubURL(actions job bad) = %+v, want nil", got)
	}
}

func TestParseGitHubURL_ActionsRunViaFragment(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/actions/runs/42/attempts/1#summary-99")
	got := parseGitHubURL(u)
	if got == nil || got.urlType != "actions-job" || got.runID != 42 || got.jobID != 99 {
		t.Errorf("parseGitHubURL(actions fragment) = %+v, want actions-job runID=42 jobID=99", got)
	}
}

func TestParseGitHubURL_ActionsRunViaFragmentBadID(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/actions/runs/42/attempts/1#summary-abc")
	got := parseGitHubURL(u)
	if got == nil || got.urlType != "actions-run" {
		t.Errorf("parseGitHubURL(actions fragment bad) = %+v, want actions-run fallback", got)
	}
}

func TestParseGitHubURL_DiscussionNumber(t *testing.T) {
	u := mustParseURL(t, "https://github.com/owner/repo/discussions/7")
	got := parseGitHubURL(u)
	if got == nil || got.urlType != "discussion" || got.number != 7 {
		t.Errorf("parseGitHubURL(discussion) = %+v, want discussion/7", got)
	}
}

func TestExtractJobFromFragment(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		want    int64
	}{
		{"summary-123", false, 123},
		{"summary-987654", false, 987654},
		{"job-123", true, 0},
		{"summary-abc", true, 0},
		{"", true, 0},
	}
	for _, tt := range tests {
		got, err := extractJobFromFragment(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("extractJobFromFragment(%q) err = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("extractJobFromFragment(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestExtractJobFromFragment_BadPrefix(t *testing.T) {
	if _, err := extractJobFromFragment("step-1"); err == nil {
		t.Error("expected error for non-summary prefix")
	}
}

func TestExtractJobFromFragment_BadNumber(t *testing.T) {
	_, err := extractJobFromFragment("summary-abc")
	if err == nil {
		t.Fatal("expected error for non-numeric job id")
	}
	if !strings.Contains(err.Error(), "invalid syntax") {
		t.Errorf("error = %q, want containing 'invalid syntax'", err.Error())
	}
}

func TestExtractJobFromFragment_NonSummary(t *testing.T) {
	_, err := extractJobFromFragment("step-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not a job") {
		t.Errorf("error = %q, want containing 'not a job'", err.Error())
	}
}

func TestFormatGHTime_Empty(t *testing.T) {
	if got := formatGHTime(""); got != "" {
		t.Errorf("formatGHTime('') = %q, want empty", got)
	}
}

func TestFormatGHTime_Invalid(t *testing.T) {
	if got := formatGHTime("not-a-time"); got != "not-a-time" {
		t.Errorf("formatGHTime(invalid) = %q, want 'not-a-time'", got)
	}
}

func TestFormatGHTime_RFC3339(t *testing.T) {
	got := formatGHTime("2024-01-15T10:30:00Z")
	if got != "2024-01-15" {
		t.Errorf("formatGHTime(rfc3339) = %q, want '2024-01-15'", got)
	}
}

func TestComputeDuration_Empty(t *testing.T) {
	if got := computeDuration("", ""); got != "" {
		t.Errorf("computeDuration('') = %q, want empty", got)
	}
}

func TestComputeDuration_BadStart(t *testing.T) {
	if got := computeDuration("bad", "2024-01-15T10:30:00Z"); got != "" {
		t.Errorf("computeDuration(bad start) = %q, want empty", got)
	}
}

func TestComputeDuration_BadEnd(t *testing.T) {
	if got := computeDuration("2024-01-15T10:30:00Z", "bad"); got != "" {
		t.Errorf("computeDuration(bad end) = %q, want empty", got)
	}
}

func TestComputeDuration_NegativeClampsToZero(t *testing.T) {
	got := computeDuration("2024-01-15T10:30:00Z", "2024-01-15T10:29:00Z")
	if got != "0:00" {
		t.Errorf("computeDuration(negative) = %q, want '0:00'", got)
	}
}

func TestComputeDuration_MinutesOnly(t *testing.T) {
	got := computeDuration("2024-01-15T10:30:00Z", "2024-01-15T10:35:30Z")
	if got != "5:30" {
		t.Errorf("computeDuration(5:30) = %q, want '5:30'", got)
	}
}

func TestComputeDuration_OverHour(t *testing.T) {
	got := computeDuration("2024-01-15T10:30:00Z", "2024-01-15T11:35:30Z")
	if got != "1:05:30" {
		t.Errorf("computeDuration(1h5m) = %q, want '1:05:30'", got)
	}
}

func TestRenderLanguageField(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		var sb strings.Builder
		renderLanguageField(&sb, nil)
		if sb.Len() != 0 {
			t.Errorf("expected no output for nil, got %q", sb.String())
		}
	})
	t.Run("string", func(t *testing.T) {
		var sb strings.Builder
		renderLanguageField(&sb, "en")
		if !strings.Contains(sb.String(), "en") {
			t.Errorf("expected language output, got %q", sb.String())
		}
	})
	t.Run("string slice", func(t *testing.T) {
		var sb strings.Builder
		renderLanguageField(&sb, []any{"en", "fr"})
		if !strings.Contains(sb.String(), "en, fr") {
			t.Errorf("expected joined languages, got %q", sb.String())
		}
	})
}

// ===== fetchGitHubURL coverage =====

func TestFetchGitHubURL_NewRequestError(t *testing.T) {
	_, err := fetchGitHubURL(context.Background(), http.DefaultClient, "://invalid")
	if err == nil {
		t.Fatal("expected error for bad URL")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("fetchGitHubURL err = %q, want 'invalid'", err.Error())
	}
}

func TestFetchGitHubURL_ClientDoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	_, err := fetchGitHubURL(context.Background(), http.DefaultClient, srv.URL)
	if err == nil {
		t.Fatal("expected error for unreachable")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("fetchGitHubURL err = %q, want 'refused'", err.Error())
	}
}

func TestFetchGitHubURL_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	_, err := fetchGitHubURL(context.Background(), &http.Client{Transport: &redirectTransport{server: srv}}, "https://api.github.com/repos/o/r")
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want 403", err.Error())
	}
}

func TestFetchGitHubAPI_ClientDoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	_, err := fetchGitHubAPI(context.Background(), &http.Client{Transport: &redirectTransport{server: srv}}, "/repos/o/r")
	if err == nil {
		t.Fatal("expected error for closed server")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("fetchGitHubAPI err = %q, want 'refused'", err.Error())
	}
}

func TestFetchGitHubAPI_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad auth"}`))
	}))
	defer srv.Close()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	_, err := fetchGitHubAPI(context.Background(), &http.Client{Transport: &redirectTransport{server: srv}}, "/repos/o/r")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "bad auth") {
		t.Errorf("error = %q, want 'bad auth'", err.Error())
	}
}

func TestFetchGitHubAPI_GH_TOKEN(t *testing.T) {
	// Test that GH_TOKEN env var is picked up (header sent to server).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mytoken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "mytoken")
	_, err := fetchGitHubAPI(context.Background(), &http.Client{Transport: &redirectTransport{server: srv}}, "/repos/o/r")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchGitHubURL_GH_TOKEN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh_token_val" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh_token_val")
	_, err := fetchGitHubURL(context.Background(), &http.Client{Transport: &redirectTransport{server: srv}}, srv.URL+"/test")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
}

// ===== renderRepo coverage =====

func TestRenderRepo_NoDescriptionNoLanguageNoLicense(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			_, _ = w.Write([]byte(`{"tree":[]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/readme") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"stargazers_count":0,"forks_count":0,"open_issues_count":0,"default_branch":"main"}`))
	}))
	defer server.Close()
	gh := &ghURL{urlType: "repo", owner: "o", repo: "r"}
	got, err := renderRepo(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh)
	if err != nil {
		t.Fatalf("renderRepo error = %v", err)
	}
	if !strings.Contains(got.Content, "(no README found)") {
		t.Errorf("missing no-readme notice: %q", got.Content)
	}
}

func TestRenderRepo_WithSubdirsAndFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			_, _ = w.Write([]byte(`{"tree":[
				{"path":"src","type":"tree"},
				{"path":"README.md","type":"blob"},
				{"path":"src/main.go","type":"blob"},
				{"path":"src/lib/util.go","type":"blob"},
				{"path":"docs/guide.md","type":"blob"}
			]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/readme") {
			_, _ = w.Write([]byte(`{"encoding":"base64","content":"UmVhZG1l"}`)) // "Readme"
			return
		}
		_, _ = w.Write([]byte(`{"description":"d","stargazers_count":5,"forks_count":3,"open_issues_count":1,"language":"Go","license":{"spdx_id":"MIT"},"default_branch":"main"}`))
	}))
	defer server.Close()
	gh := &ghURL{urlType: "repo", owner: "o", repo: "r"}
	got, err := renderRepo(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh)
	if err != nil {
		t.Fatalf("renderRepo error = %v", err)
	}
	for _, s := range []string{"[dir] src/", "README.md", "Go", "MIT", "Readme"} {
		if !strings.Contains(got.Content, s) {
			t.Errorf("renderRepo missing %q: %q", s, got.Content)
		}
	}
}

func TestRenderRepo_TreeUnmarshalErrGivesFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			_, _ = w.Write([]byte(`not json`))
			return
		}
		if strings.Contains(r.URL.Path, "/readme") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"stargazers_count":0,"forks_count":0,"open_issues_count":0,"default_branch":"main"}`))
	}))
	defer server.Close()
	gh := &ghURL{urlType: "repo", owner: "o", repo: "r"}
	got, err := renderRepo(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh)
	if err != nil {
		t.Fatalf("renderRepo error = %v", err)
	}
	// Bad JSON in tree: HTTP succeeds (treeErr == nil), JSON parse fails silently.
	// Result: tree section exists but empty (no "(unable to fetch tree)" marker).
	// Verify the repo still renders.
	if !strings.Contains(got.Content, "# o/r") {
		t.Errorf("renderRepo missing header: %q", got.Content)
	}
}

// ===== renderTree coverage =====

func TestRenderTree_SubdirWithReadme(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte(`[{"name":"main.go","type":"file"},{"name":"util","type":"dir"}]`))
			return
		}
		if strings.Contains(r.URL.Path, "/readme/") {
			_, _ = w.Write([]byte(`{"encoding":"base64","content":"SGVsbG8="}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: server}}
	result, _ := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/tree/main/src"), client, nil)
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Hello") {
		t.Errorf("expected decoded README content, got: %q", result.Content)
	}
}

func TestRenderTree_DirType(t *testing.T) {
	// Test that "dir" type renders as [dir] in output
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte(`[{"name":"subdir","type":"dir"},{"name":"file.go","type":"file"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: server}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/tree/main"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "[dir] subdir/") {
		t.Errorf("expected dir marker, got: %q", result.Content)
	}
}

func TestRenderTree_ReadmeEncodingNotBase64(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte(`[{"name":"f.go","type":"file"}]`))
			return
		}
		if strings.Contains(r.URL.Path, "/readme/") {
			_, _ = w.Write([]byte(`{"encoding":"none","content":"plain"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: server}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/tree/main/src"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	// README section should not appear when encoding != base64
	if strings.Contains(result.Content, "plain") {
		t.Errorf("README should not be rendered for non-base64 encoding: %q", result.Content)
	}
}

func TestRenderTree_RootDisplayPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte(`[{"name":"f.go","type":"file"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	client := &http.Client{Transport: &redirectTransport{server: server}}
	result, err := HandleGitHub(context.Background(), mustParseURL(t, "https://github.com/o/r/tree/HEAD"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "root") {
		t.Errorf("expected 'root' display path, got: %q", result.Content)
	}
}

// ===== renderActionsRun coverage =====

func TestRenderActionsRun_ConclusionDisplayTitleSHA(t *testing.T) {
	// Cover: DisplayTitle empty (falls back to Name), Conclusion != "", HeadSha truncated to 7
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/jobs") {
			_, _ = w.Write([]byte(`{"jobs":[{"id":100,"name":"build","status":"completed","conclusion":"success","steps":[{"name":"Checkout","status":"completed","conclusion":"success","number":1,"started_at":"2024-01-15T10:00:00Z","completed_at":"2024-01-15T10:01:00Z"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"name":"CI","run_number":1,"status":"in_progress","conclusion":"action_required","head_branch":"main","head_sha":"abcdef1234567","event":"push","html_url":"https://github.com/o/r/actions/runs/1","created_at":"2024-01-15T10:00:00Z","updated_at":"2024-01-15T10:30:00Z"}`))
	}))
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	gh := &ghURL{urlType: "actions-run", owner: "o", repo: "r", runID: 1}
	got, err := renderActionsRun(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh)
	if err != nil {
		t.Fatalf("renderActionsRun error = %v", err)
	}
	// "CI" from Name (DisplayTitle absent), in_progress (action_required) conclusion
	for _, s := range []string{"CI", "in_progress (action_required)", "abcdef1", "https://github.com"} {
		if !strings.Contains(got.Content, s) {
			t.Errorf("renderActionsRun missing %q: %q", s, got.Content)
		}
	}
}

// ===== renderActionsJob coverage =====

func TestRenderActionsJob_EmptyWorkflowNameRunInfoFallback(t *testing.T) {
	// WorkflowName empty, RunNumber 0, runInfo provides them
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs/100") {
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","run_id":1,"runner_name":"r1","steps":[],"started_at":"2024-01-15T10:00:00Z","completed_at":"2024-01-15T10:01:00Z"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/runs/1") {
			_, _ = w.Write([]byte(`{"name":"CI","display_title":"My Workflow","run_number":42,"status":"completed","conclusion":"success"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/logs") {
			_, _ = w.Write([]byte(""))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	gh := &ghURL{urlType: "actions-job", owner: "o", repo: "r", jobID: 100}
	got, err := renderActionsJob(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh)
	if err != nil {
		t.Fatalf("renderActionsJob error = %v", err)
	}
	// Workflow name from runInfo (Name, not DisplayTitle)
	if !strings.Contains(got.Content, "**Workflow:** CI") {
		t.Errorf("renderActionsJob missing workflow name: %q", got.Content)
	}
	// Run number from runInfo
	if !strings.Contains(got.Content, "#42") {
		t.Errorf("renderActionsJob missing run number: %q", got.Content)
	}
	// Runner name
	if !strings.Contains(got.Content, "r1") {
		t.Errorf("renderActionsJob missing runner: %q", got.Content)
	}
}

func TestRenderActionsJob_LogsWithStripTimestamp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs/100") {
			_, _ = w.Write([]byte(`{"id":100,"name":"build","status":"completed","conclusion":"success","run_id":1,"steps":[],"started_at":"2024-01-15T10:00:00Z","completed_at":"2024-01-15T10:01:00Z"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/runs/1") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/logs") {
			_, _ = w.Write([]byte("2024-01-15T00:00:00.0000000Z build started\n2024-01-15T00:01:00.0000000Z build complete\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	gh := &ghURL{urlType: "actions-job", owner: "o", repo: "r", jobID: 100}
	got, err := renderActionsJob(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh)
	if err != nil {
		t.Fatalf("renderActionsJob error = %v", err)
	}
	// Check the stripped log line appears
	if !strings.Contains(got.Content, "build started") {
		t.Errorf("renderActionsJob missing stripped log line: %q", got.Content)
	}
	if strings.Contains(got.Content, "2024-01-15T00:00:00.0000000Z") {
		t.Errorf("renderActionsJob still contains timestamp: %q", got.Content)
	}
}

// ===== renderIssue no-user-body coverage =====

func TestRenderIssue_NoUserNoBody(t *testing.T) {
	// item.User == nil (user="unknown"), item.Body == "" (no description)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/comments") {
			_, _ = w.Write([]byte(`[{"body":"c1","created_at":"2024-01-02T00:00:00Z"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"title":"T","state":"open","number":1,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`))
	}))
	defer server.Close()

	gh := &ghURL{urlType: "issue", owner: "o", repo: "r", number: 1}
	got, err := renderIssue(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh, false)
	if err != nil {
		t.Fatalf("renderIssue error = %v", err)
	}
	if !strings.Contains(got.Content, "unknown") {
		t.Errorf("renderIssue missing unknown user: %q", got.Content)
	}
	if !strings.Contains(got.Content, "(no description)") {
		t.Errorf("renderIssue missing no-description: %q", got.Content)
	}
}

// ===== renderIssuesList bad JSON =====

func TestRenderIssuesList_BadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	gh := &ghURL{urlType: "issues", owner: "o", repo: "r"}
	_, err := renderIssuesList(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, gh)
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
	if !strings.Contains(err.Error(), "parse issues") {
		t.Errorf("error = %q, want 'parse issues'", err.Error())
	}
}

// ===== fetchAllComments error paths =====

func TestFetchAllComments_APIErrorGivesEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	comments, err := fetchAllComments(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, "o", "r", 1)
	if err != nil {
		t.Fatalf("expected nil on API error (graceful break), got %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}
}

func TestFetchAllComments_BadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	comments, err := fetchAllComments(context.Background(), &http.Client{Transport: &redirectTransport{server: server}}, "o", "r", 1)
	if err != nil {
		t.Fatalf("expected nil on bad JSON, got %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}
}
