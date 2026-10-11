package dream

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
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

// writeWatermarkRaw writes the watermark file with exact bytes.
func writeWatermarkRaw(t *testing.T, memoryDir, content string) {
	t.Helper()
	path := filepath.Join(memoryDir, watermarkFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The file holds decimal epoch milliseconds: base 10 and the full int64 width
// are both part of the contract.
func TestReadWatermark_ExactEpochMillis(t *testing.T) {
	dir := t.TempDir()
	writeWatermarkRaw(t, dir, "1700000000000")

	got, err := ReadWatermark(dir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	want := time.UnixMilli(1700000000000)
	if !got.Equal(want) {
		t.Errorf("ReadWatermark = %v, want %v", got, want)
	}
}

func TestReadWatermark_MaxInt64Millis(t *testing.T) {
	dir := t.TempDir()
	writeWatermarkRaw(t, dir, "9223372036854775807")

	got, err := ReadWatermark(dir)
	if err != nil {
		t.Fatalf("ReadWatermark: %v", err)
	}
	want := time.UnixMilli(9223372036854775807)
	if !got.Equal(want) {
		t.Errorf("ReadWatermark = %v, want %v (int64 width must not be narrowed)", got, want)
	}
}

// A failing MkdirAll must surface as the mkdir error: swallowing the guard
// would report the downstream WriteFile failure instead and send the operator
// looking at the wrong path.
func TestWriteWatermark_MkdirFailureReported(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	err := WriteWatermark(filepath.Join(blocker, "memory"))
	if err == nil {
		t.Fatal("WriteWatermark should fail when the parent is a regular file")
	}
	var pe *os.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T (%v), want *os.PathError", err, err)
	}
	if pe.Op != "mkdir" {
		t.Errorf("error Op = %q, want \"mkdir\" (got %v)", pe.Op, err)
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("error = %v, want ENOTDIR", err)
	}
}

// effectivePerm accounts for the process umask, which the kernel applies to
// the mode argument of mkdir/open.
func effectivePerm(t *testing.T, mode os.FileMode) os.FileMode {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, []byte("x"), 0o777); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	info, err := os.Stat(probe)
	if err != nil {
		t.Fatalf("stat probe: %v", err)
	}
	umask := 0o777 & ^info.Mode().Perm()
	return mode & ^umask & 0o777
}

func TestWriteWatermark_Modes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	if err := WriteWatermark(dir); err != nil {
		t.Fatalf("WriteWatermark: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got, want := dirInfo.Mode().Perm(), effectivePerm(t, 0o755); got != want {
		t.Errorf("created dir perm = %o, want %o", got, want)
	}

	fileInfo, err := os.Stat(filepath.Join(dir, watermarkFileName))
	if err != nil {
		t.Fatalf("stat watermark: %v", err)
	}
	if got, want := fileInfo.Mode().Perm(), effectivePerm(t, 0o644); got != want {
		t.Errorf("watermark file perm = %o, want %o", got, want)
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
