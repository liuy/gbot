package wechat

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/media"
)

func TestBuildCdnUploadURL(t *testing.T) {
	t.Parallel()
	got := buildCdnUploadURL("https://cdn.example.com/c2c", "param with spaces", "key/with/slash")
	want := "https://cdn.example.com/c2c/upload?encrypted_query_param=param+with+spaces&filekey=key%2Fwith%2Fslash"
	if got != want {
		t.Errorf("buildCdnUploadURL = %q, want %q", got, want)
	}
}

func TestGetUploadURL(t *testing.T) {
	t.Parallel()
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upload_full_url":"https://cdn.example.com/upload","upload_param":"param123"}`))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := GetUploadURL(context.Background(), client, srv.URL, "tok",
		&GetUploadURLRequest{FileKey: "fk", MediaType: MediaImage, ToUserID: "u1",
			RawSize: 100, FileSize: 112, NoNeedThumb: true, AesKey: "deadbeef"})
	if err != nil {
		t.Fatalf("GetUploadURL: %v", err)
	}
	if resp.UploadFullURL != "https://cdn.example.com/upload" {
		t.Errorf("UploadFullURL = %q", resp.UploadFullURL)
	}
	if resp.UploadParam != "param123" {
		t.Errorf("UploadParam = %q", resp.UploadParam)
	}
	if receivedBody["filekey"] != "fk" {
		t.Errorf("request filekey = %v, want fk", receivedBody["filekey"])
	}
	if receivedBody["no_need_thumb"] != true {
		t.Errorf("request no_need_thumb = %v, want true", receivedBody["no_need_thumb"])
	}
	if receivedBody["base_info"] == nil {
		t.Error("request missing base_info")
	}
}

func TestUploadToCDN_Success(t *testing.T) {
	t.Parallel()
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("x-encrypted-param", "download-param-xyz")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	key := []byte("0123456789abcdef")
	plaintext := []byte("secret payload")
	ciphertext, err := media.EncryptAesEcb(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb: %v", err)
	}

	dp, err := uploadToCDN(context.Background(), client, srv.URL, ciphertext)
	if err != nil {
		t.Fatalf("uploadToCDN: %v", err)
	}
	if dp != "download-param-xyz" {
		t.Errorf("downloadParam = %q, want download-param-xyz", dp)
	}
	// The CDN received the encrypted (padded) bytes, not the plaintext.
	if !bytes.Equal(receivedBody, ciphertext) {
		t.Errorf("CDN received %d bytes, want ciphertext %d bytes", len(receivedBody), len(ciphertext))
	}
	if len(receivedBody) == len(plaintext) {
		t.Error("CDN received unpadded plaintext — encryption/padding not applied")
	}
}

func TestUploadToCDN_ClientError_NoRetry(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("x-error-message", "bad request")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := uploadToCDN(context.Background(), client, srv.URL, []byte("data"))
	if err == nil {
		t.Fatal("expected client error, got nil")
	}
	if !strings.Contains(err.Error(), "client error") {
		t.Errorf("error = %q, want 'client error'", err.Error())
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, want '400'", err.Error())
	}
	if calls != 1 {
		t.Errorf("server called %d times, want 1 (no retry on client error)", calls)
	}
}

func TestUploadToCDN_ServerErrorThenSuccess(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("x-error-message", "internal error")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("x-encrypted-param", "dp-after-retry")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	dp, err := uploadToCDN(context.Background(), client, srv.URL, []byte("data"))
	if err != nil {
		t.Fatalf("uploadToCDN after retry: %v", err)
	}
	if dp != "dp-after-retry" {
		t.Errorf("downloadParam = %q, want dp-after-retry", dp)
	}
	if calls != 2 {
		t.Errorf("server called %d times, want 2 (retry then success)", calls)
	}
}

func TestUploadToCDN_AllAttemptsFail(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("x-error-message", "down")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := uploadToCDN(context.Background(), client, srv.URL, []byte("data"))
	if err == nil {
		t.Fatal("expected error after all retries fail, got nil")
	}
	if !strings.Contains(err.Error(), "server error") {
		t.Errorf("error = %q, want 'server error'", err.Error())
	}
	if calls != uploadMaxRetries {
		t.Errorf("server called %d times, want %d", calls, uploadMaxRetries)
	}
}

