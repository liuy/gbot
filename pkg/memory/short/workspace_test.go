package short

import (
	"encoding/json"
	"os"
	"path/filepath"
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
}

func TestWriteWorkspaceMeta_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	// .gbot/ doesn't exist yet
	meta := &WorkspaceMeta{
		CurrentSessionID: "sess-123",
		LastActiveAt:     time.Now().Truncate(time.Millisecond),
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
	var parsed map[string]interface{}
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
