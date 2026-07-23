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
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/liuy/gbot/pkg/markitdown"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// Text file reading limits — TS aligned: FileReadTool/limits.ts
// maxSizeBytes gates on total file size (pre-read stat), skipped when user
// provides explicit limit. maxTokens gates on estimated token count (post-read).
const (
	MaxFileReadBytes  = 256 * 1024 // 256KB — TS: MAX_OUTPUT_SIZE
	MaxFileReadTokens = 25000      // TS: DEFAULT_MAX_OUTPUT_TOKENS
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
	".pdf":    true,
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

// Extensions that markitdown can convert to markdown.
// omp: CONVERTIBLE_EXTENSIONS = [".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".rtf", ".epub"]
// Plus ".ipynb" and ".csv" which markitdown also handles.
var convertibleExtensions = map[string]bool{
	".pdf":   true,
	".doc":   true,
	".docx":  true,
	".ppt":   true,
	".pptx":  true,
	".xls":   true,
	".xlsx":  true,
	".epub":  true,
	".ipynb": true,
	".csv":   true,
	".zip":   true,
}

// Input is the file read tool input schema.
// Source: FileReadTool.ts — Zod schema for file read input.
type Input struct {
	FilePath string `json:"file_path" validate:"required"`
	Offset   int    `json:"offset,omitempty"` // 1-indexed line number to start from
	Limit    int    `json:"limit,omitempty"`  // max number of lines to read
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
				"description": "The absolute path to the file to read. For SQLite databases (.sqlite/.sqlite3/.db/.db3), append selectors after the path: ':table' for schema+sample, ':table:id' for a row by primary key, '?limit=20&offset=0' for pagination, '?where=col=\\'val\\'' for filtering, or '?q=SELECT ...' for raw SQL. For archives (.zip/.tar/.tar.gz/.7z/.rar/etc.), append ':path/inside' to read a member or list a directory."
			},
			"offset": {
				"type": "integer",
				"description": "Line number to start reading from (1-indexed). Only provide if the file is too large to read at once."
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of lines to read. Only provide if the file is too large to read at once."
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
		CheckPermissions_: func(input json.RawMessage, tctx *tool.ToolUseContext) types.PermissionResult {
			var in Input
			if json.Unmarshal(input, &in) != nil {
				return types.PermissionAllowDecision{}
			}
			if toolresult.IsToolResultPath(in.FilePath) {
				return types.PermissionAllowDecision{}
			}
			return types.PermissionAllowDecision{}
		},
		Prompt_:       fileReadPrompt(),
		RenderResult_: renderResult,
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				return nil, err
			}
			switch probe.Type {
			case "image":
				var o ImageOutput
				if err := json.Unmarshal(raw, &o); err != nil {
					return nil, err
				}
				return &o, nil
			case "file_unchanged":
				var o FileUnchangedOutput
				if err := json.Unmarshal(raw, &o); err != nil {
					return nil, err
				}
				return &o, nil
			default:
				var o TextOutput
				if err := json.Unmarshal(raw, &o); err != nil {
					return nil, err
				}
				return &o, nil
			}
		},
		IsSearchOrRead_: func(json.RawMessage) tool.SearchReadKind {
			return tool.SearchReadKind{IsRead: true}
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			out, ok := data.(ImageOutput)
			if !ok {
				if p, ok := data.(*ImageOutput); ok {
					out = *p
				} else {
					raw, _ := json.Marshal(data)
					wrapped, _ := json.Marshal(string(raw))
					return []types.ContentBlock{types.NewTextBlock(string(wrapped))}
				}
			}
			return []types.ContentBlock{types.NewImageBlock(types.ImageSource{
				Type:      "base64",
				MediaType: out.MimeType,
				Data:      out.Base64,
			})}
		},
	})
}

