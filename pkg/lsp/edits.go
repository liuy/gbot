package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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

// CodeActionContext describes the context passed to textDocument/codeAction,
// including optional diagnostics and a filter list of CodeActionKinds.
type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Only        []string     `json:"only,omitempty"`
	TriggerKind int          `json:"triggerKind,omitempty"`
}

// CodeActions lists available quick-fixes / refactors for a range.
// Only filter and diagnostics are passed in the request context (mirrors omp
// index.ts:2297-2301). Pass nil for both to omit them.
func CodeActions(ctx context.Context, c *Client, uri string, rng Range, cctx CodeActionContext) ([]CodeAction, error) {
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
		"range":        rng,
		"context":      cctx,
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

// AppliedCodeActionResult describes what got applied by ApplyCodeAction.
// Mirrors omp AppliedCodeActionResult (utils.ts:506-510).
type AppliedCodeActionResult struct {
	Title            string
	Edits            []string
	ExecutedCommands []string
}

// ApplyCodeAction executes a code action. If the action has no embedded edit,
// it is first resolved via codeAction/resolve. Then either the workspace edit
// is applied (recording per-file modifications) or the command is executed.
// Returns nil when the action carries neither an edit nor a command (mirrors
// omp applyCodeAction null return).
// Mirrors omp utils.ts:512-552.
func ApplyCodeAction(
	ctx context.Context,
	c *Client,
	action CodeAction,
	applyEdit func(*WorkspaceEdit) ([]string, error),
) (*AppliedCodeActionResult, error) {
	resolved := action
	if resolved.Edit == nil {
		// Try codeAction/resolve; ignore errors (resolve is optional).
		r, err := ResolveCodeAction(ctx, c, action)
		if err == nil && r.Edit != nil {
			resolved = *r
		}
	}

	result := &AppliedCodeActionResult{Title: resolved.Title}

	if resolved.Edit != nil {
		var err error
		result.Edits, err = applyEdit(resolved.Edit)
		if err != nil {
			return nil, fmt.Errorf("apply workspace edit: %w", err)
		}
	}
	if resolved.Command != nil {
		params := map[string]any{
			"command":   resolved.Command.Command,
			"arguments": resolved.Command.Arguments,
		}
		if _, err := c.Request(ctx, "workspace/executeCommand", params); err != nil {
			return nil, fmt.Errorf("execute command %s: %w", resolved.Command.Command, err)
		}
		result.ExecutedCommands = append(result.ExecutedCommands, resolved.Command.Command)
	}

	if len(result.Edits) == 0 && len(result.ExecutedCommands) == 0 {
		return nil, nil
	}
	return result, nil
}

// ResolveCodeAction calls codeAction/resolve to fill in the action's edit/command.
// Mirrors omp codeAction/resolve (utils.ts:525-531).
func ResolveCodeAction(ctx context.Context, c *Client, action CodeAction) (*CodeAction, error) {
	raw, err := c.Request(ctx, "codeAction/resolve", action)
	if err != nil {
		return nil, err
	}
	var out CodeAction
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("lsp codeAction/resolve: %w", err)
	}
	return &out, nil
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

// ApplyEditsToPath is the exported form of applyEditsToPath, used by tool/lsp
// callers (rename_file) that need to write pre-validated edits to a single path.
func ApplyEditsToPath(path string, edits []TextEdit) error {
	return applyEditsToPath(path, edits)
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

// RangesOverlap reports whether two Ranges share any position other than a
// touching boundary. Mirrors omp edits.ts rangesOverlap.
func RangesOverlap(a, b Range) bool {
	return comparePos(a.Start, b.End) < 0 && comparePos(b.Start, a.End) < 0
}

// FlattenWorkspaceTextEdits collects all TextEdits from a WorkspaceEdit into a
// map[uri]edits, ignoring resource ops (create/rename/delete). Mirrors omp
// edits.ts flattenWorkspaceTextEdits — used by rename_file to coalesce
// per-server edits before applying.
func FlattenWorkspaceTextEdits(edit *WorkspaceEdit) map[string][]TextEdit {
	out := make(map[string][]TextEdit)
	if edit == nil {
		return out
	}
	push := func(uri string, edits []TextEdit) {
		if len(edits) == 0 {
			return
		}
		out[uri] = append(out[uri], edits...)
	}
	for uri, edits := range edit.Changes {
		push(uri, edits)
	}
	for _, dc := range edit.DocumentChanges {
		uri, edits, ok := extractTextDocumentEdit(dc)
		if ok {
			push(uri, edits)
		}
	}
	return out
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

// decodeLocations handles both single-Location and []Location server responses,
// including LocationLink responses (targetUri/targetRange) that some servers
// return regardless of linkSupport negotiation. Mirrors omp
// normalizeLocationResult (index.ts:377-389).
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
		// Peek element shape: Location{uri,range} vs LocationLink{targetUri,...}.
		var generic []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &generic); err != nil {
			return nil, fmt.Errorf("decodeLocations array: %w", err)
		}
		out := make([]Location, 0, len(generic))
		for _, m := range generic {
			loc, ok := locationFromMap(m)
			if !ok {
				var ks []string
				for k := range m {
					ks = append(ks, k)
				}
				slog.Debug("lsp:decodeLocations:dropped_malformed_element", "keys", ks)
				continue
			}
			out = append(out, loc)
		}
		return out, nil
	case '{':
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("decodeLocations single: %w", err)
		}
		loc, ok := locationFromMap(m)
		if !ok {
			return nil, fmt.Errorf("decodeLocations: unrecognized object: %s", string(raw))
		}
		return []Location{loc}, nil
	default:
		return nil, fmt.Errorf("decodeLocations: unexpected token %q in %s", first, string(raw))
	}
}

// locationFromMap converts either a Location ({uri, range}) or a LocationLink
// ({targetUri, targetSelectionRange, targetRange}) to a Location.
func locationFromMap(m map[string]json.RawMessage) (Location, bool) {
	if uriRaw, ok := m["uri"]; ok {
		var uri string
		if err := json.Unmarshal(uriRaw, &uri); err != nil {
			return Location{}, false
		}
		var rng Range
		if rRaw, ok := m["range"]; ok {
			_ = json.Unmarshal(rRaw, &rng)
		}
		return Location{URI: uri, Range: rng}, true
	}
	if tURI, ok := m["targetUri"]; ok {
		var uri string
		if err := json.Unmarshal(tURI, &uri); err != nil {
			return Location{}, false
		}
		var rng Range
		if rRaw, ok := m["targetSelectionRange"]; ok {
			_ = json.Unmarshal(rRaw, &rng)
		} else if rRaw, ok := m["targetRange"]; ok {
			_ = json.Unmarshal(rRaw, &rng)
		}
		return Location{URI: uri, Range: rng}, true
	}
	return Location{}, false
}
