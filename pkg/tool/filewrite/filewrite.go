// Package filewrite implements the FileWrite tool for writing files to the filesystem.
//
// Source reference: tools/FileWriteTool/FileWriteTool.ts
// 1:1 port from the TypeScript source.
package filewrite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// Source: FileWriteTool.ts — Zod schema for file write input.
type Input struct {
	FilePath string `json:"file_path" validate:"required"`
	Content  string `json:"content" validate:"required"`
}

// WriteType indicates whether a file was created or updated.
// Source: FileWriteTool.ts — output type enum.
type WriteType string

const (
	WriteTypeCreate WriteType = "create"
	WriteTypeUpdate WriteType = "update"
)

// StructuredPatchHunk represents a single hunk in a unified diff.
// Source: FileEditTool/types.ts — hunkSchema.
type StructuredPatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// GitDiff represents the git diff for a written file.
type GitDiff struct {
	Filename   string  `json:"filename"`
	Status     string  `json:"status"` // "modified" or "added"
	Additions  int     `json:"additions"`
	Deletions  int     `json:"deletions"`
	Changes    int     `json:"changes"`
	Patch      string  `json:"patch"`
	Repository *string `json:"repository"` // null if not in a git repo
}

// findGitRoot finds the git root directory for a given path.
func findGitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// checkGitRepo is the underlying function that checks git repo status.
func checkGitRepo() bool {
	root := findGitRoot("/")
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return false
		}
		root = findGitRoot(cwd)
	}
	return root != ""
}

// isInGitRepo checks if we're in a git repository. Result cached via sync.OnceValue.
var isInGitRepo = sync.OnceValue(checkGitRepo)

// shouldComputeGitDiff returns true only when CLAUDE_CODE_REMOTE env var is
// set to a truthy value ("1", "true", "yes"). This gates expensive git
// operations in the tool result.
// Source: FileWriteTool.ts — isEnvTruthy(process.env.CLAUDE_CODE_REMOTE)
func shouldComputeGitDiff() bool {
	v := os.Getenv("CLAUDE_CODE_REMOTE")
	if v == "" {
		return false
	}
	b, _ := strconv.ParseBool(v)
	return b
}

// getDefaultBranch determines the default branch name for the git repo.
// Priority: refs/remotes/origin/HEAD symref → "main" or "master" if they
// exist on origin → fallback "main".
// Source: gitFilesystem.ts — computeDefaultBranch
func getDefaultBranch(gitRoot string) string {
	// Try reading origin/HEAD symref
	cmd := exec.Command("git", "--no-optional-locks", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(output))
		// Output is like "refs/remotes/origin/main"
		if after, ok := strings.CutPrefix(ref, "refs/remotes/origin/"); ok {
			branch := after
			if branch != "" {
				return branch
			}
		}
	}

	// Check if main or master exists on origin
	for _, candidate := range []string{"main", "master"} {
		cmd := exec.Command("git", "--no-optional-locks", "rev-parse", "--verify", "refs/remotes/origin/"+candidate)
		cmd.Dir = gitRoot
		if err := cmd.Run(); err == nil {
			return candidate
		}
	}

	return "main"
}

// getDiffRef determines the best ref to diff against for a PR-like view.
// Priority:
// 1. CLAUDE_CODE_BASE_REF env var (set externally)
// 2. Merge base with the default branch
// 3. HEAD (fallback)
// Source: gitDiff.ts — getDiffRef
func getDiffRef(gitRoot string) string {
	baseBranch := os.Getenv("CLAUDE_CODE_BASE_REF")
	if baseBranch == "" {
		baseBranch = getDefaultBranch(gitRoot)
	}

	cmd := exec.Command("git", "--no-optional-locks", "merge-base", "HEAD", baseBranch)
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err == nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return trimmed
		}
	}
	return "HEAD"
}

// parseGitHubRemoteURL parses a git remote URL and returns "owner/repo" for
// github.com hosts only. Returns nil for non-github hosts or invalid URLs.
// Source: detectRepository.ts — parseGitRemote + parseGitHubRepository
func parseGitHubRemoteURL(url string) *string {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return nil
	}

	// SSH format: git@host:owner/repo.git
	sshRe := regexp.MustCompile(`^git@([^:]+):([^/]+)/([^/]+?)(?:\.git)?$`)
	if m := sshRe.FindStringSubmatch(trimmed); len(m) == 4 {
		host := m[1]
		if host == "github.com" {
			result := m[2] + "/" + m[3]
			return &result
		}
		return nil // non-github
	}

	// URL format: https://host/owner/repo.git or ssh://git@host/owner/repo
	urlRe := regexp.MustCompile(`^(?:https?|ssh|git)://(?:[^@]+@)?([^/:]+(?::\d+)?)/([^/]+)/([^/]+?)(?:\.git)?$`)
	if m := urlRe.FindStringSubmatch(trimmed); len(m) == 4 {
		host := m[1]
		// Strip port for non-HTTPS
		if !strings.HasPrefix(trimmed, "http") {
			if idx := strings.Index(host, ":"); idx >= 0 {
				host = host[:idx]
			}
		}
		if host == "github.com" {
			result := m[2] + "/" + m[3]
			return &result
		}
		return nil // non-github
	}

	return nil
}

