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
// goroutine, so the test thread is the sole reader of wsCh and can drain file
// frames directly. wsCap controls the channel capacity: wsWriter is never
// started, so wsCh is never drained — a multi-chunk file must fit entirely
// within wsCap or SendFile blocks forever.
func newSendTestConnector(t *testing.T, wsCap int) *WUIConnector {
	t.Helper()
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		wsCh:        make(chan wsMsg, wsCap),
		done:        make(chan struct{}),
	}
	return c
}

// drainFileStart reads a wsMsg from wsCh and asserts it is a text file_start
// frame, returning the parsed message.
func drainFileStart(t *testing.T, c *WUIConnector) fileStartMsg {
	t.Helper()
	select {
	case msg := <-c.wsCh:
		if msg.isBinary {
			t.Fatalf("expected text file_start frame, got binary")
		}
		var got fileStartMsg
		if err := json.Unmarshal(msg.data, &got); err != nil {
			t.Fatalf("unmarshal file_start: %v", err)
		}
		if got.Type != "file_start" {
			t.Fatalf("type = %q, want file_start", got.Type)
		}
		return got
	default:
		t.Fatal("no file_start frame received on wsCh")
	}
	return fileStartMsg{}
}

// drainFileEnd reads a wsMsg from wsCh and asserts it is a text file_end frame
// with the expected name.
func drainFileEnd(t *testing.T, c *WUIConnector, wantName string) {
	t.Helper()
	select {
	case msg := <-c.wsCh:
		if msg.isBinary {
			t.Fatalf("expected text file_end frame, got binary")
		}
		var got fileEndMsg
		if err := json.Unmarshal(msg.data, &got); err != nil {
			t.Fatalf("unmarshal file_end: %v", err)
		}
		if got.Type != "file_end" {
			t.Fatalf("type = %q, want file_end", got.Type)
		}
		if got.Name != wantName {
			t.Fatalf("file_end name = %q, want %q", got.Name, wantName)
		}
	default:
		t.Fatal("no file_end frame received on wsCh")
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

	start := drainFileStart(t, c)
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
	select {
	case msg := <-c.wsCh:
		if !msg.isBinary {
			t.Fatalf("expected binary chunk, got text: %q", string(msg.data))
		}
		if !bytes.Equal(msg.data, []byte(fileBody)) {
			t.Errorf("binary data = %q, want %q", string(msg.data), fileBody)
		}
	default:
		t.Fatal("no binary chunk received on wsCh")
	}

	drainFileEnd(t, c, "test.png")

	if len(c.wsCh) != 0 {
		t.Errorf("wsCh length = %d, want 0 (file_start + chunk + file_end consumed)", len(c.wsCh))
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

	start := drainFileStart(t, c)
	if start.Name != "big.bin" {
		t.Errorf("name = %q, want big.bin", start.Name)
	}
	if start.Size != int64(len(body)) {
		t.Errorf("size = %d, want %d", start.Size, len(body))
	}

	var collected bytes.Buffer
	chunkIdx := 0
	for collected.Len() < len(body) {
		select {
		case msg := <-c.wsCh:
			if !msg.isBinary {
				t.Fatalf("chunk %d: expected binary, got text %q", chunkIdx, string(msg.data))
			}
			if chunkIdx < 2 {
				if len(msg.data) != fileChunkSize {
					t.Errorf("chunk %d length = %d, want %d", chunkIdx, len(msg.data), fileChunkSize)
				}
			}
			collected.Write(msg.data)
			chunkIdx++
		default:
			t.Fatalf("ran out of chunks after %d bytes, want %d", collected.Len(), len(body))
		}
	}
	if chunkIdx != 3 {
		t.Errorf("chunk count = %d, want 3", chunkIdx)
	}
	if !bytes.Equal(collected.Bytes(), body) {
		t.Errorf("reassembled bytes do not match original (%d vs %d bytes)", collected.Len(), len(body))
	}
	thirdLen := len(body) - 2*fileChunkSize
	if thirdLen <= 0 || thirdLen >= fileChunkSize {
		t.Fatalf("third chunk length math wrong: %d", thirdLen)
	}

	drainFileEnd(t, c, "big.bin")
	if len(c.wsCh) != 0 {
		t.Errorf("wsCh length = %d, want 0", len(c.wsCh))
	}
}

func TestSendFile_NoSizeLimit_11MiB(t *testing.T) {
	t.Parallel()
	// 11 MiB yields 1 file_start + 44 binary chunks + 1 file_end = 46 messages.
	// wsWriter is NOT started, so wsCh must hold all 46 without blocking.
	c := newSendTestConnector(t, 1024)

	body := bytes.Repeat([]byte{0}, 11<<20)
	tmpFile := filepath.Join(t.TempDir(), "huge.bin")
	if err := os.WriteFile(tmpFile, body, 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	start := drainFileStart(t, c)
	if start.Size != int64(11<<20) {
		t.Errorf("size = %d, want %d", start.Size, 11<<20)
	}

	var collected bytes.Buffer
	for collected.Len() < len(body) {
		select {
		case msg := <-c.wsCh:
			if !msg.isBinary {
				t.Fatalf("expected binary chunk, got text %q", string(msg.data))
			}
			collected.Write(msg.data)
		default:
			t.Fatalf("ran out of chunks after %d bytes", collected.Len())
		}
	}
	if !bytes.Equal(collected.Bytes(), body) {
		t.Errorf("reassembled bytes mismatch (%d vs %d bytes)", collected.Len(), len(body))
	}

	drainFileEnd(t, c, "huge.bin")
	if len(c.wsCh) != 0 {
		t.Errorf("wsCh length = %d, want 0", len(c.wsCh))
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

	start := drainFileStart(t, c)
	if start.Size != 0 {
		t.Errorf("size = %d, want 0", start.Size)
	}

	drainFileEnd(t, c, "empty.bin")
	if len(c.wsCh) != 0 {
		t.Errorf("wsCh length = %d, want 0 (start + end only, no chunks)", len(c.wsCh))
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

	drainFileStart(t, c)
	select {
	case msg := <-c.wsCh:
		if !msg.isBinary {
			t.Fatalf("expected binary chunk, got text")
		}
		if len(msg.data) != 1 || msg.data[0] != 0x42 {
			t.Errorf("binary data = %v, want [0x42]", msg.data)
		}
	default:
		t.Fatal("no binary chunk for single-byte file")
	}
	drainFileEnd(t, c, "one.bin")
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
	if len(c.wsCh) != 0 {
		t.Errorf("wsCh length = %d, want 0 (no event on missing file)", len(c.wsCh))
	}
}

// TestSendFile_ConcurrentSerializesFrameSequences verifies that sendFileMu
// prevents two concurrent SendFile calls from interleaving their file_start →
// chunk → file_end sequences on wsCh. Each file's frames must be contiguous.
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

	// Drain all 6 frames (2 files × [start, chunk, end]).
	var frames []wsMsg
	for len(frames) < 6 {
		select {
		case msg := <-c.wsCh:
			frames = append(frames, msg)
		default:
			t.Fatalf("only %d frames drained, want 6", len(frames))
		}
	}
	if len(c.wsCh) != 0 {
		t.Errorf("wsCh length = %d, want 0", len(c.wsCh))
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
