// Package fileread implements the FileRead tool for reading file contents.
//
// Source reference: tools/FileReadTool/FileReadTool.ts
// 1:1 port from the TypeScript source.
package fileread

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

const (
	PDFMaxPagesPerRead          = 20
	PDFAtMentionInlineThreshold = 10
	PDFTargetRawSize            = 20 * 1024 * 1024  // 20 MB
	PDFMaxExtractSize           = 100 * 1024 * 1024 // 100 MB
)

// Source: FileReadTool.ts — apiLimits.ts IMAGE_MAX_WIDTH/IMAGE_MAX_HEIGHT
const (
	IMAGE_MAX_WIDTH  = 2000
	IMAGE_MAX_HEIGHT = 2000
)

var imageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
}

// Device files that would hang the process: infinite output or blocking input.
// Source: FileReadTool.ts — BLOCKED_DEVICE_PATHS.
var blockedDevicePaths = map[string]bool{
	"/dev/zero":    true,
	"/dev/urandom": true,
	"/dev/random":  true,
	"/dev/full":    true,
	"/dev/stdin":   true,
	"/dev/tty":     true,
	"/dev/console": true,
	"/dev/stdout":  true,
	"/dev/stderr":  true,
	"/dev/fd/0":    true,
	"/dev/fd/1":    true,
	"/dev/fd/2":    true,
}

// isBlockedDevicePath checks if a path is a blocked device or its alias.
// Source: FileReadTool.ts — isBlockedDevicePath function.
func isBlockedDevicePath(filePath string) bool {
	if blockedDevicePaths[filePath] {
		return true
	}
	// /proc/self/fd/0-2 and /proc/<pid>/fd/0-2 are Linux aliases for stdio
	if strings.HasPrefix(filePath, "/proc/") &&
		(strings.HasSuffix(filePath, "/fd/0") ||
			strings.HasSuffix(filePath, "/fd/1") ||
			strings.HasSuffix(filePath, "/fd/2")) {
		return true
	}
	return false
}

// Source: FileReadTool.ts — hasBinaryExtension from constants/files.js.
// .pdf, .png, .jpg, .jpeg, .gif, .webp removed for special handling.
var binaryExtensions = map[string]bool{
	".ico":    true,
	".bmp":    true,
	".svg":    true,
	".mp3":    true,
	".mp4":    true,
	".wav":    true,
	".avi":    true,
	".mov":    true,
	".mkv":    true,
	".zip":    true,
	".tar":    true,
	".gz":     true,
	".bz2":    true,
	".xz":     true,
	".pdf":    true, // kept in binaryExtensions but has special PDF handling
	".doc":    true,
	".docx":   true,
	".xls":    true,
	".xlsx":   true,
	".ppt":    true,
	".pptx":   true,
	".exe":    true,
	".dll":    true,
	".so":     true,
	".dylib":  true,
	".a":      true,
	".o":      true,
	".obj":    true,
	".class":  true,
	".pyc":    true,
	".par":    true,
	".pickle": true,
	".whl":    true,
}

// Input is the file read tool input schema.
// Source: FileReadTool.ts — Zod schema for file read input.
type Input struct {
	FilePath string `json:"file_path" validate:"required"`
	Offset   int    `json:"offset,omitempty"` // 1-indexed line number to start from
	Limit    int    `json:"limit,omitempty"`  // max number of lines to read
	Pages    string `json:"pages,omitempty"`  // PDF page range: "5", "1-10", "3-"
}

// PageRange represents a page range for PDF reading.
type PageRange struct {
	FirstPage int
	LastPage  int
}

// parsePDFPageRange parses a page range string like "5", "1-10", "3-".
// Returns nil for invalid ranges.
func parsePDFPageRange(pages string) *PageRange {
	if pages == "" {
		return nil
	}

	// Single page: "5"
	if !strings.Contains(pages, "-") {
		var pageNum int
		if _, err := fmt.Sscanf(pages, "%d", &pageNum); err != nil || pageNum <= 0 {
			return nil
		}
		return &PageRange{FirstPage: pageNum, LastPage: pageNum}
	}

	// Range: "1-10" or "3-"
	parts := strings.Split(pages, "-")
	if len(parts) != 2 {
		return nil
	}

	firstPage := 0
	lastPage := 0

	if parts[0] != "" {
		if _, err := fmt.Sscanf(parts[0], "%d", &firstPage); err != nil || firstPage <= 0 {
			return nil
		}
	} else {
		return nil
	}

	if parts[1] != "" {
		if _, err := fmt.Sscanf(parts[1], "%d", &lastPage); err != nil || lastPage <= 0 {
			return nil
		}
		if lastPage < firstPage {
			return nil
		}
	} else {
		// "3-" means to end of document
		lastPage = math.MaxInt
	}

	return &PageRange{FirstPage: firstPage, LastPage: lastPage}
}

