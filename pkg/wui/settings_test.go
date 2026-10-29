package wui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
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

// The raw endpoint is a file viewer: every byte on disk, no re-indent, no
// key filtering — the sheet must show exactly what a text editor would.
func TestSettingsGet_RawFullFile(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, settingsFixture)

	var got struct {
		Raw string `json:"raw"`
	}
	resp := getJSON(t, srv.URL+"/api/settings/raw", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got.Raw != settingsFixture {
		t.Errorf("raw must be the file verbatim, got %q", got.Raw)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (a save must never be shadowed by a cached read)", cc)
	}
}

func TestSettingsGet_RawMissingFile(t *testing.T) {
	srv := newSettingsServer(t)

	var got struct {
		Raw string `json:"raw"`
	}
	resp := getJSON(t, srv.URL+"/api/settings/raw", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 on cold start", resp.StatusCode)
	}
	if got.Raw != "" {
		t.Errorf("raw = %q, want empty string", got.Raw)
	}
}

// The raw view is strictly read-only: only GET is registered, so the Go 1.22
// method pattern must 405 anything else.
func TestSettingsGet_RawIsReadOnly(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/raw", strings.NewReader(`{"providers":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, old) {
		t.Errorf("rejected PUT must leave the file byte-identical")
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

// ---------------------------------------------------------------------------
// settings.go
// ---------------------------------------------------------------------------

func TestSettingsTimeoutDefault(t *testing.T) {
	if settingsHTTPTimeout != 10*time.Second {
		mbFatalf(t, "settingsHTTPTimeout = %v, want 10s (probe budget is a UX contract)", settingsHTTPTimeout)
	}
}

func TestGetProvidersBrokenFile(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, "{\n")

	var got settingsProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		mbFatalf(t, "status = %d, want 200 — an unreadable file is cold start, not an error", resp.StatusCode)
	}
	if got.Providers == nil || len(got.Providers) != 0 {
		mbFatalf(t, "providers = %v, want [] (never null)", got.Providers)
	}
	if got.Default.Provider != "" || got.Default.Model != "" {
		mbFatalf(t, "default = %+v, want empty", got.Default)
	}
}

func TestGetProvidersNoStoreHeader(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, settingsFixture)

	var got settingsProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		mbFatalf(t, "status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		mbFatalf(t, "Cache-Control = %q, want no-store (a save must never be shadowed by a cached read)", cc)
	}
}

func TestPutProvidersDecodeError(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/providers", strings.NewReader(`[{"name":`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		mbFatalf(t, "status = %d, want 400", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		mbFatalf(t, "body not JSON: %v", err)
	}
	if e.Error != "unexpected EOF" {
		mbFatalf(t, "error = %q, want the decode message, not a validation message", e.Error)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(old) {
		mbFatalf(t, "rejected PUT must leave the file byte-identical")
	}
}

func TestPutProvidersSaveFails(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)
	// A directory where the .bak must land makes the backup write fail, so
	// SaveProviders aborts before touching settings.json.
	if err := os.Mkdir(path+".bak", 0755); err != nil {
		t.Fatal(err)
	}

	body := `[{"name":"solo","url":"https://s","keys":["k"],"models":{"m":{}}}]`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/providers", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		mbFatalf(t, "body not JSON: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		mbFatalf(t, "status = %d (error %q), want 500 when the save fails", resp.StatusCode, e.Error)
	}
	if e.Error == "" {
		mbFatalf(t, "error envelope must carry the save failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(old) {
		mbFatalf(t, "failed save must leave settings.json unchanged")
	}
}