// getRepository resolves the github.com "owner/repo" for a git root by
// reading the remote origin URL. Returns nil if not a github.com repo.
// Source: detectRepository.ts — detectCurrentRepository + getCachedRepository
func getRepository(gitRoot string) *string {
	cmd := exec.Command("git", "--no-optional-locks", "remote", "get-url", "origin")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseGitHubRemoteURL(strings.TrimSpace(string(output)))
}

// fetchGitDiffForFile fetches git diff stats for a single file.
func fetchGitDiffForFile(filePath string) (*GitDiff, error) {
	if !isInGitRepo() {
		return nil, nil
	}

	gitRoot := findGitRoot(filepath.Dir(filePath))
	if gitRoot == "" {
		return nil, nil
	}

	relPath, err := filepath.Rel(gitRoot, filePath)
	if err != nil {
		return nil, nil
	}
	relPath = filepath.ToSlash(relPath)

	// Check if file is tracked
	cmd := exec.Command("git", "--no-optional-locks", "ls-files", "--error-unmatch", relPath)
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		// File is untracked — generate synthetic diff with repository
		result, err := generateSyntheticDiff(gitRoot, relPath, filePath)
		if err != nil {
			return nil, err
		}
		repo := getRepository(gitRoot)
		result.Repository = repo
		return result, nil
	}

	// Get git diff — use merge-base for PR-like view
	diffRef := getDiffRef(gitRoot)
	cmd = exec.Command("git", "--no-optional-locks", "diff", diffRef, "--", relPath)
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return nil, nil
	}

	result := parseGitDiffOutput(relPath, string(output))
	repo := getRepository(gitRoot)
	result.Repository = repo
	return result, nil
}

// parseGitDiffOutput parses git diff output into a GitDiff struct.
func parseGitDiffOutput(filename string, diff string) *GitDiff {
	lines := strings.Split(diff, "\n")
	var patchLines []string
	additions := 0
	deletions := 0
	inHunks := false

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			inHunks = true
		}
		if inHunks {
			patchLines = append(patchLines, line)
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				additions++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				deletions++
			}
		}
	}

	return &GitDiff{
		Filename:  filename,
		Status:    "modified",
		Additions: additions,
		Deletions: deletions,
		Changes:   additions + deletions,
		Patch:     strings.Join(patchLines, "\n"),
	}
}

// generateSyntheticDiff generates a synthetic diff for an untracked file.
func generateSyntheticDiff(gitRoot, gitPath, absPath string) (*GitDiff, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var patchLines []string
	for _, line := range lines {
		patchLines = append(patchLines, "+"+line)
	}
	patch := fmt.Sprintf("@@ -0,0 +1,%d @@\n%s", len(lines), strings.Join(patchLines, "\n"))

	return &GitDiff{
		Filename:  gitPath,
		Status:    "added",
		Additions: len(lines),
		Deletions: 0,
		Changes:   len(lines),
		Patch:     patch,
	}, nil
}

// Source: FileWriteTool.ts — tool result data.
type Output struct {
	Type            WriteType             `json:"type"`
	FilePath        string                `json:"filePath"`
	Content         string                `json:"content"`
	StructuredPatch []StructuredPatchHunk `json:"structuredPatch"`
	OriginalFile    *string               `json:"originalFile"`   // null for new files
	ContentChanged  bool                  `json:"contentChanged"` // true if file was modified since last read
	GitDiff         *GitDiff              `json:"gitDiff,omitempty"`
}

// renderWriteResult converts Write tool output to a human-readable string for TUI.
// Source: FileWriteTool/UI.tsx — renderToolResultMessage
func renderWriteResult(data any) string {
	out, ok := data.(*Output)
	if !ok {
		return fmt.Sprintf("%v", data)
	}

	if out.Type == WriteTypeCreate {
		numLines := tool.CountLines(out.Content)
		summary := fmt.Sprintf("Wrote %d lines to %s", numLines, out.FilePath)
		if out.Content == "" {
			return summary
		}
		return summary + "\n" + strings.TrimRight(out.Content, "\n")
	}

	// Update: summary + structured diff
	hunks := convertHunks(out.StructuredPatch)
	added, removed := tool.CountPatchChanges(hunks)
	summary := tool.FormatDiffSummary(added, removed)
	diff := tool.RenderDiff(hunks)
	if diff == "" {
		return summary
	}
	return summary + "\n" + diff
}

