package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
	if calls.Load() != 1 {
		t.Errorf("server called %d times, want 1 (no retry on client error)", calls.Load())
	}
}

func TestUploadToCDN_ServerErrorThenSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
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
	if calls.Load() != 2 {
		t.Errorf("server called %d times, want 2 (retry then success)", calls.Load())
	}
}

func TestUploadToCDN_AllAttemptsFail(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
	if calls.Load() != int32(uploadMaxRetries) {
		t.Errorf("server called %d times, want %d", calls.Load(), uploadMaxRetries)
	}
}

func TestUploadToCDN_MissingDownloadParam_Retries(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if calls.Load() < int32(uploadMaxRetries) {
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
	if calls.Load() != int32(uploadMaxRetries) {
		t.Errorf("server called %d times, want %d", calls.Load(), uploadMaxRetries)
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

// --- GetUploadURL error paths ---

func TestGetUploadURL_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := GetUploadURL(context.Background(), srv.Client(), srv.URL, "tok",
		&GetUploadURLRequest{FileKey: "fk"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("GetUploadURL API error: %v, want HTTP 500", err)
	}
}

func TestGetUploadURL_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{bad json`))
	}))
	defer srv.Close()

	_, err := GetUploadURL(context.Background(), srv.Client(), srv.URL, "tok",
		&GetUploadURLRequest{FileKey: "fk"})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("GetUploadURL decode error: %v, want 'decode'", err)
	}
}

// --- UploadFile — GetUploadURL failure ---

func TestUploadFile_GetUploadURL_Error(t *testing.T) {
	t.Parallel()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiSrv.Close()

	tmpDir := t.TempDir()
	filePath := tmpDir + "/test.bin"
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := UploadFile(context.Background(), client, apiSrv.URL, "tok", "user1", filePath, MediaImage)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("UploadFile GetUploadURL error: %v, want HTTP 500", err)
	}
}

// --- uploadToCDN — network error retry then fail ---

func TestUploadToCDN_NetworkError_RetriesThenFail(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Simulate connection reset by closing without response.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support Hijacker")
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := uploadToCDN(context.Background(), client, srv.URL, []byte("data"))
	if err == nil {
		t.Fatal("expected error after retries, got nil")
	}
	if !strings.Contains(err.Error(), "cdn upload") {
		t.Errorf("error = %q, want 'cdn upload'", err.Error())
	}
	if calls.Load() != int32(uploadMaxRetries) {
		t.Errorf("server called %d times, want %d", calls.Load(), uploadMaxRetries)
	}
}

// --- uploadToCDN — server error with no x-error-message header ---

func TestUploadToCDN_ServerError_NoErrorMessage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		// No x-error-message header — fallback to "status 502".
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := uploadToCDN(context.Background(), client, srv.URL, []byte("data"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "status 502") {
		t.Errorf("error = %q, want 'status 502'", err.Error())
	}
}

// --- uploadToCDN — all 200 but never x-encrypted-param ---

func TestUploadToCDN_AllAttempts_NoParam(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		// No x-encrypted-param header ever.
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := uploadToCDN(context.Background(), client, srv.URL, []byte("data"))
	if err == nil {
		t.Fatal("expected error when no param returned, got nil")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error = %q, want 'missing'", err.Error())
	}
	if calls.Load() != int32(uploadMaxRetries) {
		t.Errorf("server called %d times, want %d", calls.Load(), uploadMaxRetries)
	}
}

// --- UploadFile — uploadToCDN failure ---

func TestUploadFile_UploadToCDN_Failure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := tmpDir + "/test.bin"
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := UploadFile(context.Background(), client, srv.URL, "tok", "user1", filePath, MediaImage)
	if err == nil {
		t.Fatal("expected CDN upload error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want HTTP 500", err)
	}
}

// --- UploadFile — upload_param path (upload_full_url empty, upload_param set) ---

func TestUploadFile_UploadParamPath(t *testing.T) {
	t.Parallel()
	// CDN server that accepts the upload_param path.
	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-encrypted-param", "dp-from-param")
		w.WriteHeader(http.StatusOK)
	}))
	defer cdnSrv.Close()

	// API returns upload_full_url pointing to CDN server (empty upload_param).
	// This tests that the code correctly selects upload_full_url when both are set.
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upload_full_url":"` + cdnSrv.URL + `","upload_param":"unused"}`))
	}))
	defer apiSrv.Close()

	tmpDir := t.TempDir()
	filePath := tmpDir + "/test.bin"
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	uploaded, err := UploadFile(context.Background(), client, apiSrv.URL, "tok", "user1", filePath, MediaImage)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if uploaded.DownloadEncryptedQueryParam != "dp-from-param" {
		t.Errorf("DownloadEncryptedQueryParam = %q, want dp-from-param", uploaded.DownloadEncryptedQueryParam)
	}
}

