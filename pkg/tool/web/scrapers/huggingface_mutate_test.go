package scrapers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hfSrv(t *testing.T, apiJSON, readme string) (*http.Client, *[]string) {
	t.Helper()
	paths := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.EscapedPath())
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if apiJSON == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, apiJSON)
			return
		}
		if readme == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, readme)
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &rewriteTransport{server: srv, host: "huggingface.co"}}, paths
}

func TestHandleHuggingFace_ModelAllFields(t *testing.T) {
	client, paths := hfSrv(t, `{"modelId":"org/model","pipeline_tag":"text-classification",
		"library_name":"transformers","tags":["nlp","text"],"downloads":1234,"likes":12,"private":true,
		"gated":"auto","cardData":{"license":"apache-2.0","language":"en","datasets":["squad"],"metrics":["accuracy"]}}`,
		"  # Card body  ")
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/org/model"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# org/model\n\n**Task:** text-classification\n**Library:** transformers\n" +
		"**Downloads:** 1,234\n**Likes:** 12\n**Visibility:** Private\n**Access:** Gated\n" +
		"**License:** apache-2.0\n**Language:** en\n**Datasets:** squad\n**Metrics:** accuracy\n" +
		"**Tags:** nlp, text\n\n## Model Card\n\n# Card body"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" || result.Method != "huggingface-api" {
		t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via HuggingFace API" {
		t.Errorf("Notes = %q", result.Notes)
	}
	wantPaths := []string{"/api/models/org/model", "/org/model/raw/main/README.md"}
	if strings.Join(*paths, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("paths = %q, want %q", *paths, wantPaths)
	}
}

func TestHandleHuggingFace_ModelWithoutReadme(t *testing.T) {
	client, _ := hfSrv(t, `{"modelId":"org/m","downloads":1,"likes":1,"tags":["t"],"gated":false}`, "")
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/org/m"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# org/m\n\n**Downloads:** 1\n**Likes:** 1\n**Tags:** t\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleHuggingFace_DatasetAllFields(t *testing.T) {
	client, paths := hfSrv(t, `{"id":"org/dset","tags":["t1"],"downloads":1000,"likes":2,"private":true,
		"gated":true,"description":"A dataset.","cardData":{"license":"mit","language":["en","fr"],
		"task_categories":["cls"],"size_categories":["1K"]}}`, "DS card")
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/datasets/org/dset"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# org/dset\n\nA dataset.\n\n**Downloads:** 1,000\n**Likes:** 2\n**Visibility:** Private\n" +
		"**Access:** Gated\n**License:** mit\n**Language:** en, fr\n**Tasks:** cls\n**Size:** 1K\n**Tags:** t1\n\n" +
		"## Dataset Card\n\nDS card"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" || result.Method != "huggingface-api" {
		t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via HuggingFace API" {
		t.Errorf("Notes = %q", result.Notes)
	}
	if (*paths)[0] != "/api/datasets/org/dset" {
		t.Errorf("api path = %q", (*paths)[0])
	}
}

func TestHandleHuggingFace_SpaceAllFields(t *testing.T) {
	client, paths := hfSrv(t, `{"id":"org/sp","author":"someone","title":"A Space","sdk":"gradio",
		"tags":["s1"],"likes":1,"private":true,"cardData":{"license":"apache","app_file":"app.py"}}`, "SP card")
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/spaces/org/sp"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# org/sp\n\nA Space\n\n**Author:** someone\n**SDK:** gradio\n**Likes:** 1\n" +
		"**Visibility:** Private\n**License:** apache\n**App File:** app.py\n**Tags:** s1\n\n## Space Info\n\nSP card"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" || result.Method != "huggingface-api" {
		t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via HuggingFace API" {
		t.Errorf("Notes = %q", result.Notes)
	}
	if (*paths)[0] != "/api/spaces/org/sp" {
		t.Errorf("api path = %q", (*paths)[0])
	}
}

