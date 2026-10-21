package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestJina_ID(t *testing.T) {
	p := &JinaProvider{}
	if got := p.ID(); got != "jina" {
		t.Errorf("ID() = %q, want %q", got, "jina")
	}
}

func TestJina_IsAvailable(t *testing.T) {
	p := &JinaProvider{APIKey: "k"}
	if !p.IsAvailable() {
		t.Error("struct key: IsAvailable() = false, want true")
	}

	t.Setenv("JINA_API_KEY", "env-key")
	p = &JinaProvider{}
	if !p.IsAvailable() {
		t.Error("env fallback: IsAvailable() = false, want true")
	}

	t.Setenv("JINA_API_KEY", "")
	p = &JinaProvider{}
	if p.IsAvailable() {
		t.Error("no key: IsAvailable() = true, want false")
	}
}

func TestJina_MissingKey(t *testing.T) {
	t.Setenv("JINA_API_KEY", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request may be sent without a key")
	}))
	defer server.Close()

	p := &JinaProvider{Client: server.Client()}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL
	defer func() { jinaSearchURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	want := `jina: Jina credentials not found. Set JINA_API_KEY or configure an API key for provider "jina". (status 0)`
	if spe.Error() != want {
		t.Errorf("Error() = %q, want %q", spe.Error(), want)
	}
}

func TestJina_Search_RequestShape(t *testing.T) {
	var gotMethod, gotPath, gotQ, gotNum, gotAuth, gotAccept, gotRespond, gotRetain string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQ = r.URL.Query().Get("q")
		gotNum = r.URL.Query().Get("num")
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotRespond = r.Header.Get("X-Respond-With")
		gotRetain = r.Header.Get("X-Retain-Images")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":[]}`))
	}))
	defer server.Close()

	p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL + "/search"
	defer func() { jinaSearchURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "test query", Limit: 7})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if resp.Provider != "jina" {
		t.Errorf("Provider = %q, want jina", resp.Provider)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/search" {
		t.Errorf("path = %q, want /search", gotPath)
	}
	if gotQ != "test query" {
		t.Errorf("q = %q, want %q", gotQ, "test query")
	}
	if gotNum != "7" {
		t.Errorf("num = %q, want 7", gotNum)
	}
	if gotAuth != "Bearer jk" {
		t.Errorf("Authorization = %q, want Bearer jk", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if gotRespond != "no-content" {
		t.Errorf("X-Respond-With = %q, want no-content", gotRespond)
	}
	if gotRetain != "none" {
		t.Errorf("X-Retain-Images = %q, want none", gotRetain)
	}
}

// Entry order matters: the skip guards must fire mid-array (junk entry first,
// empty-url entry between two valid ones), so a continue-turned-break drops
// later entries and trips the exact-count assertions.
const jinaEnvelopeFixture = `{"code":200,"status":20000,"data":[` +
	`"junk",` +
	`{"title":"T1","url":"https://a.example","description":"desc one","date":"2024-01-02","content":"long"},` +
	`{"title":"T3","url":"","description":"skipped"},` +
	`{"title":null,"url":"https://b.example","description":"  ","content":" content two ","date":"not-a-date"}` +
	`]}`

func TestJina_Search_Envelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jinaEnvelopeFixture))
	}))
	defer server.Close()

	p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL + "/search"
	defer func() { jinaSearchURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "q"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Sources) != 2 {
		t.Fatalf("len(Sources) = %d, want 2 (empty-url entry skipped)", len(resp.Sources))
	}
	s0 := resp.Sources[0]
	if s0.Title != "T1" || s0.URL != "https://a.example" {
		t.Errorf("Sources[0] = %+v, want T1 / https://a.example", s0)
	}
	if s0.Snippet != "desc one" {
		t.Errorf("Sources[0].Snippet = %q, want description to win", s0.Snippet)
	}
	if s0.PublishedDate != "2024-01-02" {
		t.Errorf("Sources[0].PublishedDate = %q, want 2024-01-02", s0.PublishedDate)
	}
	parsed, err := time.Parse("2006-01-02", "2024-01-02")
	if err != nil {
		t.Fatalf("parse reference date: %v", err)
	}
	wantAge := int64(time.Since(parsed).Seconds())
	if diff := s0.AgeSeconds - wantAge; diff < -5 || diff > 5 {
		t.Errorf("Sources[0].AgeSeconds = %d, want within ±5s of %d", s0.AgeSeconds, wantAge)
	}
	s1 := resp.Sources[1]
	if s1.Title != "https://b.example" {
		t.Errorf("Sources[1].Title = %q, want URL fallback for null title", s1.Title)
	}
	if s1.Snippet != "content two" {
		t.Errorf("Sources[1].Snippet = %q, want trimmed content (blank description)", s1.Snippet)
	}
	if s1.PublishedDate != "not-a-date" || s1.AgeSeconds != 0 {
		t.Errorf("Sources[1] date fields = %q/%d, want raw string kept with age 0", s1.PublishedDate, s1.AgeSeconds)
	}
}

