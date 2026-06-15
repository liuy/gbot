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
- **callers** — What functions/methods call this function?
- **callees** — What functions/methods does this function call?
- **source** — Extract the full source text of a symbol by name. No line numbers needed.
- **inspect** — Hover + definition + callers combined. The fastest way to understand a symbol.
- **impact** — References + callers + callees combined. Assess blast-radius before changes.
- **request** — Send a raw LSP method call (escape hatch when no built-in action covers the method).

### How to pass symbols

Position-based actions (definition, references, hover, rename, etc.) take a ` + "`symbol`" + ` parameter — the symbol NAME. The tool automatically resolves it to a position via the language server. You do NOT need to know line numbers.

- ` + "`symbol`" + `: the identifier name (e.g. ` + "`callHierarchy`" + `, ` + "`Client.Request`" + `)
- ` + "`file`" + ` (optional): when provided, the tool searches within that file only (faster + disambiguates). When omitted, the tool searches the entire workspace.
- For symbols that appear multiple times, append ` + "`#N`" + ` to select the Nth occurrence (e.g. ` + "`value#2`" + `). Default is ` + "`#1`" + `.

### Available actions

| Action | symbol | file | Description |
|--------|--------|------|-------------|
| definition | yes | optional | Navigate to symbol definition |
| type_definition | yes | optional | Navigate to type definition |
| implementation | yes | optional | Find concrete implementations |
| references | yes | optional | Find all usages |
| hover | yes | optional | Get type information + docs |
| callers | yes | optional | What calls this function |
| callees | yes | optional | What this function calls |
| source | yes | optional | Extract full source text of a symbol |
| inspect | yes | optional | hover + definition + callers combined |
| impact | yes | optional | references + callers + callees combined |
| symbols | — | yes | List symbols in a file (hierarchical) |
| workspace_symbol | — | — | Search symbols across project (uses query param) |
| code_actions | yes | optional | List available quick-fixes |
| rename | yes | optional | Rename a symbol (requires new_name) |
| request | optional | optional | Raw LSP method call (query=method, payload=JSON params) |
| status | — | — | Show active language servers |
| capabilities | optional | — | Show what each server supports |
| reload | — | — | Reload LSP servers after config changes |

### Critical rules

- **NEVER** do cross-file renames with Edit/Write or Bash when LSP servers are available. Use ` + "`Lsp action=rename`" + `. Text-based renames miss shadowing, re-exports, and indirect usages.
- **ALWAYS** prefer ` + "`Lsp action=references`" + ` over Grep for finding symbol usages. LSP follows re-exports and gives line/column precision.
- **ALWAYS** prefer ` + "`Lsp action=definition`" + ` for navigating to symbol definitions. Grep can miss re-exported symbols.
- **Use ` + "`workspace_symbol`" + `** when you need to quickly find a type, function, or variable by name across the project.
- **Use ` + "`code_actions`" + `** to let the language server fix imports, add missing cases, or apply refactors.
- **Use ` + "`symbols`" + `** to understand a file's structure at a glance before reading it.
- **Use ` + "`request`" + `** as escape hatch when no built-in action covers the LSP method you need. Set ` + "`query`" + ` to the method name, optionally ` + "`payload`" + ` for custom params.`
}
