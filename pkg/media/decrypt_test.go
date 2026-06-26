package media

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecryptAesEcb_RoundTrip(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef") // 16 bytes
	plaintext := []byte("hello world")
	ciphertext, err := EncryptAesEcb(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb: %v", err)
	}

	got, err := DecryptAesEcb(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptAesEcb: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestDecryptAesEcb_MultiBlockRoundTrip(t *testing.T) {
	t.Parallel()
	key := []byte("YELLOW SUBMARINE") // 16 bytes
	// Spans 3 blocks after padding, exercising the block loop.
	plaintext := []byte("the quick brown fox jumps over the lazy dog 1234567890")
	ciphertext, err := EncryptAesEcb(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb: %v", err)
	}

	got, err := DecryptAesEcb(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptAesEcb: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestDecryptAesEcb_EmptyCiphertext(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	// Regression: an empty slice passes len%16==0; the guard prevents the
	// subsequent PKCS7 unpad from panicking on plaintext[-1].
	if _, err := DecryptAesEcb(nil, key); err == nil {
		t.Error("expected error for empty ciphertext, got nil")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want it to mention 'empty'", err.Error())
	}
	if _, err := DecryptAesEcb([]byte{}, key); err == nil {
		t.Error("expected error for zero-length ciphertext slice, got nil")
	}
}

func TestDecryptAesEcb_BadKey(t *testing.T) {
	t.Parallel()
	badKey := []byte("only15bytes!!") // 13 bytes
	ciphertext := make([]byte, aesBlock)
	if _, err := DecryptAesEcb(ciphertext, badKey); err == nil {
		t.Error("expected error for non-16-byte key, got nil")
	} else if !strings.Contains(err.Error(), "16") {
		t.Errorf("error = %q, want it to mention '16'", err.Error())
	}
}

func TestDecryptAesEcb_BadCiphertextAlignment(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	// 17 bytes — not a multiple of the 16-byte block.
	ciphertext := make([]byte, 17)
	if _, err := DecryptAesEcb(ciphertext, key); err == nil {
		t.Error("expected error for misaligned ciphertext, got nil")
	} else if !strings.Contains(err.Error(), "multiple") {
		t.Errorf("error = %q, want it to mention 'multiple'", err.Error())
	}
}

func TestPkcs7Unpad_Empty(t *testing.T) {
	t.Parallel()
	if _, err := pkcs7Unpad(nil); err == nil {
		t.Error("expected error for empty pkcs7 input, got nil")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want 'empty'", err.Error())
	}
}

func TestPkcs7Unpad_PadByteExceedsData(t *testing.T) {
	t.Parallel()
	// 4-byte data with last byte claiming pad=9, but data is shorter than 9.
	data := []byte{0xAA, 0xAA, 0xAA, 9}
	if _, err := pkcs7Unpad(data); err == nil {
		t.Error("expected error for pad byte exceeding data length, got nil")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want 'exceeds'", err.Error())
	}
}

func TestPkcs7Unpad_ValidPadding(t *testing.T) {
	t.Parallel()
	// 16 bytes ending in 4 bytes of 0x04 — valid PKCS7 pad of 4.
	data := append(fillBytes(12, 0xAA), 0x04, 0x04, 0x04, 0x04)
	got, err := pkcs7Unpad(data)
	if err != nil {
		t.Fatalf("pkcs7Unpad: %v", err)
	}
	if len(got) != 12 {
		t.Errorf("len = %d, want 12", len(got))
	}
}

func TestPkcs7Unpad_InvalidPadByteTooLarge(t *testing.T) {
	t.Parallel()
	// Last byte 17 — exceeds block size.
	data := append(fillBytes(aesBlock-1, 0x05), 17)
	_, err := pkcs7Unpad(data)
	if err == nil {
		t.Fatal("expected error for pad byte > 16, got nil")
	}
	if !strings.Contains(err.Error(), "invalid padding byte") {
		t.Errorf("error = %q, want it to mention 'invalid padding byte'", err.Error())
	}
}

func TestPkcs7Unpad_MismatchedPaddingBytes(t *testing.T) {
	t.Parallel()
	// Claims pad=3 (last byte) but the third-from-last byte differs, so the
	// padding run is invalid.
	data2 := make([]byte, aesBlock)
	for i := range data2 {
		data2[i] = 0xAA
	}
	data2[aesBlock-1] = 3 // claims pad=3
	data2[aesBlock-2] = 3
	data2[aesBlock-3] = 0xBB // mismatch
	if _, err := pkcs7Unpad(data2); err == nil {
		t.Error("expected error for mismatched padding bytes, got nil")
	} else if !strings.Contains(err.Error(), "do not match") {
		t.Errorf("error = %q, want 'do not match'", err.Error())
	}
}

func TestParseAesKey_Raw16(t *testing.T) {
	t.Parallel()
	raw := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	encoded := base64.StdEncoding.EncodeToString(raw)
	got, err := ParseAesKey(encoded)
	if err != nil {
		t.Fatalf("ParseAesKey: %v", err)
	}
	if len(got) != 16 {
		t.Fatalf("len = %d, want 16", len(got))
	}
	for i := range raw {
		if got[i] != raw[i] {
			t.Errorf("byte %d = %d, want %d", i, got[i], raw[i])
		}
	}
}

func TestParseAesKey_Hex32(t *testing.T) {
	t.Parallel()
	// 32 ASCII hex chars representing 16 bytes.
	hexStr := "00112233445566778899aabbccddeeff"
	encoded := base64.StdEncoding.EncodeToString([]byte(hexStr))
	got, err := ParseAesKey(encoded)
	if err != nil {
		t.Fatalf("ParseAesKey: %v", err)
	}
	want, _ := hex.DecodeString(hexStr)
	if len(got) != 16 {
		t.Fatalf("len = %d, want 16", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("byte %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestParseAesKey_Invalid(t *testing.T) {
	t.Parallel()
	// 20 random bytes — neither 16 nor a 32-char hex string.
	invalid := base64.StdEncoding.EncodeToString(fillBytes(20, 0xAB))
	if _, err := ParseAesKey(invalid); err == nil {
		t.Error("expected error for 20-byte decoded key, got nil")
	} else if !strings.Contains(err.Error(), "16 raw bytes") {
		t.Errorf("error = %q, want it to mention '16 raw bytes'", err.Error())
	}
}

func TestParseAesKey_InvalidBase64(t *testing.T) {
	t.Parallel()
	if _, err := ParseAesKey("!!!not-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	} else if !strings.Contains(err.Error(), "base64") {
		t.Errorf("error = %q, want it to mention 'base64'", err.Error())
	}
}

func TestDownloadAndDecrypt(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	plaintext := []byte("the eagle lands at dawn")
	ciphertext, err := EncryptAesEcb(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb: %v", err)
	}
	aesKeyBase64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	got, err := DownloadAndDecrypt(context.Background(), client, srv.URL, aesKeyBase64)
	if err != nil {
		t.Fatalf("DownloadAndDecrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestDownloadAndDecrypt_HTTPError(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	aesKeyBase64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := DownloadAndDecrypt(context.Background(), client, srv.URL, aesKeyBase64)
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention '500'", err.Error())
	}
}

func TestDownloadAndDecrypt_BadAesKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("anything"))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := DownloadAndDecrypt(context.Background(), client, srv.URL, "!!!bad-base64!!!")
	if err == nil {
		t.Fatal("expected error for bad aes key, got nil")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("error = %q, want it to mention 'base64'", err.Error())
	}
}

func TestDownloadPlain(t *testing.T) {
	t.Parallel()
	want := []byte("plain-bytes-no-encryption")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	got, err := DownloadPlain(context.Background(), client, srv.URL)
	if err != nil {
		t.Fatalf("DownloadPlain: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got = %q, want %q", got, want)
	}
}

func TestDownloadPlain_HTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := DownloadPlain(context.Background(), client, srv.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to mention '403'", err.Error())
	}
}

// fillBytes returns n copies of b as a slice (test helper to build filler buffers).
func fillBytes(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
