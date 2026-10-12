package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFetchFreeModels_HappyPath(t *testing.T) {
	// Mock OpenRouter /models response. FetchFreeModels treats baseURL as
	// the API root (already including /v1 in production: "https://openrouter.ai/api/v1"),
	// so the request path is "/models", not "/api/v1/models".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s (baseURL should already include /v1)", r.URL.Path)
		}
		if r.URL.Query().Get("max_price") != "0" {
			t.Errorf("expected max_price=0, got %q", r.URL.Query().Get("max_price"))
		}
		if r.URL.Query().Get("sort") != "most-popular" {
			t.Errorf("expected sort=most-popular, got %q", r.URL.Query().Get("sort"))
		}
		// Return 3 models, 2 with tools, 1 without.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":                   "qwen/qwen3-coder:free",
					"name":                 "Qwen3 Coder (free)",
					"context_length":       1048576,
					"pricing":              map[string]string{"prompt": "0", "completion": "0"},
					"supported_parameters": []string{"tools", "temperature", "stream"},
				},
				{
					"id":                   "openai/gpt-oss-120b:free",
					"name":                 "GPT-OSS 120B (free)",
					"context_length":       131072,
					"pricing":              map[string]string{"prompt": "0", "completion": "0"},
					"supported_parameters": []string{"temperature"},
				},
				{
					"id":                   "meta-llama/llama-3.3-70b:free",
					"name":                 "Llama 3.3 70B (free)",
					"context_length":       131072,
					"pricing":              map[string]string{"prompt": "0", "completion": "0"},
					"supported_parameters": []string{"tools", "temperature"},
				},
			},
		})
	}))
	defer srv.Close()

	models, err := FetchFreeModels(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchFreeModels error: %v", err)
	}
	// Only 2 models have tools — the one without tools must be filtered.
	if len(models) != 2 {
		t.Fatalf("expected 2 models (with tools), got %d: %+v", len(models), models)
	}
	if models[0].ID != "qwen/qwen3-coder:free" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "qwen/qwen3-coder:free")
	}
	if models[0].ContextLength != 1048576 {
		t.Errorf("models[0].ContextLength = %d, want 1048576", models[0].ContextLength)
	}
	if models[1].ID != "meta-llama/llama-3.3-70b:free" {
		t.Errorf("models[1].ID = %q, want %q", models[1].ID, "meta-llama/llama-3.3-70b:free")
	}
}

func TestFetchFreeModels_Limit10(t *testing.T) {
	data := make([]map[string]any, 15)
	for i := range data {
		data[i] = map[string]any{
			"id":                   "model-" + string(rune('a'+i)),
			"name":                 "Model " + string(rune('a'+i)),
			"context_length":       131072,
			"pricing":              map[string]string{"prompt": "0", "completion": "0"},
			"supported_parameters": []string{"tools"},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	models, err := FetchFreeModels(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchFreeModels error: %v", err)
	}
	if len(models) != 10 {
		t.Fatalf("expected 10 models (capped), got %d", len(models))
	}
}

func TestFetchFreeModels_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := FetchFreeModels(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("want non-nil error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %q, want substring %q", err.Error(), "HTTP 500")
	}
}

// A trailing slash on the configured baseURL must not become "//models".
func TestFetchFreeModels_TrimTrailingSlash(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if _, err := FetchFreeModels(context.Background(), srv.URL+"/"); err != nil {
		t.Fatalf("FetchFreeModels: %v", err)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want /models (trailing slash must be trimmed)", gotPath)
	}
	if gotQuery != "max_price=0&sort=most-popular" {
		t.Errorf("query = %q, want max_price=0&sort=most-popular", gotQuery)
	}
}

func TestFetchFreeModels_InvalidURL(t *testing.T) {
	_, err := FetchFreeModels(context.Background(), "http://[::1")
	if err == nil {
		t.Fatal("want error for unparseable URL")
	}
	if !strings.HasPrefix(err.Error(), "openrouter fetch: ") {
		t.Errorf("err = %q, want prefix %q", err.Error(), "openrouter fetch: ")
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("err %v must wrap a *url.Error", err)
	}
}

func TestFetchFreeModels_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srvURL := srv.URL
	srv.Close() // connection refused

	_, err := FetchFreeModels(context.Background(), srvURL)
	if err == nil {
		t.Fatal("want error when the endpoint is unreachable")
	}
	if !strings.HasPrefix(err.Error(), "openrouter fetch: Get ") {
		t.Errorf("err = %q, want prefix %q", err.Error(), "openrouter fetch: Get ")
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("err %v must wrap a *url.Error", err)
	}
}

// The status code must be interpolated exactly — a mutated format verb or
// a dropped %d would hide which status the endpoint actually returned.
func TestFetchFreeModels_HTTPErrorExactMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := FetchFreeModels(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("want error for HTTP 503")
	}
	if err.Error() != "openrouter fetch: HTTP 503" {
		t.Errorf("err = %q, want %q", err.Error(), "openrouter fetch: HTTP 503")
	}
}

func TestFetchFreeModels_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": [`))
	}))
	defer srv.Close()

	_, err := FetchFreeModels(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("want error for truncated JSON body")
	}
	if !strings.HasPrefix(err.Error(), "openrouter decode: ") {
		t.Errorf("err = %q, want prefix %q", err.Error(), "openrouter decode: ")
	}
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		t.Errorf("truncated body should surface io.ErrUnexpectedEOF, got syntax error %v", se)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err %v must wrap io.ErrUnexpectedEOF", err)
	}
}

// Free means BOTH prompt and completion price "0" — an OR of the two
// conditions would let half-paid models into the picker.
func TestFetchFreeModels_PricingFilterBothSides(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "paid-completion", "pricing": map[string]string{"prompt": "0", "completion": "0.5"}, "supported_parameters": []string{"tools"}},
				{"id": "paid-prompt", "pricing": map[string]string{"prompt": "1", "completion": "0"}, "supported_parameters": []string{"tools"}},
				{"id": "no-pricing", "pricing": map[string]string{}, "supported_parameters": []string{"tools"}},
				{"id": "free", "context_length": 131072, "pricing": map[string]string{"prompt": "0", "completion": "0"}, "supported_parameters": []string{"tools"}},
			},
		})
	}))
	defer srv.Close()

	models, err := FetchFreeModels(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchFreeModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %+v, want exactly 1 (only free passes both price checks)", models)
	}
	if models[0].ID != "free" {
		t.Errorf("models[0].ID = %q, want free", models[0].ID)
	}
}

// The cap must keep the FIRST 10 (server sorts by popularity), not just
// any 10.
func TestFetchFreeModels_Limit10KeepsFirstTen(t *testing.T) {
	data := make([]map[string]any, 15)
	for i := range data {
		data[i] = map[string]any{
			"id":                   fmt.Sprintf("model-%02d", i),
			"context_length":       131072,
			"pricing":              map[string]string{"prompt": "0", "completion": "0"},
			"supported_parameters": []string{"tools"},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	models, err := FetchFreeModels(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchFreeModels: %v", err)
	}
	if len(models) != 10 {
		t.Fatalf("len(models) = %d, want 10", len(models))
	}
	for i := range 10 {
		want := fmt.Sprintf("model-%02d", i)
		if models[i].ID != want {
			t.Errorf("models[%d].ID = %q, want %q", i, models[i].ID, want)
		}
	}
}
