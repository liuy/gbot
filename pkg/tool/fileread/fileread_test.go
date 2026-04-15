package fileread_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := fileread.New()

	if tt.Name() != "Read" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "Read")
	}
	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly() = false, want true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = false, want true")
	}
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %d, want %d", tt.InterruptBehavior(), tool.InterruptCancel)
	}
	if tt.Prompt() == "" {
		t.Error("Prompt() is empty")
	}
	if !strings.Contains(tt.Prompt(), "Reads a file") {
		t.Errorf("Prompt() = %q, should contain 'Reads a file'", tt.Prompt())
	}
	if !tt.IsEnabled() {
		t.Error("IsEnabled() = false, want true")
	}
}

func TestNewInputSchema(t *testing.T) {
	t.Parallel()

	tt := fileread.New()
	schema := tt.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
	required, _ := obj["required"].([]any)
	if len(required) == 0 || required[0] != "file_path" {
		t.Errorf("InputSchema() required = %v, want [\"file_path\"]", required)
	}
	props, _ := obj["properties"].(map[string]any)
	if _, ok := props["file_path"]; !ok {
		t.Error("InputSchema() missing 'file_path' property")
	}
	if _, ok := props["offset"]; !ok {
		t.Error("InputSchema() missing 'offset' property")
	}
	if _, ok := props["limit"]; !ok {
		t.Error("InputSchema() missing 'limit' property")
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()

	tt := fileread.New()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with path", `{"file_path":"/tmp/test.go"}`, "/tmp/test.go"},
		{"invalid json", `{invalid`, "Read a file from the filesystem"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			desc, err := tt.Description(json.RawMessage(tc.input))
			if err != nil {
				t.Fatalf("Description() error: %v", err)
			}
			if desc != tc.want {
				t.Errorf("Description() = %q, want %q", desc, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Execute — happy paths
// ---------------------------------------------------------------------------

func TestExecute_ReadWholeFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if output.Content != content {
		t.Errorf("Content = %q, want %q", output.Content, content)
	}
	if output.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", output.FilePath, fp)
	}
	if output.NumLines != 3 {
		t.Errorf("NumLines = %d, want 3", output.NumLines)
	}
	if output.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", output.StartLine)
	}
	if output.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", output.TotalLines)
	}
}

func TestExecute_ReadFileNoTrailingNewline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "noeol.txt")
	content := "line1\nline2\nline3"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(fileread.TextOutput)
	if output.NumLines != 3 {
		t.Errorf("NumLines = %d, want 3", output.NumLines)
	}
	if output.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", output.TotalLines)
	}
}

func TestExecute_ReadEmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(fileread.TextOutput)
	if output.Content != "" {
		t.Errorf("Content = %q, want empty", output.Content)
	}
	if output.NumLines != 0 {
		t.Errorf("NumLines = %d, want 0", output.NumLines)
	}
}

func TestExecute_ReadWithOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "offset.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","offset":3}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(fileread.TextOutput)
	if output.NumLines != 3 {
		t.Errorf("NumLines = %d, want 3", output.NumLines)
	}
	if output.StartLine != 3 {
		t.Errorf("StartLine = %d, want 3", output.StartLine)
	}
	if output.TotalLines != 5 {
		t.Errorf("TotalLines = %d, want 5", output.TotalLines)
	}
	if output.Content != "line3\nline4\nline5" {
		t.Errorf("Content = %q, want %q", output.Content, "line3\\nline4\\nline5")
	}
	if strings.Contains(output.Content, "line1") {
		t.Errorf("Content = %q, should NOT contain 'line1'", output.Content)
	}
	if strings.Contains(output.Content, "line2") {
		t.Errorf("Content = %q, should NOT contain 'line2'", output.Content)
	}
}

