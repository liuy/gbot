package wui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// newSendTestConnector builds a WUIConnector WITHOUT starting the wsWriter
// goroutine, so the test thread is the sole consumer of the outbound queue
// and can take file frames directly. wsCap controls the queue capacity: no
// writer drains it, so a multi-chunk file must fit entirely within wsCap or
// SendFile blocks forever.
func newSendTestConnector(t *testing.T, wsCap int) *WUIConnector {
	t.Helper()
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
	}
	c.outQ.Store(newOutQueue(wsCap))
	return c
}

// parseFileStart asserts frame is a text file_start frame and returns the
// parsed message.
func parseFileStart(t *testing.T, frame wsMsg) fileStartMsg {
	t.Helper()
	if frame.isBinary {
		t.Fatalf("expected text file_start frame, got binary")
	}
	var got fileStartMsg
	if err := json.Unmarshal(frame.data, &got); err != nil {
		t.Fatalf("unmarshal file_start: %v", err)
	}
	if got.Type != "file_start" {
		t.Fatalf("type = %q, want file_start", got.Type)
	}
	return got
}

// parseFileEnd asserts frame is a text file_end frame with the expected
// name.
func parseFileEnd(t *testing.T, frame wsMsg, wantName string) {
	t.Helper()
	if frame.isBinary {
		t.Fatalf("expected text file_end frame, got binary")
	}
	var got fileEndMsg
	if err := json.Unmarshal(frame.data, &got); err != nil {
		t.Fatalf("unmarshal file_end: %v", err)
	}
	if got.Type != "file_end" {
		t.Fatalf("type = %q, want file_end", got.Type)
	}
	if got.Name != wantName {
		t.Fatalf("file_end name = %q, want %q", got.Name, wantName)
	}
}

func TestSendFile_PushesFileEvent(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t, 16)

	const fileBody = "fake-png-bytes"
	tmpFile := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(tmpFile, []byte(fileBody), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	// Pushes are synchronous appends: all three frames are queued the
	// moment SendFile returns.
	frames := takeOutboundFrames(c)
	if len(frames) != 3 {
		t.Fatalf("outbound frames = %d, want 3 (file_start + chunk + file_end)", len(frames))
	}

	start := parseFileStart(t, frames[0])
	if start.Name != "test.png" {
		t.Errorf("name = %q, want test.png", start.Name)
	}
	if start.Mime != "image/png" {
		t.Errorf("mime = %q, want image/png", start.Mime)
	}
	if start.Size != int64(len(fileBody)) {
		t.Errorf("size = %d, want %d", start.Size, len(fileBody))
	}

	// One binary chunk equal to the file bytes (file fits in one 256 KiB chunk).
	if !frames[1].isBinary {
		t.Fatalf("expected binary chunk, got text: %q", string(frames[1].data))
	}
	if !bytes.Equal(frames[1].data, []byte(fileBody)) {
		t.Errorf("binary data = %q, want %q", string(frames[1].data), fileBody)
	}

	parseFileEnd(t, frames[2], "test.png")

	if n := c.outQ.Load().len(); n != 0 {
		t.Errorf("outbound queue length = %d, want 0 (file_start + chunk + file_end consumed)", n)
	}
}

func TestSendFile_MultiChunk(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t, 16)

	// 600 KiB of deterministic bytes → 3 chunks (256K + 256K + 88K).
	body := bytes.Repeat([]byte("abcdef"), 102400)
	tmpFile := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(tmpFile, body, 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	frames := takeOutboundFrames(c)
	if len(frames) != 5 {
		t.Fatalf("outbound frames = %d, want 5 (file_start + 3 chunks + file_end)", len(frames))
	}

	start := parseFileStart(t, frames[0])
	if start.Name != "big.bin" {
		t.Errorf("name = %q, want big.bin", start.Name)
	}
	if start.Size != int64(len(body)) {
		t.Errorf("size = %d, want %d", start.Size, len(body))
	}

	var collected bytes.Buffer
	for i := 1; i <= 3; i++ {
		if !frames[i].isBinary {
			t.Fatalf("chunk %d: expected binary, got text %q", i-1, string(frames[i].data))
		}
		if i <= 2 && len(frames[i].data) != fileChunkSize {
			t.Errorf("chunk %d length = %d, want %d", i-1, len(frames[i].data), fileChunkSize)
		}
		collected.Write(frames[i].data)
	}
	if !bytes.Equal(collected.Bytes(), body) {
		t.Errorf("reassembled bytes do not match original (%d vs %d bytes)", collected.Len(), len(body))
	}
	thirdLen := len(body) - 2*fileChunkSize
	if thirdLen <= 0 || thirdLen >= fileChunkSize {
		t.Fatalf("third chunk length math wrong: %d", thirdLen)
	}
	if len(frames[3].data) != thirdLen {
		t.Errorf("third chunk length = %d, want %d", len(frames[3].data), thirdLen)
	}

	parseFileEnd(t, frames[4], "big.bin")
	if n := c.outQ.Load().len(); n != 0 {
		t.Errorf("outbound queue length = %d, want 0", n)
	}
}