func TestValidateKnownTypes(t *testing.T) {
	for _, typ := range []string{"", "auto", "openai", "anthropic", "responses"} {
		body := `[{"name":"p","url":"https://a","keys":["k"],"models":{"m":{}}}]`
		if typ != "" {
			body = `[{"name":"p","type":"` + typ + `","url":"https://a","keys":["k"],"models":{"m":{}}}]`
		}
		var providers []config.Provider
		if err := json.Unmarshal([]byte(body), &providers); err != nil {
			t.Fatalf("type %q: unmarshal: %v", typ, err)
		}
		if msg := validateProviders(providers); msg != "" {
			t.Errorf("type %q rejected: %q", typ, msg)
		}
	}
}

func TestTestProviderDecodeError(t *testing.T) {
	srv := newSettingsServer(t)

	var e struct {
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/test", `{"name":`, &e)
	if resp.StatusCode != http.StatusBadRequest {
		mbFatalf(t, "status = %d, want 400", resp.StatusCode)
	}
	if e.Error != "unexpected EOF" {
		mbFatalf(t, "error = %q, want the decode message", e.Error)
	}
}

func TestProbeAnthropicWire(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg_1","content":[]}`))
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "a", "type": "anthropic", "url": "`+up.URL+`",
		"keys": ["ak"], "models": {"m1": {}, "m2": {}}
	}`, &got)
	if !got.OK || got.Error != "" {
		mbFatalf(t, "ok=%v error=%q, want a successful probe", got.OK, got.Error)
	}
	if len(*calls) != 1 {
		mbFatalf(t, "upstream hits = %d, want 1", len(*calls))
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/v1/messages" {
		mbFatalf(t, "request = %s %s, want POST /v1/messages", call.Method, call.Path)
	}
	if call.Header.Get("x-api-key") != "ak" {
		mbFatalf(t, "x-api-key = %q, want ak", call.Header.Get("x-api-key"))
	}
	if call.Header.Get("anthropic-version") != "2023-06-01" {
		mbFatalf(t, "anthropic-version = %q, want 2023-06-01", call.Header.Get("anthropic-version"))
	}
	if ct := call.Header.Get("Content-Type"); ct != "application/json" {
		mbFatalf(t, "Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		mbFatalf(t, "ping body not JSON: %v (%s)", err, call.Body)
	}
	if body.Model != "m1" || body.MaxTokens != 1 || len(body.Messages) != 1 ||
		body.Messages[0].Role != "user" || body.Messages[0].Content != "ping" {
		mbFatalf(t, "ping body = %+v, want model=m1 max_tokens=1 one user ping message", body)
	}
}

func TestProbeResponsesWire(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "r", "type": "responses", "url": "`+up.URL+`",
		"keys": ["rk"], "models": {"m1": {}, "m2": {}}
	}`, &got)
	if !got.OK || got.Error != "" {
		mbFatalf(t, "ok=%v error=%q, want a successful probe", got.OK, got.Error)
	}
	if len(*calls) != 1 {
		mbFatalf(t, "upstream hits = %d, want 1", len(*calls))
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/responses" {
		mbFatalf(t, "request = %s %s, want POST against the protocol endpoint /responses", call.Method, call.Path)
	}
	if auth := call.Header.Get("Authorization"); auth != "Bearer rk" {
		mbFatalf(t, "Authorization = %q, want Bearer rk", auth)
	}
	if ct := call.Header.Get("Content-Type"); ct != "application/json" {
		mbFatalf(t, "Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Model           string `json:"model"`
		Input           string `json:"input"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		mbFatalf(t, "probe body not JSON: %v (%s)", err, call.Body)
	}
	if body.Model != "m1" || body.Input != "ping" || body.MaxOutputTokens != 1 {
		mbFatalf(t, "probe body = %+v, want model=m1 input=ping max_output_tokens=1", body)
	}
}

func TestProbeResponsesNoModels(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the responses probe needs a model name; no call is expected without one")
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "r", "type": "responses", "url": "`+up.URL+`", "keys": ["k"], "models": {}
	}`, &got)
	if got.OK || got.Error != "no models configured" {
		mbFatalf(t, "ok=%v error=%q, want the no-models error", got.OK, got.Error)
	}
	if len(*calls) != 0 {
		mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
	}
}

