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

// npmSrv serves the registry latest endpoint and the download-stats endpoint
// from one test server; downloads=0 makes the stats endpoint answer 404.
func npmSrv(t *testing.T, pkgJSON string, downloads int) (*http.Client, *string) {
	t.Helper()
	latestPath := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			*latestPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, pkgJSON)
		case strings.HasPrefix(r.URL.Path, "/downloads/point/last-week/"):
			if downloads == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"downloads":`+strconv.Itoa(downloads)+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &redirectTransport{server: srv}}, latestPath
}

func TestHandleNpm_FullOutput(t *testing.T) {
	client, latestPath := npmSrv(t, `{"name":"left-pad","version":"1.3.0","description":"String left pad",
		"license":"MIT","homepage":"https://github.com/azer","repository":{"type":"git","url":"https://github.com/azer/left-pad"},
		"keywords":["pad","string"],"dependencies":{"lodash":"^4.0.0"},"readme":"# Left Pad"}`, 1234567)

	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://www.npmjs.com/package/left-pad"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# left-pad\n\nString left pad\n\n**Latest:** 1.3.0 · **License:** MIT\n" +
		"**Weekly Downloads:** 1,234,567\n\n**Homepage:** https://github.com/azer\n" +
		"**Repository:** https://github.com/azer/left-pad\n**Keywords:** pad, string\n" +
		"\n## Dependencies\n\n- lodash: ^4.0.0\n\n---\n\n## README\n\n# Left Pad\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", result.ContentType)
	}
	if result.Method != "npm-api" {
		t.Errorf("Method = %q, want npm-api", result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via npm registry API" {
		t.Errorf("Notes = %q, want [Fetched via npm registry API]", result.Notes)
	}
	if *latestPath != "/left-pad/latest" {
		t.Errorf("registry path = %q, want /left-pad/latest", *latestPath)
	}
}

func TestHandleNpm_MinimalOutput(t *testing.T) {
	client, _ := npmSrv(t, `{"name":"x","version":"","license":"","keywords":[],"dependencies":{}}`, 0)

	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://npmjs.com/package/x"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# x\n\n**Latest:**  · **License:** \n**Weekly Downloads:** N/A\n\n" +
		"\n## Dependencies\n\nNone\n\n---\n\n## README\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleNpm_SingleKeywordAndDependency(t *testing.T) {
	client, _ := npmSrv(t, `{"name":"y","version":"0.0.1","license":"ISC","keywords":["only"],
		"dependencies":{"a":"1"},"repository":""}`, 0)

	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://npmjs.com/package/y"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# y\n\n**Latest:** 0.0.1 · **License:** ISC\n**Weekly Downloads:** N/A\n\n" +
		"**Keywords:** only\n\n## Dependencies\n\n- a: 1\n\n---\n\n## README\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// repository may be a bare string, an object, or an object without a url.
func TestHandleNpm_RepositoryForms(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		wantLine   string
	}{
		{"object", `{"type":"git","url":"https://git.example/r.git"}`, "**Repository:** https://git.example/r.git\n"},
		{"string", `"https://git.example/str.git"`, "**Repository:** https://git.example/str.git\n"},
		{"object_without_url", `{}`, ""},
		{"empty_string", `""`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := npmSrv(t, `{"name":"z","version":"1.0.0","license":"MIT","repository":`+tt.repository+`}`, 0)
			result, err := HandleNpm(context.Background(), mustParseURL(t, "https://npmjs.com/package/z"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			want := "# z\n\n**Latest:** 1.0.0 · **License:** MIT\n**Weekly Downloads:** N/A\n\n" +
				tt.wantLine + "\n## Dependencies\n\nNone\n\n---\n\n## README\n\n"
			if result.Content != want {
				t.Errorf("Content = %q, want %q", result.Content, want)
			}
		})
	}
}

func TestHandleNpm_HostVariants(t *testing.T) {
	client, _ := npmSrv(t, `{"name":"x"}`, 0)
	for _, raw := range []string{"https://www.npmjs.com/package/x", "https://npmjs.com/package/x"} {
		result, err := HandleNpm(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result == nil {
			t.Errorf("%s: expected result, got nil", raw)
		}
	}
	for _, raw := range []string{"https://www.npmjs.org/package/x", "https://npmjs.org/package/x", "/package/x", "https://www.npmjs.com/x", "https://www.npmjs.com/package/"} {
		result, err := HandleNpm(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestHandleNpm_FetchErrorKeepsChain(t *testing.T) {
	sentinel := errors.New("registry unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://npmjs.com/package/x"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	want := `fetch npm package: Get "https://registry.npmjs.org/x/latest": registry unreachable`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not unwrap to the transport error", err)
	}
}

func TestHandleNpm_InvalidJSONKeepsChain(t *testing.T) {
	client, _ := npmSrv(t, `5`, 0)
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://npmjs.com/package/x"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "parse npm package: ") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "parse npm package: ")
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error %v does not unwrap to *json.UnmarshalTypeError", err)
	}
}

func TestHandleNpm_PackageWithoutName(t *testing.T) {
	client, _ := npmSrv(t, `{"version":"1.0.0"}`, 0)
	result, err := HandleNpm(context.Background(), mustParseURL(t, "https://npmjs.com/package/gone"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for package without name, got %+v", result)
	}
}
