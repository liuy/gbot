package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

func TestResolvePrimary(t *testing.T) {
	cfg := &Config{
		Model: ModelSpec{"default": "p1/m1"},
		Providers: []Provider{
			{Name: "p1", URL: "http://127.0.0.1:1", Keys: []string{"k1"}, Models: newModelSet("m1")},
			{Name: "p2", URL: "http://127.0.0.1:2", Keys: []string{"k2"}, Models: newModelSet("m2")},
		},
	}
	providerMap, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	prov, model, pcfg, err := cfg.ResolvePrimary(providerMap)
	if err != nil {
		t.Fatalf("ResolvePrimary: %v", err)
	}
	if prov == nil || prov.Name() != "p1" {
		t.Errorf("provider = %v, want p1", prov)
	}
	if model != "m1" {
		t.Errorf("model = %q, want m1", model)
	}
	if pcfg == nil || pcfg.Name != "p1" {
		t.Errorf("provider cfg = %+v, want p1", pcfg)
	}

	// Model resolving to a provider without a live instance in the map is an
	// error, not a silent fallback.
	cfgNoKey := &Config{
		Model:     ModelSpec{"default": "p2/m2"},
		Providers: []Provider{{Name: "p2", URL: "u", Keys: []string{"k"}, Models: newModelSet("m2")}},
	}
	_, _, _, err = cfgNoKey.ResolvePrimary(ProviderMap{"other": prov})
	if err == nil {
		t.Fatal("ResolvePrimary must fail when the resolved provider has no live instance")
	}
	if !strings.Contains(err.Error(), "p2") {
		t.Errorf("error must name the missing provider, got: %v", err)
	}
}

// TestCreateAllProviders_InputModalitiesWiring drives one end-to-end request
// through a provider built from settings-shaped model declarations: a model
// declaring image input must see its tool-result image on the wire.
func TestCreateAllProviders_InputModalitiesWiring(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		lastBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"c","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	cfg := &Config{Providers: []Provider{{
		Name: "zhipu", URL: srv.URL, Keys: []string{"k"},
		Type: ProviderTypeOpenAI,
		Models: NewModelsFromMap(map[string]ModelConfig{
			"glm-5.3-flash": {Input: []string{"text", "image"}},
		}),
	}}}
	pm, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	resp, err := pm["zhipu"].Complete(t.Context(), &llm.Request{
		Model:     "glm-5.3-flash",
		MaxTokens: 16,
		Messages: []types.Message{
			{Role: types.RoleAssistant, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "c1", Name: "Read", Input: json.RawMessage(`{}`)},
			}},
			{Role: types.RoleUser, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "c1", Content: json.RawMessage(`[{"type":"text","text":"ok"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QQ"}}]`)},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "ok" {
		t.Errorf("response content = %+v, want text %q", resp.Content, "ok")
	}

	var root struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(lastBody, &root); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(root.Messages) != 3 || root.Messages[2].Role != "user" {
		t.Fatalf("messages = %+v, want assistant+tool+trailing user image message", root.Messages)
	}
	if !strings.Contains(string(lastBody), `"image_url"`) {
		t.Error("declared-vision model request carries no image_url, want the tool-result image on the wire")
	}
}

// TestResolvePrimary_ErrorPaths pins the two failure entries: an
// unresolvable model propagates ResolveModel's error verbatim, and a config
// with no providers at all errors instead of returning a nil triple.
func TestResolvePrimary_ErrorPaths(t *testing.T) {
	cfgBadModel := &Config{
		Model:     ModelSpec{"default": "nope/does-not-exist"},
		Providers: []Provider{{Name: "t", URL: "u", Keys: []string{"k"}, Models: newModelSet("m")}},
	}
	_, _, _, err := cfgBadModel.ResolvePrimary(ProviderMap{})
	if err == nil {
		t.Fatal("ResolvePrimary must fail when the configured model resolves to nothing")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error must carry the unresolved model name, got: %v", err)
	}

	cfgNoProviders := &Config{}
	_, _, _, err = cfgNoProviders.ResolvePrimary(ProviderMap{})
	if err == nil {
		t.Fatal("ResolvePrimary must fail with no providers configured")
	}
}

// TestResolvePrimary_KeylessProviderSurvivesSibling pins the shape that keeps
// app's resolvePrimaryProvider guard from being dead code: CreateAllProviders
// skips the keyless provider but a sibling keeps the map non-empty, so the
// boot path reaches ResolvePrimary with a nil error and a missing entry.
func TestResolvePrimary_KeylessProviderSurvivesSibling(t *testing.T) {
	cfg := &Config{
		Model: ModelSpec{"default": "p2/m2"},
		Providers: []Provider{
			{Name: "p1", URL: "http://127.0.0.1:1", Keys: []string{"k1"}, Models: newModelSet("m1")},
			{Name: "p2", URL: "http://127.0.0.1:2", Keys: []string{"$GBOT_PIN_UNSET_KEY"}, Models: newModelSet("m2")},
		},
	}
	pm, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	if len(pm) != 1 {
		t.Fatalf("providerMap has %d entries (%v), want only p1", len(pm), pm)
	}
	if _, ok := pm["p1"]; !ok {
		t.Fatalf("providerMap = %v, want p1", pm)
	}
	_, _, _, err = cfg.ResolvePrimary(pm)
	if err == nil {
		t.Fatal("ResolvePrimary must fail for the resolved provider that was skipped for having no key")
	}
	if !strings.Contains(err.Error(), "p2") {
		t.Errorf("error must name the skipped provider, got: %v", err)
	}
}

// newModelSet builds a Models set holding the given model ids.
func newModelSet(ids ...string) Models {
	m := Models{}
	for _, id := range ids {
		m.Set(id, ModelConfig{})
	}
	return m
}
