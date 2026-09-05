package wui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// settingsFixture mirrors the shape of the real ~/.gbot/settings.json:
// model tier + permission_mode + two providers, one explicitly typed
// ("openai") and one untyped (auto).
const settingsFixture = `{
  "model": {"default": "zhipu/glm-5.3"},
  "permission_mode": "default",
  "providers": [
    {
      "name": "xiaomi",
      "url": "https://api.xiaomimimo.com/anthropic",
      "keys": ["sk-xiaomi-test"],
      "models": {
        "mimo-v2.5-pro": {"context": "500k", "input": ["text", "image", "video"]},
        "mimo-v2.5": {"context": "500k"}
      }
    },
    {
      "name": "zhipu",
      "type": "openai",
      "url": "https://open.bigmodel.cn/api/coding/paas/v4",
      "keys": ["zhipu-test-key"],
      "models": {
        "glm-5.3": {"context": "1M"},
        "glm-5.3-flash": {"context": "1M", "input": ["text", "image"]}
      },
      "extra_params": {"tool_stream": true}
    }
  ]
}`

// newSettingsServer mounts the settings routes on a fresh mux over an
// isolated HOME (t.Setenv keeps every test off the real ~/.gbot).
func newSettingsServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// seedSettings pre-writes the fixture into $HOME/.gbot/settings.json and
// returns its path and exact bytes (for unchanged assertions).
func seedSettings(t *testing.T, content string) (path string, raw []byte) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".gbot", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path, []byte(content)
}

// getJSON fetches url and decodes the body into out.
func getJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, body)
	}
	return resp
}

