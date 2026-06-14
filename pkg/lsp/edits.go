package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// =============================================================================
// LSP method wrappers — one per method used by Phase 2 tools.
// All take a *Client and return typed results; errors propagate verbatim.
// =============================================================================

// Definition returns the location where the symbol at position is defined.
func Definition(ctx context.Context, c *Client, uri string, pos Position) ([]Location, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"position":     pos,
	}
	raw, err := c.Request(ctx, "textDocument/definition", params)
	if err != nil {
		return nil, err
	}
	return decodeLocations(raw)
}

// References returns all locations referencing the symbol at position.
func References(ctx context.Context, c *Client, uri string, pos Position) ([]Location, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"position":     pos,
		"context":      map[string]bool{"includeDeclaration": true},
	}
	raw, err := c.Request(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}
	return decodeLocations(raw)
}

// Implementation returns implementations of the interface at position.
func Implementation(ctx context.Context, c *Client, uri string, pos Position) ([]Location, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"position":     pos,
	}
	raw, err := c.Request(ctx, "textDocument/implementation", params)
	if err != nil {
		return nil, err
	}
	return decodeLocations(raw)
}

// WorkspaceSymbol queries symbols matching the query string across the project.
func WorkspaceSymbol(ctx context.Context, c *Client, query string) ([]SymbolInformation, error) {
	raw, err := c.Request(ctx, "workspace/symbol", map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	var out []SymbolInformation
	if err := json.Unmarshal(raw, &out); err != nil {
		// Some servers return a single object instead of an array.
		var one SymbolInformation
		if err2 := json.Unmarshal(raw, &one); err2 == nil && one.Name != "" {
			return []SymbolInformation{one}, nil
		}
		return nil, fmt.Errorf("lsp workspaceSymbol: %w", err)
	}
	return out, nil
}

// DocumentSymbols returns the hierarchical symbol tree of a document.
func DocumentSymbols(ctx context.Context, c *Client, uri string) ([]DocumentSymbol, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
	}
	raw, err := c.Request(ctx, "textDocument/documentSymbol", params)
	if err != nil {
		return nil, err
	}
	var out []DocumentSymbol
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("lsp documentSymbol: %w", err)
	}
	return out, nil
}

// HoverAt returns hover info (signature + doc) for the symbol at position.
func HoverAt(ctx context.Context, c *Client, uri string, pos Position) (*Hover, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"position":     pos,
	}
	raw, err := c.Request(ctx, "textDocument/hover", params)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var h Hover
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("lsp hover: %w", err)
	}
	return &h, nil
}

