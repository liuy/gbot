package filewrite

func fileWritePrompt() string {
	return "Writes a file to the local filesystem.\n\n" +
		"Usage:\n" +
		"- This tool will overwrite the existing file if there is one at the provided path.\n" +
		"- If this is an existing file, you MUST use the Read tool first to read the file's contents. This tool will fail if you did not read the file first.\n" +
		"- Prefer the Edit tool for modifying existing files — it only sends the diff. Only use this tool for complete rewrites.\n" +
		"- NEVER create documentation files (*.md) or README files unless explicitly requested by the User.\n" +
		"- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked."
}
