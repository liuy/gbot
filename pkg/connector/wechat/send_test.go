package wechat

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool"
)

// recordedItem captures one sendItemFn call.
type recordedItem struct {
	item Item
}

// newSendTestConnector builds a WeChatConnector with stubbed upload + send seams.
func newSendTestConnector(t *testing.T) (*WeChatConnector, *UploadedFileInfo, *[]recordedItem) {
	t.Helper()
	uploaded := &UploadedFileInfo{
		FileKey:                     "filekey123",
		DownloadEncryptedQueryParam: "download-param-abc",
		AesKey:                      "00112233445566778899aabbccddeeff",
		FileSize:                    500,
		FileSizeCiphertext:          512,
	}
	var sent []recordedItem
	c := &WeChatConnector{
		client:       &http.Client{},
		state:        &State{AccountID: "bot-account", BaseURL: "https://api.example.com", Token: "tok"},
		activeUserID: "user-active",
		uploadFn: func(ctx context.Context, client *http.Client, baseURL, token, toUserID,
			filePath string, mediaType int) (*UploadedFileInfo, error) {
			return uploaded, nil
		},
		sendItemFn: func(ctx context.Context, client *http.Client, baseURL, token,
			fromUser, toUser string, item Item, contextToken, clientID string) error {
			sent = append(sent, recordedItem{item: item})
			return nil
		},
	}
	return c, uploaded, &sent
}

func TestSendFile_Image(t *testing.T) {
	t.Parallel()
	c, uploaded, sent := newSendTestConnector(t)
	err := c.SendFile(context.Background(), "/tmp/photo.png", "")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sendItemFn calls = %d, want 1", len(*sent))
	}
	item := (*sent)[0].item
	if item.Type != ItemImage {
		t.Errorf("item.Type = %d, want %d (ItemImage)", item.Type, ItemImage)
	}
	if item.ImageItem == nil {
		t.Fatal("ImageItem is nil")
	}
	if item.ImageItem.MidSize != uploaded.FileSizeCiphertext {
		t.Errorf("MidSize = %d, want %d", item.ImageItem.MidSize, uploaded.FileSizeCiphertext)
	}
	if item.ImageItem.Media == nil {
		t.Fatal("Media is nil")
	}
	if item.ImageItem.Media.EncryptQueryParam != uploaded.DownloadEncryptedQueryParam {
		t.Errorf("EncryptQueryParam = %q, want %q",
			item.ImageItem.Media.EncryptQueryParam, uploaded.DownloadEncryptedQueryParam)
	}
	if item.ImageItem.Media.EncryptType != 1 {
		t.Errorf("EncryptType = %d, want 1", item.ImageItem.Media.EncryptType)
	}
	// aes_key is base64 of the UTF-8 bytes of the hex string (NOT hex-decoded).
	wantAes := base64.StdEncoding.EncodeToString([]byte(uploaded.AesKey))
	if item.ImageItem.Media.AesKey != wantAes {
		t.Errorf("AesKey = %q, want %q (base64 of hex string UTF-8 bytes)",
			item.ImageItem.Media.AesKey, wantAes)
	}
}

func TestSendFile_Video(t *testing.T) {
	t.Parallel()
	c, uploaded, sent := newSendTestConnector(t)
	err := c.SendFile(context.Background(), "/tmp/clip.mp4", "")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sendItemFn calls = %d, want 1", len(*sent))
	}
	item := (*sent)[0].item
	if item.Type != ItemVideo {
		t.Errorf("item.Type = %d, want %d (ItemVideo)", item.Type, ItemVideo)
	}
	if item.VideoItem == nil {
		t.Fatal("VideoItem is nil")
	}
	if item.VideoItem.VideoSize != uploaded.FileSizeCiphertext {
		t.Errorf("VideoSize = %d, want %d", item.VideoItem.VideoSize, uploaded.FileSizeCiphertext)
	}
	if item.VideoItem.Media == nil || item.VideoItem.Media.EncryptQueryParam == "" {
		t.Error("VideoItem Media/EncryptQueryParam missing")
	}
}

func TestSendFile_File(t *testing.T) {
	t.Parallel()
	c, _, sent := newSendTestConnector(t)
	err := c.SendFile(context.Background(), "/tmp/report.pdf", "")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sendItemFn calls = %d, want 1", len(*sent))
	}
	item := (*sent)[0].item
	if item.Type != ItemFile {
		t.Errorf("item.Type = %d, want %d (ItemFile)", item.Type, ItemFile)
	}
	if item.FileItem == nil {
		t.Fatal("FileItem is nil")
	}
	if item.FileItem.FileName != "report.pdf" {
		t.Errorf("FileName = %q, want 'report.pdf'", item.FileItem.FileName)
	}
	// Len is the plaintext size (uploaded.FileSize=500), as a string, not ciphertext.
	if item.FileItem.Len != "500" {
		t.Errorf("Len = %q, want '500' (plaintext size as string)", item.FileItem.Len)
	}
}

