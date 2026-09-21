package config_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

// readRemoteDevices reads settings.json back and decodes its
// "desktops" key, failing the test if the file or key is
// missing/invalid.
func readRemoteDevices(t *testing.T) []config.RemoteDevice {
	t.Helper()
	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, data)
	}
	var devices []config.RemoteDevice
	if err := json.Unmarshal(raw["desktops"], &devices); err != nil {
		t.Fatalf("desktops key invalid: %v", err)
	}
	return devices
}

func TestSaveRemoteDevices_CreatesFileWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
	}

	if _, err := os.Stat(settingsPath(t)); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
	got := readRemoteDevices(t)
	if len(got) != 1 || got[0].Name != "win11" || got[0].Addr != "ws://127.0.0.1:8006" {
		t.Errorf("desktops = %+v, want single win11 entry", got)
	}
}

func TestSaveRemoteDevices_PreservesOtherKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seed := `{
  "model": {"default": "zhipu/glm-5.3"},
  "providers": [{"name": "zhipu", "url": "https://a", "keys": ["k"], "models": {"glm-5.3": {}}}]
}`
	seedSettingsFile(t, seed, 0600)

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
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
	// MarshalIndent re-indents nested values, so byte equality holds only
	// after compacting both sides — what is asserted is data equality.
	for _, key := range []string{"model", "providers"} {
		if gotCompact, wantCompact := compactJSON(t, got[key]), compactJSON(t, want[key]); gotCompact != wantCompact {
			t.Errorf("key %q not preserved verbatim:\n got %s\nwant %s", key, gotCompact, wantCompact)
		}
	}
	if d := readRemoteDevices(t); len(d) != 1 || d[0].Name != "win11" {
		t.Errorf("desktops = %+v, want win11 only", d)
	}
}

func TestSaveRemoteDevices_BackupCreatedBeforeWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := `{"model":{"default":"zhipu/glm-5.2"},"desktops":[{"name":"old","addr":"ws://127.0.0.1:1"}]}`
	seedSettingsFile(t, old, 0600)

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "new", Addr: "ws://127.0.0.1:8006"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
	}

	bak, err := os.ReadFile(settingsPath(t) + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != old {
		t.Errorf(".bak must hold the OLD content verbatim:\n got %s\nwant %s", bak, old)
	}
	if got := readRemoteDevices(t); len(got) != 1 || got[0].Name != "new" {
		t.Errorf("new desktops not written: %+v", got)
	}
}

func TestSaveRemoteDevices_NoBackupWithoutPriorFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
	}

	if _, err := os.Stat(settingsPath(t) + ".bak"); !os.IsNotExist(err) {
		t.Errorf("no prior file existed, .bak must not be created (err=%v)", err)
	}
}

func TestSaveRemoteDevices_NewFileMode0600(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
	}

	info, err := os.Stat(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("new file mode = %o, want 600 (carries VNC passwords)", got)
	}
}

func TestSaveRemoteDevices_PreservesExistingMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"desktops":[{"name":"a","addr":"ws://127.0.0.1:1"}]}`, 0644)

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "b", Addr: "ws://127.0.0.1:8006"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
	}

	info, err := os.Stat(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Errorf("mode = %o, want preserved 644", got)
	}
}

func TestSaveRemoteDevices_MalformedExistingIsReplaced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, "{garbage", 0600)

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices should heal a garbage file: %v", err)
	}

	bak, err := os.ReadFile(settingsPath(t) + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != "{garbage" {
		t.Errorf(".bak = %q, want the garbage bytes preserved", bak)
	}
	if data, err := os.ReadFile(settingsPath(t)); err != nil {
		t.Fatal(err)
	} else if !json.Valid(data) {
		t.Errorf("new file not valid JSON: %s", data)
	}
}

func TestSaveRemoteDevices_LoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveRemoteDevices([]config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
		{Name: "nas", Addr: "wss://nas.local:8006", Pass: "pw"},
	}); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
		{Name: "nas", Addr: "wss://nas.local:8006", Pass: "pw"},
	}
	if len(cfg.Desktops) != len(want) {
		t.Fatalf("Desktops = %+v, want %+v", cfg.Desktops, want)
	}
	for i := range want {
		if cfg.Desktops[i] != want[i] {
			t.Errorf("Desktops[%d] = %+v, want %+v", i, cfg.Desktops[i], want[i])
		}
	}
}

func TestLoadMigratesLegacyRemoteDesktopKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"zhipu/glm-5.3"},"remote_desktop":[{"name":"win11","addr":"ws://127.0.0.1:8006"},{"name":"nas","addr":"wss://nas.local:8006","pass":"pw"}]}`, 0600)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []config.RemoteDevice{
		{Name: "win11", Addr: "ws://127.0.0.1:8006"},
		{Name: "nas", Addr: "wss://nas.local:8006", Pass: "pw"},
	}
	if len(cfg.Desktops) != len(want) {
		t.Fatalf("Desktops = %+v, want %+v", cfg.Desktops, want)
	}
	for i := range want {
		if cfg.Desktops[i] != want[i] {
			t.Errorf("Desktops[%d] = %+v, want %+v", i, cfg.Desktops[i], want[i])
		}
	}

	if err := config.SaveRemoteDevices(cfg.Desktops); err != nil {
		t.Fatalf("SaveRemoteDevices: %v", err)
	}
	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, data)
	}
	if _, ok := raw["remote_desktop"]; ok {
		t.Errorf("legacy remote_desktop key must be dropped on save: %s", data)
	}
	if compactJSON(t, raw["desktops"]) != compactJSON(t, mustMarshal(t, want)) {
		t.Errorf("desktops = %s, want %+v preserved", raw["desktops"], want)
	}
}

// A file carrying both keys must honor desktops, not the legacy one.
func TestLoadPrefersDesktopsOverLegacyKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"desktops":[{"name":"new","addr":"ws://127.0.0.1:6080"}],"remote_desktop":[{"name":"old","addr":"ws://127.0.0.1:1"}]}`, 0600)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []config.RemoteDevice{{Name: "new", Addr: "ws://127.0.0.1:6080"}}
	if len(cfg.Desktops) != 1 || cfg.Desktops[0] != want[0] {
		t.Fatalf("Desktops = %+v, want %+v", cfg.Desktops, want)
	}
}

// An explicit empty desktops array is a deliberate "no desktops configured"
// choice — it must not fall back to a stale legacy key. Guards the nil-vs-
// len==0 distinction in the load fallback.
func TestLoad_EmptyDesktopsDoesNotFallBackToLegacyKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"desktops":[],"remote_desktop":[{"name":"old","addr":"ws://127.0.0.1:1"}]}`, 0600)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Desktops == nil || len(cfg.Desktops) != 0 {
		t.Fatalf("Desktops = %+v (%T), want non-nil empty slice", cfg.Desktops, cfg.Desktops)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