// pinCDNServer records how many POSTs arrived and replies with a download
// param unless an override handler is supplied.
func pinCDNServer(t *testing.T, hits *atomic.Int32, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("x-encrypted-param", "dl-param")
		w.WriteHeader(http.StatusOK)
	}))
}

// pinAPIResponse serves a fixed getuploadurl JSON body.
func pinAPIResponse(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// UploadFile must send the full getuploadurl envelope: the filekey/aeskey it
// generated, the plaintext md5/size and the padded ciphertext size.
func TestPinUploadFile_RequestEnvelope(t *testing.T) {
	content := []byte("hello wechat upload envelope")
	dir := t.TempDir()
	path := dir + "/f.bin"
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	sum := md5.Sum(content)

	var hits atomic.Int32
	cdn := pinCDNServer(t, &hits, nil)
	defer cdn.Close()

	var body map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upload_full_url":"` + cdn.URL + `"}`))
	}))
	defer api.Close()

	info, err := UploadFile(context.Background(), &http.Client{Timeout: 5 * time.Second},
		api.URL, "tok", "user-1", path, MediaFile)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	for _, key := range []string{"filekey", "media_type", "to_user_id", "rawsize",
		"rawfilemd5", "filesize", "no_need_thumb", "aeskey", "base_info"} {
		if _, ok := body[key]; !ok {
			t.Errorf("getuploadurl request missing %q: %v", key, body)
		}
	}
	if body["media_type"] != float64(MediaFile) {
		t.Errorf("media_type = %v, want %d", body["media_type"], MediaFile)
	}
	if body["to_user_id"] != "user-1" {
		t.Errorf("to_user_id = %v, want user-1", body["to_user_id"])
	}
	if body["rawsize"] != float64(len(content)) {
		t.Errorf("rawsize = %v, want %d", body["rawsize"], len(content))
	}
	if body["rawfilemd5"] != hex.EncodeToString(sum[:]) {
		t.Errorf("rawfilemd5 = %v, want %s", body["rawfilemd5"], hex.EncodeToString(sum[:]))
	}
	if body["filesize"] != float64(media.AesEcbPaddedSize(len(content))) {
		t.Errorf("filesize = %v, want %d", body["filesize"], media.AesEcbPaddedSize(len(content)))
	}
	if body["no_need_thumb"] != true {
		t.Errorf("no_need_thumb = %v, want true", body["no_need_thumb"])
	}
	if body["filekey"] != info.FileKey || body["aeskey"] != info.AesKey {
		t.Errorf("returned keys (%q,%q) differ from the request keys (%v,%v)",
			info.FileKey, info.AesKey, body["filekey"], body["aeskey"])
	}
	if len(info.FileKey) != 32 || len(info.AesKey) != 32 {
		t.Errorf("filekey/aeskey lengths = %d/%d, want 32 hex chars each", len(info.FileKey), len(info.AesKey))
	}
	if info.FileSize != len(content) || info.FileSizeCiphertext != media.AesEcbPaddedSize(len(content)) {
		t.Errorf("sizes = %d/%d, want %d/%d", info.FileSize, info.FileSizeCiphertext,
			len(content), media.AesEcbPaddedSize(len(content)))
	}
	if info.DownloadEncryptedQueryParam != "dl-param" {
		t.Errorf("DownloadEncryptedQueryParam = %q, want dl-param", info.DownloadEncryptedQueryParam)
	}
}

