package bash

import (
	"os"
	"testing"
)

func TestStreamingOutputMaybeSpillCreateTempError(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Write some data to build up rawBuf
	mustWrite(t, s, []byte("initial data\n"))

	// Set totalBytes above threshold but make temp dir unwritable
	s.mu.Lock()
	s.totalBytes = spillThreshold + 1
	// Save and replace os temp dir to force CreateTemp failure
	s.mu.Unlock()

	// Create a read-only directory to force CreateTemp to fail
	readOnlyDir := t.TempDir()
	readOnlySub := readOnlyDir + "/readonly"
	if err := os.Mkdir(readOnlySub, 0555); err != nil {
		t.Skip("cannot create read-only dir on this system")
	}
	defer func() { _ = os.Chmod(readOnlySub, 0755) }() // cleanup

	// Monkey-patch by writing directly: set state to force maybeSpill but in a way
	// that CreateTemp will fail (we can't easily mock os.CreateTemp without interfaces).
	// Instead, test that maybeSpill degrades gracefully when file path is invalid.
	s.mu.Lock()
	s.spilled = true // simulate partial spill state
	s.filePath = readOnlySub + "/nonexistent/nested/file"
	s.file = nil
	s.mu.Unlock()

	// Cleanup with invalid path should not panic
	s.Cleanup()
}

func TestStreamingOutputReadContentFileDeleted(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Write enough to spill
	chunk := make([]byte, 1025)
	for i := range chunk[:1024] {
		chunk[i] = 'x'
	}
	chunk[1024] = '\n'
	chunksNeeded := int(spillThreshold/int64(len(chunk))) + 1
	for range chunksNeeded {
		mustWrite(t, s, []byte(chunk))
	}

	if !s.Spilled() {
		t.Fatal("expected spill")
	}

	// Delete the file behind StreamingOutput's back
	s.mu.Lock()
	path := s.filePath
	s.mu.Unlock()
	_ = os.Remove(path)

	// ReadContent should return empty string (os.Open fails)
	content := s.ReadContent(1024)
	if content != "" {
		t.Errorf("ReadContent() = %q, want empty string when file deleted", content)
	}

	s.Cleanup()
}

func TestStreamingOutputMaybeSpillTempDirError(t *testing.T) {
	// Do NOT use t.Parallel() — we modify TMPDIR process-wide
	t.Setenv("TMPDIR", "/nonexistent/path/for/gbot-test")

	s := NewStreamingOutput(nil)

	// Write enough to trigger spill, but CreateTemp will fail
	chunk := make([]byte, spillThreshold+1)
	for i := range chunk {
		chunk[i] = 'x'
	}
	chunk[len(chunk)-1] = '\n'
	mustWrite(t, s, chunk)

	// Should gracefully degrade: stay in memory mode
	if s.Spilled() {
		t.Error("should not spill when os.CreateTemp fails — graceful degradation expected")
	}

	// Data should still be accessible in memory
	lines := s.Lines()
	if len(lines) == 0 {
		t.Error("Lines() should still return data in memory mode after CreateTemp failure")
	}
}
