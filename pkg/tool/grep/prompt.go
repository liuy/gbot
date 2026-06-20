package grep

// bt is a backtick character.
const bt = "`"

func grepPrompt() string {
	return "A powerful search tool built on ripgrep\n\n" +
		"  Usage:\n" +
		"  - ALWAYS use Grep for search tasks. NEVER invoke " + bt + "grep" + bt + " or " + bt + "rg" + bt + " as a Bash command. The Grep tool has been optimized for correct permissions and access.\n" +
		"  - Supports full regex syntax (e.g., " + bt + "log.*Error" + bt + ", " + bt + "function\\s+\\w+" + bt + ")\n" +
		"  - Filter files with glob parameter (e.g., " + bt + "*.js" + bt + ", " + bt + "**/*.tsx" + bt + ") or type parameter (e.g., " + bt + "js" + bt + ", " + bt + "py" + bt + ", " + bt + "rust" + bt + ")\n" +
		"  - Output modes: " + bt + "content" + bt + " shows matching lines, " + bt + "files_with_matches" + bt + " shows only file paths (default), " + bt + "count" + bt + " shows match counts\n" +
		"  - Use Agent tool for open-ended searches requiring multiple rounds\n" +
		"  - Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use " + bt + "interface\\{\\}" + bt + " to find " + bt + "interface{}" + bt + " in Go code)\n" +
		"  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like " + bt + "struct \\{[\\s\\S]*?field" + bt + ", use " + bt + "multiline: true" + bt + "\n\n" +
		"  Code symbols vs text:\n" +
		"  - For searching code symbols (function names, class names, type names, variable names), prefer Lsp references or Lsp workspace_symbol — they return only real code references and skip matches in comments, strings, and documentation.\n" +
		"  - To understand what a symbol does and who calls it, use Lsp inspect.\n" +
		"  - To assess the blast-radius of changing a symbol, use Lsp impact.\n" +
		"  - For searching log messages, configuration values, documentation text, or any non-code content, continue using Grep."
}
