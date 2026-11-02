package scrapers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// pypiSrv serves the package JSON and the pypistats endpoint; lastWeek=0
// makes the stats endpoint answer 404 so the download line is skipped.
func pypiSrv(t *testing.T, infoJSON string, lastWeek int) (*http.Client, *string) {
	t.Helper()
	infoPath := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/pypi/") && strings.HasSuffix(r.URL.Path, "/json"):
			*infoPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, infoJSON)
		case strings.HasPrefix(r.URL.Path, "/api/packages/"):
			if lastWeek == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"last_week":`+strconv.Itoa(lastWeek)+`,"last_month":0}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &redirectTransport{server: srv}}, infoPath
}

func TestHandlePyPI_FullOutput(t *testing.T) {
	client, infoPath := pypiSrv(t, `{"info":{"name":"requests","version":"2.31.0",
		"summary":"Python HTTP for Humans.","description":"Long description.",
		"author":"Kenneth Reitz","author_email":"me@kennethreitz.com","home_page":"https://requests.readthedocs.io",
		"license":"Apache 2.0","project_urls":{"Homepage":"https://requests.readthedocs.io"},
		"requires_dist":["charset-normalizer>=2,<4","idna>=2.5,<4"]}}`, 1234567)

	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/requests/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# requests v2.31.0\n\nPython HTTP for Humans.\n\n" +
		"**Author:** Kenneth Reitz <me@kennethreitz.com>\n**License:** Apache 2.0\n" +
		"**Homepage:** https://requests.readthedocs.io\n**Weekly Downloads:** 1,234,567\n\n" +
		"## Links\n\n- Homepage: https://requests.readthedocs.io\n\n" +
		"## Dependencies\n\n- charset-normalizer>=2,<4\n- idna>=2.5,<4\n" +
		"\n---\n\n## Description\n\nLong description.\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", result.ContentType)
	}
	if result.Method != "pypi-api" {
		t.Errorf("Method = %q, want pypi-api", result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via PyPI JSON API" {
		t.Errorf("Notes = %q, want [Fetched via PyPI JSON API]", result.Notes)
	}
	if *infoPath != "/pypi/requests/json" {
		t.Errorf("API path = %q, want /pypi/requests/json", *infoPath)
	}
}

func TestHandlePyPI_MinimalOutput(t *testing.T) {
	client, _ := pypiSrv(t, `{"info":{"name":"bare","version":"","project_urls":{},"requires_dist":[]}}`, 0)

	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/bare"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# bare v\n\n\n## Dependencies\n\nNone\n\n---\n\n## Description\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// The Author line is emitted when either half is present, and each half alone
// must still reach the template.
func TestHandlePyPI_AuthorVariants(t *testing.T) {
	tests := []struct {
		name string
		info string
		want string
	}{
		{"both", `"author":"Ann","author_email":"ann@example.com"`, "**Author:** Ann <ann@example.com>\n"},
		{"name_only", `"author":"Ann"`, "**Author:** Ann <>\n"},
		{"email_only", `"author_email":"ann@example.com"`, "**Author:**  <ann@example.com>\n"},
		{"neither", `"author":"","author_email":""`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := pypiSrv(t, `{"info":{"name":"p","version":"1.0",`+tt.info+`}}`, 0)
			result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/p"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			want := "# p v1.0\n\n" + tt.want + "\n## Dependencies\n\nNone\n\n---\n\n## Description\n\n"
			if result.Content != want {
				t.Errorf("Content = %q, want %q", result.Content, want)
			}
		})
	}
}

func TestHandlePyPI_DownloadBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		lastWeek int
		wantLine string
	}{
		{"zero_omits_line", 0, ""},
		{"one_prints_line", 1, "**Weekly Downloads:** 1\n"},
		{"thousands_grouped", 1000, "**Weekly Downloads:** 1,000\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := pypiSrv(t, `{"info":{"name":"p","version":"1.0"}}`, tt.lastWeek)
			result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/p"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			want := "# p v1.0\n\n" + tt.wantLine + "\n## Dependencies\n\nNone\n\n---\n\n## Description\n\n"
			if result.Content != want {
				t.Errorf("Content = %q, want %q", result.Content, want)
			}
		})
	}
}

func TestHandlePyPI_PathVariants(t *testing.T) {
	client, _ := pypiSrv(t, `{"info":{"name":"p"}}`, 0)
	for _, raw := range []string{"https://pypi.org/project/", "https://pypi.org/simple/p/", "https://pypi.org/"} {
		result, err := HandlePyPI(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestHandlePyPI_PackageWithoutName(t *testing.T) {
	client, _ := pypiSrv(t, `{"info":{"version":"1.0"}}`, 0)
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/gone"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for nameless package, got %+v", result)
	}
}

func TestHandlePyPI_FetchErrorKeepsChain(t *testing.T) {
	sentinel := errors.New("pypi unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/p"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	want := `fetch PyPI package: Get "https://pypi.org/pypi/p/json": pypi unreachable`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not unwrap to the transport error", err)
	}
}

func TestHandlePyPI_InvalidJSONKeepsChain(t *testing.T) {
	client, _ := pypiSrv(t, `5`, 0)
	result, err := HandlePyPI(context.Background(), mustParseURL(t, "https://pypi.org/project/p"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "parse PyPI package: ") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "parse PyPI package: ")
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error %v does not unwrap to *json.UnmarshalTypeError", err)
	}
}