// upload_full_url alone is a complete answer: it must be used verbatim rather
// than rebuilt from an empty upload_param.
func TestPinUploadFile_PrefersFullURL(t *testing.T) {
	content := []byte("x")
	dir := t.TempDir()
	path := dir + "/f.bin"
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	var hits atomic.Int32
	var gotPath string
	cdn := pinCDNServer(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("x-encrypted-param", "dl-direct")
		w.WriteHeader(http.StatusOK)
	})
	defer cdn.Close()
	api := pinAPIResponse(`{"upload_full_url":"` + cdn.URL + `/direct"}`)
	defer api.Close()

	info, err := UploadFile(context.Background(), &http.Client{Timeout: 5 * time.Second},
		api.URL, "tok", "u1", path, MediaImage)
	if err != nil {
		t.Fatalf("UploadFile with only upload_full_url: %v", err)
	}
	if info.DownloadEncryptedQueryParam != "dl-direct" {
		t.Errorf("DownloadEncryptedQueryParam = %q, want dl-direct", info.DownloadEncryptedQueryParam)
	}
	if gotPath != "/direct" {
		t.Errorf("CDN request path = %q, want /direct (the URL must be used verbatim)", gotPath)
	}
}

// upload_param alone must fall through to the built CDN URL; the failure that
// follows in a sandbox comes from the CDN host, never from request
// construction (which is what a dropped else-branch produces).
func TestPinUploadFile_UploadParamBuildsCDNURL(t *testing.T) {
	content := []byte("x")
	dir := t.TempDir()
	path := dir + "/f.bin"
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	var hits atomic.Int32
	cdn := pinCDNServer(t, &hits, nil)
	defer cdn.Close()
	api := pinAPIResponse(`{"upload_param":"param-only"}`)
	defer api.Close()

	_, err := UploadFile(context.Background(), &http.Client{Timeout: 5 * time.Second},
		api.URL, "tok", "u1", path, MediaImage)
	if err == nil {
		t.Fatal("UploadFile with only upload_param unexpectedly succeeded against the real CDN")
	}
	if strings.Contains(err.Error(), "no upload URL") {
		t.Errorf("UploadFile error = %q, want the upload_param path to build a CDN URL, not the no-URL failure", err)
	}
	if strings.Contains(err.Error(), "create request") {
		t.Errorf("UploadFile error = %q, want a CDN request to the built URL, not a request-construction failure", err)
	}
	// An empty cdnURL surfaces as `Post ""`; the built URL must be non-empty
	// whatever the CDN answers.
	if strings.Contains(err.Error(), `Post ""`) {
		t.Errorf("UploadFile error = %q, want the built CDN URL to be used", err)
	}
}

// Neither URL field means the upload cannot proceed.
func TestPinUploadFile_NoUploadURL(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/f.bin"
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	var hits atomic.Int32
	cdn := pinCDNServer(t, &hits, nil)
	defer cdn.Close()
	api := pinAPIResponse(`{}`)
	defer api.Close()

	info, err := UploadFile(context.Background(), &http.Client{Timeout: 5 * time.Second},
		api.URL, "tok", "u1", path, MediaImage)
	if err == nil {
		t.Fatalf("UploadFile with no URL fields = %+v, want an error", info)
	}
	if !strings.Contains(err.Error(), "no upload URL") {
		t.Errorf("UploadFile error = %q, want the no-upload-URL message", err)
	}
	if info != nil {
		t.Errorf("UploadFile result = %+v, want nil alongside the error", info)
	}
	if hits.Load() != 0 {
		t.Errorf("CDN hits = %d, want 0 when no upload URL is available", hits.Load())
	}
}

// uploadToCDN must retry exactly uploadMaxRetries times on server errors.
func TestPinUploadToCDN_RetryCount(t *testing.T) {
	var hits atomic.Int32
	srv := pinCDNServer(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-error-message", "boom")
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	_, err := uploadToCDN(context.Background(), &http.Client{Timeout: 5 * time.Second}, srv.URL, []byte("ct"))
	if err == nil {
		t.Fatal("uploadToCDN with a 500 server = nil error, want failure")
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("CDN attempts = %d, want 3 (uploadMaxRetries)", got)
	}
	if !strings.Contains(err.Error(), "CDN upload server error: boom") {
		t.Errorf("uploadToCDN error = %q, want the server-error message with the header text", err)
	}
}

// The client-error fast exit covers exactly [400, 500): 399 and 500 are
// server-side and must be retried.
func TestPinUploadToCDN_StatusBoundaries(t *testing.T) {
	cases := []struct {
		status      int
		wantHits    int32
		wantMessage string
	}{
		{399, 3, "CDN upload server error"},
		{400, 1, "CDN upload client error 400"},
		{499, 1, "CDN upload client error 499"},
		{500, 3, "CDN upload server error"},
	}
	for _, tc := range cases {
		var hits atomic.Int32
		srv := pinCDNServer(t, &hits, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		})
		_, err := uploadToCDN(context.Background(), &http.Client{Timeout: 5 * time.Second}, srv.URL, []byte("ct"))
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: uploadToCDN = nil error, want failure", tc.status)
		}
		if hits.Load() != tc.wantHits {
			t.Errorf("status %d: attempts = %d, want %d", tc.status, hits.Load(), tc.wantHits)
		}
		if !strings.Contains(err.Error(), tc.wantMessage) {
			t.Errorf("status %d: error = %q, want it to contain %q", tc.status, err, tc.wantMessage)
		}
	}
}

