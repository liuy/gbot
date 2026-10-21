package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestTavily_ID(t *testing.T) {
	p := &TavilyProvider{}
	if got := p.ID(); got != "tavily" {
		t.Errorf("ID() = %q, want %q", got, "tavily")
	}
}

func TestTavily_IsAvailable(t *testing.T) {
	p := &TavilyProvider{APIKey: "k"}
	if !p.IsAvailable() {
		t.Error("struct key: IsAvailable() = false, want true")
	}

	t.Setenv("TAVILY_API_KEY", "env-key")
	p = &TavilyProvider{}
	if !p.IsAvailable() {
		t.Error("env fallback: IsAvailable() = false, want true")
	}

	t.Setenv("TAVILY_API_KEY", "")
	p = &TavilyProvider{}
	if p.IsAvailable() {
		t.Error("no key: IsAvailable() = true, want false")
	}
}

func TestTavily_APIKeyPriority(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "env-key")
	p := &TavilyProvider{APIKey: "struct-key"}
	if got := p.apiKey(); got != "struct-key" {
		t.Errorf("apiKey() = %q, want struct key to win", got)
	}
	p = &TavilyProvider{}
	if got := p.apiKey(); got != "env-key" {
		t.Errorf("apiKey() = %q, want env fallback", got)
	}
}

func TestTavily_MissingKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request may be sent without a key")
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client()}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	want := `tavily: Tavily credentials not found. Set TAVILY_API_KEY or configure an API key for provider "tavily". (status 0)`
	if spe.Error() != want {
		t.Errorf("Error() = %q, want %q", spe.Error(), want)
	}
}

func TestTavily_Search_RequestShape(t *testing.T) {
	var gotMethod, gotPath, gotCT, gotAuth string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k-struct"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "test query", Limit: 7})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if resp.Provider != "tavily" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "tavily")
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/" {
		t.Errorf("path = %q, want %q", gotPath, "/")
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if gotAuth != "Bearer k-struct" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer k-struct")
	}
	if body["query"] != "test query" {
		t.Errorf("query = %v, want %q", body["query"], "test query")
	}
	if body["search_depth"] != "basic" {
		t.Errorf("search_depth = %v, want basic", body["search_depth"])
	}
	if body["max_results"] != float64(7) {
		t.Errorf("max_results = %v, want 7", body["max_results"])
	}
	if body["include_answer"] != "advanced" {
		t.Errorf("include_answer = %v, want advanced", body["include_answer"])
	}
	if body["include_raw_content"] != false {
		t.Errorf("include_raw_content = %v, want false", body["include_raw_content"])
	}
	if len(body) != 5 {
		t.Errorf("request body has %d keys (%v), want exactly 5", len(body), body)
	}
}

func TestTavily_Search_Clamp(t *testing.T) {
	var captured float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		captured = body["max_results"].(float64)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	for _, c := range []struct {
		limit int
		want  float64
	}{
		{0, 5}, {3, 3}, {25, 20}, {-5, 1},
	} {
		if _, err := p.Search(context.Background(), SearchParams{Query: "q", Limit: c.limit}); err != nil {
			t.Fatalf("Limit=%d: Search() error = %v", c.limit, err)
		}
		if captured != c.want {
			t.Errorf("Limit=%d: max_results = %v, want %v", c.limit, captured, c.want)
		}
	}
}

