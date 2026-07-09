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
	"strings"
	"time"

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
	"callers":          true,
	"callees":          true,
	"source":           true,
	"inspect":          true,
	"impact":           true,
	"status":           true,
	"capabilities":     true,
}

// Input is the LSP tool input schema.
type Input struct {
	Action  string `json:"action"`             // required: definition, references, hover, symbols, rename, etc.
	File    string `json:"file,omitempty"`     // optional: file path (disambiguates + speeds up symbol resolution)
	Symbol  string `json:"symbol,omitempty"`   // symbol name (resolved to position via documentSymbol or workspace_symbol)
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
				"enum": ["definition", "type_definition", "implementation", "references", "hover", "symbols", "workspace_symbol", "code_actions", "rename", "rename_file", "reload", "status", "capabilities", "request", "callers", "callees", "source", "inspect", "impact"],
				"description": "LSP action to perform"
			},
			"file": {
				"type": "string",
				"description": "Optional. File path to disambiguate symbol resolution. When omitted, workspace_symbol is used to find the symbol across the project."
			},
			"symbol": {
				"type": "string",
				"description": "Symbol name (e.g. function/type/variable name). Required for position-based actions. Automatically resolved to a position via documentSymbol (if file given) or workspace_symbol. Use symbol#N to disambiguate the Nth occurrence."
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
			return formatLspDescription(in), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, reg, input, tctx)
		},
		RenderResult_: func(data any) string {
			switch v := data.(type) {
			case string:
				return v
			case json.RawMessage:
				var s string
				if json.Unmarshal(v, &s) == nil {
					return s
				}
				return string(v)
			default:
				b, _ := json.Marshal(data)
				return string(b)
			}
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			return readonlyActions[in.Action]
		},
		IsSearchOrRead_: func(json.RawMessage) tool.SearchReadKind {
			return tool.SearchReadKind{IsLsp: true}
		},
		Prompt_:            LSPPrompt(),
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
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
	case "definition", "type_definition", "implementation", "references", "hover", "symbols", "code_actions", "rename", "callers", "callees", "source", "inspect", "impact", "check":
		return fileOp(ctx, reg, in, workingDir)
	default:
		return nil, fmt.Errorf("unknown LSP action: %s", in.Action)
	}
}

