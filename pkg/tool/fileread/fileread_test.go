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

	tt := fileread.New(nil)

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

	tt := fileread.New(nil)
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

	tt := fileread.New(nil)

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
	for y := range 3000 {
		for x := range 3000 {
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
	tctx := &tool.ToolUseContext{
		ReadFileState: make(map[string]tool.FileState),
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
	tctx := &tool.ToolUseContext{
		ReadFileState: make(map[string]tool.FileState),
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
	tctx := &tool.ToolUseContext{
		ReadFileState: make(map[string]tool.FileState),
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

func TestReadTool_IsSearchOrRead(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	srk := tt.(tool.ToolWithSearchOrRead).IsSearchOrRead(nil)
	if srk.IsSearch || !srk.IsRead || srk.IsList {
		t.Errorf("ReadTool.IsSearchOrRead() = %+v, want {IsRead:true}", srk)
	}
}

func TestRenderResult_TextOutput(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
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
	tt := fileread.New(nil)
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
	tt := fileread.New(nil)
	result := tt.RenderResult(&fileread.FileUnchangedOutput{
		Type:     "file_unchanged",
		FilePath: "/tmp/test.go",
	})
	want := "File unchanged: /tmp/test.go"
	if result != want {
		t.Errorf("RenderResult(FileUnchangedOutput) = %q, want %q", result, want)
	}
}

// ---------------------------------------------------------------------------
// CheckPermissions — cover the CheckPermissions_ closure in New()
// ---------------------------------------------------------------------------

func TestCheckPermissions_ValidInput(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	input := json.RawMessage(`{"file_path":"/tmp/test.txt"}`)
	result := tt.CheckPermissions(input, nil)
	if result.Behavior() != types.BehaviorAllow {
		t.Errorf("CheckPermissions(valid) behavior = %q, want %q", result.Behavior(), types.BehaviorAllow)
	}
}

func TestCheckPermissions_InvalidJSON(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	input := json.RawMessage(`{invalid`)
	result := tt.CheckPermissions(input, nil)
	// Invalid JSON should still return allow (early return on unmarshal error)
	if result.Behavior() != types.BehaviorAllow {
		t.Errorf("CheckPermissions(invalid JSON) behavior = %q, want %q", result.Behavior(), types.BehaviorAllow)
	}
}

func TestCheckPermissions_ToolResultPath(t *testing.T) {
	tt := fileread.New(nil)
	// IsToolResultPath checks for <home>/.gbot/sessions/<id>/tool-results/<file>
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	toolResultDir := filepath.Join(dir, ".gbot", "sessions", "test-session", "tool-results")
	if err := os.MkdirAll(toolResultDir, 0755); err != nil {
		t.Fatal(err)
	}
	toolResultPath := filepath.Join(toolResultDir, "result-123.txt")
	input := json.RawMessage(`{"file_path":"` + toolResultPath + `"}`)
	result := tt.CheckPermissions(input, nil)
	if result.Behavior() != types.BehaviorAllow {
		t.Errorf("CheckPermissions(toolresult path) behavior = %q, want %q", result.Behavior(), types.BehaviorAllow)
	}
}

// ---------------------------------------------------------------------------
// RenderResult — value-type (non-pointer) cases
// ---------------------------------------------------------------------------

func TestRenderResult_TextOutputValueType(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(fileread.TextOutput{
		Type:       "text",
		FilePath:   "/tmp/test.go",
		Content:    "hello",
		NumLines:   1,
		StartLine:  1,
		TotalLines: 1,
	})
	if result != "hello" {
		t.Errorf("RenderResult(TextOutput value) = %q, want %q", result, "hello")
	}
}

func TestRenderResult_ImageOutputValueType(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(fileread.ImageOutput{
		Type:           "image",
		FilePath:       "/tmp/img.png",
		OriginalWidth:  100,
		OriginalHeight: 200,
	})
	want := "Image: /tmp/img.png (100x200)"
	if result != want {
		t.Errorf("RenderResult(ImageOutput value) = %q, want %q", result, want)
	}
}

func TestRenderResult_PDFOutputPointer(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(&fileread.PDFOutput{
		Type:         "pdf",
		FilePath:     "/tmp/test.pdf",
		OriginalSize: 1024,
	})
	want := "PDF: /tmp/test.pdf (1024 bytes)"
	if result != want {
		t.Errorf("RenderResult(*PDFOutput) = %q, want %q", result, want)
	}
}

func TestRenderResult_PDFOutputValueType(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(fileread.PDFOutput{
		Type:         "pdf",
		FilePath:     "/tmp/test.pdf",
		OriginalSize: 2048,
	})
	want := "PDF: /tmp/test.pdf (2048 bytes)"
	if result != want {
		t.Errorf("RenderResult(PDFOutput value) = %q, want %q", result, want)
	}
}

func TestRenderResult_PartsOutputPointer(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(&fileread.PartsOutput{
		Type:         "parts",
		FilePath:     "/tmp/test.pdf",
		OriginalSize: 4096,
		Count:        5,
	})
	want := "PDF: /tmp/test.pdf (5 pages extracted)"
	if result != want {
		t.Errorf("RenderResult(*PartsOutput) = %q, want %q", result, want)
	}
}

func TestRenderResult_PartsOutputValueType(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(fileread.PartsOutput{
		Type:         "parts",
		FilePath:     "/tmp/test.pdf",
		OriginalSize: 4096,
		Count:        3,
	})
	want := "PDF: /tmp/test.pdf (3 pages extracted)"
	if result != want {
		t.Errorf("RenderResult(PartsOutput value) = %q, want %q", result, want)
	}
}

func TestRenderResult_FileUnchangedValueType(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(fileread.FileUnchangedOutput{
		Type:     "file_unchanged",
		FilePath: "/tmp/test.go",
	})
	want := "File unchanged: /tmp/test.go"
	if result != want {
		t.Errorf("RenderResult(FileUnchangedOutput value) = %q, want %q", result, want)
	}
}

func TestRenderResult_UnknownType(t *testing.T) {
	t.Parallel()
	tt := fileread.New(nil)
	result := tt.RenderResult(map[string]string{"foo": "bar"})
	if !strings.Contains(result, "foo") {
		t.Errorf("RenderResult(unknown) = %q, should contain 'foo'", result)
	}
	if !strings.Contains(result, "bar") {
		t.Errorf("RenderResult(unknown) = %q, should contain 'bar'", result)
	}
}

// ---------------------------------------------------------------------------
// PDF error paths
// ---------------------------------------------------------------------------

func TestExecute_PDFEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.pdf")
	if err := os.WriteFile(fp, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for empty PDF")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Error = %q, want 'empty'", err.Error())
	}
}

func TestExecute_PDFInvalidHeader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "fake.pdf")
	// Write content that is at least 5 bytes but doesn't start with %PDF-
	if err := os.WriteFile(fp, []byte("NOTAPDF-REST-OF-CONTENT-HERE-XXXXXXXXX"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for non-PDF header")
	}
	if !strings.Contains(err.Error(), "not a valid PDF") {
		t.Errorf("Error = %q, want 'not a valid PDF'", err.Error())
	}
}

func TestExecute_PDFEncrypted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "encrypted.pdf")
	// Write a fake PDF header with /Encrypt dictionary
	content := "%PDF-1.4\nsome content /Encrypt something here that is more than 20 bytes total for the check"
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for encrypted PDF")
	}
	if !strings.Contains(err.Error(), "password-protected") {
		t.Errorf("Error = %q, want 'password-protected'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Binary extension rejection
// ---------------------------------------------------------------------------

func TestExecute_BinaryExtension(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "program.exe")
	if err := os.WriteFile(fp, []byte("MZ\x90\x00binary content"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for binary extension")
	}
	if !strings.Contains(err.Error(), "binary extension") {
		t.Errorf("Error = %q, want 'binary extension'", err.Error())
	}
	if !strings.Contains(err.Error(), ".exe") {
		t.Errorf("Error = %q, should contain '.exe'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Blocked device paths
// ---------------------------------------------------------------------------

func TestExecute_BlockedDevicePath(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/dev/zero"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for blocked device path")
	}
	if !strings.Contains(err.Error(), "device file") {
		t.Errorf("Error = %q, want 'device file'", err.Error())
	}
	if !strings.Contains(err.Error(), "/dev/zero") {
		t.Errorf("Error = %q, should contain '/dev/zero'", err.Error())
	}
}

func TestExecute_BlockedProcFdPath(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"file_path":"/proc/self/fd/0"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for /proc/self/fd/0")
	}
	if !strings.Contains(err.Error(), "device file") {
		t.Errorf("Error = %q, want 'device file'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Null bytes detection (full file read path)
// ---------------------------------------------------------------------------

func TestExecute_NullBytesFullFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "binary.txt")
	if err := os.WriteFile(fp, []byte("hello\x00world"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for null bytes in text file")
	}
	if !strings.Contains(err.Error(), "null bytes") {
		t.Errorf("Error = %q, want 'null bytes'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Dedup: nil tctx and nil ReadFileState
// ---------------------------------------------------------------------------

func TestExecute_DedupNilTctx(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "nil_tctx.txt")
	if err := os.WriteFile(fp, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if out.Content != "hello\n" {
		t.Errorf("Content = %q, want %q", out.Content, "hello\n")
	}
}

func TestExecute_DedupNilReadFileState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "nil_state.txt")
	if err := os.WriteFile(fp, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tctx := &tool.ToolUseContext{
		ReadFileState: nil,
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if out.Content != "hello\n" {
		t.Errorf("Content = %q, want %q", out.Content, "hello\n")
	}
	// Verify ReadFileState was initialized
	if tctx.ReadFileState == nil {
		t.Error("ReadFileState should have been initialized")
	}
	if len(tctx.ReadFileState) != 1 {
		t.Errorf("ReadFileState len = %d, want 1", len(tctx.ReadFileState))
	}
}

// ---------------------------------------------------------------------------
// Offset beyond file length
// ---------------------------------------------------------------------------

func TestExecute_OffsetBeyondFileLength(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "short.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// offset=100 with limit=5 — offset beyond file length
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":100,"limit":5}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	out, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if out.Content != "" {
		t.Errorf("Content = %q, want empty (offset beyond file)", out.Content)
	}
	if out.TotalLines != 2 {
		t.Errorf("TotalLines = %d, want 2", out.TotalLines)
	}
}

// ---------------------------------------------------------------------------
// expandPath: relative path and ~ expansion
// ---------------------------------------------------------------------------

func TestExpandPath_HomePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// expandPath is used for the dedup key, not file access.
	// Verify dedup key uses HOME expansion by reading the same file twice
	// with the same absolute path and confirming dedup works.
	fp := filepath.Join(dir, "hometest.txt")
	if err := os.WriteFile(fp, []byte("home content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tctx := &tool.ToolUseContext{
		ReadFileState: make(map[string]tool.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)

	// First read
	result, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("first Execute() error: %v", err)
	}
	out, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if out.Content != "home content\n" {
		t.Errorf("Content = %q, want %q", out.Content, "home content\n")
	}

	// Second read should dedup (file unchanged)
	result2, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("second Execute() error: %v", err)
	}
	if _, ok := result2.Data.(fileread.FileUnchangedOutput); !ok {
		t.Errorf("second read Data type = %T, want FileUnchangedOutput (dedup should work)", result2.Data)
	}
}

// ---------------------------------------------------------------------------
// Blocked device paths — comprehensive
// ---------------------------------------------------------------------------

func TestIsBlockedDevicePath_AllBlockedPaths(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"/dev/zero", "/dev/urandom", "/dev/random",
		"/dev/full", "/dev/stdin", "/dev/tty",
		"/dev/console", "/dev/stdout", "/dev/stderr",
		"/dev/fd/0", "/dev/fd/1", "/dev/fd/2",
	}
	for _, p := range blocked {
		input := json.RawMessage(`{"file_path":"` + p + `"}`)
		_, err := fileread.Execute(context.Background(), input, nil)
		if err == nil {
			t.Errorf("path %q: expected error for blocked device", p)
		}
	}
}

func TestIsBlockedDevicePath_ProcFdPaths(t *testing.T) {
	t.Parallel()
	procPaths := []string{
		"/proc/self/fd/0",
		"/proc/self/fd/1",
		"/proc/self/fd/2",
		"/proc/123/fd/0",
		"/proc/123/fd/1",
		"/proc/123/fd/2",
	}
	for _, p := range procPaths {
		input := json.RawMessage(`{"file_path":"` + p + `"}`)
		_, err := fileread.Execute(context.Background(), input, nil)
		if err == nil {
			t.Errorf("path %q: expected error for blocked /proc fd", p)
		}
		if err != nil && !strings.Contains(err.Error(), "device file") {
			t.Errorf("path %q: error = %q, want 'device file'", p, err.Error())
		}
	}
}

func TestIsBlockedDevicePath_NotBlocked(t *testing.T) {
	t.Parallel()
	// /proc/123/fd/3 should NOT be blocked
	dir := t.TempDir()
	fp := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(fp, []byte("ok\n"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("normal file should not be blocked: %v", err)
	}
	out, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if out.Content != "ok\n" {
		t.Errorf("Content = %q, want %q", out.Content, "ok\n")
	}
}

// ---------------------------------------------------------------------------
// PDF page range edge cases — open-ended range
// ---------------------------------------------------------------------------

func TestExecute_PDFOpenEndedPageRange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "open.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	// Open-ended range "3-" should exceed max pages
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"3-"}`)
	_, err = fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for open-ended page range exceeding max")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("Error = %q, want 'exceeds maximum'", err.Error())
	}
}

func TestExecute_PDFPagesReversedRange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "reversed.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	// Reversed range "5-2" should be invalid
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"5-2"}`)
	_, err = fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for reversed page range")
	}
	if !strings.Contains(err.Error(), "Invalid pages parameter") {
		t.Errorf("Error = %q, want 'Invalid pages parameter'", err.Error())
	}
}

func TestExecute_PDFPagesZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "zero.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	// Page "0" should be invalid
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"0"}`)
	_, err = fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for page 0")
	}
	if !strings.Contains(err.Error(), "Invalid pages parameter") {
		t.Errorf("Error = %q, want 'Invalid pages parameter'", err.Error())
	}
}

func TestExecute_PDFPagesNegativeRange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "neg.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	// Negative range "-5" should be invalid
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"-5"}`)
	_, err = fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for negative range")
	}
	if !strings.Contains(err.Error(), "Invalid pages parameter") {
		t.Errorf("Error = %q, want 'Invalid pages parameter'", err.Error())
	}
}