func TestSendFile_NoSizeLimit_11MiB(t *testing.T) {
	t.Parallel()
	// 11 MiB yields 1 file_start + 44 binary chunks + 1 file_end = 46 messages.
	// wsWriter is NOT started, so the queue must hold all 46 without blocking.
	c := newSendTestConnector(t, 1024)

	body := bytes.Repeat([]byte{0}, 11<<20)
	tmpFile := filepath.Join(t.TempDir(), "huge.bin")
	if err := os.WriteFile(tmpFile, body, 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	frames := takeOutboundFrames(c)
	if len(frames) != 46 {
		t.Fatalf("outbound frames = %d, want 46 (file_start + 44 chunks + file_end)", len(frames))
	}

	start := parseFileStart(t, frames[0])
	if start.Size != int64(11<<20) {
		t.Errorf("size = %d, want %d", start.Size, 11<<20)
	}

	var collected bytes.Buffer
	for _, frame := range frames[1:45] {
		if !frame.isBinary {
			t.Fatalf("expected binary chunk, got text %q", string(frame.data))
		}
		collected.Write(frame.data)
	}
	if !bytes.Equal(collected.Bytes(), body) {
		t.Errorf("reassembled bytes mismatch (%d vs %d bytes)", collected.Len(), len(body))
	}

	parseFileEnd(t, frames[45], "huge.bin")
	if n := c.outQ.Load().len(); n != 0 {
		t.Errorf("outbound queue length = %d, want 0", n)
	}
}

func TestSendFile_EmptyFile(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t, 16)

	tmpFile := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(tmpFile, nil, 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	frames := takeOutboundFrames(c)
	if len(frames) != 2 {
		t.Fatalf("outbound frames = %d, want 2 (file_start + file_end, no chunks)", len(frames))
	}
	start := parseFileStart(t, frames[0])
	if start.Size != 0 {
		t.Errorf("size = %d, want 0", start.Size)
	}
	parseFileEnd(t, frames[1], "empty.bin")
	if n := c.outQ.Load().len(); n != 0 {
		t.Errorf("outbound queue length = %d, want 0 (start + end only, no chunks)", n)
	}
}

func TestSendFile_SingleByte(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t, 16)

	tmpFile := filepath.Join(t.TempDir(), "one.bin")
	if err := os.WriteFile(tmpFile, []byte{0x42}, 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	frames := takeOutboundFrames(c)
	if len(frames) != 3 {
		t.Fatalf("outbound frames = %d, want 3 (file_start + chunk + file_end)", len(frames))
	}
	parseFileStart(t, frames[0])
	if !frames[1].isBinary {
		t.Fatalf("expected binary chunk, got text")
	}
	if len(frames[1].data) != 1 || frames[1].data[0] != 0x42 {
		t.Errorf("binary data = %v, want [0x42]", frames[1].data)
	}
	parseFileEnd(t, frames[2], "one.bin")
}

func TestSendFile_MissingFile(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t, 16)

	err := c.SendFile(context.Background(), filepath.Join(t.TempDir(), "nope.png"), "")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "send file") {
		t.Errorf("error = %q, want 'send file'", err.Error())
	}
	if frames := takeOutboundFrames(c); len(frames) != 0 {
		t.Errorf("outbound frames = %d, want 0 (no event on missing file)", len(frames))
	}
}

// TestSendFile_ConcurrentSerializesFrameSequences verifies that sendFileMu
// prevents two concurrent SendFile calls from interleaving their file_start →
// chunk → file_end sequences on the outbound queue. Each file's frames must
// be contiguous.
func TestSendFile_ConcurrentSerializesFrameSequences(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t, 16)

	bodyA := bytes.Repeat([]byte("a"), 100)
	bodyB := bytes.Repeat([]byte("b"), 100)
	fileA := filepath.Join(t.TempDir(), "a.txt")
	fileB := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(fileA, bodyA, 0o644); err != nil {
		t.Fatalf("write fileA: %v", err)
	}
	if err := os.WriteFile(fileB, bodyB, 0o644); err != nil {
		t.Fatalf("write fileB: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = c.SendFile(context.Background(), fileA, "")
	}()
	go func() {
		defer wg.Done()
		_ = c.SendFile(context.Background(), fileB, "")
	}()
	wg.Wait()

	// All 6 frames (2 files × [start, chunk, end]) must be queued by the
	// time both SendFile calls return.
	frames := takeOutboundFrames(c)
	if len(frames) != 6 {
		t.Fatalf("outbound frames = %d, want 6", len(frames))
	}
	if n := c.outQ.Load().len(); n != 0 {
		t.Errorf("outbound queue length = %d, want 0", n)
	}

	// Assert the sequence is one of two non-interleaved orders. Between a
	// file_start and its matching file_end (same name) there must be no
	// file_start with a different name.
	validateContiguous := func(first, second string) bool {
		idx := 0
		for idx < len(frames) {
			var start fileStartMsg
			if frames[idx].isBinary {
				return false
			}
			if err := json.Unmarshal(frames[idx].data, &start); err != nil {
				return false
			}
			if start.Type != "file_start" {
				return false
			}
			// The next frame must be the binary chunk for this file.
			if !frames[idx+1].isBinary {
				return false
			}
			// The frame after must be file_end with the same name.
			var end fileEndMsg
			if frames[idx+2].isBinary {
				return false
			}
			if err := json.Unmarshal(frames[idx+2].data, &end); err != nil {
				return false
			}
			if end.Type != "file_end" || end.Name != start.Name {
				return false
			}
			idx += 3
		}
		return true
	}

	if !validateContiguous("a.txt", "b.txt") {
		t.Errorf("frame sequences interleaved: %v", frames)
	}
}
