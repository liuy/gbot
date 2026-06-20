package short

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadWorkspaceMeta_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	meta, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Fatalf("expected nil when file doesn't exist, got %+v", meta)
	}
}

func TestReadWorkspaceMeta_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	gbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "meta.json"), []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := ReadWorkspaceMeta(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse workspace meta") {
		t.Errorf("error should mention parse workspace meta, got: %v", err)
	}
}

func TestWriteWorkspaceMeta_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	// .gbot/ doesn't exist yet
	meta := &WorkspaceMeta{
		CurrentSessionID: "sess-123",
		LastActiveAt:     time.Now().Truncate(time.Millisecond), // REAL-TIME: deterministic timestamp for test data
	}

	if err := WriteWorkspaceMeta(dir, meta); err != nil {
		t.Fatalf("WriteWorkspaceMeta error: %v", err)
	}

	// Verify .gbot/meta.json exists
	path := filepath.Join(dir, ".gbot", "meta.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected .gbot/meta.json to exist")
	}

	// Verify content
	got, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta error: %v", err)
	}
	if got.CurrentSessionID != "sess-123" {
		t.Errorf("CurrentSessionID = %q, want %q", got.CurrentSessionID, "sess-123")
	}
	if !got.LastActiveAt.Equal(meta.LastActiveAt) {
		t.Errorf("LastActiveAt = %v, want %v", got.LastActiveAt, meta.LastActiveAt)
	}
}

func TestWriteWorkspaceMeta_Overwrites(t *testing.T) {
	dir := t.TempDir()

	meta1 := &WorkspaceMeta{CurrentSessionID: "sess-1"}
	if err := WriteWorkspaceMeta(dir, meta1); err != nil {
		t.Fatalf("first write: %v", err)
	}

	meta2 := &WorkspaceMeta{CurrentSessionID: "sess-2"}
	if err := WriteWorkspaceMeta(dir, meta2); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta error: %v", err)
	}
	if got.CurrentSessionID != "sess-2" {
		t.Errorf("CurrentSessionID = %q, want %q", got.CurrentSessionID, "sess-2")
	}
}

func TestRoundTrip_EmptyMeta(t *testing.T) {
	dir := t.TempDir()

	meta := &WorkspaceMeta{}
	if err := WriteWorkspaceMeta(dir, meta); err != nil {
		t.Fatalf("WriteWorkspaceMeta error: %v", err)
	}

	got, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta error: %v", err)
	}
	if got.CurrentSessionID != "" {
		t.Errorf("CurrentSessionID = %q, want empty", got.CurrentSessionID)
	}

	// Verify the JSON file is valid
	path := filepath.Join(dir, ".gbot", "meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
}

func TestRoundTrip_FullMeta(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

	meta := &WorkspaceMeta{
		CurrentSessionID: "abc-def-ghi",
		LastActiveAt:     ts,
	}

	if err := WriteWorkspaceMeta(dir, meta); err != nil {
		t.Fatalf("WriteWorkspaceMeta error: %v", err)
	}

	got, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta error: %v", err)
	}
	if got.CurrentSessionID != "abc-def-ghi" {
		t.Errorf("CurrentSessionID = %q, want %q", got.CurrentSessionID, "abc-def-ghi")
	}
	if !got.LastActiveAt.Equal(ts) {
		t.Errorf("LastActiveAt = %v, want %v", got.LastActiveAt, ts)
	}
}

func TestReadWorkspaceMeta_LegacyFormat(t *testing.T) {
	dir := t.TempDir()
	gbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Pre-multi-engine format: only current_session_id.
	legacy := `{"current_session_id":"legacy-sess","last_active_at":"2026-04-19T12:00:00Z"}`
	if err := os.WriteFile(filepath.Join(gbotDir, "meta.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	if got.CurrentSessionID != "legacy-sess" {
		t.Errorf("CurrentSessionID = %q, want legacy-sess", got.CurrentSessionID)
	}
	if len(got.Engines) != 0 {
		t.Errorf("Engines = %v, want empty (legacy file)", got.Engines)
	}
}

func TestReadWorkspaceMeta_NewFormat(t *testing.T) {
	dir := t.TempDir()
	gbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw := `{
		"current_session_id":"s1",
		"engines":[
			{"id":"main","name":"main","active_session_id":"s1","model":"sonnet"},
			{"id":"e2","name":"engine-2","active_session_id":"s2","model":"opus"}
		],
		"active_engine_id":"main",
		"last_active_at":"2026-04-19T12:00:00Z"
	}`
	if err := os.WriteFile(filepath.Join(gbotDir, "meta.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	if got.CurrentSessionID != "s1" {
		t.Errorf("CurrentSessionID = %q, want s1", got.CurrentSessionID)
	}
	if len(got.Engines) != 2 {
		t.Fatalf("len(Engines) = %d, want 2", len(got.Engines))
	}
	if got.Engines[1].ID != "e2" || got.Engines[1].Model != "opus" {
		t.Errorf("Engines[1] = %+v, want id=e2 model=opus", got.Engines[1])
	}
	if got.ActiveEngineID != "main" {
		t.Errorf("ActiveEngineID = %q, want main", got.ActiveEngineID)
	}
}

func TestWriteWorkspaceMeta_DualWrite(t *testing.T) {
	dir := t.TempDir()
	meta := &WorkspaceMeta{
		CurrentSessionID: "s1",
		Engines: []EngineMeta{
			{ID: "main", Name: "main", ActiveSessionID: "s1", Model: "sonnet"},
			{ID: "e2", Name: "engine-2", ActiveSessionID: "s2", Model: "opus"},
		},
		ActiveEngineID: "main",
		LastActiveAt:   time.Now(), // REAL-TIME: test data; field is not asserted against
	}
	if err := WriteWorkspaceMeta(dir, meta); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}
	// Read raw bytes and assert BOTH keys are present.
	path := filepath.Join(dir, ".gbot", "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "\"current_session_id\"") {
		t.Errorf("missing current_session_id in dual-write output: %s", body)
	}
	if !strings.Contains(body, "\"engines\"") {
		t.Errorf("missing engines array in dual-write output: %s", body)
	}
	if !strings.Contains(body, "\"active_engine_id\"") {
		t.Errorf("missing active_engine_id in dual-write output: %s", body)
	}
}
