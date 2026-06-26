package media

import (
	"crypto/aes"
	"fmt"
)

// EncryptAesEcb encrypts plaintext with AES-128-ECB and PKCS7 padding.
// Symmetric counterpart of DecryptAesEcb. key must be 16 bytes.
// Port of openclaw src/cdn/aes-ecb.ts:encryptAesEcb.
//
// Go's crypto/cipher omits ECB, so we encrypt block-by-block with
// cipher.Block.Encrypt and apply PKCS7 padding manually — mirroring what the
// decrypt side already does in reverse.
func EncryptAesEcb(plaintext, key []byte) ([]byte, error) {
	if len(key) != aesBlock {
		return nil, fmt.Errorf("aes-ecb: key must be 16 bytes (AES-128), got %d", len(key))
	}

	padded, err := pkcs7Pad(plaintext)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes-ecb: new cipher: %w", err)
	}

	out := make([]byte, len(padded))
	for start := 0; start < len(padded); start += aesBlock {
		end := start + aesBlock
		block.Encrypt(out[start:end], padded[start:end])
	}
	return out, nil
}

// AesEcbPaddedSize returns the ciphertext length for a plaintext of the given
// size under AES-128-ECB with PKCS7 padding. Port of
// openclaw src/cdn/aes-ecb.ts:aesEcbPaddedSize, which is
// Math.ceil((plaintextSize + 1) / 16) * 16.
//
// PKCS7 always pads, even when the input is an exact block multiple, so the +1
// forces the next block in that case (a 16-byte input pads to 32 bytes).
func AesEcbPaddedSize(plaintextSize int) int {
	return ((plaintextSize + 1 + aesBlock - 1) / aesBlock) * aesBlock
}

// pkcs7Pad appends PKCS7 padding so the result is a whole number of blocks.
// padLen is in [1, aesBlock]; an empty input pads to one full block of 0x10,
// matching Node's createCipheriv("aes-128-ecb").final() on empty input.
func pkcs7Pad(data []byte) ([]byte, error) {
	if len(data) > maxPlaintextSize {
		return nil, fmt.Errorf("pkcs7: plaintext size %d exceeds maximum %d", len(data), maxPlaintextSize)
	}
	padLen := aesBlock - len(data)%aesBlock
	padded := make([]byte, len(data)+padLen)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	return padded, nil
}

// maxPlaintextSize bounds pkcs7Pad's allocation. 2GB matches Node's Buffer cap
// for the upload path; the CDN upload limit is far smaller, but this guard
// exists solely to make an absurd input fail fast instead of allocating.
const maxPlaintextSize = 1 << 31