// convertHunks converts filewrite-specific hunks to tool.DiffHunk.
func convertHunks(hunks []StructuredPatchHunk) []tool.DiffHunk {
	if len(hunks) == 0 {
		return nil
	}
	result := make([]tool.DiffHunk, len(hunks))
	for i, h := range hunks {
		result[i] = tool.DiffHunk{
			OldStart: h.OldStart,
			OldLines: h.OldLines,
			NewStart: h.NewStart,
			NewLines: h.NewLines,
			Lines:    h.Lines,
		}
	}
	return result
}

// New creates the FileWrite tool.
// Source: tools/FileWriteTool/FileWriteTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path", "content"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to write. Will create parent directories if needed."
			},
			"content": {
				"type": "string",
				"description": "The content to write to the file."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Write",
		Aliases_:     []string{"filewrite", "write"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Write content to a file", nil
			}
			return in.FilePath, nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return false
		},
		IsDestructive_: func(json.RawMessage) bool {
			return true
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false
		},
		MaxResultSizeChars: 100000,
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_:            fileWritePrompt(),
		RenderResult_:      renderWriteResult,
	})
}

// expandPath converts a file path to an absolute path.
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

// getMtimeMs returns the modification time in milliseconds.
func getMtimeMs(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return info.ModTime().UnixMilli(), nil
}

// normalizeLineEndings converts all CRLF and CR line endings to LF.
// Source: FileWriteTool.ts — writeTextContent with 'LF' line ending mode.
func normalizeLineEndings(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return content
}

// splitLines splits text on newlines, returning non-empty lines only.
// This mirrors strings.Split(strings.TrimSuffix(s, "\n"), "\n") while
// discarding the trailing empty element that Split produces.
func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	n := len(lines)
	if n > 0 && lines[n-1] == "" {
		n--
	}
	result := make([]string, 0, n)
	for i := 0; i < n; i++ {
		result = append(result, lines[i])
	}
	return result
}

// getStructuredPatch computes structured unified diff hunks between old and new content.
// Each hunk includes up to ctxLines lines of leading/trailing context.
// Source: diff npm package structuredPatch with context=3.
func getStructuredPatch(oldContent, newContent string) []StructuredPatchHunk {
	// Use line-level diff (equivalent to diff npm's diffLines + structuredPatch).
	// Falls back to diffmatchpatch if content is too large.
	hunks := tool.ComputePatch(oldContent, newContent)
	if hunks != nil {
		result := make([]StructuredPatchHunk, len(hunks))
		for i, h := range hunks {
			result[i] = StructuredPatchHunk{
				OldStart: h.OldStart,
				OldLines: h.OldLines,
				NewStart: h.NewStart,
				NewLines: h.NewLines,
				Lines:    h.Lines,
			}
		}
		return result
	}

	// Fallback for very large files: use diffmatchpatch character-level diff.
	const ctxLines = 3

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldContent, newContent, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var patchHunks []StructuredPatchHunk
	var hunkLines []string
	var oldLineNum, newLineNum int

	hasChanges := func() bool {
		for _, l := range hunkLines {
			if len(l) > 0 && (l[0] == '-' || l[0] == '+') {
				return true
			}
		}
		return false
	}

	trailingCtx := func() int {
		n := 0
		for i := len(hunkLines) - 1; i >= 0; i-- {
			if len(hunkLines[i]) > 0 && hunkLines[i][0] == ' ' {
				n++
			} else {
				break
			}
		}
		return n
	}

	emit := func() {
		if len(hunkLines) == 0 || !hasChanges() {
			return
		}
		if tc := trailingCtx(); tc > ctxLines {
			hunkLines = hunkLines[:len(hunkLines)-(tc-ctxLines)]
		}
		linesCopy := make([]string, len(hunkLines))
		copy(linesCopy, hunkLines)
		var oldCnt, newCnt int
		for _, l := range linesCopy {
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
		patchHunks = append(patchHunks, StructuredPatchHunk{
			OldStart: oldLineNum - oldCnt + 1,
			OldLines: oldCnt,
			NewStart: newLineNum - newCnt + 1,
			NewLines: newCnt,
			Lines:    linesCopy,
		})
		tc := min(trailingCtx(), ctxLines)
		if tc > 0 {
			saved := make([]string, tc)
			copy(saved, hunkLines[len(hunkLines)-tc:])
			hunkLines = saved
		} else {
			hunkLines = nil
		}
	}

	for _, d := range diffs {
		lines := splitLines(d.Text)
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			for _, line := range lines {
				hunkLines = append(hunkLines, " "+line)
				if hasChanges() {
					if trailingCtx() >= ctxLines {
						emit()
					}
				} else {
					if len(hunkLines) > ctxLines {
						hunkLines = hunkLines[len(hunkLines)-ctxLines:]
					}
				}
				oldLineNum++
				newLineNum++
			}
		case diffmatchpatch.DiffDelete:
			for _, line := range lines {
				hunkLines = append(hunkLines, "-"+line)
				oldLineNum++
			}
		case diffmatchpatch.DiffInsert:
			for _, line := range lines {
				hunkLines = append(hunkLines, "+"+line)
				newLineNum++
			}
		}
	}
	emit()
	return patchHunks
}