// settingsProviders is the wire shape of GET /api/settings/providers.
type settingsProviders struct {
	Providers []map[string]any `json:"providers"`
	Default   struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"default"`
}

func TestSettingsGet_ProvidersAndDefault(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, settingsFixture)

	var got settingsProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(got.Providers) != 2 {
		t.Fatalf("providers length = %d, want 2", len(got.Providers))
	}
	if got.Providers[0]["name"] != "xiaomi" || got.Providers[1]["name"] != "zhipu" {
		t.Errorf("provider order = %v,%v — must match file order", got.Providers[0]["name"], got.Providers[1]["name"])
	}

	zhipu := got.Providers[1]
	if zhipu["url"] != "https://open.bigmodel.cn/api/coding/paas/v4" {
		t.Errorf("zhipu url = %v", zhipu["url"])
	}
	if keys, ok := zhipu["keys"].([]any); !ok || len(keys) != 1 || keys[0] != "zhipu-test-key" {
		t.Errorf("zhipu keys = %v", zhipu["keys"])
	}
	if zhipu["type"] != "openai" {
		t.Errorf("explicit type must round-trip, got %v", zhipu["type"])
	}
	if extra, ok := zhipu["extra_params"].(map[string]any); !ok || extra["tool_stream"] != true {
		t.Errorf("zhipu extra_params = %v", zhipu["extra_params"])
	}
	models, ok := zhipu["models"].(map[string]any)
	if !ok {
		t.Fatalf("zhipu models not an object: %v", zhipu["models"])
	}
	glm, ok := models["glm-5.3"].(map[string]any)
	if !ok {
		t.Fatalf("glm-5.3 missing: %v", models)
	}
	// "1M" survives the round-trip in human form (IntOrHuman marshals
	// exact k/M multiples back as strings).
	if glm["context"] != "1M" {
		t.Errorf("glm-5.3 context = %v, want 1M", glm["context"])
	}

	if _, typed := got.Providers[0]["type"]; typed {
		t.Errorf("xiaomi type must be absent (omitempty), got %v", got.Providers[0]["type"])
	}

	if got.Default.Provider != "zhipu" || got.Default.Model != "glm-5.3" {
		t.Errorf("default = %+v, want zhipu/glm-5.3", got.Default)
	}
}

func TestSettingsGet_MissingFile(t *testing.T) {
	srv := newSettingsServer(t)

	var got settingsProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 on cold start", resp.StatusCode)
	}
	if got.Providers == nil {
		t.Errorf("providers must be [], never null")
	}
	if len(got.Providers) != 0 {
		t.Errorf("providers = %v, want empty", got.Providers)
	}
	if got.Default.Provider != "" || got.Default.Model != "" {
		t.Errorf("default = %+v, want empty strings", got.Default)
	}
}

func TestSettingsGet_EmptyProviders(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, `{}`)

	var got settingsProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got.Providers == nil || len(got.Providers) != 0 {
		t.Errorf("providers = %v, want [] (never null)", got.Providers)
	}
}

func TestSettingsPut_WritesFileAndBacksUp(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)

	body := `[{
      "name": "minimax",
      "url": "https://api.minimax.chat/v1",
      "keys": ["mm-key"],
      "models": {"abab6.5s-chat": {"context": "245k", "max_tokens": "32k"}}
    }, {
      "name": "zhipu",
      "type": "openai",
      "url": "https://open.bigmodel.cn/api/coding/paas/v4",
      "keys": ["zhipu-key-2"],
      "models": {"glm-5.3": {}},
      "extra_params": {"tool_stream": false}
    }]`
	resp, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/providers", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	putResp, err := srv.Client().Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer putResp.Body.Close()
	respBody, _ := io.ReadAll(putResp.Body)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", putResp.StatusCode, respBody)
	}
	if strings.TrimSpace(string(respBody)) != `{"ok":true}` {
		t.Errorf("body = %s, want {\"ok\":true}", respBody)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("file invalid: %v", err)
	}
	// The `model` key must survive byte-identical (semantic compare — the
	// whole file is re-indented by design).
	if !jsonEqual(t, file["model"], json.RawMessage(`{"default": "zhipu/glm-5.3"}`)) {
		t.Errorf("model key = %s, want {\"default\":\"zhipu/glm-5.3\"}", file["model"])
	}
	var providers []map[string]any
	if err := json.Unmarshal(file["providers"], &providers); err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || providers[0]["name"] != "minimax" || providers[1]["name"] != "zhipu" {
		t.Errorf("written providers = %+v", providers)
	}

	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf(".bak missing: %v", err)
	}
	if string(bak) != string(old) {
		t.Errorf(".bak must hold the pre-save bytes verbatim")
	}
}

func TestSettingsPut_Validation(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)

	cases := []struct {
		name string
		body string
	}{
		{"empty array", `[]`},
		{"missing name", `[{"name":"","url":"https://a","keys":["k"],"models":{"m":{}}}]`},
		{"duplicate names", `[
		  {"name":"dupe","url":"https://a","keys":["k"],"models":{"m":{}}},
		  {"name":"dupe","url":"https://b","keys":["k"],"models":{"m":{}}}]`},
		{"missing url", `[{"name":"p","url":"","keys":["k"],"models":{"m":{}}}]`},
		{"zero models", `[{"name":"p","url":"https://a","keys":["k"],"models":{}}]`},
		{"empty-string key", `[{"name":"p","url":"https://a","keys":[""],"models":{"m":{}}}]`},
		{"unknown type", `[{"name":"p","url":"https://a","keys":["k"],"models":{"m":{}},"type":"graphql"}]`},
		{"malformed JSON", `[{"name":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/providers", strings.NewReader(tc.body))
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", resp.StatusCode, body)
			}
			var e struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &e); err != nil || e.Error == "" {
				t.Errorf("body %s must carry a non-empty error", body)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, old) {
				t.Errorf("rejected PUT must leave the file byte-identical")
			}
			if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
				t.Errorf("rejected PUT must not create .bak (err=%v)", err)
			}
		})
	}
}

