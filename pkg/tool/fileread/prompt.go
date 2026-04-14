package fileread

func fileReadPrompt() string {
	return "Reads a file from the local filesystem. You can access any file directly by using this tool.\n" +
		"Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.\n\n" +
		"Usage:\n" +
		"- The file_path parameter must be an absolute path, not a relative path\n" +
		"- By default, it reads up to 2000 lines starting from the beginning of the file\n" +
		"- You can optionally specify a line offset and limit (especially handy for long files), but it's recommended to read the whole file by not providing these parameters\n" +
		"- Results are returned using cat -n format, with line numbers starting at 1\n" +
		"- This tool can read images (eg PNG, JPG, etc). When reading an image file the contents are presented visually.\n" +
		"- This tool can read PDF files (.pdf). For large PDFs (more than 10 pages), you MUST provide the pages parameter to read specific page ranges (e.g., pages: \"1-5\"). Reading a large PDF without the pages parameter will fail. Maximum 20 pages per request.\n" +
		"- This tool can only read files, not directories. To read a directory, use an ls command via the Bash tool.\n" +
		"- You will regularly be asked to read screenshots. If the user provides a path to a screenshot, ALWAYS use this tool to view the file at the path. This tool will work with all temporary file paths.\n" +
		"- If you read a file that exists but has empty contents you will receive a system reminder warning in place of file contents."
}