// fileOp is the dispatch for file-scoped actions.
// Position-based actions resolve the symbol to a position via documentSymbol
// (if file is given) or workspace_symbol (if not), so the LLM never needs
// to pass line numbers.
func fileOp(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (*tool.ToolResult, error) {
	// rename validation early so the error is about new_name, not symbol resolution
	if in.Action == "rename" && in.NewName == "" {
		return nil, fmt.Errorf("new_name parameter required for rename")
	}

	// symbols only needs a file, no symbol resolution
	if in.Action == "symbols" {
		if in.File == "" {
			return nil, fmt.Errorf("file parameter required for symbols")
		}
		return symbolsAction(ctx, reg, in, workingDir)
	}

	// source, inspect, impact have their own resolve + dispatch
	switch in.Action {
	case "source":
		return sourceAction(ctx, reg, in, workingDir)
	case "inspect":
		return inspectAction(ctx, reg, in, workingDir)
	case "impact":
		return impactAction(ctx, reg, in, workingDir)
	}

	// All other actions need a symbol name
	if in.Symbol == "" {
		return nil, fmt.Errorf("symbol parameter required for %s", in.Action)
	}

	uri, pos, c, spec, err := resolveAndOpen(ctx, reg, in, workingDir)
	if err != nil {
		return nil, err
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
	case "callers":
		return callHierarchy(ctx, c, uri, pos, workingDir, "callers")
	case "callees":
		return callHierarchy(ctx, c, uri, pos, workingDir, "callees")
	case "code_actions":
		return codeActions(ctx, c, uri, pos, in)
	case "rename":
		return rename(ctx, c, uri, pos, in.NewName, workingDir, boolPtrVal(in.Apply, true), spec)
	default:
		return nil, fmt.Errorf("unknown file action: %s", in.Action)
	}
}

// resolveAndOpen resolves the symbol to a position, ensures the file is open
// in the LSP server, and returns everything the action handlers need.
func resolveAndOpen(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (uri string, pos lsp.Position, c *lsp.Client, spec lsp.ServerSpec, err error) {
	uri, pos, err = resolveSymbolPosition(ctx, reg, in.Symbol, in.File, workingDir)
	if err != nil {
		return "", lsp.Position{}, nil, lsp.ServerSpec{}, err
	}

	targetFile := lsp.URItoPath(uri)
	ext := filepath.Ext(targetFile)
	if ext == "" {
		return "", lsp.Position{}, nil, lsp.ServerSpec{}, fmt.Errorf("no extension: %s", targetFile)
	}

	c, err = reg.ForFile(ctx, targetFile)
	if err != nil {
		return "", lsp.Position{}, nil, lsp.ServerSpec{}, fmt.Errorf("lsp for file: %w", err)
	}
	spec, _ = reg.SpecForFile(targetFile)

	langID := lsp.DetectLanguage(targetFile)
	if langID == "" {
		return "", lsp.Position{}, nil, lsp.ServerSpec{}, fmt.Errorf("unknown language: %s", ext)
	}

	if !c.IsFileOpen(uri) {
		if err := ensureFileOpenWithGuard(ctx, c, uri, langID, targetFile); err != nil {
			return "", lsp.Position{}, nil, lsp.ServerSpec{}, err
		}
	}

	return uri, pos, c, spec, nil
}

// symbolsAction handles the symbols-only path (needs file, no symbol resolution).
func symbolsAction(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (*tool.ToolResult, error) {
	targetFile := resolvePath(in.File, workingDir)
	ext := filepath.Ext(targetFile)
	if ext == "" {
		return nil, fmt.Errorf("no extension: %s", targetFile)
	}

	c, err := reg.ForFile(ctx, targetFile)
	if err != nil {
		return nil, fmt.Errorf("lsp for file: %w", err)
	}

	uri := lsp.FileToURI(targetFile)
	langID := lsp.DetectLanguage(targetFile)
	if langID == "" {
		return nil, fmt.Errorf("unknown language: %s", ext)
	}

	if err := ensureFileOpenWithGuard(ctx, c, uri, langID, targetFile); err != nil {
		return nil, err
	}

	return symbols(ctx, c, uri, targetFile, workingDir)
}

// ensureFileOpenWithGuard stats the file, checks the 10MB limit, reads it,
// and sends didOpen to the server. Skips if already open.
func ensureFileOpenWithGuard(ctx context.Context, c *lsp.Client, uri, langID, targetFile string) error {
	if c.IsFileOpen(uri) {
		return nil
	}
	info, err := os.Stat(targetFile)
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	if info.Size() > 10*1024*1024 {
		return fmt.Errorf("file too large for LSP analysis (%.1fMB, max 10MB)", float64(info.Size())/1024/1024)
	}
	content, err := os.ReadFile(targetFile)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if err := c.EnsureFileOpen(ctx, uri, langID, string(content)); err != nil {
		return fmt.Errorf("ensure open: %w", err)
	}
	return nil
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

// formatLspDescription returns a short summary of an LSP invocation for the
// TUI tool header. Format: "<action> <target>".
// Examples:
//
//	"definition pkg/foo.go:Bar"   // position-based, with file and symbol
//	"definition Bar"             // position-based, symbol only
//	"rename Bar → Baz"            // rename
//	"rename_file old.go → new.go" // rename_file
//	"symbols pkg/foo.go"          // file-scoped
//	"workspace_symbol Foo"        // query-driven
//	"request textDocument/hover"  // raw method
//	"status"                      // no params
func formatLspDescription(in Input) string {
	switch in.Action {
	case "rename":
		switch {
		case in.Symbol != "" && in.NewName != "":
			return fmt.Sprintf("rename %s → %s", in.Symbol, in.NewName)
		case in.NewName != "":
			return "rename → " + in.NewName
		case in.Symbol != "":
			return "rename " + in.Symbol
		}
		return "rename"
	case "rename_file":
		switch {
		case in.Symbol != "" && in.NewName != "":
			return fmt.Sprintf("rename_file %s → %s", in.Symbol, in.NewName)
		case in.NewName != "":
			return "rename_file → " + in.NewName
		case in.Symbol != "":
			return "rename_file " + in.Symbol
		}
		return "rename_file"
	case "symbols":
		if in.File != "" {
			return "symbols " + in.File
		}
		return "symbols"
	case "workspace_symbol":
		if in.Query != "" {
			return "workspace_symbol " + in.Query
		}
		return "workspace_symbol"
	case "request":
		if in.Query != "" {
			return "request " + in.Query
		}
		return "request"
	case "status", "capabilities", "reload":
		return in.Action
	}
	// Position-based actions: definition, type_definition, implementation,
	// references, hover, code_actions, callers, callees, source, inspect, impact.
	switch {
	case in.Symbol != "" && in.File != "":
		return fmt.Sprintf("%s %s:%s", in.Action, in.File, in.Symbol)
	case in.Symbol != "":
		return in.Action + " " + in.Symbol
	case in.File != "":
		return in.Action + " " + in.File
	}
	return in.Action
}