func TestExecute_ReadWithLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "limit.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","offset":2,"limit":2}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(fileread.TextOutput)
	if output.NumLines != 2 {
		t.Errorf("NumLines = %d, want 2", output.NumLines)
	}
	if output.Content != "line2\nline3" {
		t.Errorf("Content = %q, want %q", output.Content, "line2\\nline3")
	}
	if strings.Contains(output.Content, "line1") {
		t.Errorf("Content should NOT contain 'line1'")
	}
	if strings.Contains(output.Content, "line4") {
		t.Errorf("Content should NOT contain 'line4'")
	}
}

func TestExecute_ReadWithZeroOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "zerooffset.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// offset=0 with limit set should treat offset as 1
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":0,"limit":2}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(fileread.TextOutput)
	if output.NumLines != 2 {
		t.Errorf("NumLines = %d, want 2", output.NumLines)
	}
	if output.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1 (zero offset normalised)", output.StartLine)
	}
	if output.Content != "line1\nline2" {
		t.Errorf("Content = %q, want %q", output.Content, "line1\\nline2")
	}
}

// ---------------------------------------------------------------------------
// Execute — error paths
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := fileread.Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("Error = %q, want 'parse input'", err.Error())
	}
}

func TestExecute_EmptyFilePath(t *testing.T) {
	t.Parallel()

	_, err := fileread.Execute(context.Background(), json.RawMessage(`{"file_path":""}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for empty file_path")
	}
	if !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("Error = %q, want 'file_path is required'", err.Error())
	}
}

func TestExecute_FileNotFound(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"file_path":"/nonexistent/file.txt"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing file")
	}
	// Check for specific error messages about file not existing
	errMsg := err.Error()
	if !strings.Contains(errMsg, "does not exist") && !strings.Contains(errMsg, "no such file") {
		t.Errorf("Error = %q, want 'does not exist' or 'no such file'", errMsg)
	}
}

func TestExecute_Directory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	input := json.RawMessage(`{"file_path":"` + dir + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("Error = %q, want 'directory'", err.Error())
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("Error = %q, should contain path %q", err.Error(), dir)
	}
}

func TestExecute_StatPermissionDenied(t *testing.T) {
	t.Parallel()
	// Create a directory without execute permission to trigger non-IsNotExist stat error
	dir := t.TempDir()
	restricted := filepath.Join(dir, "restricted")
	if err := os.MkdirAll(restricted, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(restricted, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	// Remove execute permission from parent directory
	if err := os.Chmod(restricted, 0000); err != nil {
		t.Skip("chmod not supported")
	}
	defer func() { _ = os.Chmod(restricted, 0755) }() // restore for cleanup

	input := json.RawMessage(`{"file_path":"` + target + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for permission denied")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("Error = %q, should contain path %q", err.Error(), target)
	}
}

// ---------------------------------------------------------------------------
// Output JSON
// ---------------------------------------------------------------------------

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := fileread.TextOutput{
		Type:       "text",
		FilePath:   "/tmp/test.txt",
		Content:    "hello\nworld",
		NumLines:   2,
		StartLine:  1,
		TotalLines: 2,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got fileread.TextOutput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.FilePath != output.FilePath {
		t.Errorf("FilePath = %q, want %q", got.FilePath, output.FilePath)
	}
	if got.Content != output.Content {
		t.Errorf("Content = %q, want %q", got.Content, output.Content)
	}
	if got.NumLines != output.NumLines {
		t.Errorf("NumLines = %d, want %d", got.NumLines, output.NumLines)
	}
}

// ---------------------------------------------------------------------------
// PDF reading
// ---------------------------------------------------------------------------

func TestExecute_ReadPDF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	pdfOut, ok := result.Data.(fileread.PDFOutput)
	if !ok {
		t.Fatalf("Data type = %T, want PDFOutput", result.Data)
	}
	if pdfOut.Type != "pdf" {
		t.Errorf("Type = %q, want %q", pdfOut.Type, "pdf")
	}
	if pdfOut.Base64 == "" {
		t.Error("Base64 is empty")
	}
	// Verify base64 decodes back to original content
	decoded, err := base64.StdEncoding.DecodeString(pdfOut.Base64)
	if err != nil {
		t.Fatalf("Base64 decode error: %v", err)
	}
	if len(decoded) != len(data) {
		t.Errorf("Decoded length = %d, want %d", len(decoded), len(data))
	}
	if pdfOut.OriginalSize != int64(len(data)) {
		t.Errorf("OriginalSize = %d, want %d", pdfOut.OriginalSize, len(data))
	}
	if pdfOut.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", pdfOut.FilePath, fp)
	}
}

func TestExecute_ReadPDFWithPages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "pages.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"1"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// When pdftoppm is available, pages param returns PartsOutput
	partsOut, ok := result.Data.(fileread.PartsOutput)
	if !ok {
		// Fallback: PDFOutput when pdftoppm not available
		pdfOut, ok := result.Data.(fileread.PDFOutput)
		if !ok {
			t.Fatalf("Data type = %T, want PDFOutput or PartsOutput", result.Data)
		}
		if pdfOut.Type != "pdf" {
			t.Errorf("Type = %q, want %q", pdfOut.Type, "pdf")
		}
		if pdfOut.FilePath != fp {
			t.Errorf("FilePath = %q, want %q", pdfOut.FilePath, fp)
		}
		if pdfOut.OriginalSize != int64(len(data)) {
			t.Errorf("OriginalSize = %d, want %d", pdfOut.OriginalSize, len(data))
		}
		return
	}
	if partsOut.Type != "parts" {
		t.Errorf("Type = %q, want %q", partsOut.Type, "parts")
	}
	if partsOut.Count != 1 {
		t.Errorf("Count = %d, want 1 (pages='1')", partsOut.Count)
	}
	if partsOut.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", partsOut.FilePath, fp)
	}
	if partsOut.OutputDir == "" {
		t.Error("OutputDir is empty")
	}
	if partsOut.OriginalSize != int64(len(data)) {
		t.Errorf("OriginalSize = %d, want %d", partsOut.OriginalSize, len(data))
	}
}

