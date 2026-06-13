package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleHuggingFace_NoMatch(t *testing.T) {
	result, _ := HandleHuggingFace(context.Background(), mustParseURL(t, "https://example.com/models/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-HF host, got %+v", result)
	}
}

func TestHandleHuggingFace_Model(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/models/") {
			_, _ = w.Write([]byte(`{"id":"bert-base","modelId":"bert-base","author":"google","downloads":1000000,"likes":50,"tags":["transformer","bert"],"pipeline_tag":"fill-mask","library_name":"transformers"}`))
			return
		}
		_, _ = w.Write([]byte("# BERT\n\nBidirectional encoder."))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/bert-base"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "bert-base") {
		t.Errorf("expected model name, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "1,000,000") {
		t.Errorf("expected downloads, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_Dataset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/datasets/") {
			_, _ = w.Write([]byte(`{"id":"squad","author":"rajpurkar","downloads":500000}`))
			return
		}
		_, _ = w.Write([]byte("# SQuAD"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/datasets/squad"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "squad") {
		t.Errorf("expected dataset name, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_Space(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/spaces/") {
			_, _ = w.Write([]byte(`{"id":"gradio/chatbot","author":"gradio","likes":100}`))
			return
		}
		_, _ = w.Write([]byte("# Chatbot"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/spaces/gradio/chatbot"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "gradio") {
		t.Errorf("expected author, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_User(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/models/nouser") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/api/users/nouser") {
			_, _ = w.Write([]byte(`{"user":"nouser","fullname":"No User","numModels":5,"numDatasets":3,"numSpaces":2,"orgs":[{"name":"org1"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/nouser"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "nouser") {
		t.Errorf("expected username, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "org1") {
		t.Errorf("expected org, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_DatasetWithCardData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/datasets/") {
			_, _ = w.Write([]byte(`{"id":"squad","description":"Reading comprehension dataset","downloads":50000,"likes":10,"private":true,"cardData":{"license":"CC-BY-SA-4.0","task_categories":["question-answering"],"size_categories":["100K<n<1M"]},"tags":["nlp"]}`))
			return
		}
		_, _ = w.Write([]byte("# SQuAD"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/datasets/squad"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "CC-BY-SA-4.0") {
		t.Errorf("expected license, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "question-answering") {
		t.Errorf("expected task, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Private") {
		t.Errorf("expected private marker, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_SpaceFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/spaces/") {
			_, _ = w.Write([]byte(`{"id":"gradio/chatbot","author":"gradio","likes":100,"tags":["gradio","chatbot"],"sdk":"gradio","private":true,"gated":false}`))
			return
		}
		_, _ = w.Write([]byte("# Chatbot"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/spaces/gradio/chatbot"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "gradio") {
		t.Errorf("expected author, got: %q", result.Content)
	}
}

func TestRenderHFModel_FullCardData(t *testing.T) {
	model := hfModel{
		ModelID:     "bert-base",
		PipelineTag: "fill-mask",
		LibraryName: "transformers",
		Downloads:   1000,
		Likes:       50,
		Tags:        []string{"nlp", "bert"},
		CardData: &struct {
			License  string   `json:"license"`
			Language any      `json:"language"`
			Datasets []string `json:"datasets"`
			Metrics  []string `json:"metrics"`
		}{
			License:  "MIT",
			Language: "en",
			Datasets: []string{"squad"},
			Metrics:  []string{"accuracy"},
		},
	}
	md := renderHFModel(model)
	for _, s := range []string{"fill-mask", "transformers", "MIT", "en", "squad", "accuracy", "nlp", "bert"} {
		if !strings.Contains(md, s) {
			t.Errorf("renderHFModel missing %q: %q", s, md)
		}
	}
}

func TestRenderHFModel_CardDataWithSliceLanguage(t *testing.T) {
	model := hfModel{
		ModelID: "multi",
		CardData: &struct {
			License  string   `json:"license"`
			Language any      `json:"language"`
			Datasets []string `json:"datasets"`
			Metrics  []string `json:"metrics"`
		}{
			Language: []any{"en", "fr"},
		},
	}
	md := renderHFModel(model)
	if !strings.Contains(md, "en, fr") {
		t.Errorf("renderHFModel missing language slice: %q", md)
	}
}

func TestRenderHFModel_EmptyPipelineTag(t *testing.T) {
	model := hfModel{ModelID: "test", Downloads: 0, Likes: 0}
	md := renderHFModel(model)
	if md == "" {
		t.Fatal("renderHFModel returned empty for minimal model")
	}
	if strings.Contains(md, "**Task:**") {
		t.Errorf("unexpected Task field: %q", md)
	}
	if strings.Contains(md, "**Library:**") {
		t.Errorf("unexpected Library field: %q", md)
	}
}

func TestRenderHFSpace_Full(t *testing.T) {
	space := hfSpace{
		ID:      "gradio/chatbot",
		Author:  "gradio",
		Title:   "ChatBot",
		SDK:     "gradio",
		Likes:   100,
		Private: true,
		Tags:    []string{"gradio", "nlp"},
		CardData: &struct {
			License string `json:"license"`
			SDK     string `json:"sdk"`
			AppFile string `json:"app_file"`
		}{
			License: "Apache-2.0",
			AppFile: "app.py",
		},
	}
	md := renderHFSpace(space)
	for _, s := range []string{"gradio/chatbot", "ChatBot", "gradio", "Apache-2.0", "app.py", "Private"} {
		if !strings.Contains(md, s) {
			t.Errorf("renderHFSpace missing %q: %q", s, md)
		}
	}
}

func TestFetchPlainText_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	_, err := fetchPlainText(context.Background(), client, srv.URL+"/missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestFetchPlainText_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchPlainText(ctx, &http.Client{Transport: &redirectTransport{server: srv}}, srv.URL)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %q, want 'context'", err.Error())
	}
}

func TestHandleHuggingFace_NoID(t *testing.T) {
	result, _ := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for root path, got %+v", result)
	}
}

func TestHandleHuggingFace_ReservedPath(t *testing.T) {
	result, _ := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/docs/transformers"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for reserved path, got %+v", result)
	}
}

func TestHandleHuggingFace_ModelAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/owner/model"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when API fails, got %+v", result)
	}
}

func TestHandleHuggingFace_DatasetAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/datasets/owner/dataset"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when API fails, got %+v", result)
	}
}

func TestHandleHuggingFace_SpaceAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/spaces/user/space"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when API fails, got %+v", result)
	}
}

func TestHandleHuggingFace_ModelFallsToUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/models/foo") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/api/users/foo") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":"foo","fullname":"Foo Bar","numModels":3,"numDatasets":2,"numSpaces":1,"orgs":[{"name":"org1"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Foo Bar") {
		t.Errorf("expected fullname, got: %q", result.Content)
	}
}

func TestHandleHuggingFace_BothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when both API calls fail, got %+v", result)
	}
}

func TestHandleHuggingFace_ModelGated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/models/") {
			_, _ = w.Write([]byte(`{"id":"gated-model","modelId":"gated-model","gated":"manual","private":true,"downloads":100,"library_name":"transformers","tags":["nlp"]}`))
			return
		}
		_, _ = w.Write([]byte("# Model Card"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &redirectTransport{server: srv}}
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/owner/gated-model"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Gated") {
		t.Errorf("expected Gated marker, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Private") {
		t.Errorf("expected Private marker, got: %q", result.Content)
	}
}

func TestParseHuggingFaceURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://huggingface.co/bert-base", "modelOrUser"},
		{"https://huggingface.co/owner/bert-base", "model"},
		{"https://huggingface.co/datasets/squad", "dataset"},
		{"https://huggingface.co/datasets/owner/squad", "dataset"},
		{"https://huggingface.co/spaces/gradio/chatbot", "space"},
		{"https://huggingface.co/owner", "modelOrUser"},
		{"https://huggingface.co/", "none"},
		{"https://example.com/foo", "none"},
		{"https://huggingface.co/docs/transformers", "none"},
	}
	for _, tt := range tests {
		u := mustParseURL(t, tt.url)
		got := parseHuggingFaceURL(u)
		switch tt.want {
		case "none":
			if got != nil {
				t.Errorf("parseHuggingFaceURL(%q) = %+v, want nil", tt.url, got)
			}
		case "model":
			if got == nil || got.kind != hfKindModel {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want model", tt.url, got)
			}
		case "modelOrUser":
			if got == nil || got.kind != hfKindModelOrUser {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want modelOrUser", tt.url, got)
			}
		case "dataset":
			if got == nil || got.kind != hfKindDataset {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want dataset", tt.url, got)
			}
		case "space":
			if got == nil || got.kind != hfKindSpace {
				t.Errorf("parseHuggingFaceURL(%q) kind = %v, want space", tt.url, got)
			}
		}
	}
}