// x-error-message wins over the body; with no header the body is the message;
// with neither, the status is.
func TestPinUploadToCDN_ErrorMessageFallback(t *testing.T) {
	cases := []struct {
		name   string
		header string
		body   string
		want   string
	}{
		{"header wins", "hdr-error", "body-error", "hdr-error"},
		{"body fallback", "", "body-error", "body-error"},
		{"status fallback", "", "", "status 500"},
	}
	for _, tc := range cases {
		var hits atomic.Int32
		srv := pinCDNServer(t, &hits, func(w http.ResponseWriter, r *http.Request) {
			if tc.header != "" {
				w.Header().Set("x-error-message", tc.header)
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, tc.body)
		})
		_, err := uploadToCDN(context.Background(), &http.Client{Timeout: 5 * time.Second}, srv.URL, []byte("ct"))
		srv.Close()
		if err == nil {
			t.Fatalf("%s: uploadToCDN = nil error, want failure", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want it to contain %q", tc.name, err, tc.want)
		}
		if tc.name == "header wins" && strings.Contains(err.Error(), "body-error") {
			t.Errorf("%s: error = %q, want the header message, not the body", tc.name, err)
		}
	}
}

// A malformed CDN URL is not retriable: it must fail once, naming the request
// construction step.
func TestPinUploadToCDN_BadURLNoRetry(t *testing.T) {
	var hits atomic.Int32
	srv := pinCDNServer(t, &hits, nil)
	defer srv.Close()

	_, err := uploadToCDN(context.Background(), &http.Client{Timeout: 5 * time.Second}, "http://[::1", []byte("ct"))
	if err == nil {
		t.Fatal("uploadToCDN(bad url) = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "cdn upload: create request:") {
		t.Errorf("uploadToCDN error = %q, want the create-request message", err)
	}
	if hits.Load() != 0 {
		t.Errorf("CDN hits = %d, want 0 for an unparseable URL", hits.Load())
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("uploadToCDN error = %q, want it to wrap the *url.Error from URL parsing", err)
	}
}

// Network failures must keep the transport error in the chain so callers can
// classify them.
func TestPinUploadToCDN_PreservesTransportError(t *testing.T) {
	var hits atomic.Int32
	srv := pinCDNServer(t, &hits, nil)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := uploadToCDN(ctx, &http.Client{Timeout: 5 * time.Second}, srv.URL, []byte("ct"))
	if err == nil {
		t.Fatal("uploadToCDN(cancelled ctx) = nil error, want failure")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("uploadToCDN error = %q, want it to wrap context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "cdn upload: attempt") {
		t.Errorf("uploadToCDN error = %q, want the per-attempt prefix", err)
	}
}

// A CDN response without the download header must be reported as such.
func TestPinUploadToCDN_MissingDownloadParam(t *testing.T) {
	var hits atomic.Int32
	srv := pinCDNServer(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	_, err := uploadToCDN(context.Background(), &http.Client{Timeout: 5 * time.Second}, srv.URL, []byte("ct"))
	if err == nil {
		t.Fatal("uploadToCDN without x-encrypted-param = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "missing x-encrypted-param header") {
		t.Errorf("uploadToCDN error = %q, want the missing-header message", err)
	}
	if hits.Load() != 3 {
		t.Errorf("CDN attempts = %d, want 3 (uploadMaxRetries)", hits.Load())
	}
}

// pinTransport is a stub RoundTripper that records every request so its
// context can be inspected after the call — the only way to see whether
// uploadToCDN released the per-attempt timeout context.
type pinTransport struct {
	mu   sync.Mutex
	reqs []*http.Request
	fn   func(*http.Request) (*http.Response, error)
}

func (tr *pinTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	tr.reqs = append(tr.reqs, r)
	tr.mu.Unlock()
	return tr.fn(r)
}

func (tr *pinTransport) recorded() []*http.Request {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]*http.Request(nil), tr.reqs...)
}

// Every attempt must release its request context, on success and on transport
// failure alike.
func TestPinUploadToCDN_ReleasesRequestContext(t *testing.T) {
	cases := []struct {
		name     string
		fn       func(*http.Request) (*http.Response, error)
		wantReqs int
	}{
		{"success", func(*http.Request) (*http.Response, error) {
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}
			resp.Header.Set("x-encrypted-param", "dp")
			return resp, nil
		}, 1},
		{"transport error", func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		}, 3},
	}
	for _, tc := range cases {
		tr := &pinTransport{fn: tc.fn}
		// No client timeout: reqCtx must be the only cancel source, so a
		// dropped cancel() stays visible on the recorded request.
		client := &http.Client{Transport: tr}
		_, err := uploadToCDN(context.Background(), client, "https://cdn.invalid/upload", []byte("ct"))
		if tc.name == "success" && err != nil {
			t.Fatalf("%s: uploadToCDN = %v, want success", tc.name, err)
		}
		if tc.name != "success" && err == nil {
			t.Fatalf("%s: uploadToCDN = nil error, want failure", tc.name)
		}
		reqs := tr.recorded()
		if len(reqs) != tc.wantReqs {
			t.Fatalf("%s: recorded requests = %d, want %d", tc.name, len(reqs), tc.wantReqs)
		}
		for i, r := range reqs {
			if r.Context().Err() == nil {
				t.Errorf("%s: request[%d] context not released after uploadToCDN returned", tc.name, i)
			}
		}
	}
}

// The CDN POST must carry the octet-stream content type the CDN requires.
func TestPinUploadToCDN_ContentType(t *testing.T) {
	var gotType string
	var hits atomic.Int32
	srv := pinCDNServer(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		w.Header().Set("x-encrypted-param", "dp")
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	if _, err := uploadToCDN(context.Background(), &http.Client{Timeout: 5 * time.Second}, srv.URL, []byte("ct")); err != nil {
		t.Fatalf("uploadToCDN: %v", err)
	}
	if gotType != "application/octet-stream" {
		t.Errorf("CDN Content-Type = %q, want application/octet-stream", gotType)
	}
}

// getuploadurl decode failures must stay inspectable through the wrap.
func TestPinGetUploadURL_DecodeWrap(t *testing.T) {
	api := pinAPIResponse(`{"upload_full_url":`)
	defer api.Close()

	_, err := GetUploadURL(context.Background(), &http.Client{Timeout: 5 * time.Second}, api.URL, "tok",
		&GetUploadURLRequest{FileKey: "fk"})
	if err == nil {
		t.Fatal("GetUploadURL with truncated JSON = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "iLink getuploadurl decode:") {
		t.Errorf("GetUploadURL error = %q, want the decode prefix", err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("GetUploadURL error = %q, want it to wrap a *json.SyntaxError", err)
	}
}

// A missing local file must stay identifiable through the wrap.
func TestPinUploadFile_ReadFileWrap(t *testing.T) {
	_, err := UploadFile(context.Background(), &http.Client{Timeout: 5 * time.Second},
		"http://127.0.0.1:1", "tok", "u1", "/nonexistent/gbot-wechat-upload.bin", MediaImage)
	if err == nil {
		t.Fatal("UploadFile(missing file) = nil error, want failure")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("UploadFile error = %q, want it to wrap os.ErrNotExist", err)
	}
}

// A CDN failure must propagate out of UploadFile as an error, not a nil result.
func TestPinUploadFile_CDNFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/f.bin"
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	var hits atomic.Int32
	cdn := pinCDNServer(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-error-message", "cdn down")
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cdn.Close()
	api := pinAPIResponse(`{"upload_full_url":"` + cdn.URL + `"}`)
	defer api.Close()

	info, err := UploadFile(context.Background(), &http.Client{Timeout: 5 * time.Second},
		api.URL, "tok", "u1", path, MediaImage)
	if err == nil {
		t.Fatalf("UploadFile with a failing CDN = %+v, want an error", info)
	}
	if info != nil {
		t.Errorf("UploadFile result = %+v, want nil alongside the error", info)
	}
	if !strings.Contains(err.Error(), "cdn down") {
		t.Errorf("UploadFile error = %q, want the CDN error message", err)
	}
}
