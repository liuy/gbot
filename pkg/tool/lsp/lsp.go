// Package lsp implements the Lsp tool — direct access to language server protocol
// operations. This is the standalone alternative to embedding LSP into Read/Grep/Edit/Write.
//
// Reference: oh-my-pi/src/lsp/index.ts (LspTool class, ~2477 lines)
package lsptool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// readonlyActions lists actions that don't mutate workspace or server state.
// "request" is intentionally excluded — it can invoke arbitrary LSP methods
// like workspace/executeCommand that mutate state. Mirrors omp
// LSP_READONLY_ACTIONS (index.ts:38-48).
var readonlyActions = map[string]bool{
	"definition":       true,
	"type_definition":  true,
	"implementation":   true,
	"references":       true,
	"hover":            true,
	"symbols":          true,
	"workspace_symbol": true,
	"status":           true,
	"capabilities":     true,
}

// Input is the LSP tool input schema.
type Input struct {
	Action  string `json:"action"`             // required: definition, references, hover, symbols, rename, etc.
	File    string `json:"file,omitempty"`     // file path (required for file-scoped actions)
	Line    int    `json:"line,omitempty"`     // 1-based line number
	Symbol  string `json:"symbol,omitempty"`   // symbol name at the position
	NewName string `json:"new_name,omitempty"` // new name for rename / destination path for rename_file
	Apply   *bool  `json:"apply,omitempty"`    // apply edits (default true for write actions)
	Query   string `json:"query,omitempty"`    // query string for workspace_symbol / custom request
	Payload string `json:"payload,omitempty"`  // JSON-encoded params for action=request
	Timeout int    `json:"timeout,omitempty"`  // timeout in seconds (default 30)
}

// New creates the Lsp tool. The registry is captured in a closure so that
// each tool instance owns its own registry reference (the previous package-level
// lspReg was a data race when multiple tests ran New in parallel).
func New(reg *lsp.Registry) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["action"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["definition", "type_definition", "implementation", "references", "hover", "symbols", "workspace_symbol", "code_actions", "rename", "rename_file", "reload", "status", "capabilities", "request"],
				"description": "LSP action to perform"
			},
			"file": {
				"type": "string",
				"description": "Absolute path to the file (required for file-scoped actions like definition, references, hover, symbols)"
			},
			"line": {
				"type": "integer",
				"description": "1-based line number for the position (optional, default: 1)"
			},
			"symbol": {
				"type": "string",
				"description": "Symbol name at the position (required by project-aware servers for references/rename/definition to avoid fallback to wrong identifier)"
			},
			"new_name": {
				"type": "string",
				"description": "New symbol name (required for rename action) or destination path (required for rename_file action)"
			},
			"apply": {
				"type": "boolean",
				"description": "Whether to apply edits (default true for write actions, false for read actions)"
			},
			"query": {
				"type": "string",
				"description": "Symbol search query for workspace_symbol, or LSP method name for action=request"
			},
			"payload": {
				"type": "string",
				"description": "JSON-encoded params for action=request. Overrides auto-built {textDocument, position} shape when present."
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (default: 30)"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Lsp",
		Aliases_:     []string{"lsp"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Query language server for code intelligence", nil
			}
			return "Lsp " + in.Action, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, reg, input, tctx)
		},
		RenderResult_: func(data any) string {
			if s, ok := data.(string); ok {
				return s
			}
			b, _ := json.Marshal(data)
			return string(b)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			return readonlyActions[in.Action]
		},
		Prompt_:            LSPPrompt(),
		IsConcurrencySafe_: func(json.RawMessage) bool { return false },
		InterruptBehavior_: tool.InterruptCancel,
	})
}

// execute dispatches a tool invocation to the action handler.
func execute(ctx context.Context, reg *lsp.Registry, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if reg == nil {
		return nil, fmt.Errorf("LSP not available: no registry")
	}

	workingDir := tctx.WorkingDir

	timeoutSec := in.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	// Derive a wall-clock timeout so a slow server can't hang forever. We
	// distinguish wall-clock expiry (DeadlineExceeded) from caller cancel
	// (Canceled) after dispatch — mirrors omp index.ts:2458-2468.
	derivedCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	result, err := dispatch(derivedCtx, reg, in, workingDir)
	if err == nil {
		return result, nil
	}
	// Wall-clock timeout: give the LLM actionable context (server may be indexing).
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, fmt.Errorf("LSP %s timed out after %ds (server may still be indexing; try again or pass timeout=<larger>)", in.Action, timeoutSec)
	}
	return nil, err
}

