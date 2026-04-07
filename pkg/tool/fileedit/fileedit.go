package fileedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

var MaxEditFileSize int64 = 1024 * 1024 * 1024

type Input struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// PatchHunk represents a single hunk in a unified diff.
// Source: FileEditTool/types.ts — hunkSchema.
type PatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

type Output struct {
	FilePath        string     `json:"filePath"`
	OldString       string     `json:"oldString"`
	NewString       string     `json:"newString"`
	ReplaceAll      bool       `json:"replaceAll"`
	OriginalFile    *string    `json:"originalFile"`
	StructuredPatch []PatchHunk `json:"structuredPatch"`
}

type fileReadResult struct {
	content    string
	fileExists bool
	hasBOM     bool
	hasCRLF    bool
	fileMode   os.FileMode
}

func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path", "old_string", "new_string"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to modify"
			},
			"old_string": {
				"type": "string",
				"description": "The text to replace. Must be unique in the file unless replace_all is true."
			},
			"new_string": {
				"type": "string",
				"description": "The text to replace it with (must be different from old_string)"
			},
			"replace_all": {
				"type": "boolean",
				"description": "Replace all occurrences of old_string. Default: false."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "FileEdit",
		Aliases_:     []string{"fileedit", "edit"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Edit a file with string replacement", nil
			}
			return fmt.Sprintf("Edit file: %s", in.FilePath), nil
		},
		Call_:              Execute,
		IsReadOnly_:        func(json.RawMessage) bool { return false },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return false },
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_:            "Performs exact string replacements in files.\n\nUsage:\n- You must read the file first before editing it.\n- When editing text from Read tool output, ensure you preserve the exact indentation.\n- The edit will FAIL if old_string is not unique in the file. Either provide more context or use replace_all.\n- Use replace_all for replacing and renaming strings across the file.",
	})
}

func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	// Expand path
	fp := in.FilePath
	if !filepath.IsAbs(fp) && !strings.HasPrefix(fp, "~/") && tctx != nil && tctx.WorkingDir != "" {
		fp = filepath.Join(tctx.WorkingDir, fp)
	}
	fullFilePath := expandPath(fp)

	if in.OldString == in.NewString {
		return nil, fmt.Errorf("no changes to make: old_string and new_string are exactly the same")
	}

	stat, statErr := os.Stat(fullFilePath)
	if statErr == nil {
		if stat.Size() > MaxEditFileSize {
			return nil, fmt.Errorf("file is too large to edit (%d bytes). Maximum editable file size is %d bytes", stat.Size(), MaxEditFileSize)
		}
	}

	fr := readFileForEdit(fullFilePath)

	// Must-read-first + staleness validation for existing files
	if fr.fileExists && tctx != nil && tctx.ReadFileState != nil {
		state, hasState := tctx.ReadFileState[fullFilePath]
		if !hasState || state.IsPartialView {
			return nil, fmt.Errorf("file has not been read yet, read it first before editing")
		}
		if info, statErr := os.Stat(fullFilePath); statErr == nil {
			if info.ModTime().UnixMilli() > state.Timestamp {
				return nil, fmt.Errorf("file has been modified since read, read it again before editing")
			}
		}
	}

	if !fr.fileExists {
		if in.OldString == "" {
			if err := os.WriteFile(fullFilePath, []byte(in.NewString), 0o644); err != nil {
				return nil, fmt.Errorf("write file: %w", err)
			}
			return &tool.ToolResult{Data: &Output{
				FilePath:   fullFilePath,
				OldString:  "",
				NewString:  in.NewString,
				ReplaceAll: false,
			}}, nil
		}
		return nil, fmt.Errorf("file does not exist: %s", fullFilePath)
	}

	if in.OldString == "" {
		if strings.TrimSpace(fr.content) != "" {
			return nil, fmt.Errorf("cannot create new file - file already exists")
		}
		if err := os.WriteFile(fullFilePath, []byte(in.NewString), fr.fileMode); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
		return &tool.ToolResult{Data: &Output{
			FilePath:   fullFilePath,
			OldString:  "",
			NewString:  in.NewString,
			ReplaceAll: false,
		}}, nil
	}

	actualOldString, found := FindActualString(fr.content, in.OldString)
	var appliedReplacements []struct{ From, To string }
	if !found {
		// Try desanitize fallback — API may have sanitized XML-like tags
		var desanitizedOld string
		desanitizedOld, appliedReplacements = desanitizeMatchString(in.OldString)
		if desanitizedOld != in.OldString && strings.Contains(fr.content, desanitizedOld) {
			actualOldString = desanitizedOld
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("string to replace not found in file.\nString: %s", in.OldString)
	}

	count := strings.Count(fr.content, actualOldString)

	if count > 1 && !in.ReplaceAll {
		return nil, fmt.Errorf("found %d matches of the string to replace, but replace_all is false. To replace all occurrences, set replace_all to true. To replace only one occurrence, please provide more context to uniquely identify the instance.\nString: %s", count, in.OldString)
	}

	actualNewString := PreserveQuoteStyle(in.OldString, actualOldString, in.NewString)

	// Apply same desanitize replacements to new_string if any were applied to old_string
	for _, r := range appliedReplacements {
		actualNewString = strings.ReplaceAll(actualNewString, r.From, r.To)
	}

	updatedContent := ApplyEditToFile(fr.content, actualOldString, actualNewString, in.ReplaceAll)

	writeContent := updatedContent
	if fr.hasCRLF {
		writeContent = strings.ReplaceAll(writeContent, "\n", "\r\n")
	}

	if fr.hasBOM {
		bom := []byte{0xFF, 0xFE}
		encoded := append(bom, encodeUTF16LE(writeContent)...)
		if err := os.WriteFile(fullFilePath, encoded, fr.fileMode); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
	} else {
		if err := os.WriteFile(fullFilePath, []byte(writeContent), fr.fileMode); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
	}

	// Compute structured patch
	patch := getStructuredPatch(fr.content, updatedContent)
	originalFile := fr.content

	return &tool.ToolResult{Data: &Output{
		FilePath:        fullFilePath,
		OldString:       actualOldString,
		NewString:       actualNewString,
		ReplaceAll:      in.ReplaceAll,
		OriginalFile:    &originalFile,
		StructuredPatch: patch,
	}}, nil
}

