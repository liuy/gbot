package media

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestEncryptAesEcb_RoundTrip_SubBlock(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	plaintext := []byte("hello world") // 11 bytes, < one block

	ciphertext, err := EncryptAesEcb(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb: %v", err)
	}
	if len(ciphertext) != AesEcbPaddedSize(len(plaintext)) {
		t.Errorf("ciphertext len = %d, want %d", len(ciphertext), AesEcbPaddedSize(len(plaintext)))
	}

	got, err := DecryptAesEcb(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptAesEcb: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestEncryptAesEcb_RoundTrip_Empty(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	// Empty input pads to one full block of 0x10.
	ciphertext, err := EncryptAesEcb(nil, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb(nil): %v", err)
	}
	if len(ciphertext) != aesBlock {
		t.Errorf("empty-input ciphertext len = %d, want %d", len(ciphertext), aesBlock)
	}
	if len(ciphertext) != AesEcbPaddedSize(0) {
		t.Errorf("AesEcbPaddedSize(0) = %d, want %d", AesEcbPaddedSize(0), len(ciphertext))
	}

	got, err := DecryptAesEcb(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptAesEcb: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("decrypted empty input = %d bytes, want 0", len(got))
	}
}

func TestEncryptAesEcb_RoundTrip_ExactMultiple(t *testing.T) {
	t.Parallel()
	key := []byte("YELLOW SUBMARINE")
	// 32 bytes = exact 2-block multiple; PKCS7 must still add a full pad block.
	plaintext := bytes.Repeat([]byte{0xAB}, 2*aesBlock)

	ciphertext, err := EncryptAesEcb(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb: %v", err)
	}
	// Exact-multiple input must pad to the NEXT block (3 blocks), not stay at 2.
	if len(ciphertext) != 3*aesBlock {
		t.Errorf("exact-multiple ciphertext len = %d, want %d", len(ciphertext), 3*aesBlock)
	}
	if len(ciphertext) != AesEcbPaddedSize(len(plaintext)) {
		t.Errorf("AesEcbPaddedSize(%d) = %d, want %d",
			len(plaintext), AesEcbPaddedSize(len(plaintext)), len(ciphertext))
	}

	got, err := DecryptAesEcb(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptAesEcb: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypted mismatch: len got=%d want=%d", len(got), len(plaintext))
	}
}

func TestEncryptAesEcb_RoundTrip_Random1KB(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	plaintext := make([]byte, 1024)
	if _, err := rand.Read(plaintext); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	ciphertext, err := EncryptAesEcb(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAesEcb: %v", err)
	}
	// 1024 = exact multiple → pads to next block (1040 = 65 blocks).
	if len(ciphertext) != 65*aesBlock {
		t.Errorf("1KB ciphertext len = %d, want %d", len(ciphertext), 65*aesBlock)
	}

	got, err := DecryptAesEcb(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptAesEcb: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypted mismatch for 1KB random input")
	}
}

func TestEncryptAesEcb_BadKeyLength(t *testing.T) {
	t.Parallel()
	badKey := []byte("only15bytes!!") // 13 bytes
	_, err := EncryptAesEcb([]byte("data"), badKey)
	if err == nil {
		t.Fatal("expected error for non-16-byte key, got nil")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Errorf("error = %q, want it to mention '16'", err.Error())
	}
}

func TestAesEcbPaddedSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want int
	}{
		{0, 16},      // empty → 1 block
		{1, 16},      // 1 byte → 1 block
		{15, 16},     // 15 bytes → 1 block
		{16, 32},     // exact block → pads to NEXT block (2)
		{17, 32},     // 17 → 2 blocks
		{31, 32},     // 31 → 2 blocks
		{32, 48},     // exact 2 blocks → pads to 3
		{1024, 1040}, // exact 64 blocks → pads to 65
	}
	for _, c := range cases {
		if got := AesEcbPaddedSize(c.in); got != c.want {
			t.Errorf("AesEcbPaddedSize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
