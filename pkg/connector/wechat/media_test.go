package wechat

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/types"
)

// aesEcbEncryptTest applies PKCS7 padding + AES-128-ECB encryption so media
// tests can build CDN-style encrypted blobs that media.DecryptAesEcb will
// recover. (The stdlib omits ECB, hence the manual block loop.)
func aesEcbEncryptTest(plaintext, key []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	padLen := 16 - len(plaintext)%16
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	out := make([]byte, len(padded))
	for start := 0; start < len(padded); start += 16 {
		block.Encrypt(out[start:start+16], padded[start:start+16])
	}
	return out
}

// readFile is os.ReadFile re-exported for tests in this file.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// newMediaTestConnector builds a connector with a real media cache rooted at a
// temp dir and an httptest.Server-derived client, so downloadMedia can hit a
// local server and write into an isolated media. The returned cleanup stops
// the cache goroutine.
func newMediaTestConnector(t *testing.T) (*WeChatConnector, *media.Store) {
	t.Helper()
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	c := &WeChatConnector{
		hub:        hub.NewHub(),
		client:     &http.Client{Timeout: 5 * time.Second},
		mediaCache: store,
		inboundCh:  make(chan inboundMessage, 10),
	}
	c.queryFn = func(context.Context, string, string) {}
	c.queryWithContentFn = func(context.Context, []types.ContentBlock, string) {}
	c.dedup = newDedupSet(MessageDedupTTLSeconds)
	return c, store
}

// encryptAesEcbForMediaTest builds an AES-encrypted CDN blob for media tests
// by delegating to aesEcbEncryptTest.
func encryptAesEcbForMediaTest(plaintext, key []byte) []byte {
	return aesEcbEncryptTest(plaintext, key)
}

