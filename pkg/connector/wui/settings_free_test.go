package wui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The free path must hit the same filtered query as the startup fetch
// (max_price=0, popularity-sorted) — otherwise the UI preview diverges
// from what a free:true provider actually gets at boot.
func TestFetchModels_FreeTop10(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek/deepseek-r1:free","context_length":64000,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`))
	}))
	defer upstream.Close()

	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	req := httptest.NewRequest("POST", "/api/settings/models",
		strings.NewReader(`{"url":"`+upstream.URL+`","key":"k","type":"openai","free":true}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	u, err := url.Parse(gotPath)
	if err != nil {
		t.Fatalf("upstream path %q: %v", gotPath, err)
	}
	if u.Path != "/models" || u.Query().Get("max_price") != "0" || u.Query().Get("sort") != "most-popular" {
		t.Errorf("upstream query = %q, want max_price=0&sort=most-popular", gotPath)
	}
	body := rec.Body.String()
	// 64000 < the humanize threshold — IntOrHuman passes it through raw.
	for _, want := range []string{`"id":"deepseek/deepseek-r1:free"`, `"context":"64000"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %s: %s", want, body)
		}
	}
}

// A zero/absent context_length must omit the context field entirely — a
// literal "0" would defeat the frontend's placeholder fallback.
func TestFetchModels_FreeZeroContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m:free","context_length":0,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`))
	}))
	defer upstream.Close()

	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	req := httptest.NewRequest("POST", "/api/settings/models",
		strings.NewReader(`{"url":"`+upstream.URL+`","key":"k","type":"openai","free":true}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"context"`) {
		t.Errorf("zero context_length leaked into response: %s", body)
	}
}

// Upstream failures surface as mode:error with the upstream status text.
func TestFetchModels_FreeUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	req := httptest.NewRequest("POST", "/api/settings/models",
		strings.NewReader(`{"url":"`+upstream.URL+`","key":"k","type":"openai","free":true}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `"mode":"error"`) || !strings.Contains(rec.Body.String(), "500") {
		t.Errorf("want mode:error with status, got %s", rec.Body.String())
	}
}