// A single path segment is ambiguous: the model endpoint is tried first.
func TestHandleHuggingFace_SingleSegmentFallsBackToUser(t *testing.T) {
	client, paths := hfSrv(t, "", "")
	// Both endpoints 404, so nothing is rendered.
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/someone"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when model and user both fail, got %+v", result)
	}
	if len(*paths) != 2 || (*paths)[0] != "/api/models/someone" || (*paths)[1] != "/api/users/someone" {
		t.Errorf("paths = %q, want model then user", *paths)
	}
}

func TestHandleHuggingFace_SingleSegmentModel(t *testing.T) {
	client, _ := hfSrv(t, `{"modelId":"solo","downloads":1}`, "")
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/solo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Content != "# solo\n\n**Downloads:** 1\n\n" {
		t.Errorf("Content = %q", result.Content)
	}
}

// The single-segment model path also carries the model card.
func TestHandleHuggingFace_SingleSegmentModelWithCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/solo":
			_, _ = io.WriteString(w, `{"modelId":"solo","downloads":1}`)
		case "/solo/raw/main/README.md":
			_, _ = io.WriteString(w, "Solo card")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "huggingface.co"}}

	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/solo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# solo\n\n**Downloads:** 1\n\n## Model Card\n\nSolo card"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" || result.Method != "huggingface-api" {
		t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via HuggingFace API" {
		t.Errorf("Notes = %q", result.Notes)
	}
}

