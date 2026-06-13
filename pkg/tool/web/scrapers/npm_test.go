package scrapers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleNpm_NoMatch(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://example.com/package/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-npm host, got %+v", result)
	}
}

func TestHandleNpm_NonPackagePath(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/search?q=react"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/package/ path, got %+v", result)
	}
}

func TestHandleNpm_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/downloads/point/") {
			_, _ = w.Write([]byte(`{"downloads":1234567}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"express","version":"4.18.0","description":"Fast web framework","license":"MIT","homepage":"https://expressjs.com","repository":{"type":"git","url":"git+https://github.com/expressjs/express.git"}}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/express"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# express") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Fast web framework") {
		t.Errorf("expected description, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "1,234,567") {
		t.Errorf("expected formatted download count, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
}

func TestHandleNpm_EmptyName(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty package name, got %+v", result)
	}
}

func TestHandleNpm_NoName(t *testing.T) {
	result, _ := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty name, got %+v", result)
	}
}

func TestHandleNpm_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	_, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/missing"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestHandleNpm_DependenciesSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/downloads/point/") {
			_, _ = w.Write([]byte(`{"downloads":100}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"my-pkg","version":"1.0.0","description":"d","license":"MIT","dependencies":{"express":"^4.0.0","lodash":"^4.17.0"}}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/my-pkg"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "express") {
		t.Errorf("expected dependency, got: %q", result.Content)
	}
}

func TestExtractRepoURL_StringForm(t *testing.T) {
	got := extractRepoURL(json.RawMessage(`"https://github.com/foo/bar"`))
	if got != "https://github.com/foo/bar" {
		t.Errorf("extractRepoURL(string) = %q, want URL", got)
	}
}

func TestExtractRepoURL_EmptyString(t *testing.T) {
	if got := extractRepoURL(json.RawMessage(`""`)); got != "" {
		t.Errorf("extractRepoURL(empty) = %q, want empty", got)
	}
}

func TestExtractRepoURL_ObjectForm(t *testing.T) {
	got := extractRepoURL(json.RawMessage(`{"type": "git", "url": "git+https://github.com/foo/bar.git"}`))
	if got != "git+https://github.com/foo/bar.git" {
		t.Errorf("extractRepoURL(object) = %q, want url field", got)
	}
}

func TestExtractRepoURL_InvalidJSON(t *testing.T) {
	if got := extractRepoURL(json.RawMessage(`not json`)); got != "" {
		t.Errorf("extractRepoURL(invalid) = %q, want empty", got)
	}
}

func TestExtractRepoURL_ObjectNoURL(t *testing.T) {
	if got := extractRepoURL(json.RawMessage(`{"type": "git"}`)); got != "" {
		t.Errorf("extractRepoURL(no url) = %q, want empty", got)
	}
}

func TestHandleNpm_EmptyNameFromAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"","version":"1.0.0"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/empty"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty name from API, got %+v", result)
	}
}

func TestHandleNpm_NoDependencies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/downloads/point/") {
			_, _ = w.Write([]byte(`{"downloads":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"simple","version":"1.0.0","description":"test","license":"MIT"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/simple"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "None") {
		t.Errorf("expected 'None' for no deps, got: %q", result.Content)
	}
}

func TestHandleNpm_DownloadStatsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/downloads/point/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"name":"nopkg","version":"1.0.0","license":"MIT"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/nopkg"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "N/A") {
		t.Errorf("expected 'N/A' for failed dl stats, got: %q", result.Content)
	}
}

func TestHandleNpm_NoHomepage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"bare","version":"0.0.1","license":"MIT","description":"bare package"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &pathRoutingTransport{server: srv, hosts: map[string]string{
		"registry.npmjs.org": "",
		"api.npmjs.org":      "",
	}}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/bare"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if strings.Contains(result.Content, "Homepage:") {
		t.Errorf("unexpected Homepage field: %q", result.Content)
	}
}
