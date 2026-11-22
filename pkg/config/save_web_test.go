package config_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

// readWeb decodes the "web" key of settings.json, failing the test when the
// key is absent — callers assert presence explicitly when removal is expected.
func readWeb(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, data)
	}
	var web map[string]string
	if err := json.Unmarshal(raw["web"], &web); err != nil {
		t.Fatalf("web key invalid: %v", err)
	}
	return web
}

func TestSaveWeb_MergesAndPreservesUnknownKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"web":{"proxy":"http://localhost:10809","anysearch":"as-old","zhipu":"zk"}}`, 0600)

	if err := config.SaveWeb("http://p:1", map[string]string{"anysearch": "as-new", "tavily": "tk"}); err != nil {
		t.Fatalf("SaveWeb: %v", err)
	}

	web := readWeb(t)
	if len(web) != 4 {
		t.Fatalf("web = %v (len %d), want 4 keys", web, len(web))
	}
	want := map[string]string{
		"proxy":     "http://p:1",
		"anysearch": "as-new",
		"tavily":    "tk",
		"zhipu":     "zk",
	}
	for k, v := range want {
		if web[k] != v {
			t.Errorf("web[%q] = %q, want %q", k, web[k], v)
		}
	}
}

func TestSaveWeb_EmptyKeyRemovesEngine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"web":{"tavily":"tk"}}`, 0600)

	if err := config.SaveWeb("", map[string]string{"tavily": ""}); err != nil {
		t.Fatalf("SaveWeb: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	if _, ok := raw["web"]; ok {
		t.Fatalf("web key = %s, want it removed once the map is empty", raw["web"])
	}
}

func TestSaveWeb_EmptyProxyRemovesProxyKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"web":{"proxy":"http://localhost:10809","tavily":"tk"}}`, 0600)

	if err := config.SaveWeb("", map[string]string{"tavily": "tk"}); err != nil {
		t.Fatalf("SaveWeb: %v", err)
	}

	web := readWeb(t)
	if len(web) != 1 {
		t.Fatalf("web = %v (len %d), want only tavily", web, len(web))
	}
	if web["tavily"] != "tk" {
		t.Errorf("web[tavily] = %q, want tk", web["tavily"])
	}
	if _, ok := web["proxy"]; ok {
		t.Errorf("web[proxy] = %q, want the reserved key removed on empty proxy", web["proxy"])
	}
}

func TestSaveWeb_CreatesWhenFileMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveWeb("http://x:1", map[string]string{"tavily": "tk"}); err != nil {
		t.Fatalf("SaveWeb: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("new file does not unmarshal: %v", err)
	}
	for _, key := range []string{"model", "providers"} {
		if _, exists := raw[key]; exists {
			t.Errorf("%s key must be absent, got %s", key, raw[key])
		}
	}
	web := readWeb(t)
	if len(web) != 2 {
		t.Fatalf("web = %v (len %d), want exactly proxy+tavily", web, len(web))
	}
	if web["proxy"] != "http://x:1" || web["tavily"] != "tk" {
		t.Errorf("web = %v, want {proxy:http://x:1, tavily:tk}", web)
	}
}

func TestSaveWeb_PreservesOtherTopLevelKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seed := `{
  "model": {"default": "minimax/abab6.5s-chat"},
  "providers": [{"name": "minimax", "url": "https://api.minimax.chat/v1", "keys": ["k"], "models": {"abab6.5s-chat": {}}}],
  "theme": "dark"
}`
	seedSettingsFile(t, seed, 0600)

	if err := config.SaveWeb("http://p:1", map[string]string{"tavily": "tk"}); err != nil {
		t.Fatalf("SaveWeb: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(seed), &want); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "providers", "theme"} {
		if gotCompact, wantCompact := compactJSON(t, got[key]), compactJSON(t, want[key]); gotCompact != wantCompact {
			t.Errorf("key %q not preserved verbatim:\n got %s\nwant %s", key, gotCompact, wantCompact)
		}
	}
}

func TestSaveWeb_BackupAndMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seed := `{"web":{"tavily":"old"}}`
	seedSettingsFile(t, seed, 0640)

	if err := config.SaveWeb("http://p:1", map[string]string{"tavily": "tk"}); err != nil {
		t.Fatalf("SaveWeb: %v", err)
	}

	bak, err := os.ReadFile(settingsPath(t) + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != seed {
		t.Errorf(".bak must hold the OLD content verbatim:\n got %s\nwant %s", bak, seed)
	}
	info, err := os.Stat(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0640 {
		t.Errorf("settings.json mode = %o, want preserved 640", got)
	}
}

func TestSaveWeb_CorruptJSONStartsFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"web":{"zhipu":"zk"},"trunc`, 0600)

	if err := config.SaveWeb("", map[string]string{"tavily": "tk"}); err != nil {
		t.Fatalf("SaveWeb should heal a garbage file: %v", err)
	}

	bak, err := os.ReadFile(settingsPath(t) + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	// Partial parse: the truncated tail must not leak a half-decoded web
	// map into the fresh start — zhipu dies with the corrupt file.
	if string(bak) != `{"web":{"zhipu":"zk"},"trunc` {
		t.Errorf(".bak = %q, want the garbage bytes preserved", bak)
	}
	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("new file not valid JSON: %s", data)
	}
	web := readWeb(t)
	if len(web) != 1 || web["tavily"] != "tk" {
		t.Errorf("web = %v, want only tavily:tk", web)
	}
}

func TestSaveWeb_UnreadableSettingsIsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(settingsPath(t), 0755); err != nil {
		t.Fatal(err)
	}
	err := config.SaveWeb("http://p:1", nil)
	if err == nil {
		t.Fatal("SaveWeb must fail when settings.json is not a readable file")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("err = %v, want the read error to surface (EISDIR)", err)
	}
}