func TestExecute_PDFTooManyPages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.pdf")
	data, err := os.ReadFile("/tmp/test15pages.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	// With pdftoppm, large PDFs are now extracted via page images instead of erroring
	// So we just verify it succeeds (PartsOutput or PDFOutput depending on size)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// Verify we get a valid PDF output
	switch out := result.Data.(type) {
	case fileread.PartsOutput:
		if out.Type != "parts" {
			t.Errorf("PartsOutput.Type = %q, want parts", out.Type)
		}
		if out.FilePath != fp {
			t.Errorf("FilePath = %q, want %q", out.FilePath, fp)
		}
		if out.OriginalSize != int64(len(data)) {
			t.Errorf("OriginalSize = %d, want %d", out.OriginalSize, len(data))
		}
	case fileread.PDFOutput:
		if out.Type != "pdf" {
			t.Errorf("PDFOutput.Type = %q, want pdf", out.Type)
		}
		if out.FilePath != fp {
			t.Errorf("FilePath = %q, want %q", out.FilePath, fp)
		}
		if out.OriginalSize != int64(len(data)) {
			t.Errorf("OriginalSize = %d, want %d", out.OriginalSize, len(data))
		}
	default:
		t.Fatalf("Data type = %T, want PartsOutput or PDFOutput", result.Data)
	}
}

func TestExecute_PDFInvalidPages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "inv.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"abc"}`)
	_, err = fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for invalid pages param")
	}
	if !strings.Contains(err.Error(), "Invalid pages parameter") {
		t.Errorf("Error = %q, want 'Invalid pages parameter'", err.Error())
	}
}

func TestExecute_PDFPagesExceedMax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "max.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"1-25"}`)
	_, err = fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for page range exceeding max")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("Error = %q, want 'exceeds maximum'", err.Error())
	}
}

