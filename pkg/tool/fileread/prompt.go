package fileread

func fileReadPrompt() string {
	return `Reads a file from the local filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The file_path parameter must be an absolute path, not a relative path.
- By default, it reads up to 2000 lines starting from the beginning of the file.
- You can optionally specify a line offset and limit (especially handy for long files), but it's recommended to read the whole file by not providing these parameters.
- Results are returned using cat -n format, with line numbers starting at 1.
- This tool can only read files, not directories. To read a directory, use an ls command via the Bash tool.
- You will regularly be asked to read screenshots. If the user provides a path to a screenshot, ALWAYS use this tool to view the file at the path. This tool will work with all temporary file paths.
- If you read a file that exists but has empty contents you will receive a system reminder warning in place of file contents.

# Images
This tool can read images (eg PNG, JPG, etc). When reading an image file contents are presented visually.

# PDFs
This tool can read PDF files (.pdf). For large PDFs (more than 10 pages), you MUST provide the pages parameter to read specific page ranges (e.g., pages: "1-5"). Reading a large PDF without the pages parameter will fail. Maximum 20 pages per request.

# Binary documents (via markitdown)
Converts these formats to markdown:
- Office: .doc, .docx, .ppt, .pptx, .xls, .xlsx
- Web: .html, .htm, .xml, .rss
- Data: .csv, .ipynb
- Document: .epub
- Archive: .zip (extracts and converts internal files)

Conversion output supports line offset and limit just like text files.

# SQLite databases
For .sqlite, .sqlite3, .db, .db3 files, append a selector after the path:
- file.db — list tables with row counts
- file.db:table — schema + sample rows
- file.db:table:42 — single row by primary key (or rowid)
- file.db:table?limit=20&offset=0 — paginated rows
- file.db:table?where=status='active'&order=created:desc — filtered, sorted rows
- file.db?q=SELECT ... — read-only raw SQL query

Detection is by file extension AND magic bytes — a .sqlite file that is not a real SQLite database is rejected.

# Archives
Reads members inside archives without extraction. Supported formats: .zip, .tar, .tar.gz/.tgz, .tar.xz/.txz, .tar.bz2/.tbz2, .tar.zst, .tar.lz4, .gz, .bz2, .xz, .zst, .lz4, .7z, .rar.
- archive.zip — list root directory
- archive.zip:dir/ — list subdirectory
- archive.zip:dir/file.ts — read a member (supports offset/limit)

Binary members (non-UTF-8 or containing NUL bytes) are rejected with a placeholder message. Single-file compression formats (.gz, .bz2, .xz, .zst, .lz4) decompress and return the inner file's content directly.
`
}