func TestSettingsPut_IntOrHumanRejectsGarbage(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)

	body := `[{"name":"p","url":"https://a","keys":["k"],"models":{"m":{"context":"200kk"}}}]`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/providers", strings.NewReader(body))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — invalid IntOrHuman is a decode error", resp.StatusCode)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, old) {
		t.Errorf("file must be unchanged after decode failure")
	}
}

func TestSettingsPut_WrongMethod(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, settingsFixture)

	resp, err := http.Post(srv.URL+"/api/settings/providers", "application/json", strings.NewReader("[]"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 from the Go 1.22 method pattern", resp.StatusCode)
	}
}

// jsonEqual reports semantic JSON equality (decode both sides into any).
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("jsonEqual: %v (%s)", err, a)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("jsonEqual: %v (%s)", err, b)
	}
	return jsonEqualValue(av, bv)
}

func jsonEqualValue(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// ---------------------------------------------------------------------------
// Probe (POST /api/settings/test) and model-list (POST /api/settings/models)
// — each test registers a real loopback upstream so the HTTP boundary,
// headers, and bodies are exercised for real.
// ---------------------------------------------------------------------------

// upstreamCall captures what the fake upstream received.
type upstreamCall struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// newUpstream serves handler and records every hit.
func newUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *[]upstreamCall) {
	t.Helper()
	calls := &[]upstreamCall{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, upstreamCall{r.Method, r.URL.Path, r.Header.Clone(), body})
		handler(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, calls
}

// postSettingsJSON POSTs body to url and decodes the response into out.
func postSettingsJSON(t *testing.T, url, body string, out any) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, data)
	}
	return resp
}

func TestSettingsTest_OpenAI_OK(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})

	var got struct {
		OK        bool   `json:"ok"`
		LatencyMs int    `json:"latencyMs"`
		Error     string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "zhipu", "type": "openai", "url": "`+up.URL+`/",
		"keys": ["sk-test"], "models": {"glm-5.3": {}}
	}`, &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d — probe outcomes are data, not transport failures", resp.StatusCode)
	}
	if !got.OK || got.Error != "" {
		t.Fatalf("ok=%v error=%q, want ok=true", got.OK, got.Error)
	}
	if got.LatencyMs < 0 {
		t.Errorf("latencyMs = %d, want >= 0", got.LatencyMs)
	}
	if len(*calls) != 1 {
		t.Fatalf("upstream hits = %d, want 1", len(*calls))
	}
	call := (*calls)[0]
	if call.Method != http.MethodGet || call.Path != "/models" {
		t.Errorf("request = %s %s, want GET /models (trailing slash must not double-slash)", call.Method, call.Path)
	}
	if auth := call.Header.Get("Authorization"); auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", auth)
	}
}

func TestSettingsTest_OpenAI_BadKey(t *testing.T) {
	srv := newSettingsServer(t)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "zhipu", "type": "openai", "url": "`+up.URL+`",
		"keys": ["sk-test"], "models": {"glm-5.3": {}}
	}`, &got)
	if got.OK {
		t.Fatal("ok=true, want false on 401")
	}
	if !strings.Contains(got.Error, "401") {
		t.Errorf("error = %q, want it to name the status", got.Error)
	}
	// Leak guard: the key must never surface in the error envelope.
	errText := got.Error
	if strings.Contains(errText, "sk-test") {
		t.Errorf("error %q leaks the API key", errText)
	}
}