func TestHandleHuggingFace_UserAllCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/someone":
			w.WriteHeader(http.StatusNotFound)
		case "/api/users/someone":
			_, _ = io.WriteString(w, `{"user":"someone","fullname":"Some One","numModels":1234,
				"numDatasets":5,"numSpaces":1,"orgs":[{"name":"Org A"},{"name":"Org B"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "huggingface.co"}}

	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/someone"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# someone\n\n**Name:** Some One\n**Models:** 1,234\n**Datasets:** 5\n**Spaces:** 1\n" +
		"**Organizations:** Org A, Org B\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" || result.Method != "huggingface-api" {
		t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via HuggingFace API" {
		t.Errorf("Notes = %q", result.Notes)
	}
}

// Counts of exactly one separate "at least one" from "more than one".
func TestHandleHuggingFace_UserSingleCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/someone":
			w.WriteHeader(http.StatusNotFound)
		case "/api/users/someone":
			_, _ = io.WriteString(w, `{"user":"someone","numModels":1,"numDatasets":1,"numSpaces":1,
				"orgs":[{"name":"Only Org"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "huggingface.co"}}

	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/someone"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# someone\n\n**Models:** 1\n**Datasets:** 1\n**Spaces:** 1\n**Organizations:** Only Org\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleHuggingFace_UserWithoutCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/someone":
			w.WriteHeader(http.StatusNotFound)
		case "/api/users/someone":
			_, _ = io.WriteString(w, `{"user":"someone","fullname":"","numModels":0,"numDatasets":0,
				"numSpaces":0,"orgs":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "huggingface.co"}}

	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/someone"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Content != "# someone\n\n" {
		t.Errorf("Content = %q, want %q", result.Content, "# someone\n\n")
	}
}

func TestParseHuggingFaceURL_AllKinds(t *testing.T) {
	tests := []struct {
		raw  string
		kind hfKind
		id   string
		nil_ bool
	}{
		{"https://huggingface.co/org/model", hfKindModel, "org/model", false},
		{"https://huggingface.co/datasets/org/dset", hfKindDataset, "org/dset", false},
		{"https://huggingface.co/datasets/only", hfKindDataset, "only", false},
		{"https://huggingface.co/spaces/org/sp", hfKindSpace, "org/sp", false},
		{"https://huggingface.co/spaces/org", hfKindModel, "spaces/org", false},
		{"https://huggingface.co/spaces/org/sp/extra", hfKindSpace, "org/sp", false},
		{"https://huggingface.co/solo", hfKindModelOrUser, "solo", false},
		{"https://huggingface.co/datasets", hfKindModelOrUser, "datasets", false},
		{"https://huggingface.co/spaces", hfKindModelOrUser, "spaces", false},
		{"https://huggingface.co/org/model/extra", hfKindModel, "org/model", false},
		{"https://huggingface.co/docs/guide", hfKindModel, "docs/guide", true},
		{"https://huggingface.co/blog", hfKindModelOrUser, "blog", true},
		{"https://huggingface.co/pricing", hfKindModelOrUser, "pricing", true},
		{"https://huggingface.co/enterprise", hfKindModelOrUser, "enterprise", true},
		{"https://huggingface.co/join", hfKindModelOrUser, "join", true},
		{"https://huggingface.co/login", hfKindModelOrUser, "login", true},
		{"https://huggingface.co/settings/x", hfKindModel, "settings/x", true},
		{"https://huggingface.co/", hfKindModel, "", true},
		{"https://example.com/org/model", hfKindModel, "org/model", true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := parseHuggingFaceURL(mustParseURL(t, tt.raw))
			if tt.nil_ {
				if got != nil {
					t.Errorf("parseHuggingFaceURL(%q) = %+v, want nil", tt.raw, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseHuggingFaceURL(%q) = nil, want kind %d id %q", tt.raw, tt.kind, tt.id)
			}
			if got.kind != tt.kind || got.ID != tt.id {
				t.Errorf("parseHuggingFaceURL(%q) = kind %d id %q, want kind %d id %q", tt.raw, got.kind, got.ID, tt.kind, tt.id)
			}
		})
	}
}

func TestHandleHuggingFace_Unhandled(t *testing.T) {
	client, _ := hfSrv(t, "", "")
	for _, raw := range []string{"https://example.com/org/model", "https://huggingface.co/", "https://huggingface.co/docs/x"} {
		result, err := HandleHuggingFace(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestHandleHuggingFace_ModelAPIFailure(t *testing.T) {
	client, _ := hfSrv(t, "", "")
	result, err := HandleHuggingFace(context.Background(), mustParseURL(t, "https://huggingface.co/org/model"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when the model API fails, got %+v", result)
	}
}

func TestFetchAndDecode_TransportError(t *testing.T) {
	sentinel := errors.New("hf unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	var dst hfModel
	err := fetchAndDecode(context.Background(), client, "https://huggingface.co/api/models/x", &dst)
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
}

func TestFetchPlainText_TrimsAndPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "\n  body text \n\n")
	}))
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "huggingface.co"}}
	got, err := fetchPlainText(context.Background(), client, "https://huggingface.co/x/raw/main/README.md")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got != "body text" {
		t.Errorf("got %q, want %q", got, "body text")
	}

	sentinel := errors.New("readme unreachable")
	bad := &http.Client{Transport: errTransport{err: sentinel}}
	if _, err := fetchPlainText(context.Background(), bad, "https://huggingface.co/x"); !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
}

func TestRenderHFModel(t *testing.T) {
	full := hfModel{ModelID: "org/m", PipelineTag: "task", LibraryName: "lib", Downloads: 1234567,
		Likes: 1, Private: true, Gated: "auto",
		CardData: &struct {
			License  string   `json:"license"`
			Language any      `json:"language"`
			Datasets []string `json:"datasets"`
			Metrics  []string `json:"metrics"`
		}{License: "mit", Language: []any{"en", "fr"}, Datasets: []string{"d1"}, Metrics: []string{"m1"}},
		Tags: []string{"t1"}}
	want := "# org/m\n\n**Task:** task\n**Library:** lib\n**Downloads:** 1,234,567\n**Likes:** 1\n" +
		"**Visibility:** Private\n**Access:** Gated\n**License:** mit\n**Language:** en, fr\n" +
		"**Datasets:** d1\n**Metrics:** m1\n**Tags:** t1\n\n"
	if got := renderHFModel(full); got != want {
		t.Errorf("renderHFModel() = %q, want %q", got, want)
	}

	empty := hfModel{ModelID: "e", CardData: &struct {
		License  string   `json:"license"`
		Language any      `json:"language"`
		Datasets []string `json:"datasets"`
		Metrics  []string `json:"metrics"`
	}{}}
	if got := renderHFModel(empty); got != "# e\n\n\n" {
		t.Errorf("renderHFModel(empty cardData) = %q, want %q", got, "# e\n\n\n")
	}

	minimal := hfModel{ModelID: "bare"}
	if got := renderHFModel(minimal); got != "# bare\n\n\n" {
		t.Errorf("renderHFModel() = %q, want %q", got, "# bare\n\n\n")
	}

	for _, gated := range []any{nil, false} {
		m := hfModel{ModelID: "g", Gated: gated}
		if got := renderHFModel(m); got != "# g\n\n\n" {
			t.Errorf("renderHFModel(gated=%v) = %q, want no Access line", gated, got)
		}
	}
	for _, gated := range []any{true, "auto"} {
		m := hfModel{ModelID: "g", Gated: gated}
		if !strings.Contains(renderHFModel(m), "**Access:** Gated\n") {
			t.Errorf("renderHFModel(gated=%v) missing Access line: %q", gated, renderHFModel(m))
		}
	}
}

func TestRenderHFDataset(t *testing.T) {
	full := hfDataset{ID: "org/d", Tags: []string{"t1"}, Downloads: 1, Likes: 1234, Private: true, Gated: true,
		Description: "Desc.",
		CardData: &struct {
			License        string   `json:"license"`
			Language       any      `json:"language"`
			TaskCategories []string `json:"task_categories"`
			SizeCategories []string `json:"size_categories"`
		}{License: "mit", Language: "en", TaskCategories: []string{"cls"}, SizeCategories: []string{"1K"}}}
	want := "# org/d\n\nDesc.\n\n**Downloads:** 1\n**Likes:** 1,234\n**Visibility:** Private\n" +
		"**Access:** Gated\n**License:** mit\n**Language:** en\n**Tasks:** cls\n**Size:** 1K\n**Tags:** t1\n\n"
	if got := renderHFDataset(full); got != want {
		t.Errorf("renderHFDataset() = %q, want %q", got, want)
	}
	one := hfDataset{ID: "d", Downloads: 1, Likes: 1, CardData: &struct {
		License        string   `json:"license"`
		Language       any      `json:"language"`
		TaskCategories []string `json:"task_categories"`
		SizeCategories []string `json:"size_categories"`
	}{}}
	if got := renderHFDataset(one); got != "# d\n\n**Downloads:** 1\n**Likes:** 1\n\n" {
		t.Errorf("renderHFDataset(one) = %q", got)
	}

	minimal := hfDataset{ID: "d"}
	if got := renderHFDataset(minimal); got != "# d\n\n\n" {
		t.Errorf("renderHFDataset() = %q, want %q", got, "# d\n\n\n")
	}
	// gated:false is an explicit "not gated"; only a missing field is unknown.
	ungated := hfDataset{ID: "g", Gated: false}
	if got := renderHFDataset(ungated); got != "# g\n\n\n" {
		t.Errorf("renderHFDataset(gated=false) = %q, want no Access line", got)
	}
}

func TestRenderHFSpace(t *testing.T) {
	full := hfSpace{ID: "org/s", Author: "a", Title: "T", SDK: "gradio", Tags: []string{"t1"}, Likes: 1,
		Private: true,
		CardData: &struct {
			License string `json:"license"`
			SDK     string `json:"sdk"`
			AppFile string `json:"app_file"`
		}{License: "mit", AppFile: "app.py"}}
	want := "# org/s\n\nT\n\n**Author:** a\n**SDK:** gradio\n**Likes:** 1\n**Visibility:** Private\n" +
		"**License:** mit\n**App File:** app.py\n**Tags:** t1\n\n"
	if got := renderHFSpace(full); got != want {
		t.Errorf("renderHFSpace() = %q, want %q", got, want)
	}
	minimal := hfSpace{ID: "s"}
	if got := renderHFSpace(minimal); got != "# s\n\n\n" {
		t.Errorf("renderHFSpace() = %q, want %q", got, "# s\n\n\n")
	}
}

func TestRenderLanguageField_Variants(t *testing.T) {
	tests := []struct {
		name string
		lang any
		want string
	}{
		{"nil", nil, ""},
		{"string", "en", "**Language:** en\n"},
		{"list", []any{"en", "fr"}, "**Language:** en, fr\n"},
		{"empty list", []any{}, "**Language:** \n"},
		{"other type", 42, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			renderLanguageField(&sb, tt.lang)
			if sb.String() != tt.want {
				t.Errorf("renderLanguageField(%v) = %q, want %q", tt.lang, sb.String(), tt.want)
			}
		})
	}
}