// PrepareRename validates that position is a renameable symbol and returns its range.
// Returns (nil, nil) if the server declines (e.g., position is in a comment).
// Per LSP spec the response is either {range, placeholder}, a bare Range, or null.
func PrepareRename(ctx context.Context, c *Client, uri string, pos Position) (*Range, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"position":     pos,
	}
	raw, err := c.Request(ctx, "textDocument/prepareRename", params)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// First try {range: {...}, placeholder: "..."} shape.
	var wrap struct {
		Range Range `json:"range"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Range.Start.Line >= 0 {
		return &wrap.Range, nil
	}
	// Fall back to bare Range shape (legacy servers).
	var r Range
	if err := json.Unmarshal(raw, &r); err == nil {
		return &r, nil
	}
	return nil, nil
}

// Rename performs the symbol rename and returns the workspace edit set.
func Rename(ctx context.Context, c *Client, uri string, pos Position, newName string) (*WorkspaceEdit, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"position":     pos,
		"newName":      newName,
	}
	raw, err := c.Request(ctx, "textDocument/rename", params)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var edit WorkspaceEdit
	if err := json.Unmarshal(raw, &edit); err != nil {
		return nil, fmt.Errorf("lsp rename: %w", err)
	}
	return &edit, nil
}

// CodeActions lists available quick-fixes / refactors for a range.
func CodeActions(ctx context.Context, c *Client, uri string, rng Range) ([]CodeAction, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"range":        rng,
		"context": map[string]any{
			"diagnostics": []any{},
			"only": []string{
				"quickfix",
				"source.organizeImports",
				"source.fixAll",
			},
		},
	}
	raw, err := c.Request(ctx, "textDocument/codeAction", params)
	if err != nil {
		return nil, err
	}
	var out []CodeAction
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("lsp codeAction: %w", err)
	}
	return out, nil
}

// ApplyCodeAction executes a code action that carries a Command (workspace/executeCommand).
// Code actions with an embedded WorkspaceEdit should use ApplyWorkspaceEdit instead.
func ApplyCodeAction(ctx context.Context, c *Client, action CodeAction) error {
	if action.Command == nil {
		if action.Edit != nil {
			_, err := ApplyWorkspaceEdit(action.Edit)
			return err
		}
		return nil
	}
	params := map[string]any{
		"command":   action.Command.Command,
		"arguments": action.Command.Arguments,
	}
	_, err := c.Request(ctx, "workspace/executeCommand", params)
	return err
}

// =============================================================================
// WorkspaceEdit application (translated from omp edits.ts).
// Edits are applied bottom-to-top to preserve line indices; overlapping edits are rejected.
// =============================================================================

// ApplyWorkspaceEdit writes the WorkspaceEdit to disk. Returns the list of files modified.
//
// Atomicity is per-file: each file's edits are validated before that file is written,
// and a failed file is skipped without blocking others. Workspace-wide atomicity
// (all-or-nothing with temp+rename) is intentionally not implemented — gbot's LSP
// consumers (rename, codeAction) produce small edit sets where partial application
// is recoverable by re-running the LSP operation.
//
// documentChanges resource ops (create/rename/delete) are handled; both the legacy
// `changes` map and `documentChanges` array are supported.
func ApplyWorkspaceEdit(edit *WorkspaceEdit) ([]string, error) {
	if edit == nil {
		return nil, nil
	}

	var changed []string
	var errs []string

	if len(edit.DocumentChanges) > 0 {
		for _, dc := range edit.DocumentChanges {
			uri, edits, ok := extractTextDocumentEdit(dc)
			if ok {
				// Text edit.
				if len(edits) == 0 {
					continue
				}
				path := uriToPath(uri)
				if err := applyEditsToPath(path, edits); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", path, err))
					continue
				}
				changed = append(changed, path)
				continue
			}
			// Resource op (create/rename/delete).
			if desc, err := applyResourceOp(dc); err != nil {
				errs = append(errs, err.Error())
			} else if desc != "" {
				changed = append(changed, desc)
			}
		}
	} else if len(edit.Changes) > 0 {
		for uri, edits := range edit.Changes {
			if len(edits) == 0 {
				continue
			}
			path := uriToPath(uri)
			if err := applyEditsToPath(path, edits); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			changed = append(changed, path)
		}
	}

	if len(changed) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("applyWorkspaceEdit: all failed: %s", strings.Join(errs, "; "))
	}
	return changed, nil
}

// applyResourceOp handles CreateFile / RenameFile / DeleteFile ops.
// Returns a human-readable description on success or "" if the entry isn't a resource op.
func applyResourceOp(dc map[string]any) (string, error) {
	kind, _ := dc["kind"].(string)
	if kind == "" {
		return "", nil
	}
	switch kind {
	case "create":
		uri, _ := dc["uri"].(string)
		path := uriToPath(uri)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return "", fmt.Errorf("create %s: mkdir: %w", path, err)
		}
		if err := os.WriteFile(path, []byte{}, 0644); err != nil {
			return "", fmt.Errorf("create %s: write: %w", path, err)
		}
		return path, nil
	case "rename":
		oldURI, _ := dc["oldUri"].(string)
		newURI, _ := dc["newUri"].(string)
		oldPath := uriToPath(oldURI)
		newPath := uriToPath(newURI)
		if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
			return "", fmt.Errorf("rename %s: mkdir: %w", newPath, err)
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			return "", fmt.Errorf("rename %s → %s: %w", oldPath, newPath, err)
		}
		return newPath, nil
	case "delete":
		uri, _ := dc["uri"].(string)
		path := uriToPath(uri)
		if err := os.RemoveAll(path); err != nil {
			return "", fmt.Errorf("delete %s: %w", path, err)
		}
		return path, nil
	}
	return "", nil
}

// extractTextDocumentEdit pulls (uri, edits) out of a documentChanges element.
// Returns ok=false for resource ops (Create/Rename/Delete) — we don't handle them here.
func extractTextDocumentEdit(dc map[string]any) (string, []TextEdit, bool) {
	td, ok := dc["textDocument"].(map[string]any)
	if !ok {
		return "", nil, false
	}
	uri, _ := td["uri"].(string)
	if uri == "" {
		return "", nil, false
	}
	rawEdits, ok := dc["edits"]
	if !ok {
		return uri, nil, true
	}
	// Re-marshal then unmarshal each edit through JSON so the typed TextEdit
	// captures the nested range/position fields regardless of the incoming map shape.
	body, err := json.Marshal(rawEdits)
	if err != nil {
		return uri, nil, true
	}
	var edits []TextEdit
	if err := json.Unmarshal(body, &edits); err != nil {
		return uri, nil, true
	}
	return uri, edits, true
}

// applyEditsToPath applies TextEdits to a file in place.
func applyEditsToPath(path string, edits []TextEdit) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	applied, err := applyTextEditsToString(string(content), edits)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(applied), 0644)
}

// applyTextEditsToString replaces ranges in content with the new text.
// Edits are applied bottom-to-top to keep indices stable; overlapping edits are rejected.
// Mirrors omp's edits.ts applyTextEditsToString + sortAndValidateTextEdits.
func applyTextEditsToString(content string, edits []TextEdit) (string, error) {
	if len(edits) == 0 {
		return content, nil
	}
	sorted := append([]TextEdit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Range.Start, sorted[j].Range.Start
		if a.Line != b.Line {
			return a.Line > b.Line
		}
		return a.Character > b.Character
	})

	// Detect overlap: each edit's start must be >= next edit's end.
	for i := 0; i < len(sorted)-1; i++ {
		later := sorted[i].Range.Start
		earlierEnd := sorted[i+1].Range.End
		if comparePos(earlierEnd, later) > 0 {
			return "", fmt.Errorf("overlapping edits")
		}
	}

	lines := splitKeepNL(content)
	for _, e := range sorted {
		startLine, startChar := e.Range.Start.Line, e.Range.Start.Character
		endLine, endChar := e.Range.End.Line, e.Range.End.Character

		// Bounds-check against the actual line slice.
		if startLine >= len(lines) {
			return "", fmt.Errorf("edit start line %d out of range (have %d lines)", startLine, len(lines))
		}
		if endLine >= len(lines) {
			endLine = len(lines) - 1
			endChar = len(lines[endLine])
		}

		if startLine == endLine {
			line := lines[startLine]
			lines[startLine] = sliceStr(line, 0, startChar) + e.NewText + sliceStr(line, endChar, len(line))
		} else {
			startLineContent := lines[startLine]
			endLineContent := lines[endLine]
			merged := sliceStr(startLineContent, 0, startChar) + e.NewText + sliceStr(endLineContent, endChar, len(endLineContent))
			lines = append(lines[:startLine], append([]string{merged}, lines[endLine+1:]...)...)
		}
	}
	return strings.Join(lines, ""), nil
}

// splitKeepNL splits content on newlines, preserving the newline char at each line end.
func splitKeepNL(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func sliceStr(s string, lo, hi int) string {
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	if lo > hi {
		return ""
	}
	return s[lo:hi]
}

func comparePos(a, b Position) int {
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	return a.Character - b.Character
}

// uriToPath converts a file:// URI back to a filesystem path.
func uriToPath(uri string) string {
	uri = strings.TrimPrefix(uri, "file://")
	// Windows: file:///C:/path -> C:/path
	if len(uri) > 2 && uri[0] == '/' && uri[2] == ':' {
		uri = uri[1:]
	}
	uri = strings.ReplaceAll(uri, "%20", " ")
	if abs, err := filepath.Abs(uri); err == nil {
		return abs
	}
	return uri
}

// decodeLocations handles both single-Location and []Location server responses.
// Dispatches by peeking at the first non-whitespace byte: '[' = array, '{' = single.
func decodeLocations(raw json.RawMessage) ([]Location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Find first non-whitespace byte.
	first := byte(0)
	for _, b := range raw {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			first = b
			break
		}
	}
	switch first {
	case '[':
		var arr []Location
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("decodeLocations array: %w", err)
		}
		return arr, nil
	case '{':
		var one Location
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("decodeLocations single: %w", err)
		}
		return []Location{one}, nil
	default:
		return nil, fmt.Errorf("decodeLocations: unexpected token %q in %s", first, string(raw))
	}
}