// Output is the file read tool output interface.
// Source: FileReadTool.ts — discriminated output union.
type Output interface{ output() }

// TextOutput represents normal text file output.
type TextOutput struct {
	Type       string `json:"type"`
	FilePath   string `json:"filePath"`
	Content    string `json:"content"`
	NumLines   int    `json:"numLines"`
	StartLine  int    `json:"startLine"`
	TotalLines int    `json:"totalLines"`
}

func (TextOutput) output() {}

// ImageOutput represents image file output.
type ImageOutput struct {
	Type           string `json:"type"`
	FilePath       string `json:"filePath"`
	Base64         string `json:"base64"`
	MimeType       string `json:"mimeType"`
	OriginalSize   int64  `json:"originalSize"`
	OriginalWidth  int    `json:"originalWidth"`
	OriginalHeight int    `json:"originalHeight"`
	DisplayWidth   int    `json:"displayWidth"`
	DisplayHeight  int    `json:"displayHeight"`
}

func (ImageOutput) output() {}

// PDFOutput represents PDF file output.
type PDFOutput struct {
	Type         string `json:"type"`
	FilePath     string `json:"filePath"`
	Base64       string `json:"base64"`
	OriginalSize int64  `json:"originalSize"`
}

func (PDFOutput) output() {}

// PartsOutput represents extracted PDF page images.
type PartsOutput struct {
	Type         string `json:"type"`
	FilePath     string `json:"filePath"`
	OriginalSize int64  `json:"originalSize"`
	Count        int    `json:"count"`
	OutputDir    string `json:"outputDir"`
}

func (PartsOutput) output() {}

// PDFError represents a PDF processing error.
type PDFError struct {
	Reason  string
	Message string
}

func (e *PDFError) Error() string {
	return e.Message
}

// isPdftoppmAvailable checks if pdftoppm is available.
func isPdftoppmAvailable() bool {
	cmd := exec.Command("pdftoppm", "-v")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

var checkPdftoppm = sync.OnceValue(isPdftoppmAvailable)

// getPDFPageCount returns page count via pdfinfo command.
func getPDFPageCount(filePath string) int {
	cmd := exec.Command("pdfinfo", filePath)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	// Parse "Pages: N" from output
	for line := range strings.SplitSeq(string(output), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				n, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
				return n
			}
		}
	}
	return 0
}

// extractPDFPages extracts PDF pages as JPEG images using pdftoppm.
// Returns the output directory and count of pages extracted.
func extractPDFPages(filePath string, firstPage, lastPage int) (string, int, error) {
	tmpDir, err := os.MkdirTemp("", "pdf-extract-*")
	if err != nil {
		return "", 0, err
	}
	prefix := filepath.Join(tmpDir, "page")
	args := []string{"-jpeg", "-r", "100"}
	if firstPage > 0 {
		args = append(args, "-f", strconv.Itoa(firstPage))
	}
	if lastPage > 0 && lastPage != math.MaxInt {
		args = append(args, "-l", strconv.Itoa(lastPage))
	}
	args = append(args, filePath, prefix)

	cmd := exec.Command("pdftoppm", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, fmt.Errorf("pdftoppm failed: %s", string(output))
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, err
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jpg") {
			count++
		}
	}
	return tmpDir, count, nil
}

// FileUnchangedOutput represents a deduplication stub when file hasn't changed.
type FileUnchangedOutput struct {
	Type     string `json:"type"`
	FilePath string `json:"filePath"`
}

func (FileUnchangedOutput) output() {}

// New creates the FileRead tool.
// Source: tools/FileReadTool/FileReadTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to read."
			},
			"offset": {
				"type": "integer",
				"description": "Line number to start reading from (1-indexed). Only provide if the file is too large to read at once."
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of lines to read. Only provide if the file is too large to read at once."
			},
			"pages": {
				"type": "string",
				"description": "PDF page range: '5' for single page, '1-10' for range, '3-' for from page to end."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Read",
		Aliases_:     []string{"fileread", "read", "cat"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Read a file from the filesystem", nil
			}
			return in.FilePath, nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return true // reading is always read-only
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true // reading is concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: -1, // -1 = no truncation (TS: Infinity)
		Prompt_:            fileReadPrompt(),
		RenderResult_:      renderResult,
	})
}

