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

// cratesSrv serves the crates.io API on one path and the docs.rs README on
// another, recording the API request path so the crate name in the URL is pinned.
func cratesSrv(t *testing.T, apiJSON, readmeHTML string) (*http.Client, *string) {
	t.Helper()
	apiPath := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/crates/"):
			*apiPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, apiJSON)
		case strings.HasPrefix(r.URL.Path, "/crate/"):
			if readmeHTML == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, readmeHTML)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &redirectTransport{server: srv}}, apiPath
}

func TestHandleCratesIo_FullOutput(t *testing.T) {
	client, apiPath := cratesSrv(t, `{"crate":{
		"name":"serde","description":"Serialization framework","homepage":"https://serde.rs",
		"documentation":"https://docs.rs/serde","repository":"https://github.com/serde-rs/serde",
		"max_version":"1.0.0","downloads":1000000,"recent_downloads":50000,
		"keywords":["serialization","no_std"],"categories":["encoding"]},
		"versions":[{"num":"1.0.0","license":"MIT"},{"num":"0.9.0","license":"Apache-2.0"}]}`, "")

	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# serde v1.0.0\n\nSerialization framework\n\n" +
		"**Downloads:** 1000000 · **Version Count:** 2 · **Recent Downloads:** 50000\n" +
		"**License:** MIT\n**Homepage:** https://serde.rs\n**Repository:** https://github.com/serde-rs/serde\n" +
		"**Documentation:** https://docs.rs/serde\n**Keywords:** serialization, no_std\n**Categories:** encoding\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", result.ContentType)
	}
	if result.Method != "cratesio-api" {
		t.Errorf("Method = %q, want cratesio-api", result.Method)
	}
	if result.Notes != nil {
		t.Errorf("Notes = %q, want nil", result.Notes)
	}
	if *apiPath != "/api/v1/crates/serde" {
		t.Errorf("API path = %q, want /api/v1/crates/serde", *apiPath)
	}
}

func TestHandleCratesIo_MinimalOutput(t *testing.T) {
	client, _ := cratesSrv(t, `{"crate":{"name":"bare","max_version":"","downloads":0,"recent_downloads":0,
		"keywords":[],"categories":[]},"versions":null}`, "")

	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/bare"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# bare v\n\n**Downloads:** 0 · **Recent Downloads:** 0\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// One-element lists separate "at least one" from "more than one".
func TestHandleCratesIo_SingleValueLists(t *testing.T) {
	client, _ := cratesSrv(t, `{"crate":{"name":"one","max_version":"2.0.0","downloads":5,"recent_downloads":1,
		"keywords":["only"],"categories":["single"]},
		"versions":[{"num":"2.0.0","license":"MIT"}]}`, "")

	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/one"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# one v2.0.0\n\n**Downloads:** 5 · **Version Count:** 1 · **Recent Downloads:** 1\n" +
		"**License:** MIT\n**Keywords:** only\n**Categories:** single\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// The README section appears only above 50 chars and is cut at 1000.
func TestHandleCratesIo_ReadmeBoundaries(t *testing.T) {
	const api = `{"crate":{"name":"serde","max_version":"1.0.0","downloads":1,"recent_downloads":1},
		"versions":[{"num":"1.0.0","license":"MIT"}]}`
	tests := []struct {
		name        string
		body        int
		wantSection bool
		wantLen     int
	}{
		{"fifty_chars_omitted", 50, false, 0},
		{"fifty_one_chars_kept", 51, true, 51},
		{"thousand_chars_kept", 1000, true, 1000},
		{"thousand_one_chars_truncated", 1001, true, 1003},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readme := `<div class="readme"><p>` + strings.Repeat("x", tt.body) + `</p></div>`
			client, _ := cratesSrv(t, api, readme)
			result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			has := strings.Contains(result.Content, "## README")
			if has != tt.wantSection {
				t.Fatalf("README section present = %v, want %v (content %q)", has, tt.wantSection, result.Content)
			}
			if !tt.wantSection {
				return
			}
			idx := strings.Index(result.Content, "## README\n\n")
			if idx < 0 {
				t.Fatalf("missing README header in %q", result.Content)
			}
			section := strings.TrimSuffix(result.Content[idx+len("## README\n\n"):], "\n")
			if len(section) != tt.wantLen {
				t.Errorf("README section len = %d, want %d (tail %q)", len(section), tt.wantLen, section[max(0, len(section)-8):])
			}
		})
	}
}

func TestHandleCratesIo_ReadmeSectionLayout(t *testing.T) {
	readme := `<div class="readme"><p>` + strings.Repeat("y", 60) + `</p></div>`
	client, _ := cratesSrv(t, `{"crate":{"name":"serde","max_version":"1.0.0","downloads":1,"recent_downloads":2},
		"versions":[{"num":"1.0.0","license":"MIT"}]}`, readme)

	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# serde v1.0.0\n\n**Downloads:** 1 · **Version Count:** 1 · **Recent Downloads:** 2\n**License:** MIT\n" +
		"\n---\n\n## README\n\n" + strings.Repeat("y", 60) + "\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleCratesIo_PathVariants(t *testing.T) {
	client, _ := cratesSrv(t, `{"crate":{"name":"x"},"versions":[]}`, "")
	for _, raw := range []string{
		"https://crates.io/crates",
		"https://crates.io/crates/",
		"https://crates.io/crates//x",
		"https://crates.io/blog/post",
		"https://crates.io/",
	} {
		result, err := HandleCratesIo(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestHandleCratesIo_APIErrorWrapsTransportError(t *testing.T) {
	sentinel := errors.New("crates unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	result, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	want := `crates.io API error for serde: Get "https://crates.io/api/v1/crates/serde": crates unreachable`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not unwrap to the transport error", err)
	}
}

func TestHandleCratesIo_InvalidJSONWrapsSyntaxError(t *testing.T) {
	client, _ := cratesSrv(t, "{oops", "")
	_, err := HandleCratesIo(context.Background(), mustParseURL(t, "https://crates.io/crates/serde"), client, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), `invalid crates.io response for serde: `) {
		t.Errorf("error = %q, want prefix %q", err.Error(), "invalid crates.io response for serde: ")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error %v does not unwrap to *json.SyntaxError", err)
	}
}