func TestSendFile_CaptionIgnored_SingleCall(t *testing.T) {
	t.Parallel()
	c, _, sent := newSendTestConnector(t)
	err := c.SendFile(context.Background(), "/tmp/photo.png", "check this out")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	// Caption is ignored — only the media item is sent.
	if len(*sent) != 1 {
		t.Fatalf("sendItemFn calls = %d, want 1 (caption ignored, media only)", len(*sent))
	}
	if (*sent)[0].item.Type != ItemImage {
		t.Errorf("item.Type = %d, want ItemImage", (*sent)[0].item.Type)
	}
}

func TestSendFile_NoActiveUser(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		client:       &http.Client{},
		state:        &State{AccountID: "bot"},
		activeUserID: "", // no active user
	}
	err := c.SendFile(context.Background(), "/tmp/photo.png", "")
	if err == nil {
		t.Fatal("expected error for no active user, got nil")
	}
	if !strings.Contains(err.Error(), "no active user") {
		t.Errorf("error = %q, want 'no active user'", err.Error())
	}
}

func TestSendFile_UploadError(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		client:       &http.Client{},
		state:        &State{AccountID: "bot"},
		activeUserID: "user",
		uploadFn: func(ctx context.Context, client *http.Client, baseURL, token, toUserID,
			filePath string, mediaType int) (*UploadedFileInfo, error) {
			return nil, errors.New("cdn unreachable")
		},
		sendItemFn: func(ctx context.Context, client *http.Client, baseURL, token,
			fromUser, toUser string, item Item, contextToken, clientID string) error {
			t.Error("sendItemFn should not be called when upload fails")
			return nil
		},
	}
	err := c.SendFile(context.Background(), "/tmp/photo.png", "")
	if err == nil {
		t.Fatal("expected upload error to propagate, got nil")
	}
	if !strings.Contains(err.Error(), "upload") {
		t.Errorf("error = %q, want 'upload'", err.Error())
	}
}

func TestSendFile_MediaSendError(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		client:       &http.Client{},
		state:        &State{AccountID: "bot"},
		activeUserID: "user",
		uploadFn: func(ctx context.Context, client *http.Client, baseURL, token, toUserID,
			filePath string, mediaType int) (*UploadedFileInfo, error) {
			return &UploadedFileInfo{AesKey: "abcdef", DownloadEncryptedQueryParam: "dl", FileSize: 100, FileSizeCiphertext: 112}, nil
		},
		sendItemFn: func(ctx context.Context, client *http.Client, baseURL, token,
			fromUser, toUser string, item Item, contextToken, clientID string) error {
			return errors.New("media send failed")
		},
	}
	err := c.SendFile(context.Background(), "/tmp/doc.pdf", "here is the report")
	if err == nil || !strings.Contains(err.Error(), "media") {
		t.Fatalf("error = %v, want 'media'", err)
	}
}

func TestMediaTypeFromExt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ext  string
		want int
	}{
		{".png", MediaImage},
		{".jpg", MediaImage},
		{".jpeg", MediaImage},
		{".gif", MediaImage},
		{".webp", MediaImage},
		{".bmp", MediaImage},
		{".mp4", MediaVideo},
		{".mov", MediaVideo},
		{".avi", MediaVideo},
		{".mkv", MediaVideo},
		{".webm", MediaVideo},
		{".pdf", MediaFile},
		{".docx", MediaFile},
		{".zip", MediaFile},
		{".txt", MediaFile},
		{"", MediaFile},     // unknown → file
		{".xyz", MediaFile}, // unknown → file
	}
	for _, tc := range cases {
		if got := mediaTypeFromExt(tc.ext); got != tc.want {
			t.Errorf("mediaTypeFromExt(%q) = %d, want %d", tc.ext, got, tc.want)
		}
	}
}

func TestRegisterSendTool(t *testing.T) {
	t.Parallel()
	c, _, _ := newSendTestConnector(t)
	eng := engine.New(&engine.Params{Model: "test"})
	t.Cleanup(eng.Close)
	reg := tool.NewRegistry()
	c.RegisterSendTool(eng, reg)

	got, ok := reg.Lookup("Send")
	if !ok {
		t.Fatal("Send tool not registered")
	}
	if got.Name() != "Send" {
		t.Errorf("registered tool Name = %q, want 'Send'", got.Name())
	}

	// The "wechat" FileSender must be bound to the engine so the Send tool
	// (bound to eng) routes to the connector. newSendTestConnector stubs
	// upload+sendItem to succeed, so a nil error proves the connector's
	// SendFile was reached — a routing failure would surface as
	// "no FileSender registered for source".
	if err := eng.SendFile(engine.WithSource(context.Background(), "wechat"), "/nonexistent.png", ""); err != nil {
		t.Errorf("SendFile via engine routing = %v, want nil (sender must be registered)", err)
	}

	// Idempotent: calling again must not panic and the tool must remain.
	c.RegisterSendTool(eng, reg)
	if _, ok := reg.Lookup("Send"); !ok {
		t.Error("Send tool missing after second RegisterSendTool call")
	}
}
