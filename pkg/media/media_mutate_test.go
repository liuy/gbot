package media

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultMaxAge(t *testing.T) {
	t.Parallel()
	if DefaultMaxAge != 30*24*time.Hour {
		t.Errorf("DefaultMaxAge = %v, want 720h", DefaultMaxAge)
	}
}

// WebP needs both the RIFF and the WEBP markers; each byte position matters.
func TestSniffImageMime_WebPBytes(t *testing.T) {
	t.Parallel()
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	if got := SniffImageMime(webp); got != "image/webp" {
		t.Errorf("webp = %q, want image/webp", got)
	}
	cases := []struct {
		name string
		data []byte
	}{
		{"riff byte2 wrong", []byte("RI\x00F\x00\x00\x00\x00WEBPVP8 ")},
		{"riff byte3 wrong", []byte("RIF\x00\x00\x00\x00\x00WEBPVP8 ")},
		{"webp byte9 wrong", []byte("RIFF\x00\x00\x00\x00W\x00BPVP8 ")},
		{"webp byte11 wrong", []byte("RIFF\x00\x00\x00\x00WEB\x0bVP8 ")},
		{"too short", []byte("RIFFWEBP")},
		{"empty", []byte{}},
	}
	for _, c := range cases {
		if got := SniffImageMime(c.data); got != "" {
			t.Errorf("%s = %q, want no match", c.name, got)
		}
	}
	// Known prefixes still win.
	if got := SniffImageMime([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); got != "image/png" {
		t.Errorf("png = %q, want image/png", got)
	}
	if got := SniffImageMime([]byte("GIF89a")); got != "image/gif" {
		t.Errorf("gif = %q, want image/gif", got)
	}
	if got := SniffImageMime([]byte{0xff, 0xd8, 0xff, 0xe0}); got != "image/jpeg" {
		t.Errorf("jpeg = %q, want image/jpeg", got)
	}
}

func TestParseCachePath(t *testing.T) {
	t.Parallel()
	if got := parseCachePath("", "abc"); got != "" {
		t.Errorf("empty root = %q, want no cache path", got)
	}
	if got := parseCachePath("/root", ""); got != "" {
		t.Errorf("empty key = %q, want no cache path", got)
	}
	want := filepath.Join("/root", "documents", "parse", "abc.md")
	if got := parseCachePath("/root", "abc"); got != want {
		t.Errorf("parseCachePath = %q, want %q", got, want)
	}
}

func TestPkcs7Unpad(t *testing.T) {
	t.Parallel()
	// A single 0x01 pad byte is valid.
	got, err := pkcs7Unpad([]byte{0x41, 0x42, 0x01})
	if err != nil || !bytes.Equal(got, []byte{0x41, 0x42}) {
		t.Errorf("single pad = %v, %v, want [41 42]", got, err)
	}
	// A full block of 0x10 un pads to empty.
	got, err = pkcs7Unpad(bytes.Repeat([]byte{0x10}, 16))
	if err != nil || len(got) != 0 {
		t.Errorf("full block pad = %v, %v, want empty", got, err)
	}
	for _, c := range []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", []byte{}, "pkcs7: data is empty"},
		{"zero pad", []byte{0x41, 0x00}, "pkcs7: invalid padding byte 0"},
		{"oversized pad", append(bytes.Repeat([]byte{0x11}, 17), 0x11), "pkcs7: invalid padding byte 17"},
		{"pad longer than data", []byte{0x05, 0x05}, "pkcs7: padding byte 5 exceeds data length 2"},
		{"mismatched", []byte{0x02, 0x02, 0x03}, "pkcs7: padding bytes do not match"},
	} {
		_, err := pkcs7Unpad(c.in)
		if err == nil {
			t.Errorf("%s: expected an error", c.name)
			continue
		}
		if err.Error() != c.want {
			t.Errorf("%s: err = %q, want %q", c.name, err.Error(), c.want)
		}
	}
}

// PKCS7 always pads, so an exact block multiple grows by a full block.
func TestAesEcbPaddedSize_Boundaries(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want int }{
		{0, 16}, {1, 16}, {15, 16}, {16, 32}, {17, 32}, {31, 32}, {32, 48},
	} {
		if got := AesEcbPaddedSize(c.in); got != c.want {
			t.Errorf("AesEcbPaddedSize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// aes_key arrives either as base64 of 16 raw bytes or base64 of a 32-char hex string.
func TestParseAesKey_Formats(t *testing.T) {
	t.Parallel()
	raw := bytes.Repeat([]byte{0xab}, 16)
	got, err := ParseAesKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil || !bytes.Equal(got, raw) {
		t.Errorf("raw key = %v, %v, want the 16 bytes back", got, err)
	}
	hexKey := "0123456789abcdef0123456789abcdef"
	got, err = ParseAesKey(base64.StdEncoding.EncodeToString([]byte(hexKey)))
	if err != nil {
		t.Fatalf("hex key: %v", err)
	}
	if !bytes.Equal(got, raw2(hexKey)) {
		t.Errorf("hex key = %x, want %s", got, hexKey)
	}
	_, err = ParseAesKey(base64.StdEncoding.EncodeToString([]byte("short")))
	if err == nil {
		t.Fatal("a short key must be rejected")
	}
	if err.Error() != "parse-aes-key: aes_key must decode to 16 raw bytes or 32-char hex string, got 5 bytes" {
		t.Errorf("err = %q", err.Error())
	}
	_, err = ParseAesKey("!!!not base64!!!")
	if err == nil {
		t.Fatal("invalid base64 must be rejected")
	}
	if err.Error() != "parse-aes-key: base64 decode: illegal base64 data at input byte 0" {
		t.Errorf("err = %q", err.Error())
	}
	// 32 bytes that are not hex are rejected.
	_, err = ParseAesKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'Z'}, 32)))
	if err == nil {
		t.Fatal("32 non-hex bytes must be rejected")
	}
}

func raw2(hex string) []byte {
	out := make([]byte, 16)
	for i := range 16 {
		out[i] = hexDigit(hex[i*2])<<4 | hexDigit(hex[i*2+1])
	}
	return out
}

func hexDigit(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	}
	return 0
}