func TestJina_Search_ArrayPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"A","url":"https://a.example","description":"d","content":"c"}]`))
	}))
	defer server.Close()

	p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL + "/search"
	defer func() { jinaSearchURL = oldURL }()

	resp, err := p.Search(context.Background(), SearchParams{Query: "q"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("len(Sources) = %d, want 1 (legacy array shape)", len(resp.Sources))
	}
	if resp.Sources[0].Title != "A" || resp.Sources[0].Snippet != "d" {
		t.Errorf("Sources[0] = %+v, want A with description snippet", resp.Sources[0])
	}
}

func TestJina_Search_Truncation(t *testing.T) {
	fixture := `[`
	for i := range 7 {
		if i > 0 {
			fixture += ","
		}
		fixture += fmt.Sprintf(`{"title":"t%d","url":"https://example.com/%d","description":"d"}`, i, i)
	}
	fixture += `]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()

	p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL + "/search"
	defer func() { jinaSearchURL = oldURL }()

	// Limit 0 selects the default of 5; 7 incoming results truncate to 5.
	resp, err := p.Search(context.Background(), SearchParams{Query: "q", Limit: 0})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Sources) != 5 {
		t.Fatalf("len(Sources) = %d, want 5 (default truncation)", len(resp.Sources))
	}
	if resp.Sources[4].URL != "https://example.com/4" {
		t.Errorf("Sources[4].URL = %q, want the 5th result", resp.Sources[4].URL)
	}
}

func TestJina_Search_EnvelopeFailures(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantErr   string
		rateLimit bool
	}{
		{
			name:      "envelope failure code",
			body:      `{"code":429,"data":null}`,
			wantErr:   `jina: Jina API response reported failure (429) (status 429)`,
			rateLimit: true,
		},
		{
			name:    "data not an array",
			body:    `{"code":200,"data":{"x":1}}`,
			wantErr: `jina: Jina API returned invalid response: expected data array (status 0)`,
		},
		{
			name:    "scalar body",
			body:    `5`,
			wantErr: `jina: Jina API returned invalid response: expected an object or array (status 0)`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()

			p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
			oldURL := jinaSearchURL
			jinaSearchURL = server.URL + "/search"
			defer func() { jinaSearchURL = oldURL }()

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
		})
	}
}

func TestJina_Search_HTTPErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantErr   string
		rateLimit bool
		authError bool
	}{
		{
			name:      "429 quota body beats status",
			status:    429,
			body:      `quota exceeded`,
			wantErr:   `jina: credits exhausted (status 429)`,
			rateLimit: true,
		},
		{
			name:      "401",
			status:    401,
			body:      `no auth`,
			wantErr:   `jina: 401 unauthorized (status 401)`,
			authError: true,
		},
		{
			// Jina keeps the raw body: no JSON extraction, no statusText
			// fallback — deliberately unlike Tavily.
			name:    "500 plain body kept raw",
			status:  500,
			body:    `upstream busy`,
			wantErr: `jina: Jina API error (500): upstream busy (status 500)`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()

			p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
			oldURL := jinaSearchURL
			jinaSearchURL = server.URL + "/search"
			defer func() { jinaSearchURL = oldURL }()

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

func TestJina_Search_ReadBodyError(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)),
			Header:     http.Header{},
		}, nil
	})
	p := &JinaProvider{Client: &http.Client{Transport: transport}, APIKey: "jk"}

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "jina" {
		t.Errorf("Provider = %q, want jina", spe.Provider)
	}
	if spe.Message != "read response: unexpected EOF" {
		t.Errorf("Message = %q, want %q", spe.Message, "read response: unexpected EOF")
	}
	if spe.Status != 200 {
		t.Errorf("Status = %d, want 200 (carries resp.StatusCode)", spe.Status)
	}
}

// Chain order B failover: a jina 429 that matches no quota signal must advance
// the chain to tavily, each provider hit exactly once.
func TestSearchChain_Jina429FallsToTavily(t *testing.T) {
	jinaHits := 0
	jinaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jinaHits++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`too many requests`))
	}))
	defer jinaServer.Close()
	tavilyHits := 0
	tavilyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tavilyHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":"a","results":[{"title":"T","url":"https://t.example","content":"c"}]}`))
	}))
	defer tavilyServer.Close()

	oldJina, oldTavily := jinaSearchURL, tavilyAPIURL
	jinaSearchURL = jinaServer.URL + "/search"
	tavilyAPIURL = tavilyServer.URL
	defer func() {
		jinaSearchURL = oldJina
		tavilyAPIURL = oldTavily
	}()

	chain := &SearchChain{Providers: []SearchProvider{
		&JinaProvider{Client: jinaServer.Client(), APIKey: "jk"},
		&TavilyProvider{Client: tavilyServer.Client(), APIKey: "tk"},
	}}

	resp, err := chain.Search(context.Background(), SearchParams{Query: "q"})
	if err != nil {
		t.Fatalf("chain.Search() error = %v", err)
	}
	if resp.Provider != "tavily" {
		t.Errorf("Provider = %q, want failover to tavily", resp.Provider)
	}
	if jinaHits != 1 {
		t.Errorf("jina hits = %d, want exactly 1", jinaHits)
	}
	if tavilyHits != 1 {
		t.Errorf("tavily hits = %d, want exactly 1", tavilyHits)
	}
}

