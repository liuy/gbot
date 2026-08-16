package fileedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func init() {
	permission.RegisterContentChecker("Edit", func(input json.RawMessage, contentRules []permission.Rule) permission.RuleAction {
		path := permission.ExtractFilePath(input)
		action, _, _ := permission.CheckFilePermission(path, contentRules)
		return action
	})
}

var MaxEditFileSize int64 = 1024 * 1024 * 1024

type Input struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type Output struct {
	FilePath     string  `json:"filePath"`
	OldString    string  `json:"oldString"`
	NewString    string  `json:"newString"`
	ReplaceAll   bool    `json:"replaceAll"`
	OriginalFile *string `json:"originalFile"`
}

type fileReadResult struct {
	content    string
	fileExists bool
	hasBOM     bool
	hasCRLF    bool
	fileMode   os.FileMode
}

// renderEditResult converts Edit tool output to a human-readable string for TUI.
// Source: FileEditTool/UI.tsx — renderToolResultMessage → FileEditToolUpdatedMessage
func renderEditResult(data any) string {
	out, ok := data.(*Output)
	if !ok {
		if s, ok := data.(string); ok {
			return renderEditError(s)
		}
		return fmt.Sprintf("%v", data)
	}

	hunks := tool.ComputePatch(out.OldString, out.NewString)
	added, removed := tool.CountPatchChanges(hunks)
	summary := tool.FormatDiffSummary(added, removed)
	diff := tool.RenderDiff(hunks)
	if diff == "" {
		return summary
	}
	return summary + "\n" + diff
}

// renderEditError converts Edit error messages to short summaries for TUI.
// Source: FileEditTool/UI.tsx — renderToolUseErrorMessage.
func renderEditError(msg string) string {
	if strings.Contains(msg, "File has not been read yet") {
		return "File must be read first"
	}
	if strings.Contains(msg, "not found") {
		return "Error editing file"
	}
	return msg
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
		Name_:        "Edit",
		Aliases_:     []string{"fileedit", "edit"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Edit a file with string replacement", nil
			}
			return in.FilePath, nil
		},
		Call_:          Execute,
		IsReadOnly_:    func(json.RawMessage) bool { return false },
		IsDestructive_: func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool {
			// Edit is concurrency-safe; file-level conflict detection is
			// handled by StreamingToolExecutor (same-file edits serialize).
			return true
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100000,
		Prompt_:            fileEditPrompt(),
		RenderResult_:      renderEditResult,
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var o Output
			if err := json.Unmarshal([]byte(text), &o); err != nil {
				return nil, err
			}
			// Wire text that happens to be a JSON object decodes into an
			// all-zero Output (unknown fields ignored), which replay would
			// render as an empty diff instead of falling back to the wire
			// text. Uniform rule across wire-plaintext tools.
			if o.FilePath == "" {
				return nil, fmt.Errorf("edit: decoded output lacks identifying fields (not a legacy JSON result)")
			}
			return &o, nil
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			out, ok := data.(*Output)
			if !ok {
				raw, _ := json.Marshal(data)
				return []types.ContentBlock{types.NewTextBlock(string(raw))}
			}
			// Source: FileEditTool.ts:575-594 — one-line confirmation; the
			// diff is deliberately absent (TS keeps it only in the local
			// transcript, which gbot does not store). gbot has no
			// userModified field, so the modifiedNote is always empty.
			if out.ReplaceAll {
				return []types.ContentBlock{types.NewTextBlock(fmt.Sprintf(
					"The file %s has been updated. All occurrences were successfully replaced.", out.FilePath))}
			}
			return []types.ContentBlock{types.NewTextBlock(fmt.Sprintf(
				"The file %s has been updated successfully.", out.FilePath))}
		},
	})
}

func Execute(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
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
		hint := findNearbyHint(fr.content, in.OldString)
		if hint != "" {
			return nil, fmt.Errorf("string to replace not found in file.\nString: %s\n\nNearby lines in file:\n%s", in.OldString, hint)
		}
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

	originalFile := fr.content

	// Update ReadFileState so subsequent edits in the same turn see the
	// updated content. Source: TS FileEditTool.ts:520-525.
	if tctx != nil && tctx.ReadFileState != nil {
		info, statErr := os.Stat(fullFilePath)
		ts := int64(0)
		if statErr == nil {
			ts = info.ModTime().UnixMilli()
		}
		tctx.ReadFileState[fullFilePath] = tool.FileState{
			Content:   updatedContent,
			Timestamp: ts,
		}
	}

	return &tool.ToolResult{Data: &Output{
		FilePath:     fullFilePath,
		OldString:    actualOldString,
		NewString:    actualNewString,
		ReplaceAll:   in.ReplaceAll,
		OriginalFile: &originalFile,
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
	"<fnr>":          "<function_results>",
	"</fnr>":         "</function_results>",
	"<n>":            "<name>",
	"</n>":           "</name>",
	"<o>":            "<output>",
	"</o>":           "</output>",
	"<e>":            "<error>",
	"</e>":           "</error>",
	"<s>":            "<system>",
	"</s>":           "</system>",
	"<r>":            "<result>",
	"</r>":           "</result>",
	"< META_START >": "<META_START>",
	"< META_END >":   "<META_END>",
	"< EOT >":        "<EOT>",
	"< META >":       "<META>",
	"< SOS >":        "<SOS>",
	"\n\nH:":         "\n\nHuman:",
	"\n\nA:":         "\n\nAssistant:",
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
