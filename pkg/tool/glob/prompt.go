package glob

// bt is a backtick character.
const bt = "`"

func globPrompt() string {
	return "- Fast file pattern matching tool that works with any codebase size\n" +
		"- Supports glob patterns like " + bt + "**/*.js" + bt + " or " + bt + "src/**/*.ts" + bt + "\n" +
		"- Returns matching file paths sorted by modification time\n" +
		"- Use this tool when you need to find files by name patterns\n" +
		"- When you are doing an open ended search that may require multiple rounds of globbing and grepping, use the Agent tool instead"
}