// renderResult converts tool output to a human-readable string for the TUI.
// Mirrors TS FileReadTool/UI.tsx renderToolResultMessage: displays a one-line
// summary ("Read N lines") rather than the full file content.
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
	case *FileUnchangedOutput:
		return fmt.Sprintf("File unchanged: %s", out.FilePath)
	case FileUnchangedOutput:
		return fmt.Sprintf("File unchanged: %s", out.FilePath)
	default:
		b, _ := json.Marshal(data)
		return string(b)
	}
}

// normalizeLineEndings converts CRLF to LF and strips lone CR.
// Windows files checked out via git often have \r\n; the lone \r would
// cause the TUI to reset the cursor mid-line when rendering tool output.
func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r", "")
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

// getMimeType is intentionally absent — the byte-truthful media type is
// derived from utils.MaybeResizeAndDownsampleImageBuffer's ResizeResult
// (which carries the actual format of the bytes it emits).

// Execute reads a file and returns its contents.
// Source: FileReadTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
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

	// SQLite: path may contain :table:key or ?q=SQL syntax. Check before
	// os.Stat — "db.sqlite:users" is not a real filesystem path.
	sqliteResult, handled, sqliteErr := trySqlitePath(ctx, in)
	if handled {
		return sqliteResult, sqliteErr
	}

	// Archive: path may contain :subpath syntax. Check before
	// os.Stat — "archive.zip:dir/file.ts" is not a real filesystem path.
	archiveResult, handled, archiveErr := tryArchivePath(ctx, in)
	if handled {
		return archiveResult, archiveErr
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

	var result *tool.ToolResult
	var execErr error
	switch {
	case imageExtensions[ext]:
		result, execErr = executeImage(in, info)
	case convertibleExtensions[ext]:
		result, execErr = executeDocument(ctx, in, info)
	case binaryExtensions[ext]:
		return nil, fmt.Errorf("file has binary extension %s and cannot be read as text: %s", ext, in.FilePath)
	default:
		result, execErr = executeTextFile(ctx, in, info, tctx)
	}

	if execErr != nil || result == nil {
		return result, execErr
	}

	return boundTextOutput(result, tctx)
}

// boundTextOutput applies a universal token limit to all text-producing paths.
// Regardless of whether the output came from executeTextFile, executeDocument,
// or any future handler, if the result contains text exceeding MaxFileReadTokens,
// it is rejected with an offset/limit hint.
// Image and other non-text outputs pass through unchanged.
func boundTextOutput(result *tool.ToolResult, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if tctx != nil && tctx.UncappedOutput {
		return result, nil
	}
	textOut, ok := result.Data.(TextOutput)
	if !ok {
		return result, nil
	}
	tokens := types.EstimateTokens(textOut.Content)
	if tokens > MaxFileReadTokens {
		return nil, fmt.Errorf("file content (~%d tokens) exceeds maximum allowed tokens (%d). Use offset and limit parameters to read specific portions of the file",
			tokens, MaxFileReadTokens)
	}
	return result, nil
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

	// Pre-decode to (a) fail fast on undecodable inputs with a clear error
	// and (b) keep parity with the legacy code path's "decode image" error.
	// The resizer owns all downstream resize/compress/catch logic.
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(in.FilePath), "."))
	resized, resizeErr := utils.MaybeResizeAndDownsampleImageBuffer(data, len(data), ext)
	if resizeErr != nil {
		// Propagate — no best-effort passthrough (TS imageResizer.ts:414-431
		// throws ImageResizeError rather than emitting oversized bytes).
		return nil, fmt.Errorf("resize image: %w", resizeErr)
	}

	// On the success path the resizer always populates Dimensions (the only
	// nil-Dimensions sub-case lives in the catch path, which now propagates
	// as an error). Build ImageOutput directly.
	dims := resized.Dimensions
	outputData := resized.Buffer
	mediaType := "image/" + resized.MediaType

	output := ImageOutput{
		Type:           "image",
		FilePath:       in.FilePath,
		Base64:         base64.StdEncoding.EncodeToString(outputData),
		MimeType:       mediaType,
		OriginalSize:   info.Size(),
		OriginalWidth:  dims.OriginalWidth,
		OriginalHeight: dims.OriginalHeight,
		DisplayWidth:   dims.DisplayWidth,
		DisplayHeight:  dims.DisplayHeight,
	}

	return &tool.ToolResult{Data: output}, nil
}