func TestSettingsTest_Anthropic_OK(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg_1","content":[]}`))
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "xiaomi", "type": "anthropic", "url": "`+up.URL+`",
		"keys": ["sk-ant-test"],
		"models": {"mimo-v2.5-pro": {}, "mimo-v2.5": {}}
	}`, &got)
	if !got.OK || got.Error != "" {
		t.Fatalf("ok=%v error=%q, want ok=true", got.OK, got.Error)
	}
	if len(*calls) != 1 {
		t.Fatalf("upstream hits = %d, want 1", len(*calls))
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/v1/messages" {
		t.Fatalf("request = %s %s, want POST /v1/messages", call.Method, call.Path)
	}
	if got := call.Header.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("x-api-key = %q, want sk-ant-test", got)
	}
	if got := call.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, call.Body)
	}
	if body.Model != "mimo-v2.5-pro" {
		t.Errorf("body.model = %q, want the provider's FIRST model", body.Model)
	}
	if body.MaxTokens != 1 {
		t.Errorf("body.max_tokens = %d, want 1", body.MaxTokens)
	}
}

func TestSettingsTest_AutoResolvesFromURL(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	var got struct {
		OK bool `json:"ok"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "xiaomi", "url": "`+up.URL+`/anthropic",
		"keys": ["k"], "models": {"m": {}}
	}`, &got)
	if !got.OK {
		t.Fatal("ok=false: empty type must infer anthropic from the URL path")
	}
	// URL path contains /anthropic → anthropic protocol → POST <url>/v1/messages
	// (the same shape as the real xiaomi endpoint base).
	if len(*calls) != 1 || (*calls)[0].Method != http.MethodPost || (*calls)[0].Path != "/anthropic/v1/messages" {
		t.Errorf("calls = %+v, want POST /anthropic/v1/messages", *calls)
	}
}

func TestSettingsTest_NoKeyShortCircuits(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no network call may happen without a resolvable key")
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "p", "url": "`+up.URL+`", "keys": [], "models": {"m": {}}
	}`, &got)
	if got.OK {
		t.Fatal("ok=true, want false")
	}
	if !strings.Contains(got.Error, "no API key") {
		t.Errorf("error = %q, want 'no API key configured'", got.Error)
	}
	if len(*calls) != 0 {
		t.Errorf("upstream hits = %d, want 0", len(*calls))
	}
}

func TestSettingsTest_NoModelsAnthropicShortCircuits(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("anthropic probe needs a model name for the ping body; no call is expected without one")
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "p", "type": "anthropic", "url": "`+up.URL+`", "keys": ["k"], "models": {}
	}`, &got)
	if got.OK {
		t.Fatal("ok=true, want false")
	}
	if !strings.Contains(got.Error, "no models") {
		t.Errorf("error = %q, want 'no models configured'", got.Error)
	}
	if len(*calls) != 0 {
		t.Errorf("upstream hits = %d, want 0", len(*calls))
	}
}

func TestSettingsTest_ConnectionRefused(t *testing.T) {
	srv := newSettingsServer(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close() // port now refuses connections

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "p", "url": "`+url+`", "keys": ["k"], "models": {"m": {}}
	}`, &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d — transport errors are probe data, not HTTP failures", resp.StatusCode)
	}
	if got.OK || got.Error == "" {
		t.Errorf("ok=%v error=%q, want ok=false with the transport error", got.OK, got.Error)
	}
}

func TestSettingsTest_Timeout(t *testing.T) {
	srv := newSettingsServer(t)
	// Hold the response until the client's own timeout aborts the request —
	// the request context is the release, so there is no cleanup-order
	// coupling with the upstream server's Close.
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	old := settingsHTTPTimeout
	settingsHTTPTimeout = 150 * time.Millisecond
	t.Cleanup(func() { settingsHTTPTimeout = old })

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	// REAL-TIME: bounding a real socket timeout, so wall-clock is the point.
	start := time.Now()
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "p", "url": "`+up.URL+`", "keys": ["k"], "models": {"m": {}}
	}`, &got)
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("probe took %v — the client timeout must bound it", elapsed)
	}
	if got.OK {
		t.Error("ok=true, want false on timeout")
	}
	if !strings.Contains(got.Error, "context deadline exceeded") {
		t.Errorf("error = %q, want the Go timeout error text", got.Error)
	}
}

func TestSettingsModels_OpenAI(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"glm-4.5"},{"id":"glm-4.6"}]}`))
	})

	var got struct {
		Mode   string `json:"mode"`
		Models []struct {
			ID      string   `json:"id"`
			Context string   `json:"context,omitempty"`
			Input   []string `json:"input,omitempty"`
		} `json:"models"`
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/models",
		`{"url":"`+up.URL+`/","key":"kk","type":"openai"}`, &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got.Mode != "fetched" || got.Error != "" {
		t.Fatalf("mode=%q error=%q, want fetched", got.Mode, got.Error)
	}
	if len(got.Models) != 2 || got.Models[0].ID != "glm-4.5" || got.Models[1].ID != "glm-4.6" {
		t.Errorf("models = %+v, want [glm-4.5 glm-4.6]", got.Models)
	}
	if len(*calls) != 1 || (*calls)[0].Path != "/models" {
		t.Errorf("calls = %+v, want GET /models", *calls)
	}
}