func TestTavily_Search_Truncation(t *testing.T) {
	fixture := `{"answer":"a","results":[`
	for i := range 25 {
		if i > 0 {
			fixture += ","
		}
		fixture += fmt.Sprintf(`{"title":"t%d","url":"https://example.com/%d","content":"c"}`, i, i)
	}
	fixture += `]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	// Limit 25 clamps to the 20 cap; 25 incoming results must truncate to 20.
	resp, err := p.Search(context.Background(), SearchParams{Query: "q", Limit: 25})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Sources) != 20 {
		t.Fatalf("len(Sources) = %d, want 20 (truncated to the clamped cap)", len(resp.Sources))
	}
	if resp.Sources[19].URL != "https://example.com/19" {
		t.Errorf("Sources[19].URL = %q, want the 20th result", resp.Sources[19].URL)
	}
}

func TestTavily_Search_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Entry order matters: the skip guards fire mid-array (junk entry
		// first, empty-url entry between two valid ones) so a
		// continue-turned-break drops later entries and trips the count.
		_, _ = w.Write([]byte(`{"answer":"  Go is a language  ","results":[` +
			`"not-an-object",` +
			`{"title":"Go","url":"https://go.dev","content":"An open-source language","published_date":"2019-06-01"},` +
			`{"title":"dropped","url":""},` +
			`{"title":"","url":"https://example.com/no-title","content":"x"}` +
			`],"request_id":"req-1"}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "go language"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if resp.Provider != "tavily" {
		t.Errorf("Provider = %q, want tavily", resp.Provider)
	}
	if resp.Answer != "Go is a language" {
		t.Errorf("Answer = %q, want trimmed %q", resp.Answer, "Go is a language")
	}
	if len(resp.Sources) != 2 {
		t.Fatalf("len(Sources) = %d, want 2 (non-object and empty-url entries skipped)", len(resp.Sources))
	}
	s0 := resp.Sources[0]
	if s0.Title != "Go" || s0.URL != "https://go.dev" || s0.Snippet != "An open-source language" {
		t.Errorf("Sources[0] = %+v, want Go / https://go.dev / An open-source language", s0)
	}
	if s0.PublishedDate != "2019-06-01" {
		t.Errorf("Sources[0].PublishedDate = %q, want 2019-06-01", s0.PublishedDate)
	}
	parsed, err := time.Parse("2006-01-02", "2019-06-01")
	if err != nil {
		t.Fatalf("parse reference date: %v", err)
	}
	wantAge := int64(time.Since(parsed).Seconds())
	if diff := s0.AgeSeconds - wantAge; diff < -5 || diff > 5 {
		t.Errorf("Sources[0].AgeSeconds = %d, want within ±5s of %d", s0.AgeSeconds, wantAge)
	}
	s1 := resp.Sources[1]
	if s1.Title != "https://example.com/no-title" {
		t.Errorf("Sources[1].Title = %q, want URL fallback %q", s1.Title, s1.URL)
	}
	if s1.PublishedDate != "" || s1.AgeSeconds != 0 {
		t.Errorf("Sources[1] date fields = %q/%d, want empty/0", s1.PublishedDate, s1.AgeSeconds)
	}
}

func TestTavily_Search_HTTPErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantErr   string
		rateLimit bool
		authError bool
	}{
		{
			name:    "402",
			status:  402,
			body:    `pay up`,
			wantErr: `tavily: 402 credits exhausted (status 402)`,
		},
		{
			name:      "401",
			status:    401,
			body:      `no auth`,
			wantErr:   `tavily: 401 unauthorized (status 401)`,
			authError: true,
		},
		{
			name:      "429 credit body",
			status:    429,
			body:      `insufficient credits`,
			wantErr:   `tavily: credits exhausted (status 429)`,
			rateLimit: true,
		},
		{
			name:    "500 nested detail message",
			status:  500,
			body:    `{"detail":{"error":{"message":"boom"}}}`,
			wantErr: `tavily: Tavily API error (500): boom (status 500)`,
		},
		{
			name:    "500 valid json without extractable message keeps raw body",
			status:  500,
			body:    `{"foo":"bar"}`,
			wantErr: `tavily: Tavily API error (500): {"foo":"bar"} (status 500)`,
		},
		{
			name:    "500 empty body falls back to status text",
			status:  500,
			body:    ``,
			wantErr: `tavily: Tavily API error (500): Internal Server Error (status 500)`,
		},
		{
			name:    "500 plain text keeps raw text",
			status:  500,
			body:    `plain oops`,
			wantErr: `tavily: Tavily API error (500): plain oops (status 500)`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()

			p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
			oldURL := tavilyAPIURL
			tavilyAPIURL = server.URL
			defer func() { tavilyAPIURL = oldURL }()

			_, err := p.Search(context.Background(), SearchParams{Query: "q"})
			spe, ok := err.(*SearchProviderError)
			if !ok {
				t.Fatalf("error type = %T, want *SearchProviderError", err)
			}
			if spe.Error() != c.wantErr {
				t.Errorf("Error() = %q, want %q", spe.Error(), c.wantErr)
			}
			if spe.IsRateLimit() != c.rateLimit {
				t.Errorf("IsRateLimit() = %v, want %v", spe.IsRateLimit(), c.rateLimit)
			}
			if spe.IsAuthError() != c.authError {
				t.Errorf("IsAuthError() = %v, want %v", spe.IsAuthError(), c.authError)
			}
		})
	}
}

func TestTavily_Search_ReadBodyError(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)),
			Header:     http.Header{},
		}, nil
	})
	p := &TavilyProvider{Client: &http.Client{Transport: transport}, APIKey: "k"}

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "tavily" {
		t.Errorf("Provider = %q, want tavily", spe.Provider)
	}
	if spe.Message != "read response: unexpected EOF" {
		t.Errorf("Message = %q, want %q", spe.Message, "read response: unexpected EOF")
	}
	if spe.Status != 200 {
		t.Errorf("Status = %d, want 200 (carries resp.StatusCode)", spe.Status)
	}
}