// Explicit provider selection must not touch the providers before it.
func TestSearchChain_ExplicitTavilySkipsJina(t *testing.T) {
	jinaHits := 0
	jinaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jinaHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":[{"title":"J","url":"https://j.example","description":"d"}]}`))
	}))
	defer jinaServer.Close()
	tavilyHits := 0
	tavilyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tavilyHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://t.example","content":"c"}]}`))
	}))
	defer tavilyServer.Close()

	oldJina, oldTavily := jinaSearchURL, tavilyAPIURL
	jinaSearchURL = jinaServer.URL + "/search"
	tavilyAPIURL = tavilyServer.URL
	defer func() {
		jinaSearchURL = oldJina
		tavilyAPIURL = oldTavily
	}()

	chain := &SearchChain{Providers: []SearchProvider{
		&JinaProvider{Client: jinaServer.Client(), APIKey: "jk"},
		&TavilyProvider{Client: tavilyServer.Client(), APIKey: "tk"},
	}}

	resp, err := chain.SearchWithProvider(context.Background(), SearchParams{Query: "q"}, "tavily")
	if err != nil {
		t.Fatalf("SearchWithProvider() error = %v", err)
	}
	if resp.Provider != "tavily" {
		t.Errorf("Provider = %q, want tavily", resp.Provider)
	}
	if jinaHits != 0 {
		t.Errorf("jina hits = %d, want 0 (explicit selection skips it)", jinaHits)
	}
	if tavilyHits != 1 {
		t.Errorf("tavily hits = %d, want exactly 1", tavilyHits)
	}
}

func TestJina_client_Default(t *testing.T) {
	p := &JinaProvider{}
	if p.client() != http.DefaultClient {
		t.Fatal("expected DefaultClient when Client is nil")
	}
}

func TestJina_Search_RequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // Force the connection to fail.

	p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL + "/search"
	defer func() { jinaSearchURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "jina" {
		t.Errorf("Provider = %q, want jina", spe.Provider)
	}
	if !strings.HasPrefix(spe.Message, "request failed: ") {
		t.Errorf("Message = %q, want prefix %q", spe.Message, "request failed: ")
	}
	if spe.Status != 0 {
		t.Errorf("Status = %d, want 0 (transport failure)", spe.Status)
	}
}

func TestJina_Search_BuildRequestError(t *testing.T) {
	p := &JinaProvider{Client: http.DefaultClient, APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = "http://[::1]:namedport/search" // invalid host
	defer func() { jinaSearchURL = oldURL }()

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

func TestJina_Search_ReadBodyError_Non200(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)),
			Header:     http.Header{},
		}, nil
	})
	p := &JinaProvider{Client: &http.Client{Transport: transport}, APIKey: "jk"}

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "jina" {
		t.Errorf("Provider = %q, want jina", spe.Provider)
	}
	if spe.Message != "read response: unexpected EOF" {
		t.Errorf("Message = %q, want %q", spe.Message, "read response: unexpected EOF")
	}
	if spe.Status != 500 {
		t.Errorf("Status = %d, want 500 (carries resp.StatusCode)", spe.Status)
	}
}

func TestJina_Search_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL + "/search"
	defer func() { jinaSearchURL = oldURL }()

	_, err := p.Search(context.Background(), SearchParams{Query: "q"})
	spe, ok := err.(*SearchProviderError)
	if !ok {
		t.Fatalf("error type = %T, want *SearchProviderError", err)
	}
	if spe.Provider != "jina" {
		t.Errorf("Provider = %q, want jina", spe.Provider)
	}
	if !strings.HasPrefix(spe.Message, "parse response: ") {
		t.Errorf("Message = %q, want prefix %q", spe.Message, "parse response: ")
	}
	if spe.Status != 0 {
		t.Errorf("Status = %d, want 0 (parse failure has no HTTP status)", spe.Status)
	}
}

func TestJina_Search_Clamp(t *testing.T) {
	var gotNum string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNum = r.URL.Query().Get("num")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":[]}`))
	}))
	defer server.Close()

	p := &JinaProvider{Client: server.Client(), APIKey: "jk"}
	oldURL := jinaSearchURL
	jinaSearchURL = server.URL + "/search"
	defer func() { jinaSearchURL = oldURL }()

	for _, c := range []struct {
		limit int
		want  string
	}{
		{0, "5"}, {3, "3"}, {25, "20"}, {-5, "1"},
	} {
		if _, err := p.Search(context.Background(), SearchParams{Query: "q", Limit: c.limit}); err != nil {
			t.Fatalf("Limit=%d: Search() error = %v", c.limit, err)
		}
		if gotNum != c.want {
			t.Errorf("Limit=%d: num = %q, want %q", c.limit, gotNum, c.want)
		}
	}
}