// Execute writes content to a file.
// Source: FileWriteTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	// Expand path: resolve ~, relative paths, etc.
	// Source: FileWriteTool.ts — expandPath(file_path) in backfillObservableInput + call
	filePath := in.FilePath
	if !filepath.IsAbs(filePath) && !strings.HasPrefix(filePath, "~/") && tctx != nil && tctx.WorkingDir != "" {
		filePath = filepath.Join(tctx.WorkingDir, filePath)
	}
	fullFilePath := expandPath(filePath)

	dir := filepath.Dir(fullFilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create directories: %w", err)
	}

	var oldContent *string
	existingData, err := os.ReadFile(fullFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read existing file: %w", err)
		}
		oldContent = nil
	} else {
		s := string(existingData)
		oldContent = &s
	}

	// Must-read-first + staleness validation
	// Source: FileWriteTool.ts — validateInput (error codes 2 & 3)
	if oldContent != nil && tctx != nil && tctx.ReadFileState != nil {
		state, hasState := tctx.ReadFileState[fullFilePath]
		if !hasState || state.IsPartialView {
			return nil, fmt.Errorf("file has not been read yet, read it first before writing")
		}
		// Staleness check: compare current mtime vs read timestamp
		info, statErr := os.Stat(fullFilePath)
		if statErr == nil {
			currentMtime := info.ModTime().UnixMilli()
			if currentMtime > state.Timestamp {
				return nil, fmt.Errorf("file has been modified since read, read it again before writing")
			}
		}
	}

	// Check if file was modified since last read (for contentChanged output)
	var contentChanged bool
	if oldContent != nil {
		info, err := os.Stat(fullFilePath)
		if err == nil && tctx != nil && tctx.ReadFileState != nil {
			if state, ok := tctx.ReadFileState[fullFilePath]; ok {
				contentChanged = info.ModTime().UnixMilli() != state.Timestamp
			}
		}
	}

	// Normalize line endings to LF (Unix style)
	// Source: FileWriteTool.ts — writeTextContent(fullFilePath, content, enc, 'LF')
	normalizedContent := normalizeLineEndings(in.Content)

	data := []byte(normalizedContent)
	if err := os.WriteFile(fullFilePath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	// Build structured patch for updates
	// Source: FileWriteTool.ts — meta.content is CRLF-normalized
	var structuredPatch []StructuredPatchHunk
	if oldContent != nil {
		normalizedOld := normalizeLineEndings(*oldContent)
		structuredPatch = getStructuredPatch(normalizedOld, normalizedContent)
	}

	// Try to get git diff (gated by CLAUDE_CODE_REMOTE env var)
	// Source: FileWriteTool.ts — isEnvTruthy(process.env.CLAUDE_CODE_REMOTE)
	var gitDiff *GitDiff
	if shouldComputeGitDiff() {
		gitDiff, _ = fetchGitDiffForFile(fullFilePath)
	}

	// Update read timestamp to invalidate stale writes
	// Source: FileWriteTool.ts — readFileState.set(fullFilePath, {...})
	if tctx != nil {
		if tctx.ReadFileState == nil {
			tctx.ReadFileState = make(map[string]types.FileState)
		}
		mtimeMs, _ := getMtimeMs(fullFilePath)
		tctx.ReadFileState[fullFilePath] = types.FileState{
			Content:   normalizedContent,
			Timestamp: mtimeMs,
		}
	}

	if oldContent == nil {
		return &tool.ToolResult{Data: &Output{
			Type:            WriteTypeCreate,
			FilePath:        fullFilePath,
			Content:         normalizedContent,
			StructuredPatch: structuredPatch,
			OriginalFile:    nil,
			ContentChanged:  contentChanged,
			GitDiff:         gitDiff,
		}}, nil
	}

	return &tool.ToolResult{Data: &Output{
		Type:            WriteTypeUpdate,
		FilePath:        fullFilePath,
		Content:         normalizedContent,
		StructuredPatch: structuredPatch,
		OriginalFile:    oldContent,
		ContentChanged:  contentChanged,
		GitDiff:         gitDiff,
	}}, nil
}
