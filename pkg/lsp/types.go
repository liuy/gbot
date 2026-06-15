package lsp

import "encoding/json"

// Position is a 0-based position inside a text document (LSP spec).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a (start, end] pair of positions in a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location ties a URI to a Range.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// TextEdit replaces Range with NewText.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// TextDocumentIdentifier references a document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier adds a version for didOpen/didChange tracking.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// TextDocumentItem is the payload for didOpen.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// Hover is the response from textDocument/hover.
type Hover struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range,omitempty"`
}

// SymbolKind matches LSP's SymbolKind enum (subset we care about).
type SymbolKind int

const (
	SymbolFile          SymbolKind = 1
	SymbolModule        SymbolKind = 2
	SymbolNamespace     SymbolKind = 3
	SymbolPackage       SymbolKind = 4
	SymbolClass         SymbolKind = 5
	SymbolMethod        SymbolKind = 6
	SymbolProperty      SymbolKind = 7
	SymbolField         SymbolKind = 8
	SymbolConstructor   SymbolKind = 9
	SymbolEnum          SymbolKind = 10
	SymbolInterface     SymbolKind = 11
	SymbolFunction      SymbolKind = 12
	SymbolVariable      SymbolKind = 13
	SymbolConstant      SymbolKind = 14
	SymbolString        SymbolKind = 15
	SymbolNumber        SymbolKind = 16
	SymbolBoolean       SymbolKind = 17
	SymbolArray         SymbolKind = 18
	SymbolObject        SymbolKind = 19
	SymbolKey           SymbolKind = 20
	SymbolNull          SymbolKind = 21
	SymbolEnumMember    SymbolKind = 22
	SymbolStruct        SymbolKind = 23
	SymbolEvent         SymbolKind = 24
	SymbolOperator      SymbolKind = 25
	SymbolTypeParameter SymbolKind = 26
)

// DocumentSymbol is the hierarchical symbol tree from textDocument/documentSymbol.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolInformation is the flat symbol entry from workspace/symbol.
type SymbolInformation struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	Location      Location   `json:"location"`
	ContainerName string     `json:"containerName,omitempty"`
}

// CodeAction represents a quick-fix or refactor offered by the server.
type CodeAction struct {
	Title       string              `json:"title"`
	Kind        string              `json:"kind,omitempty"`
	Edit        *WorkspaceEdit      `json:"edit,omitempty"`
	Command     *Command            `json:"command,omitempty"`
	IsPreferred bool                `json:"isPreferred,omitempty"`
	Disabled    *CodeActionDisabled `json:"disabled,omitempty"`
}

// CodeActionDisabled carries the reason a code action cannot be applied.
type CodeActionDisabled struct {
	Reason string `json:"reason"`
}

// Diagnostic is a server-emitted issue used by codeAction context and
// (intentionally unused in gbot) the diagnostics action.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// Command is an executable action returned by codeAction resolution.
// Arguments uses json.RawMessage to preserve numeric types (avoid float64 coercion).
type Command struct {
	Title     string            `json:"title"`
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments,omitempty"`
}

// WorkspaceEdit bundles multiple file edits (changes map or documentChanges list).
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []map[string]any      `json:"documentChanges,omitempty"`
}