// renderResult converts tool output to a human-readable string for the TUI.
func renderResult(data any) string {
	switch out := data.(type) {
	case *TextOutput:
		return out.Content
	case TextOutput:
		return out.Content
	case *ImageOutput:
		return fmt.Sprintf("Image: %s (%dx%d)", out.FilePath, out.OriginalWidth, out.OriginalHeight)
	case ImageOutput:
		return fmt.Sprintf("Image: %s (%dx%d)", out.FilePath, out.OriginalWidth, out.OriginalHeight)
	case *PDFOutput:
		return fmt.Sprintf("PDF: %s (%d bytes)", out.FilePath, out.OriginalSize)
	case PDFOutput:
		return fmt.Sprintf("PDF: %s (%d bytes)", out.FilePath, out.OriginalSize)
	case *PartsOutput:
		return fmt.Sprintf("PDF: %s (%d pages extracted)", out.FilePath, out.Count)
	case PartsOutput:
		return fmt.Sprintf("PDF: %s (%d pages extracted)", out.FilePath, out.Count)
	case *FileUnchangedOutput:
		return fmt.Sprintf("File unchanged: %s", out.FilePath)
	case FileUnchangedOutput:
		return fmt.Sprintf("File unchanged: %s", out.FilePath)
	default:
		b, _ := json.Marshal(data)
		return string(b)
	}
}

// countLines returns total line count for a file path.
func countTotalLines(filePath string) (int, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, err
	}
	count := strings.Count(string(data), "\n")
	return count, nil
}

// getMtimeMs returns the modification time in milliseconds.
func getMtimeMs(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return info.ModTime().UnixMilli(), nil
}

// expandPath returns an absolute path for deduplication key.
// Source: FileReadTool.ts — expandPath utility.
func expandPath(filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	if strings.HasPrefix(filePath, "~/") {
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, filePath[2:])
		}
	}
	abs, _ := filepath.Abs(filePath)
	return abs
}

// getMimeType returns the MIME type for an image extension.
func getMimeType(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg":
		return "image/jpeg"
	case ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// Execute reads a file and returns its contents.
// Source: FileReadTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	// SECURITY: Check for blocked device paths to prevent hanging
	if isBlockedDevicePath(in.FilePath) {
		return nil, fmt.Errorf("cannot read device file: %s", in.FilePath)
	}

	info, err := os.Stat(in.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file does not exist: %s", in.FilePath)
		}
		return nil, fmt.Errorf("stat file: %w", err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %s", in.FilePath)
	}

	ext := strings.ToLower(filepath.Ext(in.FilePath))

	if imageExtensions[ext] {
		return executeImage(in, info)
	}

	if ext == ".pdf" {
		return executePDF(ctx, in, info)
	}

	if binaryExtensions[ext] {
		return nil, fmt.Errorf("file has binary extension %s and cannot be read as text: %s", ext, in.FilePath)
	}

	// Text file handling with deduplication
	return executeTextFile(ctx, in, info, tctx)
}

// executeImage handles image file reading.
// Source: FileReadTool.ts — image handling with resize via imageResizer.ts
func executeImage(in Input, info os.FileInfo) (*tool.ToolResult, error) {
	data, err := os.ReadFile(in.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("empty image file: %s", in.FilePath)
	}

	// Decode full image to get dimensions and pixel data
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	origWidth := img.Bounds().Dx()
	origHeight := img.Bounds().Dy()
	displayWidth := origWidth
	displayHeight := origHeight
	outputData := data

	if origWidth > IMAGE_MAX_WIDTH || origHeight > IMAGE_MAX_HEIGHT {
		ratio := math.Min(float64(IMAGE_MAX_WIDTH)/float64(origWidth), float64(IMAGE_MAX_HEIGHT)/float64(origHeight))
		displayWidth = int(float64(origWidth) * ratio)
		displayHeight = int(float64(origHeight) * ratio)

		resized := resizeImage(img, displayWidth, displayHeight)

		var buf bytes.Buffer
		switch format {
		case "jpeg":
			if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 85}); err != nil {
				return nil, fmt.Errorf("encode resized image: %w", err)
			}
		default: // png, gif, and everything else encode as png
			if err := png.Encode(&buf, resized); err != nil {
				return nil, fmt.Errorf("encode resized image: %w", err)
			}
		}
		outputData = buf.Bytes()
	}

	ext := strings.ToLower(filepath.Ext(in.FilePath))

	output := ImageOutput{
		Type:           "image",
		FilePath:       in.FilePath,
		Base64:         base64.StdEncoding.EncodeToString(outputData),
		MimeType:       getMimeType(ext),
		OriginalSize:   info.Size(),
		OriginalWidth:  origWidth,
		OriginalHeight: origHeight,
		DisplayWidth:   displayWidth,
		DisplayHeight:  displayHeight,
	}

	return &tool.ToolResult{Data: output}, nil
}