func TestProbeBadURLGuards(t *testing.T) {
	srv := newSettingsServer(t)
	_, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unparseable URL must fail before any request goes out")
	})
	for _, typ := range []string{"anthropic", "responses", "openai"} {
		var got struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/test", `{
			"name": "p", "type": "`+typ+`", "url": "http://a b/",
			"keys": ["k"], "models": {"m": {}}
		}`, &got)
		if got.OK {
			mbFatalf(t, "type %s: ok=true on an unparseable URL", typ)
		}
		if !strings.Contains(got.Error, "invalid character") {
			mbFatalf(t, "type %s: error = %q, want the URL parse failure", typ, got.Error)
		}
	}
	if len(*calls) != 0 {
		mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
	}
}

func TestProbeStatusClassification(t *testing.T) {
	srv := newSettingsServer(t)

	t.Run("299 is success", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(299)
			_, _ = w.Write([]byte(`{}`))
		})
		var got struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/test", `{
			"name": "p", "url": "`+up.URL+`", "keys": ["k"], "models": {"m": {}}
		}`, &got)
		if !got.OK || got.Error != "" {
			mbFatalf(t, "ok=%v error=%q, want success — 299 is a 2xx", got.OK, got.Error)
		}
	})
	t.Run("401 error names the status", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		var got struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/test", `{
			"name": "p", "url": "`+up.URL+`", "keys": ["k"], "models": {"m": {}}
		}`, &got)
		if got.OK || got.Error != "HTTP 401" {
			mbFatalf(t, "ok=%v error=%q, want exactly HTTP 401", got.OK, got.Error)
		}
	})
}

func TestProbeLatencyMeasured(t *testing.T) {
	srv := newSettingsServer(t)
	// The upstream stalls on a channel until the test releases it, so the
	// probe's measured window provably spans the release delay — a constant
	// or zero latencyMs cannot pass.
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{}`))
	})

	type probeReply struct {
		OK        bool   `json:"ok"`
		LatencyMs int    `json:"latencyMs"`
		Error     string `json:"error"`
	}
	reply := make(chan probeReply, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/settings/test", "application/json",
			strings.NewReader(`{
				"name": "p", "url": "`+up.URL+`", "keys": ["k"], "models": {"m": {}}
			}`))
		if err != nil {
			reply <- probeReply{Error: err.Error()}
			return
		}
		defer resp.Body.Close()
		var got probeReply
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			reply <- probeReply{Error: "decode: " + err.Error()}
			return
		}
		reply <- got
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never saw the probe request")
	}
	// Hold the probe in flight for 50ms of real time so the measured
	// latency has a guaranteed floor.
	time.Sleep(50 * time.Millisecond) // REAL-TIME
	close(release)

	select {
	case got := <-reply:
		if !got.OK {
			mbFatalf(t, "ok=false error=%q, want success", got.Error)
		}
		if got.LatencyMs < 25 {
			mbFatalf(t, "latencyMs = %d, want >= 25 (the upstream stalled 50ms)", got.LatencyMs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe request never returned after release")
	}
}

func TestProbeOutboundContext(t *testing.T) {
	srv := newSettingsServer(t)
	started := make(chan struct{}, 1)
	sawCancel := make(chan struct{}, 1)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		sawCancel <- struct{}{}
	})
	old := settingsHTTPTimeout
	settingsHTTPTimeout = 800 * time.Millisecond
	t.Cleanup(func() { settingsHTTPTimeout = old })

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/settings/test",
		strings.NewReader(`{"name":"r","type":"responses","url":"`+up.URL+`","keys":["k"],"models":{"m":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	// Cancel only once the probe is provably in flight at the upstream —
	// channel handshake instead of a sleep racing the request dispatch.
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			mbFatalf(t, "client error = %v, want the cancellation to surface", err)
		}
	case <-time.After(3 * time.Second):
		mbFatalf(t, "client request never returned after cancel")
	}
	// The cancellation must reach the upstream long before settingsHTTPTimeout
	// could fire: the outbound probe rides the request context.
	select {
	case <-sawCancel:
	case <-time.After(400 * time.Millisecond):
		mbFatalf(t, "upstream did not observe the cancellation within 400ms — the outbound probe must ride the request context")
	}
}

func TestFetchModelsFree(t *testing.T) {
	srv := newSettingsServer(t)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "max_price=0&sort=most-popular" {
			t.Errorf("query = %q, want the free-models filter", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"f1","name":"F1","context_length":1,
			 "pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]},
			{"id":"f2","context_length":131072,
			 "pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}
		]}`))
	})

	var got struct {
		Mode   string `json:"mode"`
		Models []struct {
			ID      string `json:"id"`
			Context string `json:"context"`
		} `json:"models"`
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","free":true}`, &got)
	if resp.StatusCode != http.StatusOK {
		mbFatalf(t, "status = %d", resp.StatusCode)
	}
	if got.Mode != "fetched" || got.Error != "" {
		mbFatalf(t, "mode=%q error=%q, want fetched", got.Mode, got.Error)
	}
	if len(got.Models) != 2 {
		mbFatalf(t, "models = %+v, want 2", got.Models)
	}
	if got.Models[0].ID != "f1" || got.Models[0].Context != "1" {
		mbFatalf(t, "f1 = %+v, want context 1 (small values stay plain digits)", got.Models[0])
	}
	if got.Models[1].ID != "f2" || got.Models[1].Context != "128k" {
		mbFatalf(t, "f2 = %+v, want context 128k (exact k multiple in human form)", got.Models[1])
	}
}