func TestTavily_Search_NonObjectBodies(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantAnswer string
	}{
		{"answer only", `{"answer":"only answer"}`, "only answer"},
		{"array body", `[1,2]`, ""},
		{"string body", `"ok"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()

			p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
			oldURL := tavilyAPIURL
			tavilyAPIURL = server.URL
			defer func() { tavilyAPIURL = oldURL }()

			// Non-map JSON maps to an empty record (asRecord ?? {}), never an
			// error: a struct-typed unmarshal would fail over here.
			resp, err := p.Search(context.Background(), SearchParams{Query: "q"})
			if err != nil {
				t.Fatalf("Search() error = %v, want success", err)
			}
			if len(resp.Sources) != 0 {
				t.Errorf("len(Sources) = %d, want 0", len(resp.Sources))
			}
			if resp.Answer != c.wantAnswer {
				t.Errorf("Answer = %q, want %q", resp.Answer, c.wantAnswer)
			}
		})
	}
}

func TestTavily_client_Default(t *testing.T) {
	p := &TavilyProvider{}
	if p.client() != http.DefaultClient {
		t.Fatal("expected DefaultClient when Client is nil")
	}
}

func TestTavily_Search_RequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // Force the connection to fail.

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "tavily" {
		t.Errorf("Provider = %q, want tavily", spe.Provider)
	}
	if !strings.HasPrefix(spe.Message, "request failed: ") {
		t.Errorf("Message = %q, want prefix %q", spe.Message, "request failed: ")
	}
	if spe.Status != 0 {
		t.Errorf("Status = %d, want 0 (transport failure)", spe.Status)
	}
}

func TestTavily_Search_BuildRequestError(t *testing.T) {
	p := &TavilyProvider{Client: http.DefaultClient, APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = "http://[::1]:namedport" // invalid host
	defer func() { tavilyAPIURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
	if !strings.Contains(err.Error(), "build request") {
		t.Errorf("error = %v, want to contain 'build request'", err)
	}
	// %w (not %v) keeps the underlying *url.Error reachable for errors.As.
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("error = %v, want a wrapped *url.Error", err)
	}
}

func TestTavily_Search_ReadBodyError_Non200(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)),
			Header:     http.Header{},
		}, nil
	})
	p := &TavilyProvider{Client: &http.Client{Transport: transport}, APIKey: "k"}

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "tavily" {
		t.Errorf("Provider = %q, want tavily", spe.Provider)
	}
	if spe.Message != "read response: unexpected EOF" {
		t.Errorf("Message = %q, want %q", spe.Message, "read response: unexpected EOF")
	}
	if spe.Status != 500 {
		t.Errorf("Status = %d, want 500 (carries resp.StatusCode)", spe.Status)
	}
}

func TestTavily_Search_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "tavily" {
		t.Errorf("Provider = %q, want tavily", spe.Provider)
	}
	if !strings.HasPrefix(spe.Message, "parse response: ") {
		t.Errorf("Message = %q, want prefix %q", spe.Message, "parse response: ")
	}
	if spe.Status != 0 {
		t.Errorf("Status = %d, want 0 (parse failure has no HTTP status)", spe.Status)
	}
}

func TestTavily_Search_DirectiveMapping(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: `site:github.com -site:twitter.com after:2024-01-01 before:2024-06-01 "exact phrase" x -spam`})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// Tavily prefers clean natural text: only phrases and -exclusions survive.
	if body["query"] != `"exact phrase" x -spam` {
		t.Errorf("query = %v, want %q", body["query"], `"exact phrase" x -spam`)
	}
	if got := body["include_domains"]; !reflect.DeepEqual(got, []any{"github.com"}) {
		t.Errorf("include_domains = %v, want [github.com]", got)
	}
	if got := body["exclude_domains"]; !reflect.DeepEqual(got, []any{"twitter.com"}) {
		t.Errorf("exclude_domains = %v, want [twitter.com]", got)
	}
	if body["start_date"] != "2024-01-01" {
		t.Errorf("start_date = %v, want 2024-01-01", body["start_date"])
	}
	if body["end_date"] != "2024-06-01" {
		t.Errorf("end_date = %v, want 2024-06-01", body["end_date"])
	}
	if _, ok := body["time_range"]; ok {
		t.Errorf("time_range present = %v, want absent (explicit dates suppress it)", body["time_range"])
	}
}

func TestTavily_Search_RecencyTimeRange(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		// Renderable first response: a time-filtered search that returns a
		// source must NOT be retried.
		_, _ = w.Write([]byte(`{"results":[{"title":"t","url":"https://example.com/x"}]}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	if _, err := p.Search(context.Background(), SearchParams{Query: "go generics", Recency: "week"}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want exactly 1 (renderable response, no retry)", len(bodies))
	}
	if bodies[0]["time_range"] != "week" {
		t.Errorf("time_range = %v, want week", bodies[0]["time_range"])
	}
	if bodies[0]["query"] != "go generics" {
		t.Errorf("query = %v, want passthrough for a directive-free query", bodies[0]["query"])
	}
}

func TestTavily_Search_ExplicitDatesSuppressTimeRange(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	if _, err := p.Search(context.Background(), SearchParams{Query: "go after:2024-01-01", Recency: "week"}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want exactly 2 (start_date is a time filter; the unrenderable `{}` triggers one retry)", len(bodies))
	}
	if _, ok := bodies[0]["time_range"]; ok {
		t.Errorf("time_range = %v, want absent when explicit dates present", bodies[0]["time_range"])
	}
	if bodies[0]["start_date"] != "2024-01-01" {
		t.Errorf("start_date = %v, want 2024-01-01", bodies[0]["start_date"])
	}
	if _, ok := bodies[1]["start_date"]; ok {
		t.Errorf("retry start_date = %v, want cleared", bodies[1]["start_date"])
	}
}

func TestTavily_Search_BeforeOnlySuppressesTimeRange(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	if _, err := p.Search(context.Background(), SearchParams{Query: "go before:2024-06-01", Recency: "week"}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// end_date alone is still a time filter: the unrenderable `{}` first
	// response must trigger exactly one retry.
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want exactly 2 (end_date is a time filter)", len(bodies))
	}
	if _, ok := bodies[0]["time_range"]; ok {
		t.Errorf("time_range = %v, want absent when an end_date-only bound is present", bodies[0]["time_range"])
	}
	if bodies[0]["end_date"] != "2024-06-01" {
		t.Errorf("end_date = %v, want 2024-06-01", bodies[0]["end_date"])
	}
	if _, ok := bodies[1]["end_date"]; ok {
		t.Errorf("retry end_date = %v, want cleared", bodies[1]["end_date"])
	}
}

func TestTavily_Search_TimeFilterRetry(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"results":[],"answer":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"Retry Hit","url":"https://example.com/retry","content":"c"}]}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "go generics", Recency: "week", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want exactly 2 (retry after unrenderable first response)", len(bodies))
	}
	if bodies[0]["time_range"] != "week" {
		t.Errorf("first body time_range = %v, want week", bodies[0]["time_range"])
	}
	for _, field := range []string{"time_range", "start_date", "end_date"} {
		if _, ok := bodies[1][field]; ok {
			t.Errorf("second body %s = %v, want cleared", field, bodies[1][field])
		}
	}
	if bodies[1]["query"] != "go generics" {
		t.Errorf("second body query = %v, want unchanged", bodies[1]["query"])
	}
	if len(resp.Sources) != 1 || resp.Sources[0].URL != "https://example.com/retry" {
		t.Errorf("Sources = %v, want the second response's source", resp.Sources)
	}
}