// --- Image reading ---
func TestExecute_ReadPNG(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a minimal valid PNG (1x1 red pixel)
	pngData, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==")
	fp := filepath.Join(dir, "test.png")
	if err := os.WriteFile(fp, pngData, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	imgOut, ok := result.Data.(fileread.ImageOutput)
	if !ok {
		t.Fatalf("Data type = %T, want ImageOutput", result.Data)
	}
	if imgOut.Type != "image" {
		t.Errorf("Type = %q, want %q", imgOut.Type, "image")
	}
	if imgOut.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want %q", imgOut.MimeType, "image/png")
	}
	if imgOut.Base64 == "" {
		t.Error("Base64 is empty")
	}
	// Verify base64 decodes to valid image data
	_, err = base64.StdEncoding.DecodeString(imgOut.Base64)
	if err != nil {
		t.Fatalf("Base64 decode error: %v", err)
	}
	if imgOut.OriginalWidth != 1 || imgOut.OriginalHeight != 1 {
		t.Errorf("Dimensions = %dx%d, want 1x1", imgOut.OriginalWidth, imgOut.OriginalHeight)
	}
	if imgOut.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", imgOut.FilePath, fp)
	}
}

// --- Image empty file ---
func TestExecute_ImageEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(fp, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for empty image")
	}
	if !strings.Contains(err.Error(), "empty image file") {
		t.Errorf("Error = %q, want 'empty image file'", err.Error())
	}
}

// Coverage: image error paths
// ---------------------------------------------------------------------------

func TestExecute_ImageResizedWhenOversized(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a 3000x3000 image (exceeds 2000x2000 max)
	img := image.NewRGBA(image.Rect(0, 0, 3000, 3000))
	for y := 0; y < 3000; y++ {
		for x := 0; x < 3000; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(dir, "large.png")
	if err := os.WriteFile(fp, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	imgOut, ok := result.Data.(fileread.ImageOutput)
	if !ok {
		t.Fatalf("Data type = %T, want ImageOutput", result.Data)
	}
	// Original dimensions should be 3000x3000
	if imgOut.OriginalWidth != 3000 || imgOut.OriginalHeight != 3000 {
		t.Errorf("Original dimensions = %dx%d, want 3000x3000", imgOut.OriginalWidth, imgOut.OriginalHeight)
	}
	// Display dimensions should be <= 2000x2000 (resized)
	if imgOut.DisplayWidth > 2000 || imgOut.DisplayHeight > 2000 {
		t.Errorf("Display dimensions = %dx%d, should be <= 2000x2000 after resize", imgOut.DisplayWidth, imgOut.DisplayHeight)
	}
	// Aspect ratio should be maintained (square image → still square)
	if imgOut.DisplayWidth != imgOut.DisplayHeight {
		t.Errorf("Aspect ratio not maintained: %dx%d", imgOut.DisplayWidth, imgOut.DisplayHeight)
	}
}

func TestExecute_ImageNotResizedWhenWithinLimits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a 100x100 image (within 2000x2000 limits)
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(dir, "small.png")
	if err := os.WriteFile(fp, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	imgOut := result.Data.(fileread.ImageOutput)
	if imgOut.DisplayWidth != 100 || imgOut.DisplayHeight != 100 {
		t.Errorf("Display dimensions = %dx%d, want 100x100 (no resize needed)", imgOut.DisplayWidth, imgOut.DisplayHeight)
	}
}

func TestExecute_ImageDecodeError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "corrupt.png")
	// Write a .png with invalid image data
	if err := os.WriteFile(fp, []byte("not a real image"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should reject corrupt image file")
	}
	if !strings.Contains(err.Error(), "decode image") {
		t.Errorf("Error = %q, want 'decode image' error", err.Error())
	}
}

// ---------------------------------------------------------------------------
// P1 #1: Null byte detection in offset/limit path
// ---------------------------------------------------------------------------

func TestExecute_NullBytesWithOffsetLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "binary_with_offset.bin")
	// Write file with a null byte in the second line
	if err := os.WriteFile(fp, []byte("hello\x00world\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Reading with offset/limit should detect null bytes (same as full-file path)
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":1,"limit":2}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for null bytes in offset/limit path")
	}
	if !strings.Contains(err.Error(), "null bytes") {
		t.Errorf("Error = %q, want 'null bytes' error", err.Error())
	}
}

