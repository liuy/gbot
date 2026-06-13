package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCratesIo_NoMatch(t *testing.T) {
	result, _ := HandleCratesIo(context.Background(), mustParseURL(t, "https://example.com/crates/serde"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-crates.io host, got %+v", result)
	}
}

func TestHandleCratesIo_BadPath(t *testing.T) {
	result, _ := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty path, got %+v", result)
	}
}

func TestHandleCratesIo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Host, "docs.rs") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"crate": {
				"name": "serde",
				"description": "Serialization framework",
				"homepage": "https://serde.rs",
				"documentation": "https://docs.rs/serde",
				"repository": "https://github.com/serde-rs/serde",
				"max_version": "1.0.0",
				"downloads": 1000000,
				"recent_downloads": 50000,
				"keywords": ["serialization", "no_std"],
				"categories": ["encoding"]
			},
			"versions": [{"num": "1.0.0", "license": "MIT"}]
		}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# serde") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Serialization framework") {
		t.Errorf("expected description, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "MIT") {
		t.Errorf("expected license, got: %q", result.Content)
	}
}

func TestHandleCratesIo_NoReadme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"crate":{"name":"foo","description":"test","max_version":"1.0.0","downloads":100,"recent_downloads":10,"keywords":[],"categories":[]},"versions":[]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "# foo") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleCratesIo_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/foo"), client, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error should mention invalid, got: %v", err)
	}
}

func TestHandleCratesIo_EmptyName(t *testing.T) {
	result, _ := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty name, got %+v", result)
	}
}

func TestHandleCratesIo_NoVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"crate":{"name":"foo","description":"test","max_version":"1.0.0","downloads":100,"recent_downloads":10},"versions":null}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if strings.Contains(result.Content, "Version Count") {
		t.Errorf("unexpected Version Count when versions is nil: %q", result.Content)
	}
}

func TestHandleCratesIo_NoLicense(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"crate":{"name":"foo","max_version":"1.0.0","downloads":100,"recent_downloads":10},"versions":[{"num":"1.0.0","license":""}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if strings.Contains(result.Content, "License:") {
		t.Errorf("unexpected License field when license is empty: %q", result.Content)
	}
}

func TestHandleCratesIo_WithReadme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/crate/serde") {
			_, _ = w.Write([]byte(`<div class="readme"><p>This is a long enough crate readme content that should be included in the output for testing purposes.</p></div>`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"crate":{"name":"serde","max_version":"1.0.0","downloads":1000,"recent_downloads":100},"versions":[{"num":"1.0.0","license":"MIT"}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "long enough") {
		t.Errorf("expected readme content, got: %q", result.Content)
	}
}

func TestHandleCratesIo_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/missing"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}