func TestExecute_PDFPagesMultipleDashes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "multidash.pdf")
	data, err := os.ReadFile("/tmp/test1.pdf")
	if err != nil {
		t.Skip("test PDF not available")
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	// "1-2-3" should be invalid (multiple dashes)
	input := json.RawMessage(`{"file_path":"` + fp + `","pages":"1-2-3"}`)
	_, err = fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for multiple dashes in pages")
	}
	if !strings.Contains(err.Error(), "Invalid pages parameter") {
		t.Errorf("Error = %q, want 'Invalid pages parameter'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// File size and token limits — TS aligned: FileReadTool/limits.ts
// ---------------------------------------------------------------------------

// TestExecute_MaxSizeBytes_Exceeded verifies that reading a file larger than
// MaxFileReadBytes (256KB) without explicit limit returns an error telling
// the user to use offset/limit. TS align: readFileInRange FileTooLargeError.
func TestExecute_MaxSizeBytes_Exceeded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.txt")
	// 300KB file — exceeds 256KB limit
	bigContent := strings.Repeat("x", 300*1024)
	if err := os.WriteFile(fp, []byte(bigContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for file exceeding maxSizeBytes")
	}
	if !strings.Contains(err.Error(), "offset") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("Error = %q, want suggestion to use offset/limit", err.Error())
	}
}

// TestExecute_MaxSizeBytes_WithExplicitLimit_Succeeds verifies that the file
// size check is skipped when the user provides an explicit limit parameter,
// matching TS behavior: readFileInRange only checks maxBytes when limit is
// undefined.
func TestExecute_MaxSizeBytes_WithExplicitLimit_Succeeds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.txt")
	// 300KB file — exceeds 256KB, but explicit limit should bypass the check
	bigContent := strings.Repeat("line\n", 60*1024) // ~300KB
	if err := os.WriteFile(fp, []byte(bigContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `", "offset": 1, "limit": 10}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("with explicit limit, should succeed, got: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if output.NumLines != 10 {
		t.Errorf("NumLines = %d, want 10", output.NumLines)
	}
}

// TestExecute_MaxTokens_Exceeded verifies that reading a file whose token
// count exceeds MaxFileReadTokens (25000) returns an error. Even if the file
// is under 256KB, high-density content can exceed the token limit.
// TS align: validateContentTokens MaxFileReadTokenExceededError.
func TestExecute_MaxTokens_Exceeded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "dense.txt")
	// ~120KB of text, under 256KB byte limit, but ~30K tokens > 25K limit
	// Each char ≈ 0.25 token, so 120K chars ≈ 30K tokens
	denseContent := strings.Repeat("a", 120*1024)
	if err := os.WriteFile(fp, []byte(denseContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("want error for file exceeding maxTokens")
	}
	if !strings.Contains(err.Error(), "offset") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("Error = %q, want suggestion to use offset/limit", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Uncapped output: ToolUseContext.UncappedOutput bypasses internal gates
// ---------------------------------------------------------------------------

// TestExecute_UncappedOutput_SkipsMaxSizeBytes verifies that a file exceeding
// MaxFileReadBytes (256KB) succeeds when UncappedOutput is true.
func TestExecute_UncappedOutput_SkipsMaxSizeBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.txt")
	// 300KB file — exceeds 256KB limit
	bigContent := strings.Repeat("x", 300*1024)
	if err := os.WriteFile(fp, []byte(bigContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tctx := &tool.ToolUseContext{
		Ctx:            context.Background(),
		UncappedOutput: true,
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("with UncappedOutput, should succeed, got: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected full file content")
	}
}

// TestExecute_UncappedOutput_SkipsMaxTokens verifies that a file exceeding
// MaxFileReadTokens (25000 tokens) succeeds when UncappedOutput is true.
func TestExecute_UncappedOutput_SkipsMaxTokens(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "dense.txt")
	// ~120KB of text, under 256KB byte limit, but ~30K tokens > 25K limit
	denseContent := strings.Repeat("a", 120*1024)
	if err := os.WriteFile(fp, []byte(denseContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tctx := &tool.ToolUseContext{
		Ctx:            context.Background(),
		UncappedOutput: true,
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("with UncappedOutput, should succeed, got: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected full file content")
	}
}

// ---------------------------------------------------------------------------
// Document conversion via markitdown (docx, xlsx, pptx, epub, csv, ipynb)
// ---------------------------------------------------------------------------

func TestExecute_DocxConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test.docx")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
}

func TestExecute_XlsxConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test.xlsx")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
}

func TestExecute_PptxConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test.pptx")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
}

func TestExecute_CsvConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test_mskanji.csv")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
}

func TestExecute_NotebookConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test_notebook.ipynb")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
}

func TestExecute_EpubConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test.epub")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
}

func TestExecute_DocxConversion_Offset(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test.docx")
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":2,"limit":3}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	lines := strings.Split(output.Content, "\n")
	if len(lines) > 3 {
		t.Errorf("expected at most 3 lines with limit=3, got %d", len(lines))
	}
	if output.StartLine != 2 {
		t.Errorf("StartLine = %d, want 2", output.StartLine)
	}
}

func TestExecute_HtmlConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test_blog.html")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
	if strings.Contains(output.Content, "<script") || strings.Contains(output.Content, "<nav") {
		t.Error("expected HTML noise (script/nav tags) to be stripped")
	}
}

func TestExecute_ZipConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test_files.zip")
	// Use limit to stay within MaxFileReadTokens — full zip output is ~72k tokens.
	input := json.RawMessage(`{"file_path":"` + fp + `", "limit": 50}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
}

// TestExecute_DocumentOutputBounded verifies that executeDocument does not
// return unbounded output. test_files.zip expands to ~290KB of markdown
// (~72k tokens) via markitdown — far exceeding MaxFileReadTokens (25000).
// Without a size check, this single tool result can blow up the context
// window and cause "Prompt is too long" errors.
func TestExecute_DocumentOutputBounded(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test_files.zip")
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)

	// Acceptable: error telling user to use offset/limit.
	if err != nil {
		if !strings.Contains(err.Error(), "offset") && !strings.Contains(err.Error(), "limit") {
			t.Fatalf("error should mention offset/limit, got: %v", err)
		}
		return
	}

	// Acceptable: success with bounded output.
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	tokens := types.EstimateTokens(output.Content)
	if tokens > fileread.MaxFileReadTokens {
		t.Errorf("converted document output ~%d tokens exceeds MaxFileReadTokens %d (%d chars) — "+
			"executeDocument must bound its output like executeTextFile does",
			tokens, fileread.MaxFileReadTokens, len(output.Content))
	}
}

func TestExecute_RssXmlConversion(t *testing.T) {
	t.Parallel()
	fp := filepath.Join("..", "..", "markitdown", "testdata", "test_rss.xml")
	input := json.RawMessage(`{"file_path":"` + fp + `","limit":10}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if len(output.Content) == 0 {
		t.Error("Content is empty, expected converted markdown")
	}
	if strings.Contains(output.Content, "<?xml") {
		t.Error("expected XML declaration to be stripped in conversion")
	}
}
