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
		"  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like " + bt + "struct \\{[\\s\\S]*?field" + bt + ", use " + bt + "multiline: true" + bt
}