// executePDF handles PDF file reading.
func executePDF(ctx context.Context, in Input, info os.FileInfo) (*tool.ToolResult, error) {
	if info.Size() == 0 {
		return nil, &PDFError{
			Reason:  "empty_file",
			Message: "PDF file is empty",
		}
	}

	// Validate PDF magic bytes — reject files that aren't actually PDFs
	// Source: pdf.ts — readPDF checks %PDF- header
	if info.Size() >= 5 {
		header := make([]byte, 5)
		f, err := os.Open(in.FilePath)
		if err == nil {
			_, _ = f.Read(header)
			_ = f.Close()
			if !bytes.HasPrefix(header, []byte("%PDF-")) {
				return nil, fmt.Errorf("file is not a valid PDF (missing %%PDF- header): %s", in.FilePath)
			}
		}
	}

	// Source: pdf.ts — extractPDFPages checks pdftoppm stderr for "password"
	if info.Size() >= 20 {
		data, err := os.ReadFile(in.FilePath)
		if err == nil && isPDFEncrypted(data) {
			return nil, &PDFError{
				Reason:  "password_protected",
				Message: "PDF is password-protected. Please provide an unprotected version.",
			}
		}
	}

	if info.Size() > PDFTargetRawSize && in.Pages == "" {
		return nil, &PDFError{
			Reason:  "file_too_large",
			Message: fmt.Sprintf("PDF file is larger than %d bytes. Use pages parameter to read specific ranges.", PDFTargetRawSize),
		}
	}

	// Parse pages parameter if provided
	var pageRange *PageRange
	if in.Pages != "" {
		pageRange = parsePDFPageRange(in.Pages)
		if pageRange == nil {
			return nil, &ToolError{
				Code:    7,
				Message: fmt.Sprintf("Invalid pages parameter: %s. Use formats like '1-5', '3', or '10-20'.", in.Pages),
			}
		}

		// Clamp last page to PDFMaxPagesPerRead pages
		if pageRange.LastPage == math.MaxInt {
			// Open-ended ranges (e.g. "1-") always exceed the max
			return nil, &ToolError{
				Code:    8,
				Message: fmt.Sprintf("Page range '%s' exceeds maximum of 20 pages per request.", in.Pages),
			}
		}
		count := pageRange.LastPage - pageRange.FirstPage + 1
		if count > PDFMaxPagesPerRead {
			return nil, &ToolError{
				Code:    8,
				Message: fmt.Sprintf("Page range '%s' exceeds maximum of 20 pages per request.", in.Pages),
			}
		}
	}

	// Use pdftoppm for page extraction if available
	if checkPdftoppm() {
		if pageRange != nil {
			tmpDir, count, err := extractPDFPages(in.FilePath, pageRange.FirstPage, pageRange.LastPage)
			if err != nil {
				return nil, fmt.Errorf("extract PDF pages: %w", err)
			}
			return &tool.ToolResult{Data: PartsOutput{
				Type:         "parts",
				FilePath:     in.FilePath,
				OriginalSize: info.Size(),
				Count:        count,
				OutputDir:    tmpDir,
			}}, nil
		}

		if info.Size() > PDFTargetRawSize {
			tmpDir, count, err := extractPDFPages(in.FilePath, 0, math.MaxInt)
			if err != nil {
				return nil, fmt.Errorf("extract PDF pages: %w", err)
			}
			return &tool.ToolResult{Data: PartsOutput{
				Type:         "parts",
				FilePath:     in.FilePath,
				OriginalSize: info.Size(),
				Count:        count,
				OutputDir:    tmpDir,
			}}, nil
		}
	}

	// Fallback: return full PDF as base64
	data, err := os.ReadFile(in.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read PDF file: %w", err)
	}

	output := PDFOutput{
		Type:         "pdf",
		FilePath:     in.FilePath,
		Base64:       base64.StdEncoding.EncodeToString(data),
		OriginalSize: info.Size(),
	}

	return &tool.ToolResult{Data: output}, nil
}

