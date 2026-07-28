package wui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// newSendTestConnector builds a WUIConnector WITHOUT starting the wsWriter
// goroutine, so the test thread is the sole reader of wsCh and can drain file
// events directly. Mirrors the inbound_test.go:382 manual-build pattern —
// newTestConnector starts wsWriter which, with no activeWS, discards every
// frame and the test never sees the payload.
func newSendTestConnector(t *testing.T) *WUIConnector {
	t.Helper()
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		wsCh:        make(chan []byte, 16),
		done:        make(chan struct{}),
	}
	return c
}

func TestSendFile_PushesFileEvent(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t)

	tmpFile := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(tmpFile, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	select {
	case payload := <-c.wsCh:
		var got struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Mime string `json:"mime"`
			Data string `json:"data"`
		}
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if got.Type != "file" {
			t.Errorf("type = %q, want file", got.Type)
		}
		if got.Name != "test.png" {
			t.Errorf("name = %q, want test.png", got.Name)
		}
		if got.Mime != "image/png" {
			t.Errorf("mime = %q, want image/png", got.Mime)
		}
		decoded, err := base64.StdEncoding.DecodeString(got.Data)
		if err != nil {
			t.Fatalf("base64 decode: %v", err)
		}
		if !bytes.Equal(decoded, []byte("fake-png-bytes")) {
			t.Errorf("decoded data = %q, want fake-png-bytes", string(decoded))
		}
	default:
		t.Fatal("no payload received on wsCh")
	}
}

func TestSendFile_TooLarge(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t)

	tmpFile := filepath.Join(t.TempDir(), "huge.bin")
	if err := os.WriteFile(tmpFile, bytes.Repeat([]byte{0}, 11<<20), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	err := c.SendFile(context.Background(), tmpFile, "")
	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want 'too large'", err.Error())
	}
	if len(c.wsCh) != 0 {
		t.Errorf("wsCh length = %d, want 0 (no event on size rejection)", len(c.wsCh))
	}
}

func TestSendFile_MissingFile(t *testing.T) {
	t.Parallel()
	c := newSendTestConnector(t)

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
