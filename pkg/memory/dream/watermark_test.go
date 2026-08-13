package dream

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestReadWatermark_NoFile(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadWatermark(dir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("expected zero time for missing file, got %v", got)
	}
}

func TestWriteThenRead_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	before := time.Now() // REAL-TIME: comparing file timestamp to wall clock
	if err := WriteWatermark(dir); err != nil {
		t.Fatalf("WriteWatermark: %v", err)
	}
	got, err := ReadWatermark(dir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	if got.IsZero() {
		t.Fatal("expected non-zero time after write")
	}
	// Timestamp should be within 1 second of the pre-write clock
	diff := got.Sub(before)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("watermark time %v is not within 1s of pre-write %v (diff %v)", got, before, diff)
	}
}

func TestReadWatermark_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, watermarkFileName)
	if err := os.WriteFile(path, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWatermark(dir)
	if err != nil {
		t.Fatalf("ReadWatermark should not error on corrupt file, got: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("expected zero time for corrupt file, got %v", got)
	}
}

func TestWriteWatermark_CreatesDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")
	if err := WriteWatermark(nested); err != nil {
		t.Fatalf("WriteWatermark should create nested dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, watermarkFileName)); err != nil {
		t.Errorf("watermark file should exist after write: %v", err)
	}
}

// writeWatermarkAge writes a watermark file with a timestamp set to `age` ago.
// Used by timer tests to simulate elapsed cooldown time.
func writeWatermarkAge(t *testing.T, memoryDir string, age time.Duration) {
	t.Helper()
	ts := time.Now().Add(-age) // REAL-TIME: simulating elapsed time for watermark age
	path := filepath.Join(memoryDir, watermarkFileName)
	content := strconv.FormatInt(ts.UnixMilli(), 10)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeWatermarkTime writes a watermark file with a specific timestamp.
// Used by timer tests to verify the prompt contains the formatted lastDream.
func writeWatermarkTime(t *testing.T, memoryDir string, ts time.Time) {
	t.Helper()
	path := filepath.Join(memoryDir, watermarkFileName)
	content := strconv.FormatInt(ts.UnixMilli(), 10)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