func readFileForEdit(filePath string) fileReadResult {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fileReadResult{fileExists: false}
	}

	info, statErr := os.Stat(filePath)
	fileMode := os.FileMode(0o644)
	if statErr == nil {
		fileMode = info.Mode().Perm()
	}

	hasBOM := len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE

	var content string
	if hasBOM {
		content = decodeUTF16LE(data[2:])
	} else {
		content = string(data)
	}

	hasCRLF := strings.Contains(content, "\r\n")

	content = strings.ReplaceAll(content, "\r\n", "\n")

	return fileReadResult{
		content:    content,
		fileExists: true,
		hasBOM:     hasBOM,
		hasCRLF:    hasCRLF,
		fileMode:   fileMode,
	}
}

func decodeUTF16LE(data []byte) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	u16 := make([]uint16, len(data)/2)
	for i := range u16 {
		u16[i] = uint16(data[i*2]) | uint16(data[i*2+1])<<8
	}
	return string(utf16.Decode(u16))
}

func encodeUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	data := make([]byte, len(u16)*2)
	for i, r := range u16 {
		data[i*2] = byte(r)
		data[i*2+1] = byte(r >> 8)
	}
	return data
}

// expandPath returns an absolute path for the given file path.
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

// desanitizations maps API-sanitized tags back to their real counterparts.
// Source: FileEditTool/utils.ts — DESANITIZATIONS.
var desanitizations = map[string]string{
	"<fnr>":        "<function_results>",
	"</fnr>":       "</function_results>",
	"<n>":          "<name>",
	"</n>":         "</name>",
	"<o>":          "<output>",
	"</o>":         "</output>",
	"<e>":          "<error>",
	"</e>":         "</error>",
	"<s>":          "<system>",
	"</s>":         "</system>",
	"<r>":          "<result>",
	"</r>":         "</result>",
	"< META_START >": "<META_START>",
	"< META_END >":   "<META_END>",
	"< EOT >":        "<EOT>",
	"< META >":       "<META>",
	"< SOS >":        "<SOS>",
	"\n\nH:":       "\n\nHuman:",
	"\n\nA:":       "\n\nAssistant:",
}

// desanitizeMatchString applies desanitization replacements to a match string.
// Returns the desanitized string and the list of replacements applied.
func desanitizeMatchString(s string) (string, []struct{ From, To string }) {
	result := s
	var applied []struct{ From, To string }
	for from, to := range desanitizations {
		before := result
		result = strings.ReplaceAll(result, from, to)
		if before != result {
			applied = append(applied, struct{ From, To string }{from, to})
		}
	}
	return result, applied
}

// getStructuredPatch computes structured unified diff hunks between old and new content.
func getStructuredPatch(oldContent, newContent string) []PatchHunk {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldContent, newContent, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var hunks []PatchHunk
	var hunkLines []string
	var oldLineNum, newLineNum int

	flushHunk := func() {
		if len(hunkLines) == 0 {
			return
		}
		hasChange := false
		for _, l := range hunkLines {
			if len(l) > 0 && (l[0] == '-' || l[0] == '+') {
				hasChange = true
				break
			}
		}
		if !hasChange {
			hunkLines = nil
			return
		}
		var oldCnt, newCnt int
		for _, l := range hunkLines {
			switch l[0] {
			case '-':
				oldCnt++
			case '+':
				newCnt++
			default:
				oldCnt++
				newCnt++
			}
		}
		hunks = append(hunks, PatchHunk{
			OldStart: oldLineNum - oldCnt + 1,
			OldLines: oldCnt,
			NewStart: newLineNum - newCnt + 1,
			NewLines: newCnt,
			Lines:    hunkLines,
		})
		hunkLines = nil
	}

	lines := func(text string) []string {
		parts := strings.Split(text, "\n")
		n := len(parts)
		if n > 0 && parts[n-1] == "" {
			n--
		}
		return parts[:n]
	}

	for _, d := range diffs {
		for _, line := range lines(d.Text) {
			switch d.Type {
			case diffmatchpatch.DiffEqual:
				flushHunk()
				hunkLines = append(hunkLines, " "+line)
				oldLineNum++
				newLineNum++
			case diffmatchpatch.DiffDelete:
				flushHunk()
				hunkLines = append(hunkLines, "-"+line)
				oldLineNum++
			case diffmatchpatch.DiffInsert:
				flushHunk()
				hunkLines = append(hunkLines, "+"+line)
				newLineNum++
			}
		}
	}
	flushHunk()
	return hunks
}