func TestDownloadMedia_Image(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	// A real PNG header so SniffImageMime returns image/png.
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	plaintext := append(pngHeader, []byte("body")...)
	ciphertext := encryptAesEcbForMediaTest(plaintext, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, store := newMediaTestConnector(t)
	items := []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{
			FullURL: srv.URL, AesKey: aesKeyB64,
		}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != types.ContentTypeImage {
		t.Fatalf("block.Type = %q, want image", block.Type)
	}
	if block.Source == nil {
		t.Fatal("block.Source = nil")
	}
	if block.Source.Type != "file" {
		t.Errorf("Source.Type = %q, want file", block.Source.Type)
	}
	if block.Source.MediaType != "image/png" {
		t.Errorf("Source.MediaType = %q, want image/png", block.Source.MediaType)
	}
	if block.Source.Path == "" {
		t.Fatal("Source.Path is empty, want a cache path")
	}
	// File must exist on disk under the images category.
	if got, err := readFile(block.Source.Path); err != nil {
		t.Fatalf("cached image not readable: %v", err)
	} else if string(got) != string(plaintext) {
		t.Errorf("cached image content mismatch: got %v want %v", got, plaintext)
	}
	if !strings.Contains(block.Source.Path, filepath.Join(store.RootDir, "images")) {
		t.Errorf("Path = %q, want it under the images category dir", block.Source.Path)
	}
}

func TestDownloadMedia_File(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	plaintext := []byte("%PDF-1.4 fake pdf bytes")
	ciphertext := encryptAesEcbForMediaTest(plaintext, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	items := []Item{
		{Type: ItemFile, FileItem: &FileItem{
			FileName: "report.pdf",
			Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
		}},
	}
	block := c.downloadMedia(context.Background(), items)
	// Documents become a TEXT block containing either parsed content
	// or a fallback path hint (when the Read tool cannot parse the format).
	if block.Type != types.ContentTypeText {
		t.Fatalf("block.Type = %q, want text", block.Type)
	}
	if !strings.Contains(block.Text, "report.pdf") {
		t.Errorf("Text = %q, want it to contain the file name", block.Text)
	}
	// The block must reference the document either as parsed content
	// ("[Document:") or as a path fallback ("[Document attachment:").
	if !strings.Contains(block.Text, "[Document") {
		t.Errorf("Text = %q, want '[Document' marker", block.Text)
	}
}

// TestDownloadMedia_PreservesOriginalExtension verifies that documents with
// extensions missing from the mimeToExt reverse-mapping (e.g. .pptx, .docx)
// are still saved with their original extension, not collapsed to .bin.
// ExtFromMime(MimeFromExt(".pptx")) returns ".bin" because the reverse
// map lacks pptx/docx MIME types, causing fileread to reject the file as
// binary and forcing the LLM to do an extra Read tool call.
func TestDownloadMedia_PreservesOriginalExtension(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	plaintext := []byte("PK\x03\x04 fake pptx") // ZIP magic (pptx is a zip)
	ciphertext := encryptAesEcbForMediaTest(plaintext, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	items := []Item{
		{Type: ItemFile, FileItem: &FileItem{
			FileName: "presentation.pptx",
			Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
		}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != types.ContentTypeText {
		t.Fatalf("block.Type = %q, want text", block.Type)
	}
	// The text must contain the saved path with .pptx extension, NOT .bin.
	if strings.Contains(block.Text, ".bin") {
		t.Errorf("Text = %q, contains '.bin' — original .pptx extension was lost", block.Text)
	}
	if !strings.Contains(block.Text, ".pptx") {
		t.Errorf("Text = %q, want it to contain '.pptx' extension", block.Text)
	}
}

// TestDownloadMedia_DocumentContentExtracted verifies that downloadFile
// extracts the parsed document CONTENT (not the Go struct representation)
// from fileread's ToolResult. ToolResult.Data is an `any` holding a
// TextOutput struct — fmt.Sprintf("%s", result.Data) produces the struct's
// Go representation instead of the document text.
func TestDownloadMedia_DocumentContentExtracted(t *testing.T) {
	t.Parallel()
	// Use a real xlsx so fileread's markitdown path produces real content.
	xlsxData, err := os.ReadFile("/tmp/test_inline.xlsx")
	if err != nil {
		t.Skipf("test xlsx not available: %v", err)
	}
	key := []byte("0123456789abcdef")
	ciphertext := encryptAesEcbForMediaTest(xlsxData, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	items := []Item{
		{Type: ItemFile, FileItem: &FileItem{
			FileName: "data.xlsx",
			Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
		}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != types.ContentTypeText {
		t.Fatalf("block.Type = %q, want text", block.Type)
	}
	// The document content line (after the header) must be clean markdown,
	// not a Go struct dump. fmt.Sprintf("%s", TextOutput{}) produces
	// "{text /path/to/file ## content...}" — the opening brace is the signature.
	lines := strings.SplitN(block.Text, "\n", 2)
	if len(lines) < 2 {
		t.Fatalf("Text = %q, expected at least 2 lines (header + content)", block.Text)
	}
	contentLine := lines[1]
	if strings.HasPrefix(strings.TrimSpace(contentLine), "{") {
		t.Errorf("content line is a Go struct dump, not parsed markdown:\n%s", contentLine)
	}
	if !strings.Contains(contentLine, "Name") || !strings.Contains(contentLine, "Value") {
		t.Errorf("content = %q, want parsed xlsx content (Name, Value)", contentLine)
	}
}

// TestDownloadMedia_AllDocumentFormats verifies that downloadFile correctly
// extracts TextOutput.Content for every document format markitdown supports.
// Each case generates a real file, encrypts it, serves it via httptest, and
// asserts the returned text block contains clean parsed content (not a struct
// dump or empty fallback).
func TestDownloadMedia_AllDocumentFormats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		file     string // path to pre-generated test file
		ext      string // extension for the fake filename
		contains string // substring expected in parsed content
	}{
		{"xlsx", "/tmp/fmt_test.xlsx", ".xlsx", "Name"},
		{"docx", "/tmp/fmt_test.docx", ".docx", "Hello"},
		{"pptx", "/tmp/fmt_test.pptx", ".pptx", "Test Slide"},
		{"epub", "/tmp/fmt_test.epub", ".epub", "Chapter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.file)
			if err != nil {
				t.Skipf("test file %s not available: %v", tc.file, err)
			}
			key := []byte("0123456789abcdef")
			ciphertext := encryptAesEcbForMediaTest(data, key)
			aesKeyB64 := base64.StdEncoding.EncodeToString(key)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(ciphertext)
			}))
			defer srv.Close()

			conn, _ := newMediaTestConnector(t)
			items := []Item{
				{Type: ItemFile, FileItem: &FileItem{
					FileName: "doc" + tc.ext,
					Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
				}},
			}
			block := conn.downloadMedia(context.Background(), items)
			if block.Type != types.ContentTypeText {
				t.Fatalf("block.Type = %q, want text", block.Type)
			}
			lines := strings.SplitN(block.Text, "\n", 2)
			if len(lines) < 2 {
				t.Fatalf("Text = %q, expected header + content", block.Text)
			}
			contentLine := lines[1]
			if strings.HasPrefix(strings.TrimSpace(contentLine), "{") {
				t.Errorf("%s: content is struct dump:\n%s", tc.name, contentLine)
			}
			if !strings.Contains(contentLine, tc.contains) {
				t.Errorf("%s: content = %q, want substring %q", tc.name, contentLine, tc.contains)
			}
		})
	}
}
func TestDownloadMedia_NoMedia(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	block := c.downloadMedia(context.Background(), nil)
	if block.Type != "" {
		t.Errorf("block.Type = %q, want empty (no media)", block.Type)
	}
}

func TestDownloadMedia_NilCache(t *testing.T) {
	t.Parallel()
	// mediaCache == nil must not panic and must return a zero block.
	c := &WeChatConnector{}
	block := c.downloadMedia(context.Background(), []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://x"}}},
	})
	if block.Type != "" {
		t.Errorf("block.Type = %q, want empty when cache is nil", block.Type)
	}
}

func TestDownloadMedia_DownloadFails(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	items := []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{
			FullURL: srv.URL, AesKey: aesKeyB64,
		}}},
	}
	// Graceful degrade: no panic, zero block (text-only fallback).
	block := c.downloadMedia(context.Background(), items)
	if block.Type != "" {
		t.Errorf("block.Type = %q, want empty on download failure", block.Type)
	}
}

