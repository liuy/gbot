package media

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// aesBlock is the AES block size in bytes (128 bits). ECB operates block-by-block.
const aesBlock = 16

// hexKeyRegexp matches a 32-character ASCII hex string (16 bytes when decoded).
// Used by ParseAesKey to detect the base64(hex-string) encoding variant.
var hexKeyRegexp = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// DecryptAesEcb decrypts AES-128-ECB ciphertext with PKCS7 padding.
// Port of openclaw src/cdn/aes-ecb.ts:decryptAesEcb.
//
// Go's crypto/cipher deliberately omits ECB (it is insecure), so we decrypt
// block-by-block with cipher.Block.Decrypt and strip PKCS7 padding manually.
// key must be 16 bytes (AES-128).
func DecryptAesEcb(ciphertext, key []byte) ([]byte, error) {
	if len(key) != aesBlock {
		return nil, fmt.Errorf("aes-ecb: key must be 16 bytes (AES-128), got %d", len(key))
	}
	// Empty check MUST precede the % aesBlock check: an empty slice satisfies
	// len%16==0, but PKCS7 unpad below would read plaintext[-1] and panic.
	if len(ciphertext) == 0 {
		return nil, errors.New("aes-ecb: ciphertext is empty")
	}
	if len(ciphertext)%aesBlock != 0 {
		return nil, fmt.Errorf("aes-ecb: ciphertext length %d is not a multiple of %d", len(ciphertext), aesBlock)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes-ecb: new cipher: %w", err)
	}

	plaintext := make([]byte, len(ciphertext))
	// ECB: each block is decrypted independently with the same key (no IV, no
	// chaining). This is why identical plaintext blocks produce identical
	// ciphertext blocks — the insecurity that got ECB dropped from the stdlib.
	for start := 0; start < len(ciphertext); start += aesBlock {
		end := start + aesBlock
		block.Decrypt(plaintext[start:end], ciphertext[start:end])
	}

	return pkcs7Unpad(plaintext)
}

// pkcs7Unpad strips PKCS7 padding. n is the last byte's value and must be in
// [1, aesBlock]; the final n bytes must all equal n. Port of Node's
// createDecipheriv("aes-128-ecb").final() padding handling.
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("pkcs7: data is empty")
	}
	n := int(data[len(data)-1])
	if n < 1 || n > aesBlock {
		return nil, fmt.Errorf("pkcs7: invalid padding byte %d", n)
	}
	if n > len(data) {
		return nil, fmt.Errorf("pkcs7: padding byte %d exceeds data length %d", n, len(data))
	}
	for i := len(data) - n; i < len(data); i++ {
		if data[i] != byte(n) {
			return nil, errors.New("pkcs7: padding bytes do not match")
		}
	}
	return data[:len(data)-n], nil
}

// ParseAesKey recovers the raw 16-byte AES key from a WeChat CDN aes_key field.
// Port of openclaw src/cdn/pic-decrypt.ts:parseAesKey.
//
// Two encodings are seen in the wild:
//   - base64(raw 16 bytes)           → images (aes_key from the media field)
//   - base64(hex string of 16 bytes) → file / voice / video — base64-decodes
//     to 32 ASCII hex chars which must then be hex-decoded to recover the key.
func ParseAesKey(aesKeyBase64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("parse-aes-key: base64 decode: %w", err)
	}
	if len(decoded) == aesBlock {
		return decoded, nil
	}
	if len(decoded) == 32 && hexKeyRegexp.MatchString(string(decoded)) {
		raw, err := hex.DecodeString(string(decoded))
		if err != nil {
			return nil, fmt.Errorf("parse-aes-key: hex decode: %w", err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("parse-aes-key: aes_key must decode to 16 raw bytes or 32-char hex string, got %d bytes", len(decoded))
}

// DownloadAndDecrypt fetches a WeChat CDN media URL, then AES-128-ECB decrypts
// it. Port of openclaw src/cdn/pic-decrypt.ts:downloadAndDecryptBuffer.
//
// Only the fullURL path is supported (matching openclaw's
// ENABLE_CDN_URL_FALLBACK=false default branch). aesKeyBase64 is the
// CDNMedia.aes_key field (see ParseAesKey for the two accepted encodings).
func DownloadAndDecrypt(ctx context.Context, httpClient *http.Client, fullURL, aesKeyBase64 string) ([]byte, error) {
	key, err := ParseAesKey(aesKeyBase64)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cdn-download: create request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cdn-download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		preview := make([]byte, 0, 200)
		buf := make([]byte, 200)
		// Read at most 200 bytes for the error preview — enough context without
		// buffering an arbitrarily large error body.
		if n, _ := io.ReadFull(resp.Body, buf); n > 0 {
			preview = append(preview, buf[:n]...)
		}
		return nil, fmt.Errorf("cdn-download: HTTP %d %s body=%s", resp.StatusCode, resp.Status, string(preview))
	}
	ciphertext, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cdn-download: read body: %w", err)
	}
	return DecryptAesEcb(ciphertext, key)
}

// DownloadPlain fetches a CDN URL without decryption (for media lacking an
// aes_key). Port of openclaw src/cdn/pic-decrypt.ts:downloadPlainCdnBuffer.
func DownloadPlain(ctx context.Context, httpClient *http.Client, fullURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cdn-download-plain: create request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cdn-download-plain: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cdn-download-plain: HTTP %d %s", resp.StatusCode, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cdn-download-plain: read body: %w", err)
	}
	return data, nil
}