// --- Dedup: same file read twice ---
func TestExecute_DedupSameRange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "dedup.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)

	// First read
	_, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("first Execute() error: %v", err)
	}

	// Second read same file - should return file_unchanged
	result2, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("second Execute() error: %v", err)
	}
	unchanged, ok := result2.Data.(fileread.FileUnchangedOutput)
	if !ok {
		t.Fatalf("second Data type = %T, want FileUnchangedOutput", result2.Data)
	}
	if unchanged.Type != "file_unchanged" {
		t.Errorf("Type = %q, want %q", unchanged.Type, "file_unchanged")
	}
}

// --- Dedup skipped for partial view ---
func TestExecute_DedupPartialNotSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "partial.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":1,"limit":1}`)

	// First read with limit
	_, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("first Execute() error: %v", err)
	}

	// Second read with limit - should NOT dedup (partial view)
	result2, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("second Execute() error: %v", err)
	}
	textOut, ok := result2.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput (partial not deduped)", result2.Data)
	}
	if textOut.Type != "text" {
		t.Errorf("Type = %q, want %q (partial view not deduped)", textOut.Type, "text")
	}
}

// --- Dedup skipped for different offset/limit ---
func TestExecute_DedupDifferentOffset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "offset.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\nline3\nline4\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}

	// Read with offset=1
	input1 := json.RawMessage(`{"file_path":"` + fp + `","offset":1}`)
	_, err := fileread.Execute(context.Background(), input1, tctx)
	if err != nil {
		t.Fatalf("first Execute() error: %v", err)
	}

	// Read with offset=2 - should NOT dedup
	input2 := json.RawMessage(`{"file_path":"` + fp + `","offset":2}`)
	result2, err := fileread.Execute(context.Background(), input2, tctx)
	if err != nil {
		t.Fatalf("second Execute() error: %v", err)
	}
	textOut, ok := result2.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result2.Data)
	}
	if textOut.Type != "text" {
		t.Errorf("Type = %q, want %q (different offset not deduped)", textOut.Type, "text")
	}
}

// ---------------------------------------------------------------------------
// RenderResult — human-readable output for TUI
// ---------------------------------------------------------------------------

func TestRenderResult_TextOutput(t *testing.T) {
	t.Parallel()
	tt := fileread.New()
	result := tt.RenderResult(&fileread.TextOutput{
		Type:       "text",
		FilePath:   "/tmp/test.go",
		Content:    "package main\nfunc main() {}\n",
		NumLines:   2,
		StartLine:  1,
		TotalLines: 2,
	})
	want := "package main\nfunc main() {}\n"
	if result != want {
		t.Errorf("RenderResult(TextOutput) = %q, want %q", result, want)
	}
}

func TestRenderResult_ImageOutput(t *testing.T) {
	t.Parallel()
	tt := fileread.New()
	result := tt.RenderResult(&fileread.ImageOutput{
		Type:           "image",
		FilePath:       "/tmp/img.png",
		OriginalWidth:  800,
		OriginalHeight: 600,
	})
	want := "Image: /tmp/img.png (800x600)"
	if result != want {
		t.Errorf("RenderResult(ImageOutput) = %q, want %q", result, want)
	}
}

func TestRenderResult_FileUnchanged(t *testing.T) {
	t.Parallel()
	tt := fileread.New()
	result := tt.RenderResult(&fileread.FileUnchangedOutput{
		Type:     "file_unchanged",
		FilePath: "/tmp/test.go",
	})
	want := "File unchanged: /tmp/test.go"
	if result != want {
		t.Errorf("RenderResult(FileUnchangedOutput) = %q, want %q", result, want)
	}
}