func TestUploadToCDN_MissingDownloadParam_Retries(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < uploadMaxRetries {
			// 200 but no x-encrypted-param header → retry.
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("x-encrypted-param", "final-param")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	dp, err := uploadToCDN(context.Background(), client, srv.URL, []byte("data"))
	if err != nil {
		t.Fatalf("uploadToCDN: %v", err)
	}
	if dp != "final-param" {
		t.Errorf("downloadParam = %q, want final-param", dp)
	}
	if calls != uploadMaxRetries {
		t.Errorf("server called %d times, want %d", calls, uploadMaxRetries)
	}
}

// TestUploadFile_EndToEnd exercises the full pipeline: read file → encrypt →
// getuploadurl → CDN POST. The getuploadurl server returns upload_full_url
// pointing at the CDN server, so both legs hit httptest.
func TestUploadFile_EndToEnd(t *testing.T) {
	t.Parallel()
	plaintext := []byte("the eagle lands at dawn over the ridge")
	var cdnReceived []byte

	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnReceived, _ = io.ReadAll(r.Body)
		w.Header().Set("x-encrypted-param", "download-param-e2e")
		w.WriteHeader(http.StatusOK)
	}))
	defer cdnSrv.Close()

	var getUploadBody map[string]any
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &getUploadBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upload_full_url":"` + cdnSrv.URL + `"}`))
	}))
	defer apiSrv.Close()

	// Write a temp file to upload.
	tmpDir := t.TempDir()
	filePath := tmpDir + "/test.bin"
	if err := os.WriteFile(filePath, plaintext, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	uploaded, err := UploadFile(context.Background(), client, apiSrv.URL, "tok", "user1", filePath, MediaImage)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	if uploaded.DownloadEncryptedQueryParam != "download-param-e2e" {
		t.Errorf("DownloadEncryptedQueryParam = %q", uploaded.DownloadEncryptedQueryParam)
	}
	if uploaded.FileSize != len(plaintext) {
		t.Errorf("FileSize = %d, want %d", uploaded.FileSize, len(plaintext))
	}
	if uploaded.FileSizeCiphertext != media.AesEcbPaddedSize(len(plaintext)) {
		t.Errorf("FileSizeCiphertext = %d, want %d",
			uploaded.FileSizeCiphertext, media.AesEcbPaddedSize(len(plaintext)))
	}
	if len(uploaded.FileKey) != 32 { // hex of 16 bytes
		t.Errorf("FileKey len = %d, want 32", len(uploaded.FileKey))
	}
	if len(uploaded.AesKey) != 32 { // hex of 16 bytes
		t.Errorf("AesKey len = %d, want 32", len(uploaded.AesKey))
	}

	// The CDN received encrypted (padded) bytes that decrypt back to plaintext.
	if len(cdnReceived) == len(plaintext) {
		t.Fatal("CDN received unpadded plaintext")
	}
	aesKeyBytes, err := hex.DecodeString(uploaded.AesKey)
	if err != nil {
		t.Fatalf("hex.DecodeString AesKey: %v", err)
	}
	decrypted, err := media.DecryptAesEcb(cdnReceived, aesKeyBytes)
	if err != nil {
		t.Fatalf("DecryptAesEcb: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted CDN bytes do not match original plaintext")
	}

	// Verify the getuploadurl request carried the expected fields.
	if getUploadBody["media_type"] != float64(MediaImage) {
		t.Errorf("media_type = %v, want %d", getUploadBody["media_type"], MediaImage)
	}
	if getUploadBody["to_user_id"] != "user1" {
		t.Errorf("to_user_id = %v, want user1", getUploadBody["to_user_id"])
	}
	if getUploadBody["no_need_thumb"] != true {
		t.Errorf("no_need_thumb = %v, want true", getUploadBody["no_need_thumb"])
	}
	if getUploadBody["rawsize"] != float64(len(plaintext)) {
		t.Errorf("rawsize = %v, want %d", getUploadBody["rawsize"], len(plaintext))
	}
}

func TestUploadFile_NoUploadURL(t *testing.T) {
	t.Parallel()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer apiSrv.Close()

	tmpDir := t.TempDir()
	filePath := tmpDir + "/test.bin"
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := UploadFile(context.Background(), client, apiSrv.URL, "tok", "user1", filePath, MediaImage)
	if err == nil {
		t.Fatal("expected error when getuploadurl returns no URL, got nil")
	}
	if !strings.Contains(err.Error(), "no upload URL") {
		t.Errorf("error = %q, want 'no upload URL'", err.Error())
	}
}

func TestUploadFile_FileNotFound(t *testing.T) {
	t.Parallel()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer apiSrv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := UploadFile(context.Background(), client, apiSrv.URL, "tok", "user1",
		"/nonexistent/path/file.bin", MediaImage)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "read file") {
		t.Errorf("error = %q, want 'read file'", err.Error())
	}
}
