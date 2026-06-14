package grep

// bt is a backtick character.
const bt = "`"

func grepPrompt() string {
	return "Search file contents or file names using ripgrep\n\n" +
		"  Usage:\n" +
		"  - ALWAYS use this tool for search tasks. NEVER invoke " + bt + "grep" + bt + " or " + bt + "rg" + bt + " as a Bash command.\n" +
		"  - Set " + bt + "pattern" + bt + " to search file contents by regex.\n" +
		"  - Set " + bt + "glob" + bt + " to list files matching a name pattern (e.g. " + bt + "*.go" + bt + ", " + bt + "**/*.ts" + bt + ").\n" +
		"  - When only glob is set, output_mode and content flags (-A, -B, -C, -i, -n, multiline) are ignored.\n" +
		"  - Set both " + bt + "pattern" + bt + " and " + bt + "glob" + bt + " to search content filtered by file name.\n" +
		"  - At least one of pattern or glob must be set.\n" +
		"  - Content mode supports full regex syntax (e.g., " + bt + "log.*Error" + bt + ", " + bt + "function\\s+\\w+" + bt + ")\n" +
		"  - Filter by type parameter (e.g., " + bt + "go" + bt + ", " + bt + "py" + bt + ", " + bt + "rust" + bt + ")\n" +
		"  - Output modes: " + bt + "content" + bt + " shows matching lines, " + bt + "files_with_matches" + bt + " shows only file paths (default), " + bt + "count" + bt + " shows match counts\n" +
		"  - Use Agent tool for open-ended searches requiring multiple rounds\n" +
		"  - Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use " + bt + "interface\\{\\}" + bt + " to find " + bt + "interface{}" + bt + " in Go code)\n" +
		"  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like " + bt + "struct \\{[\\s\\S]*?field" + bt + ", use " + bt + "multiline: true" + bt
}