// dispatch routes to per-action handlers. It does not know about timeouts.
func dispatch(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (*tool.ToolResult, error) {
	switch in.Action {
	case "status":
		return status(ctx, reg)
	case "capabilities":
		return capabilities(ctx, reg, in)
	case "reload":
		return reload(ctx, reg)
	case "workspace_symbol":
		return workspaceSymbol(ctx, reg, in, workingDir)
	case "request":
		return request(ctx, reg, in, workingDir)
	case "rename_file":
		return renameFile(ctx, reg, in, workingDir)
	case "definition", "type_definition", "implementation", "references", "hover", "symbols", "code_actions", "rename":
		return fileOp(ctx, reg, in, workingDir)
	default:
		return nil, fmt.Errorf("unknown LSP action: %s", in.Action)
	}
}

// fileOp is the dispatch for file-scoped actions (actions that need a file path
// and resolve a position from line + symbol).
func fileOp(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (*tool.ToolResult, error) {
	if in.File == "" {
		return nil, fmt.Errorf("file parameter required for %s", in.Action)
	}

	targetFile := resolvePath(in.File, workingDir)
	ext := filepath.Ext(targetFile)
	if ext == "" {
		return nil, fmt.Errorf("no extension: %s", targetFile)
	}

	c, err := reg.ForFile(ctx, targetFile)
	if err != nil {
		return nil, fmt.Errorf("lsp for file: %w", err)
	}
	spec, _ := reg.SpecForFile(targetFile)

	uri := lsp.FileToURI(targetFile)
	langID := lsp.DetectLanguage(targetFile)
	if langID == "" {
		return nil, fmt.Errorf("unknown language: %s", ext)
	}

	// Read file content and ensure it's open
	content, err := os.ReadFile(targetFile)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	if err := c.EnsureFileOpen(ctx, uri, langID, string(content)); err != nil {
		return nil, fmt.Errorf("ensure open: %w", err)
	}

	// Validate action-specific params early so the LLM gets the right error
	// (e.g. rename without new_name should fail on new_name, not on symbol).
	if in.Action == "rename" && in.NewName == "" {
		return nil, fmt.Errorf("new_name parameter required for rename")
	}

	line := in.Line
	if line <= 0 {
		line = 1
	}

	// Resolve column: reuse the already-read content to avoid duplicate I/O.
	col, err := resolveSymbolColumnFromContent(content, line, in.Symbol)
	if err != nil {
		return nil, err
	}

	pos := lsp.Position{Line: line - 1, Character: col}

	// Symbol-required guard: project-aware servers (gopls/tsserver/rust-analyzer)
	// need an explicit symbol name to disambiguate which identifier at the line
	// the cursor is on; without it they fall back to whatever the heuristic
	// picks and may return wrong-definition results. Mirrors omp
	// PROJECT_INDEXED_ACTIONS symbol guard (index.ts:2122-2135).
	symbolRequired := isProjectAwareLspServer(spec) && in.Symbol == ""
	switch in.Action {
	case "definition", "type_definition", "implementation", "references", "rename":
		if symbolRequired {
			return nil, fmt.Errorf("symbol parameter required for %s on project-aware servers (server may mis-identify the identifier)", in.Action)
		}
	}

	switch in.Action {
	case "definition":
		return definition(ctx, c, uri, pos, workingDir)
	case "type_definition":
		return typeDefinition(ctx, c, uri, pos, workingDir)
	case "implementation":
		return implementation(ctx, c, uri, pos, workingDir)
	case "references":
		return references(ctx, c, uri, pos, workingDir, spec)
	case "hover":
		return hover(ctx, c, uri, pos)
	case "symbols":
		return symbols(ctx, c, uri, targetFile, workingDir)
	case "code_actions":
		return codeActions(ctx, c, uri, pos, in)
	case "rename":
		return rename(ctx, c, uri, pos, in.NewName, workingDir, boolPtrVal(in.Apply, true), spec)
	default:
		return nil, fmt.Errorf("unknown file action: %s", in.Action)
	}
}

func resolvePath(p, wd string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(wd, p)
}

func boolPtrVal(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func formatJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// resolveSymbolColumn finds the 0-based column offset of a symbol on a given line.
// Reads the file from disk — callers that already have the file content should
// call resolveSymbolColumnFromContent instead to avoid duplicate I/O.
func resolveSymbolColumn(filePath string, line int, symbol string) (int, error) {
	if filePath == "" {
		return 0, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, fmt.Errorf("file not found: %s", filePath)
	}
	return resolveSymbolColumnFromContent(data, line, symbol)
}

// resolveSymbolColumnFromContent is the core column resolver that works on
// already-read file content. Mirrors omp utils.ts resolveSymbolColumn.
// Returns an error (not col=0) when the symbol is not found — a silent fallback
// to col=0 would query the wrong identifier and mislead the LLM.
func resolveSymbolColumnFromContent(content []byte, line int, symbol string) (int, error) {
	lines := strings.Split(string(content), "\n")
	if line < 1 {
		line = 1
	}
	targetLine := ""
	if line <= len(lines) {
		targetLine = lines[line-1]
	}

	// No symbol: return first non-whitespace column.
	if symbol == "" {
		for i, c := range targetLine {
			if c != ' ' && c != '\t' {
				return i, nil
			}
		}
		return 0, nil
	}

	sym, n := parseSymbolSpec(symbol)

	indexes := findSymbolMatchIndexes(targetLine, sym)
	if len(indexes) == 0 {
		indexes = findSymbolMatchIndexesCI(targetLine, sym)
	}
	if len(indexes) == 0 {
		return 0, fmt.Errorf("symbol %q not found on line %d", sym, line)
	}
	if n > len(indexes) {
		return 0, fmt.Errorf("symbol %q occurrence %d is out of bounds on line %d (found %d)", sym, n, line, len(indexes))
	}
	return byteOffsetToUTF16(targetLine, indexes[n-1]), nil
}

// byteOffsetToUTF16 converts a byte offset in s to a UTF-16 code unit offset.
// LSP Position.Character uses UTF-16 code units, not byte offsets. BMP characters
// (including CJK) count as 1 code unit; supplementary plane characters (emoji)
// count as 2 (surrogate pair).
func byteOffsetToUTF16(s string, byteOffset int) int {
	if byteOffset > len(s) {
		byteOffset = len(s)
	}
	count := 0
	for i := 0; i < byteOffset; {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r >= 0x10000 {
			count += 2
		} else {
			count++
		}
	}
	return count
}

// parseSymbolSpec splits "name#N" into name and 1-indexed occurrence.
// Specs without trailing #N return occurrence=1. Greedy match on ".+"
// so "#name#2" parses as symbol="#name" with occurrence 2.
func parseSymbolSpec(spec string) (symbol string, occurrence int) {
	m := parseSymbolSpecRe.FindStringSubmatch(spec)
	if m == nil {
		return spec, 1
	}
	n, _ := strconv.Atoi(m[2])
	if n < 1 {
		n = 1
	}
	return m[1], n
}

var parseSymbolSpecRe = regexp.MustCompile(`^(.+)#(\d+)$`)

// findSymbolMatchIndexes returns byte offsets where symbol appears as a
// standalone identifier in lineText. Bare identifiers (matching BARE_IDENTIFIER_RE)
// require word boundaries on both sides; symbols with non-identifier chars
// (e.g., operators) match anywhere.
func findSymbolMatchIndexes(lineText, symbol string) []int {
	if symbol == "" {
		return nil
	}
	requireWordBoundary := bareIdentifierRe.MatchString(symbol)
	var indexes []int
	from := 0
	for from <= len(lineText)-len(symbol) {
		idx := strings.Index(lineText[from:], symbol)
		if idx < 0 {
			break
		}
		pos := from + idx
		if requireWordBoundary {
			before := byte(' ')
			if pos > 0 {
				before = lineText[pos-1]
			}
			after := byte(' ')
			if pos+len(symbol) < len(lineText) {
				after = lineText[pos+len(symbol)]
			}
			if isIdentChar(before) || isIdentChar(after) {
				from = pos + 1
				continue
			}
		}
		indexes = append(indexes, pos)
		from = pos + len(symbol)
	}
	return indexes
}

// findSymbolMatchIndexesCI is the case-insensitive fallback when exact match returns nothing.
func findSymbolMatchIndexesCI(lineText, symbol string) []int {
	return findSymbolMatchIndexes(strings.ToLower(lineText), strings.ToLower(symbol))
}

var bareIdentifierRe = regexp.MustCompile(`^[$A-Za-z_][\w$]*$`)

func isIdentChar(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// extractHoverText converts LSP Hover contents to plain text.
func extractHoverText(contents any) string {
	if raw, ok := contents.(json.RawMessage); ok && len(raw) > 0 {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			return extractHoverText(decoded)
		}
		return string(raw)
	}
	switch v := contents.(type) {
	case string:
		return v
	case map[string]any:
		if value, ok := v["value"]; ok {
			if s, ok := value.(string); ok {
				return s
			}
		}
		return fmt.Sprintf("%v", v)
	case []any:
		var parts []string
		for _, item := range v {
			parts = append(parts, extractHoverText(item))
		}
		return strings.Join(parts, "\n\n")
	default:
		return fmt.Sprintf("%v", contents)
	}
}

// formatDocumentSymbol recursively formats a DocumentSymbol tree.
func formatDocumentSymbol(b *strings.Builder, s lsp.DocumentSymbol, depth int) {
	indent := strings.Repeat("  ", depth)
	line := s.Range.Start.Line + 1
	fmt.Fprintf(b, "%s- %s `%s`", indent, symbolKindName(s.Kind), s.Name)
	if s.Detail != "" {
		fmt.Fprintf(b, " — %s", s.Detail)
	}
	fmt.Fprintf(b, " @ line %d\n", line)
	for _, child := range s.Children {
		formatDocumentSymbol(b, child, depth+1)
	}
}

func symbolKindName(k lsp.SymbolKind) string {
	switch k {
	case lsp.SymbolFile:
		return "file"
	case lsp.SymbolModule:
		return "module"
	case lsp.SymbolNamespace:
		return "namespace"
	case lsp.SymbolPackage:
		return "package"
	case lsp.SymbolClass:
		return "class"
	case lsp.SymbolMethod:
		return "method"
	case lsp.SymbolProperty:
		return "property"
	case lsp.SymbolField:
		return "field"
	case lsp.SymbolConstructor:
		return "constructor"
	case lsp.SymbolEnum:
		return "enum"
	case lsp.SymbolInterface:
		return "interface"
	case lsp.SymbolFunction:
		return "func"
	case lsp.SymbolVariable:
		return "var"
	case lsp.SymbolConstant:
		return "const"
	case lsp.SymbolString:
		return "string"
	case lsp.SymbolNumber:
		return "number"
	case lsp.SymbolBoolean:
		return "bool"
	case lsp.SymbolArray:
		return "array"
	case lsp.SymbolObject:
		return "object"
	case lsp.SymbolKey:
		return "key"
	case lsp.SymbolNull:
		return "null"
	case lsp.SymbolEnumMember:
		return "enum member"
	case lsp.SymbolStruct:
		return "struct"
	case lsp.SymbolEvent:
		return "event"
	case lsp.SymbolOperator:
		return "operator"
	case lsp.SymbolTypeParameter:
		return "type param"
	default:
		return "symbol"
	}
}

// decodeLocations decodes a JSON-RPC result into a slice of Locations,
// handling the single-object vs array ambiguity, and LocationLink responses
// (servers that return targetUri/targetRange when linkSupport was negotiated
// at initialize time). Mirrors omp normalizeLocationResult (index.ts:377-389).
func decodeLocations(raw json.RawMessage) ([]lsp.Location, error) {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	// Try array first.
	if trimmed[0] == '[' {
		// Peek the element shape: could be Location[] or LocationLink[].
		var generic []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &generic); err != nil {
			return nil, fmt.Errorf("decodeLocations array: %w", err)
		}
		out := make([]lsp.Location, 0, len(generic))
		for _, m := range generic {
			loc, ok := locationFromMap(m)
			if !ok {
				continue
			}
			out = append(out, loc)
		}
		return out, nil
	}
	if trimmed[0] == '{' {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, fmt.Errorf("decodeLocations single: %w", err)
		}
		if loc, ok := locationFromMap(m); ok {
			return []lsp.Location{loc}, nil
		}
		return nil, fmt.Errorf("decodeLocations: unrecognized object: %s", string(trimmed))
	}
	return nil, fmt.Errorf("decodeLocations: unexpected token in %s", string(trimmed))
}

// locationFromMap converts either a Location-shaped map ({uri, range}) or a
// LocationLink-shaped map ({targetUri, targetSelectionRange, targetRange}) to
// a Location. LocationLink uses targetSelectionRange when present (mirrors
// omp normalizeLocationResult).
func locationFromMap(m map[string]json.RawMessage) (lsp.Location, bool) {
	if uriRaw, ok := m["uri"]; ok {
		var uri string
		if err := json.Unmarshal(uriRaw, &uri); err != nil {
			return lsp.Location{}, false
		}
		var rng lsp.Range
		if rRaw, ok := m["range"]; ok {
			_ = json.Unmarshal(rRaw, &rng)
		}
		return lsp.Location{URI: uri, Range: rng}, true
	}
	if tURI, ok := m["targetUri"]; ok {
		var uri string
		if err := json.Unmarshal(tURI, &uri); err != nil {
			return lsp.Location{}, false
		}
		var rng lsp.Range
		// Prefer targetSelectionRange (the focused span), fall back to targetRange.
		if rRaw, ok := m["targetSelectionRange"]; ok {
			_ = json.Unmarshal(rRaw, &rng)
		} else if rRaw, ok := m["targetRange"]; ok {
			_ = json.Unmarshal(rRaw, &rng)
		}
		return lsp.Location{URI: uri, Range: rng}, true
	}
	return lsp.Location{}, false
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// isMethodNotFoundError reports whether err indicates an LSP "method not found"
// (-32601) response, so callers can silently skip servers that don't implement
// an optional method like workspace/willRenameFiles. Mirrors omp isMethodNotFoundError.
func isMethodNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "method not found") ||
		strings.Contains(msg, "unhandled method") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "-32601")
}
