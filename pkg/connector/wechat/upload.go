package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/media"
)

// GetUploadURL fetches a pre-signed CDN upload URL.
// Port of openclaw src/api/api.ts:getUploadUrl.
func GetUploadURL(ctx context.Context, client *http.Client, baseURL, token string,
	req *GetUploadURLRequest) (*GetUploadURLResponse, error) {

	req.BaseInfo = baseInfo()
	raw, err := apiPost(ctx, client, baseURL, EPGetUploadURL, req, token, APITimeoutMs*time.Millisecond)
	if err != nil {
		return nil, err
	}
	var resp GetUploadURLResponse
	if err := decodeResponse(raw, &resp); err != nil {
		return nil, fmt.Errorf("iLink getuploadurl decode: %w", err)
	}
	return &resp, nil
}

// UploadedFileInfo is the result of uploading one file to the CDN.
// Port of openclaw src/cdn/upload.ts:UploadedFileInfo.
type UploadedFileInfo struct {
	FileKey                     string
	DownloadEncryptedQueryParam string // from CDN x-encrypted-param header
	AesKey                      string // hex of raw 16 bytes
	FileSize                    int    // plaintext bytes
	FileSizeCiphertext          int    // padded ciphertext bytes
}

// UploadFile encrypts a local file and uploads it to the WeChat CDN.
// Port of openclaw src/cdn/upload.ts:uploadMediaToCdn.
// mediaType is MediaImage / MediaVideo / MediaFile.
func UploadFile(ctx context.Context, client *http.Client, baseURL, token, toUserID,
	filePath string, mediaType int) (*UploadedFileInfo, error) {

	plaintext, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("upload: read file %s: %w", filePath, err)
	}

	rawsize := len(plaintext)
	sum := md5.Sum(plaintext)
	rawfilemd5 := hex.EncodeToString(sum[:])
	filesize := media.AesEcbPaddedSize(rawsize)

	fileKeyBytes := make([]byte, 16)
	if _, err := rand.Read(fileKeyBytes); err != nil {
		return nil, fmt.Errorf("upload: gen filekey: %w", err)
	}
	filekey := hex.EncodeToString(fileKeyBytes)

	aeskey := make([]byte, 16)
	if _, err := rand.Read(aeskey); err != nil {
		return nil, fmt.Errorf("upload: gen aeskey: %w", err)
	}

	uploadResp, err := GetUploadURL(ctx, client, baseURL, token, &GetUploadURLRequest{
		FileKey:     filekey,
		MediaType:   mediaType,
		ToUserID:    toUserID,
		RawSize:     rawsize,
		RawFileMD5:  rawfilemd5,
		FileSize:    filesize,
		NoNeedThumb: true,
		AesKey:      hex.EncodeToString(aeskey),
	})
	if err != nil {
		return nil, err
	}

	uploadFullURL := strings.TrimSpace(uploadResp.UploadFullURL)
	uploadParam := uploadResp.UploadParam
	if uploadFullURL == "" && uploadParam == "" {
		return nil, fmt.Errorf("upload: getUploadUrl returned no upload URL")
	}

	var cdnURL string
	if uploadFullURL != "" {
		cdnURL = uploadFullURL
	} else {
		cdnURL = buildCdnUploadURL(CDNBaseURL, uploadParam, filekey)
	}

	ciphertext, err := media.EncryptAesEcb(plaintext, aeskey)
	if err != nil {
		return nil, fmt.Errorf("upload: encrypt: %w", err)
	}

	downloadParam, err := uploadToCDN(ctx, client, cdnURL, ciphertext)
	if err != nil {
		return nil, err
	}

	return &UploadedFileInfo{
		FileKey:                     filekey,
		DownloadEncryptedQueryParam: downloadParam,
		AesKey:                      hex.EncodeToString(aeskey),
		FileSize:                    rawsize,
		FileSizeCiphertext:          filesize,
	}, nil
}

// uploadMaxRetries matches openclaw src/cdn/cdn-upload.ts:UPLOAD_MAX_RETRIES.
const uploadMaxRetries = 3

// uploadToCDN POSTs encrypted bytes to the CDN and returns the download param
// from the x-encrypted-param response header. Port of openclaw
// src/cdn/cdn-upload.ts:uploadBufferToCdn.
//
// Retries up to uploadMaxRetries times on server errors and network failures;
// client errors (4xx) abort immediately (matching TS's "client error" fast-exit).
func uploadToCDN(ctx context.Context, client *http.Client, cdnURL string,
	ciphertext []byte) (string, error) {

	var downloadParam string
	var lastError error

	for attempt := 1; attempt <= uploadMaxRetries; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, APITimeoutMs*time.Millisecond)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cdnURL, bytes.NewReader(ciphertext))
		if err != nil {
			cancel()
			// Request construction failure is not retriable.
			return "", fmt.Errorf("cdn upload: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastError = fmt.Errorf("cdn upload: attempt %d: %w", attempt, err)
			continue
		}

		status := resp.StatusCode
		errMsg := resp.Header.Get("x-error-message")
		if errMsg == "" {
			body, _ := io.ReadAll(resp.Body)
			errMsg = string(body)
		}
		_ = resp.Body.Close()
		cancel()

		if status >= 400 && status < 500 {
			// Client error: return immediately, no retry (TS fast-exit on "client error").
			return "", fmt.Errorf("CDN upload client error %d: %s", status, errMsg)
		}
		if status != http.StatusOK {
			lastError = fmt.Errorf("CDN upload server error: %s", fallbackMsg(errMsg, status))
			continue
		}

		// 200: extract the download param from the response header.
		if dp := resp.Header.Get("x-encrypted-param"); dp != "" {
			downloadParam = dp
			lastError = nil
			break
		}
		lastError = fmt.Errorf("CDN upload response missing x-encrypted-param header")
	}

	if downloadParam == "" {
		if lastError != nil {
			return "", lastError
		}
		return "", fmt.Errorf("CDN upload failed after %d attempts", uploadMaxRetries)
	}
	return downloadParam, nil
}

// fallbackMsg returns errMsg when non-empty, otherwise "status {n}". Mirrors
// TS's `x-error-message ?? \`status ${res.status}\“ fallback.
func fallbackMsg(errMsg string, status int) string {
	if errMsg != "" {
		return errMsg
	}
	return fmt.Sprintf("status %d", status)
}

// buildCdnUploadURL builds a CDN upload URL from upload_param and filekey.
// Port of openclaw src/cdn/cdn-url.ts:buildCdnUploadUrl.
func buildCdnUploadURL(cdnBaseURL, uploadParam, filekey string) string {
	return cdnBaseURL + "/upload?encrypted_query_param=" + url.QueryEscape(uploadParam) +
		"&filekey=" + url.QueryEscape(filekey)
}