func TestFetchModelsInputGuards(t *testing.T) {
	srv := newSettingsServer(t)

	t.Run("decode error", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/settings/models", "application/json", strings.NewReader(`{"url":`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			mbFatalf(t, "status = %d, want 400", resp.StatusCode)
		}
		var e struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			mbFatalf(t, "body not JSON: %v", err)
		}
		if e.Error != "unexpected EOF" {
			mbFatalf(t, "error = %q, want the decode message", e.Error)
		}
	})
	t.Run("missing url", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/settings/models", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			mbFatalf(t, "status = %d, want 400", resp.StatusCode)
		}
		var e struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			mbFatalf(t, "body not JSON: %v", err)
		}
		if e.Error != "url is required" {
			mbFatalf(t, "error = %q, want exactly 'url is required'", e.Error)
		}
	})
	t.Run("anthropic is manual", func(t *testing.T) {
		up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("anthropic has no model list endpoint; no fetch is expected")
		})
		var got struct {
			Mode string `json:"mode"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"anthropic"}`, &got)
		if got.Mode != "manual" {
			mbFatalf(t, "mode = %q, want manual", got.Mode)
		}
		if len(*calls) != 0 {
			mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
		}
	})
	t.Run("anthropic inferred from url path is manual", func(t *testing.T) {
		up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("an /anthropic base URL means manual mode; no fetch is expected")
		})
		var got struct {
			Mode string `json:"mode"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`/anthropic"}`, &got)
		if got.Mode != "manual" {
			mbFatalf(t, "mode = %q, want manual for an untyped /anthropic base", got.Mode)
		}
		if len(*calls) != 0 {
			mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
		}
	})
	t.Run("unparseable url", func(t *testing.T) {
		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		resp := postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"http://a b/","type":"openai"}`, &got)
		if resp.StatusCode != http.StatusOK {
			mbFatalf(t, "status = %d", resp.StatusCode)
		}
		if got.Mode != "error" {
			mbFatalf(t, "mode = %q, want error", got.Mode)
		}
		if !strings.Contains(got.Error, "invalid character") {
			mbFatalf(t, "error = %q, want the URL parse failure", got.Error)
		}
	})
}

func TestFetchModelsAuthHeader(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	var withKey struct {
		Mode string `json:"mode"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","key":"kk","type":"openai"}`, &withKey)
	if withKey.Mode != "fetched" {
		mbFatalf(t, "mode = %q, want fetched", withKey.Mode)
	}
	if auth := (*calls)[0].Header.Get("Authorization"); auth != "Bearer kk" {
		mbFatalf(t, "Authorization = %q, want Bearer kk", auth)
	}

	calls2Up, calls2 := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	var noKey struct {
		Mode string `json:"mode"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+calls2Up.URL+`","type":"openai"}`, &noKey)
	if noKey.Mode != "fetched" {
		mbFatalf(t, "mode = %q, want fetched without a key", noKey.Mode)
	}
	if auth := (*calls2)[0].Header.Get("Authorization"); auth != "" {
		mbFatalf(t, "Authorization = %q, want absent when no key was given", auth)
	}
}

func TestFetchModelsUpstreamFailures(t *testing.T) {
	srv := newSettingsServer(t)

	t.Run("unreachable upstream", func(t *testing.T) {
		closed := httptest.NewServer(http.NotFoundHandler())
		url := closed.URL
		closed.Close()

		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		resp := postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+url+`","type":"openai"}`, &got)
		if resp.StatusCode != http.StatusOK {
			mbFatalf(t, "status = %d — transport failures are data", resp.StatusCode)
		}
		if got.Mode != "error" || got.Error == "" {
			mbFatalf(t, "mode=%q error=%q, want error mode with the transport text", got.Mode, got.Error)
		}
	})
	t.Run("non-2xx with valid body", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		})
		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
		if got.Mode != "error" || got.Error != "HTTP 500" {
			mbFatalf(t, "mode=%q error=%q, want error mode naming the status", got.Mode, got.Error)
		}
	})
	t.Run("299 with valid body", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(299)
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		})
		var got struct {
			Mode   string `json:"mode"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
		if got.Mode != "fetched" || len(got.Models) != 1 || got.Models[0].ID != "m1" {
			mbFatalf(t, "mode=%q models=%+v, want fetched [m1] — 299 is a 2xx", got.Mode, got.Models)
		}
	})
	t.Run("undecodable body", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		})
		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
		if got.Mode != "error" {
			mbFatalf(t, "mode = %q, want error", got.Mode)
		}
		if !strings.Contains(got.Error, "invalid character") {
			mbFatalf(t, "error = %q, want the decode failure", got.Error)
		}
	})
}

func TestFetchModelsMergeDedupe(t *testing.T) {
	srv := newSettingsServer(t)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [{"id":"m1"}],
			"models": [
				{"slug":"m2","context_window":1024,"input_modalities":["text","image"]},
				{"slug":"m1"},
				{"slug":"","display_name":"m3"},
				{"slug":"","display_name":""},
				{"slug":"m0","context_window":0},
				{"slug":"m1c","context_window":1}
			]
		}`))
	})

	var got struct {
		Mode   string `json:"mode"`
		Models []struct {
			ID      string   `json:"id"`
			Context string   `json:"context"`
			Input   []string `json:"input"`
		} `json:"models"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
	if got.Mode != "fetched" {
		mbFatalf(t, "mode = %q, want fetched", got.Mode)
	}
	wantIDs := []string{"m2", "m1", "m3", "m0", "m1c"}
	if len(got.Models) != len(wantIDs) {
		mbFatalf(t, "models = %+v, want exactly %v (deduped, no empty ids)", got.Models, wantIDs)
	}
	for i, id := range wantIDs {
		if got.Models[i].ID != id {
			mbFatalf(t, "models[%d] = %+v, want id %q", i, got.Models[i], id)
		}
	}
	m2 := got.Models[0]
	if m2.Context != "1k" || len(m2.Input) != 2 || m2.Input[0] != "text" || m2.Input[1] != "image" {
		mbFatalf(t, "m2 = %+v, want context 1k and the input modalities passed through", m2)
	}
	if got.Models[1].Context != "" || got.Models[2].Context != "" || got.Models[3].Context != "" {
		mbFatalf(t, "zero context_window must stay unset, got %+v", got.Models[1:4])
	}
	if got.Models[4].Context != "1" {
		mbFatalf(t, "m1c context = %q, want 1", got.Models[4].Context)
	}
}

func TestFetchModelsClientTimeout(t *testing.T) {
	srv := newSettingsServer(t)
	sawTimeout := make(chan struct{}, 1)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		sawTimeout <- struct{}{}
	})
	old := settingsHTTPTimeout
	settingsHTTPTimeout = 300 * time.Millisecond
	t.Cleanup(func() { settingsHTTPTimeout = old })

	type fetchReply struct {
		Mode  string `json:"mode"`
		Error string `json:"error"`
	}
	reply := make(chan fetchReply, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/settings/models", "application/json",
			strings.NewReader(`{"url":"`+up.URL+`","type":"openai"}`))
		if err != nil {
			reply <- fetchReply{Error: err.Error()}
			return
		}
		defer resp.Body.Close()
		var got fetchReply
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			reply <- fetchReply{Error: "decode: " + err.Error()}
			return
		}
		reply <- got
	}()

	// The fetch must end within a bound around the client timeout, not hang.
	var got fetchReply
	select {
	case got = <-reply:
	case <-time.After(2 * time.Second):
		mbFatalf(t, "fetch still in flight after 2s — the %v client timeout must bound it", settingsHTTPTimeout)
	}
	if got.Mode != "error" || got.Error == "" {
		mbFatalf(t, "mode=%q error=%q, want the timeout as data", got.Mode, got.Error)
	}
	// The bound is the probe's own timeout: the upstream observes its
	// request context cancel when the client gives up.
	select {
	case <-sawTimeout:
	case <-time.After(3 * time.Second):
		mbFatalf(t, "upstream request never cancelled — the client timeout did not reach the outbound fetch")
	}
}

func TestDefaultPut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0755); err != nil {
		t.Fatal(err)
	}
	seed := `{"providers":[{"name":"zhipu","url":"https://z","type":"openai","keys":["k"],"models":{"glm-5.3":{}}}]}`
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/settings/default", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := put(`{"provider":`)
	if rec.Code != http.StatusBadRequest {
		mbFatalf(t, "decode error: status = %d, want 400", rec.Code)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		mbFatalf(t, "body not JSON: %s", rec.Body.String())
	}
	if e.Error != "invalid body: unexpected EOF" {
		mbFatalf(t, "error = %q, want the invalid-body wrapper", e.Error)
	}

	rec = put(`{"provider":"nope","model":"x"}`)
	if rec.Code != http.StatusBadRequest {
		mbFatalf(t, "unknown provider: status = %d, want 400", rec.Code)
	}
	{
		var e2 struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &e2); err != nil || e2.Error != "unknown provider" {
			mbFatalf(t, "unknown provider body = %s, want the sentinel text", rec.Body.String())
		}
	}

	rec = put(`{"provider":"zhipu","model":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		mbFatalf(t, "unknown model: status = %d, want 400", rec.Code)
	}
	var e3 struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e3); err != nil || e3.Error != "unknown model for provider" {
		mbFatalf(t, "unknown model body = %s, want the sentinel text", rec.Body.String())
	}

	rec = put(`{"provider":"zhipu","model":"glm-5.3"}`)
	if rec.Code != http.StatusOK {
		mbFatalf(t, "valid put: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"ok":true}` {
		mbFatalf(t, "body = %s, want exactly {\"ok\":true}", rec.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(home, ".gbot", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Model map[string]string `json:"model"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		mbFatalf(t, "file invalid after put: %v", err)
	}
	if file.Model["default"] != "zhipu/glm-5.3" {
		mbFatalf(t, "default = %q, want the written tier", file.Model["default"])
	}
}
