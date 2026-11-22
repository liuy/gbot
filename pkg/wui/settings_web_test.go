package wui

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsWebProviders is the GET wire shape restricted to the web section.
type settingsWebProviders struct {
	Web struct {
		Proxy   string            `json:"proxy"`
		Engines map[string]string `json:"engines"`
	} `json:"web"`
}

// putWeb PUTs body to /api/settings/web and returns status + response body.
func putWeb(t *testing.T, srvURL, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, srvURL+"/api/settings/web", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestSettingsGet_IncludesWebSection(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, strings.TrimSuffix(settingsFixture, "}")+`,
  "web": {"proxy": "http://localhost:10809", "anysearch": "as", "zhipu": "zk"}}`)

	var got settingsWebProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got.Web.Proxy != "http://localhost:10809" {
		t.Errorf("web.proxy = %q, want http://localhost:10809", got.Web.Proxy)
	}
	if got.Web.Engines == nil {
		t.Fatalf("web.engines must never be null")
	}
	if len(got.Web.Engines) != 2 {
		t.Fatalf("web.engines = %v (len %d), want exactly 2", got.Web.Engines, len(got.Web.Engines))
	}
	if got.Web.Engines["anysearch"] != "as" || got.Web.Engines["zhipu"] != "zk" {
		t.Errorf("web.engines = %v, want {anysearch:as, zhipu:zk}", got.Web.Engines)
	}
	if _, ok := got.Web.Engines["proxy"]; ok {
		t.Errorf("web.engines must not contain the reserved proxy key: %v", got.Web.Engines)
	}
}

// Cold start must answer a well-formed web section — engines coerced to {},
// never null, so the frontend can iterate it unconditionally.
func TestSettingsGet_WebColdStartEmpty(t *testing.T) {
	srv := newSettingsServer(t)

	resp, err := http.Get(srv.URL + "/api/settings/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d on cold start, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"engines":{}`) || !strings.Contains(string(body), `"providers":[]`) {
		t.Errorf("raw body must pin both never-null coercions, got %s", body)
	}
	var got settingsWebProviders
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, body)
	}
	if got.Web.Proxy != "" {
		t.Errorf("web.proxy = %q, want empty on cold start", got.Web.Proxy)
	}
	if got.Web.Engines == nil || len(got.Web.Engines) != 0 {
		t.Errorf("web.engines = %v, want empty non-nil map", got.Web.Engines)
	}
}

func TestSettingsGet_LoadErrorEmptyPayload(t *testing.T) {
	srv := newSettingsServer(t)
	home, _ := os.UserHomeDir()
	// settings.json as a directory makes config.Load fail — the GET must
	// answer the empty payload, not a 500.
	if err := os.MkdirAll(filepath.Join(home, ".gbot", "settings.json"), 0755); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/api/settings/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"providers":[]`) || !strings.Contains(string(body), `"engines":{}`) {
		t.Errorf("body = %s, want both empty coercions on load error", body)
	}
}

func TestSettingsPutWeb_SavesAndMerges(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, `{"web":{"proxy":"http://localhost:10809","zhipu":"zk","anysearch":"as"}}`)

	status, body := putWeb(t, srv.URL, `{"proxy":"  https://h:1  ","engines":{"anysearch":"","tavily":"tk"}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	if body != `{"ok":true}` {
		t.Errorf("body = %q, want {\"ok\":true}", body)
	}

	var got settingsWebProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	if got.Web.Proxy != "https://h:1" {
		t.Errorf("web.proxy = %q, want https://h:1 (trimmed, replaced)", got.Web.Proxy)
	}
	// Every accepted scheme prefix rides the same PUT path once; padding on
	// two of them pins the TrimSpace the handler applies before validating.
	for _, p := range []string{"  http://p:2  ", "socks5://s:3"} {
		if status, body := putWeb(t, srv.URL, `{"proxy":"`+p+`","engines":{}}`); status != http.StatusOK {
			t.Fatalf("PUT %s: status = %d, body %s", p, status, body)
		}
	}
	var got2 settingsWebProviders
	getJSON(t, srv.URL+"/api/settings/providers", &got2)
	if got2.Web.Proxy != "socks5://s:3" {
		t.Errorf("web.proxy = %q, want socks5://s:3 (last PUT wins, http:// accepted en route)", got2.Web.Proxy)
	}
	if len(got.Web.Engines) != 2 {
		t.Fatalf("web.engines = %v (len %d), want exactly 2 (anysearch removed, zhipu preserved)", got.Web.Engines, len(got.Web.Engines))
	}
	if got.Web.Engines["zhipu"] != "zk" || got.Web.Engines["tavily"] != "tk" {
		t.Errorf("web.engines = %v, want {zhipu:zk, tavily:tk}", got.Web.Engines)
	}
}

func TestSettingsPutWeb_SaveFailureIs500(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, `{"web":{"anysearch":"as"}}`)
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".gbot")
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	status, body := putWeb(t, srv.URL, `{"proxy":"http://p:1","engines":{}}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the save cannot write, body %s", status, body)
	}
	if !strings.Contains(body, `"save:`) {
		t.Errorf("body = %s, want the save: error prefix", body)
	}
}

func TestSettingsPutWeb_RejectsBadProxy(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, `{"web":{"tavily":"tk"}}`)

	status, body := putWeb(t, srv.URL, `{"proxy":"localhost:10809","engines":{}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (scheme-less proxy), body %s", status, body)
	}
	if !strings.Contains(body, "proxy") {
		t.Errorf("error body must mention the proxy: %s", body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(old) {
		t.Errorf("rejected PUT must not touch settings.json:\n got %s\nwant %s", after, old)
	}
}

func TestSettingsPutWeb_RejectsReservedEngineName(t *testing.T) {
	srv := newSettingsServer(t)

	status, body := putWeb(t, srv.URL, `{"engines":{"proxy":"x"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body %s", status, body)
	}
	if !strings.Contains(body, "reserved") {
		t.Errorf("error body must name the reserved key: %s", body)
	}
}

func TestSettingsPutWeb_RejectsEmptyEngineName(t *testing.T) {
	srv := newSettingsServer(t)

	status, body := putWeb(t, srv.URL, `{"engines":{"":"k"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body %s", status, body)
	}
	if !strings.Contains(body, "engine name is required") {
		t.Errorf("error body = %s, want engine name is required", body)
	}
}

func TestSettingsPutWeb_BadBody(t *testing.T) {
	srv := newSettingsServer(t)

	status, _ := putWeb(t, srv.URL, `not-json`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for undecodable body", status)
	}
}