func TestTavily_Search_NoTimeFilterNoRetry(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "go generics"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want exactly 1 (no time filter → no retry)", requests)
	}
	if len(resp.Sources) != 0 {
		t.Errorf("len(Sources) = %d, want 0", len(resp.Sources))
	}
}

func TestTavily_Search_AnswerOnlyNoRetry(t *testing.T) {
	// omp hasRenderableResponse: a non-empty answer alone is renderable, so
	// the time-filter retry must not fire for answer-only responses.
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":"Go generics landed in 1.18","results":[]}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "go generics", Recency: "week", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want exactly 1 (answer-only response is renderable, no retry)", requests)
	}
	if resp.Answer != "Go generics landed in 1.18" {
		t.Errorf("Answer = %q, want the response answer", resp.Answer)
	}
	if len(resp.Sources) != 0 {
		t.Errorf("len(Sources) = %d, want 0", len(resp.Sources))
	}
}

func TestTavily_Search_PresetParsedQuery(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := &TavilyProvider{Client: server.Client(), APIKey: "k"}
	oldURL := tavilyAPIURL
	tavilyAPIURL = server.URL
	defer func() { tavilyAPIURL = oldURL }()

	// First call: pipeline-style pre-parsed query whose Raw differs from Query.
	parsed := ParseSearchQuery(`"exact phrase" -spam site:github.com`)
	if _, err := p.Search(context.Background(), SearchParams{Query: "ignored by preset", ParsedQuery: &parsed, Limit: 5}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// Second call: fresh parse of an equivalent query.
	if _, err := p.Search(context.Background(), SearchParams{Query: `"exact phrase" -spam site:github.com`, Limit: 5}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if bodies[0]["query"] != bodies[1]["query"] {
		t.Errorf("preset query body = %v, fresh-parse body = %v, want identical", bodies[0]["query"], bodies[1]["query"])
	}
	if got := bodies[0]["include_domains"]; !reflect.DeepEqual(got, []any{"github.com"}) {
		t.Errorf("include_domains = %v, want [github.com]", got)
	}
}
