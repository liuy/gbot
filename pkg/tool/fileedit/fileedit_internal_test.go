package fileedit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeUTF16LE(t *testing.T) {
	t.Parallel()
	data := []byte{0x48, 0x00, 0x69, 0x00}
	got := decodeUTF16LE(data)
	if got != "Hi" {
		t.Errorf("decodeUTF16LE = %q, want %q", got, "Hi")
	}
}

func TestDecodeUTF16LE_OddLength(t *testing.T) {
	t.Parallel()
	data := []byte{0x48, 0x00, 0x69}
	got := decodeUTF16LE(data)
	if got != "H" {
		t.Errorf("decodeUTF16LE odd = %q, want %q", got, "H")
	}
}

func TestEncodeUTF16LE(t *testing.T) {
	t.Parallel()
	got := encodeUTF16LE("AB")
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	if got[0] != 0x41 || got[1] != 0x00 || got[2] != 0x42 || got[3] != 0x00 {
		t.Errorf("encodeUTF16LE = %x, want 41004200", got)
	}
}

func TestRoundtripUTF16LE(t *testing.T) {
	t.Parallel()
	original := "Hello, World!"
	encoded := encodeUTF16LE(original)
	decoded := decodeUTF16LE(encoded)
	if decoded != original {
		t.Errorf("roundtrip: got %q, want %q", decoded, original)
	}
}

func TestRoundtripUTF16LE_SurrogatePairs(t *testing.T) {
	t.Parallel()
	// Emoji and other non-BMP characters require surrogate pairs in UTF-16
	original := "Hello 😀 World 🌈 Test 日本語"
	encoded := encodeUTF16LE(original)
	decoded := decodeUTF16LE(encoded)
	if decoded != original {
		t.Errorf("roundtrip surrogate: got %q, want %q", decoded, original)
	}
}

func TestReadFileForEdit_FileNotExist(t *testing.T) {
	t.Parallel()
	fr := readFileForEdit("/nonexistent/file.txt")
	if fr.fileExists {
		t.Error("fileExists = true, want false")
	}
}

func TestReadFileForEdit_NormalFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(fp, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fr := readFileForEdit(fp)
	if !fr.fileExists {
		t.Fatal("fileExists = false, want true")
	}
	if fr.content != "hello\nworld\n" {
		t.Errorf("content = %q, want %q", fr.content, "hello\nworld\n")
	}
	if fr.hasBOM {
		t.Error("hasBOM = true, want false")
	}
	if fr.hasCRLF {
		t.Error("hasCRLF = true, want false")
	}
	if fr.fileMode&0o644 != 0o644 {
		t.Errorf("fileMode = %o, want 0644", fr.fileMode)
	}
}

func TestReadFileForEdit_CRLFFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "crlf.txt")
	if err := os.WriteFile(fp, []byte("line1\r\nline2\r\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fr := readFileForEdit(fp)
	if !fr.hasCRLF {
		t.Error("hasCRLF = false, want true")
	}
	if fr.content != "line1\nline2\n" {
		t.Errorf("content = %q, want %q", fr.content, "line1\nline2\n")
	}
}

func TestReadFileForEdit_UTF16LEWithBOM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "bom.txt")
	bom := []byte{0xFF, 0xFE}
	text := "hello"
	encoded := make([]byte, len(bom)+len(text)*2)
	copy(encoded, bom)
	for i, r := range text {
		v := uint16(r)
		encoded[len(bom)+i*2] = byte(v)
		encoded[len(bom)+i*2+1] = byte(v >> 8)
	}
	if err := os.WriteFile(fp, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fr := readFileForEdit(fp)
	if !fr.hasBOM {
		t.Error("hasBOM = false, want true")
	}
	if fr.content != "hello" {
		t.Errorf("content = %q, want %q", fr.content, "hello")
	}
}

func TestExecute_MaxFileSizeExceeded(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.txt")
	// Write a file larger than the temporarily lowered limit
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Temporarily lower the limit to trigger the size check
	orig := MaxEditFileSize
	MaxEditFileSize = 5 // 5 bytes — our file is 12 bytes
	defer func() { MaxEditFileSize = orig }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for oversized file")
	}
}

func TestExecute_WriteErrorOnNewFile(t *testing.T) {
	// Try to create a new file in a read-only directory
	dir := t.TempDir()
	subdir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(subdir, 0o555); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	defer func() { _ = os.Chmod(subdir, 0o755) }()

	fp := filepath.Join(subdir, "newfile.txt")
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"hello"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error for new file in read-only dir")
	}
}

func TestExecute_WriteErrorOnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	// Create empty file, then make it read-only
	if err := os.WriteFile(fp, []byte(""), 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"hello"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error on read-only empty file")
	}
}

func TestExecute_WriteErrorBOM(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "bom.txt")

	// Write UTF-16 LE with BOM
	bom := []byte{0xFF, 0xFE}
	text := "hello"
	encoded := make([]byte, len(bom)+len(text)*2)
	copy(encoded, bom)
	for i, r := range text {
		v := uint16(r)
		encoded[len(bom)+i*2] = byte(v)
		encoded[len(bom)+i*2+1] = byte(v >> 8)
	}
	if err := os.WriteFile(fp, encoded, 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"world"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error on read-only BOM file")
	}
}

func TestExecute_WriteErrorNormal(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := Execute(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want write error on read-only normal file")
	}
}