// executeDocument converts binary documents (docx, xlsx, pptx, epub, csv, ipynb)
// to markdown via markitdown, then applies offset/limit on the converted text.
// omp: convertFileWithMarkit → #buildInMemoryTextResult pipeline.
func executeDocument(ctx context.Context, in Input, info os.FileInfo) (*tool.ToolResult, error) {
	m := markitdown.New()
	result, err := m.ConvertFile(in.FilePath)
	if err != nil {
		return nil, fmt.Errorf("convert %s: %w", strings.ToLower(filepath.Ext(in.FilePath)), err)
	}

	content := result.Markdown
	if content == "" {
		return nil, fmt.Errorf("conversion produced no output: %s", in.FilePath)
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	if content != "" && strings.HasSuffix(content, "\n") {
		totalLines--
	}
	if content == "" {
		totalLines = 0
	}

	offset := max(in.Offset, 1)
	start := max(offset-1, 0)
	end := totalLines
	if in.Limit > 0 && start+in.Limit < end {
		end = start + in.Limit
	}
	if start > totalLines {
		start = totalLines
	}

	selectedLines := lines[start:end]
	selectedContent := strings.Join(selectedLines, "\n")

	return &tool.ToolResult{Data: TextOutput{
		Type:       "text",
		FilePath:   in.FilePath,
		Content:    selectedContent,
		NumLines:   len(selectedLines),
		StartLine:  offset,
		TotalLines: totalLines,
	}}, nil
}

// executeTextFile handles text file reading with deduplication.
func executeTextFile(ctx context.Context, in Input, info os.FileInfo, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	// Pre-read size check: refuse files larger than MaxFileReadBytes (256KB)
	// when no explicit limit is provided. TS align: readFileInRange FileTooLargeError.
	// Skipped when user specifies offset/limit — they know what they're asking for.
	if in.Limit == 0 && info.Size() > MaxFileReadBytes && (tctx == nil || !tctx.UncappedOutput) {
		return nil, fmt.Errorf("file content (%s) exceeds maximum allowed size (%s). Use offset and limit parameters to read specific portions of the file",
			toolresult.FormatFileSize(int(info.Size())), toolresult.FormatFileSize(MaxFileReadBytes))
	}

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

		content = normalizeLineEndings(string(data))
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

		text := normalizeLineEndings(string(data))
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

	// Post-read token check: refuse content that exceeds MaxFileReadTokens.
	// TS align: validateContentTokens MaxFileReadTokenExceededError.
	if tokens := types.EstimateTokens(content); tokens > MaxFileReadTokens && (tctx == nil || !tctx.UncappedOutput) {
		return nil, fmt.Errorf("file content (~%d tokens) exceeds maximum allowed tokens (%d). Use offset and limit parameters to read specific portions of the file",
			tokens, MaxFileReadTokens)
	}

	if tctx != nil {
		if tctx.ReadFileState == nil {
			tctx.ReadFileState = make(map[string]tool.FileState)
		}
		mtimeMs, _ := getMtimeMs(in.FilePath)
		tctx.ReadFileState[fullPath] = tool.FileState{
			Content:       content,
			Timestamp:     mtimeMs,
			Offset:        in.Offset,
			Limit:         in.Limit,
			IsPartialView: isPartialView,
		}
	}

	return &tool.ToolResult{Data: output}, nil
}

// resizeImage was removed: superseded by utils.MaybeResizeAndDownsampleImageBuffer,
// which uses draw.CatmullRom.Scale (high-quality) instead of nearest-neighbor.
