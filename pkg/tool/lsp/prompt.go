package lsptool

// LSPPrompt returns the system prompt for the Lsp tool.
func LSPPrompt() string {
	return `Interacts with Language Server Protocol (LSP) servers for code intelligence. LSP servers understand your code as a compiler does — symbols, types, references — not as text.

### When to use

Use ` + "`Lsp`" + ` INSTEAD of text-based tools (Grep, manual renames) when you need:

- **definition** — Where is this symbol defined? Safer than Grep because it follows re-exports and aliases.
- **references** — Where is this symbol used across the codebase? Finds usages that text search misses.
- **hover** — What type does this variable/function have? Faster than reading the full definition.
- **rename** — Rename a symbol across all files. NEVER use Edit/Write for cross-file symbol renames.
- **code_actions** — Apply language server quick-fixes and auto-imports.
- **workspace_symbol** — Find symbols across the project by name (like fuzzy symbol search).
- **symbols** — List all symbols in a file (functions, types, variables) with their hierarchy.
- **request** — Send a raw LSP method call (escape hatch when no built-in action covers the method).

### Available actions

| Action | File | Line | Symbol | Description |
|--------|------|------|--------|-------------|
| definition | yes | yes | recommended | Navigate to symbol definition |
| type_definition | yes | yes | recommended | Navigate to type definition |
| implementation | yes | yes | recommended | Find concrete implementations |
| references | yes | yes | recommended | Find all usages |
| hover | yes | yes | recommended | Get type information + docs |
| symbols | yes | — | — | List symbols in a file (hierarchical) |
| workspace_symbol | — | — | — | Search symbols across project (uses query param) |
| code_actions | yes | yes | — | List available quick-fixes |
| rename | yes | yes | yes | Rename a symbol (requires new_name) |
| request | yes* | — | — | Raw LSP method call (query=method, payload=JSON params) |
| status | — | — | — | Show active language servers |
| capabilities | yes* | — | — | Show what each server supports |
| reload | — | — | — | Reload LSP servers after config changes |

*file scopes to one server; omit for all servers.

### How line + symbol work together

Position-based actions (definition, references, hover, rename, etc.) use ` + "`file`" + ` + ` + "`line`" + ` + optionally ` + "`symbol`" + ` to pinpoint a location.

- **` + "`line`" + `** (1-indexed): the line where the target symbol appears — can be a declaration or a call site.
- **` + "`symbol`" + `**: the identifier text on that line. The tool searches for it to determine the column. If omitted, the first non-whitespace character on the line is used.
- The symbol must actually appear on the given line — if you're unsure of the exact line number, Read the file first to confirm. A wrong line produces "symbol not found" even though the tool is working correctly.
- For symbols that appear multiple times on the same line, append ` + "`#N`" + ` to select the Nth occurrence (e.g. ` + "`value#2`" + `). Default is ` + "`#1`" + `.
- Matching is case-insensitive as a fallback when exact case doesn't match.

### Critical rules

- **NEVER** do cross-file renames with Edit/Write or Bash when LSP servers are available. Use ` + "`Lsp action=rename`" + `. Text-based renames miss shadowing, re-exports, and indirect usages.
- **ALWAYS** prefer ` + "`Lsp action=references`" + ` over Grep for finding symbol usages. LSP follows re-exports and gives line/column precision.
- **ALWAYS** prefer ` + "`Lsp action=definition`" + ` for navigating to symbol definitions. Grep can miss re-exported symbols.
- **Verify the line number before calling.** Line numbers shift when files are edited. Do NOT manually count lines from Read output — use ` + "`workspace_symbol`" + ` or ` + "`symbols`" + ` first to get the exact line, then pass that to position-based actions. If you last read the file several turns ago or have since edited it, re-check before calling.
- **Use ` + "`workspace_symbol`" + `** when you need to quickly find a type, function, or variable by name across the project.
- **Use ` + "`code_actions`" + `** to let the language server fix imports, add missing cases, or apply refactors.
- **Use ` + "`symbols`" + `** to understand a file's structure at a glance before reading it.
- **Use ` + "`request`" + `** as escape hatch when no built-in action covers the LSP method you need. Set ` + "`query`" + ` to the method name, optionally ` + "`payload`" + ` for custom params.`
}
