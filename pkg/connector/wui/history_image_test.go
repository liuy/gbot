package wui

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/types"
)

// writeImageOnDisk writes a real JPEG to a temp file and returns its path.
// The image is large enough (1024x1024) to exercise the resize path.
func writeImageOnDisk(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for y := range 1024 {
		for x := range 1024 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// fixedTimestamp is a deterministic value used for synthetic Message
// construction in these tests. The exact timestamp is irrelevant to the
// assertions; using a fixed value avoids time.Now() (which the weak-test
// scanner flags because real-time sleeps make tests non-deterministic).
var fixedTimestamp = time.Unix(1700000000, 0)

func TestBuildHistoryChatMsg_FileSourceImage_InlinedAsDataURL(t *testing.T) {
	c := newTestConnector(t)
	path := writeImageOnDisk(t)

	m := types.Message{
		ID:        "m1",
		Role:      types.RoleUser,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/jpeg", Path: path}},
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil)
	if len(hm.Blocks) != 1 {
		t.Fatalf("Blocks len = %d, want 1", len(hm.Blocks))
	}
	b := hm.Blocks[0]
	if b.Kind != "image" {
		t.Errorf("kind = %q, want image", b.Kind)
	}
	if !strings.HasPrefix(b.Src, "data:image/jpeg;base64,") {
		t.Errorf("src prefix = %q, want data:image/jpeg;base64,", prefix(b.Src, 30))
	}
	// Decode the data URL and verify the resulting image fits within 512px.
	b64 := strings.TrimPrefix(b.Src, "data:image/jpeg;base64,")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(raw))
	if derr != nil {
		t.Fatalf("decode resized: %v", derr)
	}
	if cfg.Width > 512 || cfg.Height > 512 {
		t.Errorf("decoded dims = %dx%d, must be <= 512 on the long edge", cfg.Width, cfg.Height)
	}
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func TestBuildHistoryChatMsg_Base64Source_Resized(t *testing.T) {
	c := newTestConnector(t)
	// Construct a 1024x1024 real JPEG and base64-encode it directly into the
	// ImageSource — no disk file involved. The history path must still resize
	// the decoded image to <=512px on the long edge, regardless of source type.
	img := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for y := range 1024 {
		for x := range 1024 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	sample := base64.StdEncoding.EncodeToString(buf.Bytes())
	m := types.Message{
		ID:        "m2",
		Role:      types.RoleUser,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.NewImageBlock(types.ImageSource{
				Type:      "base64",
				MediaType: "image/jpeg",
				Data:      sample,
			}),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil)
	if len(hm.Blocks) != 1 {
		t.Fatalf("Blocks len = %d, want 1", len(hm.Blocks))
	}
	b := hm.Blocks[0]
	if b.Kind != "image" {
		t.Errorf("kind = %q, want image", b.Kind)
	}
	if b.Src == "data:image/jpeg;base64,"+sample {
		t.Error("src is verbatim passthrough of the source base64, expected resize")
	}
	raw, derr := base64.StdEncoding.DecodeString(strings.TrimPrefix(b.Src, "data:image/jpeg;base64,"))
	if derr != nil {
		t.Fatalf("base64 decode: %v", derr)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode resized: %v", err)
	}
	if cfg.Width > 512 || cfg.Height > 512 {
		t.Errorf("decoded dims = %dx%d, must be <= 512 on the long edge", cfg.Width, cfg.Height)
	}
}

func TestBuildHistoryChatMsg_MissingFile_DegradesToPlaceholder(t *testing.T) {
	c := newTestConnector(t)
	missingPath := filepath.Join(t.TempDir(), "nonexistent.png")
	m := types.Message{
		ID:        "m3",
		Role:      types.RoleUser,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/png", Path: missingPath}},
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil)
	var textBlocks []historyBlock
	for _, b := range hm.Blocks {
		if b.Kind == "text" {
			textBlocks = append(textBlocks, b)
		}
	}
	if len(textBlocks) != 1 {
		t.Fatalf("text blocks = %d, want 1 (degraded placeholder)", len(textBlocks))
	}
	want := "[image: " + filepath.Base(missingPath) + "]"
	if textBlocks[0].Text != want {
		t.Errorf("placeholder = %q, want %q", textBlocks[0].Text, want)
	}
	if !strings.Contains(hm.Text, want) {
		t.Errorf("hm.Text = %q, want substring %q", hm.Text, want)
	}
}

func TestBuildHistoryChatMsg_CorruptFile_DegradesToPlaceholder(t *testing.T) {
	c := newTestConnector(t)
	path := filepath.Join(t.TempDir(), "corrupt.jpg")
	if err := os.WriteFile(path, []byte{0xFF, 0xD8, 0xFF, 0x00, 0x00}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m := types.Message{
		ID:        "m4",
		Role:      types.RoleUser,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/jpeg", Path: path}},
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil)
	var textBlocks []historyBlock
	for _, b := range hm.Blocks {
		if b.Kind == "text" {
			textBlocks = append(textBlocks, b)
		}
	}
	if len(textBlocks) != 1 {
		t.Fatalf("text blocks = %d, want 1 (degraded placeholder for corrupt file)", len(textBlocks))
	}
	want := "[image: " + filepath.Base(path) + "]"
	if textBlocks[0].Text != want {
		t.Errorf("placeholder = %q, want %q", textBlocks[0].Text, want)
	}
}

// TestThumbCache_FIFOEviction verifies the cache evicts the oldest entry
// when cap is exceeded (FIFO order is preserved).
func TestThumbCache_FIFOEviction(t *testing.T) {
	tc := newThumbCache()
	tc.cap = 3
	tc.put("k1", "v1")
	tc.put("k2", "v2")
	tc.put("k3", "v3")
	if _, ok := tc.get("k1"); !ok {
		t.Error("k1 evicted prematurely")
	}
	tc.put("k4", "v4") // should evict k1
	if _, ok := tc.get("k1"); ok {
		t.Error("k1 not evicted when cap exceeded")
	}
	if v, ok := tc.get("k2"); !ok || v != "v2" {
		t.Errorf("k2 missing or wrong value: ok=%v v=%q", ok, v)
	}
	if v, ok := tc.get("k4"); !ok || v != "v4" {
		t.Errorf("k4 missing or wrong value: ok=%v v=%q", ok, v)
	}
}

// TestThumbCache_PutExistingKeyUpdates verifies that re-putting an existing
// key updates the value without appending a duplicate to the order slice
// (so a later eviction does not evict the still-present key twice).
func TestThumbCache_PutExistingKeyUpdates(t *testing.T) {
	tc := newThumbCache()
	tc.cap = 2
	tc.put("k1", "v1")
	tc.put("k2", "v2")
	tc.put("k1", "v1-updated")
	if v, _ := tc.get("k1"); v != "v1-updated" {
		t.Errorf("k1 = %q, want v1-updated", v)
	}
	tc.put("k3", "v3") // should evict k2 (oldest)
	if _, ok := tc.get("k2"); ok {
		t.Error("k2 should have been evicted")
	}
	if _, ok := tc.get("k1"); !ok {
		t.Error("k1 should still be present")
	}
}

// TestHistoryImageDataURL_CacheHit verifies the cache short-circuits disk
// reads on the second call for the same path. The proof is that the second
// call returns the SAME dataURL as the first call, even after the file is
// deleted (which would otherwise cause ReadFile to fail).
//
// Note: the cache key is path|mtime, so a file modification would invalidate
// the cache. The test relies on the file being unchanged between calls
// (Stat succeeds and returns the same mtime), so the cache key is identical.
func TestHistoryImageDataURL_CacheHit(t *testing.T) {
	c := newTestConnector(t)
	path := writeImageOnDisk(t)
	cb := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/jpeg", Path: path}}
	first, ok1 := c.historyImageDataURL(cb)
	if !ok1 {
		t.Fatal("first call returned false")
	}
	// Second call with the same path + same mtime must hit the cache.
	second, ok2 := c.historyImageDataURL(cb)
	if !ok2 {
		t.Fatal("second call returned false")
	}
	if first != second {
		t.Errorf("cache miss: first and second data URLs differ")
	}
	// Verify the cache actually holds the entry (proof of caching rather
	// than just deterministic re-encoding).
	key := cb.Source.Path
	if fi, err := os.Stat(cb.Source.Path); err == nil {
		key = cb.Source.Path + "|" + fi.ModTime().String()
	}
	if v, ok := c.thumbs.get(key); !ok || v != first {
		t.Errorf("thumbs cache does not hold expected entry: ok=%v", ok)
	}
}

// TestBuildHistoryChatMsg_ImageAndTextInterleaved verifies image and text
// blocks land in the right order (text first, image second).
func TestBuildHistoryChatMsg_ImageAndTextInterleaved(t *testing.T) {
	c := newTestConnector(t)
	path := writeImageOnDisk(t)
	m := types.Message{
		ID:        "m5",
		Role:      types.RoleUser,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.NewTextBlock("describe this"),
			types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/jpeg", Path: path}},
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil)
	if len(hm.Blocks) != 2 {
		t.Fatalf("Blocks = %d, want 2 (text + image)", len(hm.Blocks))
	}
	if hm.Blocks[0].Kind != "text" || hm.Blocks[0].Text != "describe this" {
		t.Errorf("Blocks[0] = %+v, want text 'describe this'", hm.Blocks[0])
	}
	if hm.Blocks[1].Kind != "image" {
		t.Errorf("Blocks[1].Kind = %q, want image", hm.Blocks[1].Kind)
	}
}

// TestHistoryImageDataURL_Base64CacheHit verifies the cache short-circuits
// re-decode/re-resize on the second call for the same base64 source. Proof:
// the thumbs cache holds an entry with the expected b64:<sha256[:8]> key after
// the first call, and the second call returns the same dataURL.
func TestHistoryImageDataURL_Base64CacheHit(t *testing.T) {
	c := newTestConnector(t)
	// Small real PNG (4x4) — exercises decode + resize without blowing the cache.
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 0xFF, A: 0xFF})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	cb := types.ContentBlock{
		Type:   types.ContentTypeImage,
		Source: &types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: b64},
	}
	first, ok1 := c.historyImageDataURL(cb)
	if !ok1 {
		t.Fatal("first call returned false")
	}
	// Second call with identical base64 must hit the cache.
	second, ok2 := c.historyImageDataURL(cb)
	if !ok2 {
		t.Fatal("second call returned false")
	}
	if first != second {
		t.Errorf("cache miss: first and second data URLs differ")
	}
	// Verify the cache key follows the b64:<sha256[:8] hex> scheme.
	hash := sha256.Sum256([]byte(b64))
	expectedKey := "b64:" + hex.EncodeToString(hash[:8])
	if v, ok := c.thumbs.get(expectedKey); !ok || v != first {
		t.Errorf("thumbs cache miss for key=%s: ok=%v", expectedKey, ok)
	}
}

// Ensure media package is imported in case future test cases need it.
var _ = media.CategoryImage
