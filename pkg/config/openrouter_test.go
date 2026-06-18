package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
