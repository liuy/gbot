package lsp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// FileToURI converts an absolute file path to a file:// URI.
// Uses net/url to percent-encode special characters (spaces, #, ?, etc.)
// per RFC 8089, matching omp's Bun.pathToFileURL behavior (utils.ts:21).
func FileToURI(absPath string) string {
	u := url.URL{
		Scheme: "file",
		Path:   absPath,
	}
	return u.String()
}

// URItoRelativePath converts a file:// URI to a workspace-relative path.
// Mirrors omp uriToRelativePath — strips the file:// prefix, percent-decodes,
// and makes the path relative to wd if possible.
func URItoRelativePath(uri, wd string) string {
	uri = strings.TrimPrefix(uri, "file://")

	decoded, err := url.PathUnescape(uri)
	if err == nil {
		uri = decoded
	}

	rel, err := filepath.Rel(wd, uri)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return uri
}

// DetectLanguage returns the LSP language ID for a file path based on its extension.
func DetectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".kt", ".kts":
		return "kotlin"
	case ".scala":
		return "scala"
	case ".php":
		return "php"
	case ".css", ".scss", ".less":
		return "css"
	case ".html", ".htm":
		return "html"
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".yaml", ".yml":
		return "yaml"
	case ".md":
		return "markdown"
	case ".sql":
		return "sql"
	case ".sh", ".bash":
		return "shellscript"
	case ".toml":
		return "toml"
	default:
		return ""
	}
}

// FormatWorkspaceEdit produces a per-file summary of a WorkspaceEdit,
// e.g. "main.go: 3 edits" or "CREATE: foo.txt". Mirrors omp utils.ts
// formatWorkspaceEdit.
func FormatWorkspaceEdit(edit *WorkspaceEdit, cwd string) []string {
	if edit == nil {
		return nil
	}
	var out []string
	for uri, edits := range edit.Changes {
		rel := URItoRelativePath(uri, cwd)
		noun := "edit"
		if len(edits) > 1 {
			noun = "edits"
		}
		out = append(out, fmt.Sprintf("%s: %d %s", rel, len(edits), noun))
	}
	for _, dc := range edit.DocumentChanges {
		uri, edits, ok := extractTextDocumentEdit(dc)
		if ok {
			rel := URItoRelativePath(uri, cwd)
			noun := "edit"
			if len(edits) > 1 {
				noun = "edits"
			}
			out = append(out, fmt.Sprintf("%s: %d %s", rel, len(edits), noun))
			continue
		}
		kind, _ := dc["kind"].(string)
		switch kind {
		case "create":
			u, _ := dc["uri"].(string)
			out = append(out, "CREATE: "+URItoRelativePath(u, cwd))
		case "rename":
			oldURI, _ := dc["oldUri"].(string)
			newURI, _ := dc["newUri"].(string)
			out = append(out, "RENAME: "+URItoRelativePath(oldURI, cwd)+" → "+URItoRelativePath(newURI, cwd))
		case "delete":
			u, _ := dc["uri"].(string)
			out = append(out, "DELETE: "+URItoRelativePath(u, cwd))
		}
	}
	return out
}

// URItoPath converts a file:// URI back to a local path.
func URItoPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}

// FormatLocation formats a Location for display.
func FormatLocation(loc Location, cwd string) string {
	rel := URItoRelativePath(loc.URI, cwd)
	return fmt.Sprintf("%s:%d:%d", rel, loc.Range.Start.Line+1, loc.Range.Start.Character+1)
}

// ReadLocationContext returns up to n lines of source context surrounding the
// given line (1-based) from the file.
func ReadLocationContext(filePath string, line, n int) []string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	start := max(line-n-1, 0)
	end := min(line+n, len(lines))
	var result []string
	for i := start; i < end; i++ {
		result = append(result, fmt.Sprintf("%6d: %s", i+1, lines[i]))
	}
	return result
}

// FormatLocationWithContext formats a Location with surrounding source lines.
func FormatLocationWithContext(loc Location, cwd string, contextLines int) string {
	if contextLines <= 0 {
		return FormatLocation(loc, cwd)
	}
	header := FormatLocation(loc, cwd)
	filePath := URItoPath(loc.URI)
	context := ReadLocationContext(filePath, loc.Range.Start.Line+1, contextLines)
	if len(context) == 0 {
		return header
	}
	var b strings.Builder
	b.WriteString(header)
	for _, line := range context {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	return b.String()
}

// FormatLocationsWithContext formats multiple locations with source context,
// grouping by file path to read each file only once.
// Returns results in the same order as input locations.
func FormatLocationsWithContext(locs []Location, cwd string, contextLines int) []string {
	if contextLines <= 0 || len(locs) == 0 {
		out := make([]string, len(locs))
		for i, loc := range locs {
			out[i] = FormatLocation(loc, cwd)
		}
		return out
	}

	// Group locations by file path, tracking order.
	type fileEntry struct {
		filePath string
		indices  []int // positions in the original locs slice
	}
	fileMap := make(map[string]*fileEntry)
	var order []string
	for i, loc := range locs {
		fp := URItoPath(loc.URI)
		entry, ok := fileMap[fp]
		if !ok {
			entry = &fileEntry{filePath: fp}
			fileMap[fp] = entry
			order = append(order, fp)
		}
		entry.indices = append(entry.indices, i)
	}

	// Read each file once.
	fileCache := make(map[string][]string)
	for fp := range fileMap {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		fileCache[fp] = strings.Split(string(data), "\n")
	}

	// Format in input order.
	out := make([]string, len(locs))
	for _, fp := range order {
		entry := fileMap[fp]
		lines := fileCache[fp]
		for _, idx := range entry.indices {
			loc := locs[idx]
			header := FormatLocation(loc, cwd)
			if len(lines) == 0 {
				out[idx] = header
				continue
			}
			start := loc.Range.Start.Line + 1
			ctxStart := max(start-contextLines-1, 0)
			ctxEnd := min(start+contextLines, len(lines))
			var b strings.Builder
			b.WriteString(header)
			for i := ctxStart; i < ctxEnd; i++ {
				b.WriteString("\n  ")
				fmt.Fprintf(&b, "%6d: %s", i+1, lines[i])
			}
			out[idx] = b.String()
		}
	}
	return out
}