func TestSettingsModels_Anthropic(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("anthropic has no model list endpoint; no fetch is expected")
	})

	var got struct {
		Mode   string   `json:"mode"`
		Models []string `json:"models"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/models",
		`{"url":"`+up.URL+`","key":"kk","type":"anthropic"}`, &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got.Mode != "manual" {
		t.Errorf("mode = %q, want manual", got.Mode)
	}
	if len(*calls) != 0 {
		t.Errorf("upstream hits = %d, want 0", len(*calls))
	}
}

func TestSettingsModels_UpstreamError(t *testing.T) {
	srv := newSettingsServer(t)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var got struct {
		Mode  string `json:"mode"`
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/models",
		`{"url":"`+up.URL+`","key":"kk","type":"openai"}`, &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d — upstream failures are data", resp.StatusCode)
	}
	if got.Mode != "error" || got.Error == "" {
		t.Errorf("mode=%q error=%q, want error mode with a message", got.Mode, got.Error)
	}
}

func TestSettingsModels_MissingURL(t *testing.T) {
	srv := newSettingsServer(t)

	resp, err := http.Post(srv.URL+"/api/settings/models", "application/json", strings.NewReader(`{"key":"k","type":"openai"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "url is required") {
		t.Errorf("body = %s, want 'url is required'", body)
	}
}

func TestSettingsDefaultPut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	seed := `{"model":{"default":"xiaomi/mimo-v2.5","pro":"zhipu/glm-5.3"},"providers":[{"name":"zhipu","url":"https://z","type":"openai","keys":["k"],"models":{"glm-5.3":{"context":"1M"}}}]}`
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(seed), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)

	// unknown provider → 400, file untouched
	req := httptest.NewRequest("PUT", "/api/settings/default", strings.NewReader(`{"provider":"nope","model":"x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown provider: %d", rec.Code)
	}
	// unknown model → 400
	req = httptest.NewRequest("PUT", "/api/settings/default", strings.NewReader(`{"provider":"zhipu","model":"nope"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown model: %d", rec.Code)
	}
	// valid → 200, spec written, pro tier kept, backup holds old default
	req = httptest.NewRequest("PUT", "/api/settings/default", strings.NewReader(`{"provider":"zhipu","model":"glm-5.3"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid put: %d %s", rec.Code, rec.Body.String())
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".gbot", "settings.json"))
	var got struct {
		Model map[string]string `json:"model"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("round-trip settings: %v", err)
	}
	if got.Model["default"] != "zhipu/glm-5.3" {
		t.Errorf("default = %q", got.Model["default"])
	}
	if got.Model["pro"] != "zhipu/glm-5.3" {
		t.Errorf("pro tier lost: %v", got.Model)
	}
	bak, _ := os.ReadFile(filepath.Join(home, ".gbot", "settings.json.bak"))
	if !strings.Contains(string(bak), "xiaomi/mimo-v2.5") {
		t.Error("backup does not hold the pre-save default")
	}
}