// executeTextFile handles text file reading with deduplication.
func executeTextFile(ctx context.Context, in Input, info os.FileInfo, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	// Use absolute path for ReadFileState key (deduplication)
	fullPath := expandPath(in.FilePath)

	// Check deduplication if tctx is provided
	if tctx != nil && tctx.ReadFileState != nil {
		mtimeMs, err := getMtimeMs(in.FilePath)
		if err == nil {
			if existingState, ok := tctx.ReadFileState[fullPath]; ok {
				// Dedup only if: same offset, same limit, same mtime, NOT partial view
				if existingState.Offset == in.Offset &&
					existingState.Limit == in.Limit &&
					existingState.Timestamp == mtimeMs &&
					!existingState.IsPartialView {
					return &tool.ToolResult{
						Data: FileUnchangedOutput{
							Type:     "file_unchanged",
							FilePath: in.FilePath,
						},
					}, nil
				}
			}
		}
	}

	// Normalize offset: 0 or 1 both mean start from line 1
	offset := max(in.Offset, 1)

	var output Output
	var content string
	var totalLines int
	isPartialView := in.Limit > 0

	// If no offset/limit, read the whole file
	if in.Offset == 0 && in.Limit == 0 {
		data, err := os.ReadFile(in.FilePath)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}

		// Check for null bytes (binary content indicator)
		if slices.Contains(data, 0) {
			return nil, fmt.Errorf("file contains binary data (null bytes): %s", in.FilePath)
		}

		content = string(data)
		totalLines = strings.Count(content, "\n")
		if content != "" && !strings.HasSuffix(content, "\n") {
			totalLines++
		}

		output = TextOutput{
			Type:       "text",
			FilePath:   in.FilePath,
			Content:    content,
			NumLines:   totalLines,
			StartLine:  1,
			TotalLines: totalLines,
		}
	} else {
		// Read with line range — single read like TS readFileInRange fast path
		data, err := os.ReadFile(in.FilePath)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}

		// Check for null bytes (binary content indicator) — same as full-file path
		if slices.Contains(data, 0) {
			return nil, fmt.Errorf("file contains binary data (null bytes): %s", in.FilePath)
		}

		text := string(data)
		allLines := strings.Split(text, "\n")

		// Compute total lines (trailing empty from final newline doesn't count)
		totalLines = len(allLines)
		if text != "" && strings.HasSuffix(text, "\n") {
			totalLines--
		}
		if text == "" {
			totalLines = 0
		}

		// Extract the requested range (offset is 1-indexed)
		start := max(offset-1, 0)
		end := totalLines
		if in.Limit > 0 && start+in.Limit < end {
			end = start + in.Limit
		}
		if start > totalLines {
			start = totalLines
		}

		selectedLines := allLines[start:end]
		content = strings.Join(selectedLines, "\n")

		output = TextOutput{
			Type:       "text",
			FilePath:   in.FilePath,
			Content:    content,
			NumLines:   len(selectedLines),
			StartLine:  offset,
			TotalLines: totalLines,
		}
	}

	if tctx != nil {
		if tctx.ReadFileState == nil {
			tctx.ReadFileState = make(map[string]types.FileState)
		}
		mtimeMs, _ := getMtimeMs(in.FilePath)
		tctx.ReadFileState[fullPath] = types.FileState{
			Content:       content,
			Timestamp:     mtimeMs,
			Offset:        in.Offset,
			Limit:         in.Limit,
			IsPartialView: isPartialView,
		}
	}

	return &tool.ToolResult{Data: output}, nil
}

// ToolError represents a structured tool error with code.
type ToolError struct {
	Code    int
	Message string
}

func (e *ToolError) Error() string {
	return e.Message
}

// resizeImage scales an image to the target dimensions using nearest-neighbor.
// Source: FileReadTool.ts — imageResizer.ts resize logic (sharp replacement).
func resizeImage(src image.Image, newWidth, newHeight int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	for y := range newHeight {
		for x := range newWidth {
			srcX := x * srcW / newWidth
			srcY := y * srcH / newHeight
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}

// isPDFEncrypted checks if PDF raw bytes contain an /Encrypt dictionary,
// indicating password protection or usage restrictions.
// Source: pdf.ts — extractPDFPages checks pdftoppm stderr for "password";
// this provides earlier detection without needing pdftoppm.
func isPDFEncrypted(data []byte) bool {
	return bytes.Contains(data, []byte("/Encrypt"))
}
