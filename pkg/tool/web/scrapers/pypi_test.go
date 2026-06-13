package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlePyPI_NoMatch(t *testing.T) {
	result, _ := HandlePyPI(context.Background(), mustParseURL(t, "https://example.com/project/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-pypi host, got %+v", result)
	}
}

func TestHandlePyPI_NonProjectPath(t *testing.T) {
	result, _ := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/search?q=requests"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/project/ path, got %+v", result)
	}
}

func TestHandlePyPI_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"info": {
				"name": "requests",
				"version": "2.31.0",
				"summary": "HTTP library",
				"description_content_type": "text/markdown",
				"author": "Kenneth Reitz",
				"author_email": "me@kennethreitz.org",
				"home_page": "https://requests.readthedocs.io",
				"project_urls": {"Documentation": "https://docs.example.com"},
				"license": "Apache-2.0",
				"requires_dist": ["urllib3>=1.21.1"]
			}
		}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "pypi.org"}}
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/requests/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# requests") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "HTTP library") {
		t.Errorf("expected summary, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Apache-2.0") {
		t.Errorf("expected license, got: %q", result.Content)
	}
}

func TestHandlePyPI_EmptyName(t *testing.T) {
	result, _ := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project//"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty package name, got %+v", result)
	}
}

func TestHandlePyPI_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "pypi.org"}}
	_, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/missing/"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404, got: %v", err)
	}
}

func TestHandlePyPI_EmptyNameFromAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"name":"","version":"1.0.0"}}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "pypi.org"}}
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/empty/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty name, got %+v", result)
	}
}

func TestHandlePyPI_NoDependenciesNoSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/pypi/") {
			_, _ = w.Write([]byte(`{"info":{"name":"simple","version":"0.1.0","author":"","author_email":"","home_page":"","keywords":""}}`))
			return
		}
		if strings.Contains(r.URL.Path, "/pypistats/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "pypi.org"}}
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/simple/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# simple") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "None") {
		t.Errorf("expected 'None' for no dependencies, got: %q", result.Content)
	}
}

func TestHandlePyPI_WithAllFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/pypi/") {
			_, _ = w.Write([]byte(`{"info":{"name":"rich","version":"13.0.0","summary":"Rich text formatting","description":"Rich is a Python library.","author":"Will McGugan","author_email":"will@example.com","home_page":"https://rich.readthedocs.io","project_urls":{"Source":"https://github.com/Textualize/rich"},"license":"MIT","requires_dist":["typing-extensions"],"keywords":"rich"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "/api/packages/") {
			_, _ = w.Write([]byte(`{"data":{"last_week":50000}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &allowHostsTransport{server: srv, hosts: []string{"pypi.org", "pypistats.org"}}}
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/rich/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "https://rich.readthedocs.io") {
		t.Errorf("expected home page, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Will McGugan") {
		t.Errorf("expected author, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "50,000") {
		t.Errorf("expected weekly downloads, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "typing-extensions") {
		t.Errorf("expected dependency, got: %q", result.Content)
	}
}