func TestDownloadMedia_NonImageBytes(t *testing.T) {
	t.Parallel()
	// Decryptable bytes that are NOT a recognized image format → zero block.
	plaintext := []byte("plain text, not an image")
	key := []byte("0123456789abcdef")
	ciphertext := encryptAesEcbForMediaTest(plaintext, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	items := []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{
			FullURL: srv.URL, AesKey: aesKeyB64,
		}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != "" {
		t.Errorf("block.Type = %q, want empty for non-image bytes", block.Type)
	}
}

func TestDownloadMedia_PriorityImageOverFile(t *testing.T) {
	t.Parallel()
	// When both an image and a file are present, IMAGE wins (priority order).
	key := []byte("0123456789abcdef")
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	ciphertext := encryptAesEcbForMediaTest(pngHeader, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	items := []Item{
		{Type: ItemFile, FileItem: &FileItem{FileName: "x.pdf", Media: &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64}}},
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != types.ContentTypeImage {
		t.Errorf("block.Type = %q, want image (IMAGE > FILE priority)", block.Type)
	}
}

func TestPickMediaItem_PriorityOrder(t *testing.T) {
	t.Parallel()
	video := &MediaItemHolder{Media: &MediaRef{FullURL: "http://v"}}
	file := &FileItem{Media: &MediaRef{FullURL: "http://f"}}
	tests := []struct {
		name  string
		items []Item
		want  int // expected item.Type
	}{
		{"none", nil, 0},
		{"only file", []Item{{Type: ItemFile, FileItem: file}}, ItemFile},
		{"image beats file", []Item{{Type: ItemFile, FileItem: file}, {Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://i"}}}}, ItemImage},
		{"video beats file", []Item{{Type: ItemFile, FileItem: file}, {Type: ItemVideo, VideoItem: video}}, ItemVideo},
		{"image beats video", []Item{{Type: ItemVideo, VideoItem: video}, {Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://i"}}}}, ItemImage},
		{"voice without text picked", []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Media: &MediaRef{FullURL: "http://vc"}}}}, ItemVoice},
		{"voice with text NOT picked", []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Text: "transcript", Media: &MediaRef{FullURL: "http://vc"}}}}, 0},
		{"no url not downloadable", []Item{{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{}}}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pickMediaItem(tc.items)
			if tc.want == 0 {
				if got != nil {
					t.Errorf("pickMediaItem = type %d, want nil", got.Type)
				}
				return
			}
			if got == nil {
				t.Fatalf("pickMediaItem = nil, want type %d", tc.want)
			}
			if got.Type != tc.want {
				t.Errorf("pickMediaItem type = %d, want %d", got.Type, tc.want)
			}
		})
	}
}

func TestProcessInbound_WithImage_EnqueuesContent(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	ciphertext := encryptAesEcbForMediaTest(pngHeader, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msg := Message{
		FromUserID: "user1",
		MessageID:  FlexString("msg-1"),
		ItemList: []Item{
			{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64}}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		// Image block + default prompt.
		if len(im.content) != 2 {
			t.Fatalf("enqueued content length = %d, want 2 (image + prompt)", len(im.content))
		}
		if im.content[0].Type != types.ContentTypeImage {
			t.Errorf("content[0].Type = %q, want image", im.content[0].Type)
		}
		if im.content[0].Source == nil || im.content[0].Source.MediaType != "image/png" {
			t.Errorf("content[0] source = %+v, want image/png", im.content[0].Source)
		}
		if im.content[1].Type != types.ContentTypeText || !strings.Contains(im.content[1].Text, "attachment") {
			t.Errorf("content[1] = %+v, want default prompt", im.content[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processInbound did not enqueue within 2s")
	}
}

func TestProcessInbound_ImageWithCaption_CaptionEnqueuedSeparately(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	ciphertext := encryptAesEcbForMediaTest(pngHeader, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	// WeChat doesn't support caption, but if a text item accompanies an image
	// in the same message, processBatch treats the image as media (merged with
	// default prompt) and the text is dropped — only media OR text per message.
	msg := Message{
		FromUserID: "user1",
		MessageID:  FlexString("msg-2"),
		ItemList: []Item{
			{Type: ItemText, TextItem: &TextItem{Text: "look at this"}},
			{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64}}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		// Media wins — text item is ignored, image + default prompt enqueued.
		if im.content[0].Type != types.ContentTypeImage {
			t.Errorf("content[0].Type = %q, want image (media takes priority)", im.content[0].Type)
		}
		if !strings.Contains(im.text, "attachment") {
			t.Errorf("im.text = %q, want default prompt", im.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processInbound did not enqueue within 2s")
	}
}

func TestProcessInbound_TextOnly_NoContent(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msg := Message{
		FromUserID: "user1",
		MessageID:  FlexString("msg-3"),
		ItemList:   []Item{{Type: ItemText, TextItem: &TextItem{Text: "just text"}}},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		if len(im.content) != 0 {
			t.Errorf("text-only message should have nil content, got %d blocks", len(im.content))
		}
		if im.text != "just text" {
			t.Errorf("im.text = %q, want 'just text'", im.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processInbound did not enqueue within 2s")
	}
}

func TestProcessInbound_EmptyDropped(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	// No text, no media → dropped, nothing enqueued.
	msg := Message{
		FromUserID: "user1",
		MessageID:  FlexString("msg-4"),
		ItemList:   []Item{{Type: ItemText, TextItem: &TextItem{Text: ""}}},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		t.Errorf("empty message should be dropped, got %+v", im)
	case <-time.After(100 * time.Millisecond):
		// pass: nothing was enqueued.
	}
}

// TestProcessInbound_MediaNoCaption_AddsDefaultPrompt verifies that when a
// document arrives without a caption, a default prompt is added as a second
// content block so the LLM has an instruction to act on. Without the fix the
// default caption was assigned AFTER content was built, so it never made it
// into the content blocks sent to QueryWithContent.
func TestProcessInbound_MediaNoCaption_AddsDefaultPrompt(t *testing.T) {
	t.Parallel()
	// Use a real xlsx so fileread produces non-empty TextOutput.
	xlsxData, err := os.ReadFile("/tmp/test_inline.xlsx")
	if err != nil {
		t.Skipf("test xlsx not available: %v", err)
	}
	key := []byte("0123456789abcdef")
	ciphertext := encryptAesEcbForMediaTest(xlsxData, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msg := Message{
		FromUserID: "user1",
		MessageID:  FlexString("msg-prompt"),
		ItemList: []Item{
			{Type: ItemFile, FileItem: &FileItem{
				FileName: "data.xlsx",
				Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
			}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		// Must have 2 blocks: [document text block, default prompt text block].
		if len(im.content) != 2 {
			t.Fatalf("content length = %d, want 2 (document + default prompt)", len(im.content))
		}
		if im.content[0].Type != types.ContentTypeText {
			t.Errorf("content[0].Type = %q, want text (document)", im.content[0].Type)
		}
		if im.content[1].Type != types.ContentTypeText {
			t.Errorf("content[1].Type = %q, want text (prompt)", im.content[1].Type)
		}
		if !strings.Contains(im.content[1].Text, "attachment") {
			t.Errorf("content[1].Text = %q, want default prompt containing 'attachment'", im.content[1].Text)
		}
		// im.text should also be set for TUI display.
		if im.text == "" {
			t.Error("im.text is empty, should carry the default prompt for TUI display")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processInbound did not enqueue within 2s")
	}
}

func TestHandleInbound_WithContentCallsQueryWithContent(t *testing.T) {
	// Not parallel: uses a spy that records the dispatched content.
	var captured []types.ContentBlock
	called := false
	h := hub.NewHub()
	c := &WeChatConnector{
		hub:       h,
		inboundCh: make(chan inboundMessage, 10),
		queryFn:   func(context.Context, string, string) { t.Error("queryFn should not be called when content is present") },
	}
	c.queryWithContentFn = func(_ context.Context, content []types.ContentBlock, _ string) {
		called = true
		captured = content
		// Close queryDone so handleInbound unblocks.
		if c.queryDone != nil {
			close(c.queryDone)
			c.queryDone = nil
		}
	}

	imgBlock := types.NewFileImageBlock("image/png", "/tmp/x.png")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.handleInbound(ctx, inboundMessage{
		userID:  "u1",
		text:    "caption",
		content: []types.ContentBlock{imgBlock},
	})

	if !called {
		t.Fatal("queryWithContentFn was not called")
	}
	if len(captured) != 1 || captured[0].Type != types.ContentTypeImage {
		t.Errorf("captured content = %+v, want one image block", captured)
	}
}

func TestHandleInbound_ContentImagePlaceholderInDispatch(t *testing.T) {
	// The EventConnectorUserMessage dispatched for an image-only message must
	// carry an [image] placeholder so the TUI does not render blank.
	spy := &hubSpy{}
	h := hub.NewHub()
	h.Subscribe(spy)
	c := &WeChatConnector{
		hub:       h,
		inboundCh: make(chan inboundMessage, 10),
		queryFn:   func(context.Context, string, string) {},
	}
	c.queryWithContentFn = func(_ context.Context, _ []types.ContentBlock, _ string) {
		if c.queryDone != nil {
			close(c.queryDone)
			c.queryDone = nil
		}
	}

	imgBlock := types.NewFileImageBlock("image/png", "/tmp/x.png")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.handleInbound(ctx, inboundMessage{
		userID:  "u1",
		text:    "",
		content: []types.ContentBlock{imgBlock},
	})

	found := false
	spy.mu.Lock()
	for _, evt := range spy.events {
		if evt.Type == types.EventConnectorUserMessage && evt.Message != nil {
			for _, cb := range evt.Message.Content {
				if cb.Type == types.ContentTypeText && strings.Contains(cb.Text, "[image]") {
					found = true
				}
			}
		}
	}
	spy.mu.Unlock()
	if !found {
		t.Error("EventConnectorUserMessage did not carry an [image] placeholder")
	}
}

// TestProcessBatch_MultipleMediaMerged verifies that multiple media messages
// arriving in one GetUpdates batch are merged into a single inboundMessage
// with all content blocks combined — not enqueued as separate messages.
func TestProcessBatch_MultipleMediaMerged(t *testing.T) {
	t.Parallel()

	xlsxData, err := os.ReadFile("/tmp/test_inline.xlsx")
	if err != nil {
		t.Skipf("test xlsx not available: %v", err)
	}
	key := []byte("0123456789abcdef")
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	// Two xlsx files served by the same server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAesEcbForMediaTest(xlsxData, key))
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msgs := []Message{
		{
			FromUserID: "user1",
			MessageID:  FlexString("msg-a"),
			ItemList: []Item{
				{Type: ItemFile, FileItem: &FileItem{
					FileName: "doc1.xlsx",
					Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
				}},
			},
		},
		{
			FromUserID: "user1",
			MessageID:  FlexString("msg-b"),
			ItemList: []Item{
				{Type: ItemFile, FileItem: &FileItem{
					FileName: "doc2.xlsx",
					Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
				}},
			},
		},
	}
	c.processBatch(context.Background(), msgs)

	select {
	case im := <-c.inboundCh:
		// Both documents should be in a single inboundMessage.
		docCount := 0
		for _, cb := range im.content {
			if cb.Type == types.ContentTypeText && strings.Contains(cb.Text, "[Document:") {
				docCount++
			}
		}
		if docCount != 2 {
			t.Errorf("merged content has %d document blocks, want 2", docCount)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processBatch did not enqueue within 2s")
	}
}

// display text for a document with a caption does not repeat the caption.
// content has [document block, caption block] — displayText starts with
// msg.text (caption), then the loop must NOT append the caption block's
// first line again.
func TestHandleInbound_DocumentDisplayTextNoDuplicate(t *testing.T) {
	spy := &hubSpy{}
	h := hub.NewHub()
	h.Subscribe(spy)
	c := &WeChatConnector{
		hub:       h,
		inboundCh: make(chan inboundMessage, 10),
		queryFn:   func(context.Context, string, string) {},
	}
	c.queryWithContentFn = func(_ context.Context, _ []types.ContentBlock, _ string) {
		if c.queryDone != nil {
			close(c.queryDone)
			c.queryDone = nil
		}
	}

	caption := "Summarize this document."
	docBlock := types.NewTextBlock("[Document: report.pdf saved at /tmp/report.pdf]\nPDF content here")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.handleInbound(ctx, inboundMessage{
		userID:  "u1",
		text:    caption,
		content: []types.ContentBlock{docBlock, types.NewTextBlock(caption)},
	})

	var displayText string
	spy.mu.Lock()
	for _, evt := range spy.events {
		if evt.Type == types.EventConnectorUserMessage && evt.Message != nil {
			for _, cb := range evt.Message.Content {
				if cb.Type == types.ContentTypeText {
					displayText = cb.Text
				}
			}
		}
	}
	spy.mu.Unlock()

	// Document header must be present with extracted name.
	if !strings.Contains(displayText, "[Document:") {
		t.Errorf("displayText %q missing '[Document:' header", displayText)
	}
	if !strings.Contains(displayText, "report.pdf") {
		t.Errorf("displayText %q missing filename 'report.pdf'", displayText)
	}
}

// TestProcessBatch_VoiceWithTranscription_EnqueuedAsText verifies that a voice
// message with transcription text is enqueued as a text message, not silently
// dropped by the media-download path (which cannot handle SILK audio).
func TestProcessBatch_VoiceWithTranscription_EnqueuedAsText(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msgs := []Message{
		{
			FromUserID: "user1",
			MessageID:  FlexString("msg-voice-1"),
			ItemList: []Item{
				{Type: ItemVoice, VoiceItem: &VoiceItem{
					Text:  "hello from voice",
					Media: &MediaRef{FullURL: "http://example.com/voice.silk"},
				}},
			},
		},
	}
	c.processBatch(context.Background(), msgs)

	select {
	case im := <-c.inboundCh:
		if im.text != "[voice transcription] hello from voice" {
			t.Errorf("im.text = %q, want %q", im.text, "[voice transcription] hello from voice")
		}
		if len(im.content) != 0 {
			t.Errorf("len(im.content) = %d, want 0 (voice transcription is text-only)", len(im.content))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("voice with transcription was not enqueued within 2s")
	}
}

// TestProcessBatch_MultiUserMedia_NotMergedAcrossUsers verifies that media
// from different users in one batch is NOT merged into a single message
// addressed to only the first user — that would route A's reply to B.
func TestProcessBatch_MultiUserMedia_NotMergedAcrossUsers(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	ciphertext := encryptAesEcbForMediaTest(pngHeader, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msgs := []Message{
		{
			FromUserID: "userA@im",
			MessageID:  FlexString("msg-a"),
			ItemList: []Item{
				{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64}}},
			},
		},
		{
			FromUserID: "userB@im",
			MessageID:  FlexString("msg-b"),
			ItemList: []Item{
				{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64}}},
			},
		},
	}
	c.processBatch(context.Background(), msgs)

	var received []inboundMessage
	for range 2 {
		select {
		case im := <-c.inboundCh:
			received = append(received, im)
		case <-time.After(2 * time.Second):
			t.Fatalf("only received %d messages, want 2", len(received))
		}
	}

	if len(received) != 2 {
		t.Fatalf("got %d messages, want 2 (one per user)", len(received))
	}
	for _, im := range received {
		if im.userID != "userA@im" && im.userID != "userB@im" {
			t.Errorf("userID = %q, want userA@im or userB@im", im.userID)
		}
	}
	seenA, seenB := false, false
	for _, im := range received {
		if im.userID == "userA@im" {
			seenA = true
		}
		if im.userID == "userB@im" {
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Errorf("expected messages for both users, got A=%v B=%v", seenA, seenB)
	}
}

func TestDownloadMedia_VideoSkipped(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	items := []Item{
		{Type: ItemVideo, VideoItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://x"}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != "" {
		t.Errorf("video download should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadMedia_VoiceWithoutTranscriptionSkipped(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	items := []Item{
		{Type: ItemVoice, VoiceItem: &VoiceItem{Media: &MediaRef{FullURL: "http://x"}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != "" {
		t.Errorf("voice download should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadMedia_NilMediaCache(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.mediaCache = nil
	items := []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://x"}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != "" {
		t.Errorf("nil cache should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadMedia_ImageDownloadFail(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	items := []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://127.0.0.1:0/nonexistent"}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != "" {
		t.Errorf("failed download should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadMedia_NonImageFormat(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not an image"))
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	items := []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL}}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != "" {
		t.Errorf("non-image data should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadMedia_FileParseFail_ReturnsPathHeader(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAesEcbForMediaTest([]byte("binary\x00data"), key))
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	items := []Item{
		{Type: ItemFile, FileItem: &FileItem{
			FileName: "unknown.bin",
			Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
		}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != types.ContentTypeText {
		t.Errorf("file parse fail should return text block, got type=%s", block.Type)
	}
	if !strings.Contains(block.Text, "[Document:") {
		t.Errorf("block.Text = %q, want '[Document:' header", block.Text)
	}
}

func TestDownloadMedia_FileEmptyContent_ReturnsPathHeader(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(""))
	}))
	defer srv.Close()

	key := []byte("0123456789abcdef")
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAesEcbForMediaTest([]byte(""), key))
	}))
	defer srv2.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	items := []Item{
		{Type: ItemFile, FileItem: &FileItem{
			FileName: "empty.txt",
			Media:    &MediaRef{FullURL: srv2.URL, AesKey: aesKeyB64},
		}},
	}
	block := c.downloadMedia(context.Background(), items)
	if block.Type != types.ContentTypeText {
		t.Errorf("empty file should return text block, got type=%s", block.Type)
	}
	if !strings.Contains(block.Text, "[Document:") {
		t.Errorf("block.Text = %q, want '[Document:' header", block.Text)
	}
}

func TestFetchMediaBytes_NoAesKey_DownloadsPlain(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain data"))
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	data, err := c.fetchMediaBytes(context.Background(), &MediaRef{FullURL: srv.URL})
	if err != nil {
		t.Fatalf("fetchMediaBytes: %v", err)
	}
	if string(data) != "plain data" {
		t.Errorf("data = %q, want 'plain data'", string(data))
	}
}

func TestFetchMediaBytes_PlainDownloadFail(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	_, err := c.fetchMediaBytes(context.Background(), &MediaRef{FullURL: "http://127.0.0.1:0/none"})
	if err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("fetchMediaBytes error = %v, want 'connect'", err)
	}
}
